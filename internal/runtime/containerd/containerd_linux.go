//go:build linux

// Package containerd implements the runtime.Runtime port over containerd + runc on
// Linux (ADR-0011): pull a curated runtime image, run it as a runc container with
// conservative OCI defaults (no added caps, no_new_privileges, optional limits),
// attach a per-worker netns with default-deny lateral (go-cni bridge + firewall),
// and harvest logs via cio.LogFile. One driver subpackage; the port stays
// container-lib-free. Integration-tested on the Linux lane (FUNCD_IT=1, root).
package containerd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	gocni "github.com/containerd/go-cni"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/runtime"
	"github.com/green-0-rabbit/funcd/internal/runtime/embedimg"
)

// stopGrace is how long Stop waits after SIGTERM before sending SIGKILL.
const stopGrace = 10 * time.Second

// runcShim is the containerd runtime-v2 shim; ociRuntimeBinary selects crun as
// the OCI runtime (C-based — lower per-worker RSS than the Go runc; drop-in
// OCI-compatible via the shim's BinaryName).
const (
	runcShim         = "io.containerd.runc.v2"
	ociRuntimeBinary = "crun"
)

// Config configures the containerd driver (the composition root supplies it).
type Config struct {
	Socket      string // /run/containerd/containerd.sock
	Snapshotter string // overlayfs
	CNIBinDir   string // /opt/cni/bin
	CNIConfDir  string // funcd-written conflist dir
	SubnetCIDR  string // lateral bridge subnet
}

// worker tracks the per-instance bookkeeping the port needs but containerd does
// not retain (the netns path/IP and the log file).
type worker struct {
	ctrID     string
	namespace v1alpha1.NamespaceName
	name      v1alpha1.ObjectName
	replica   int
	cniID     string
	netnsPath string
	ip        string
	port      int // the fixed FUNCD_PORT the shim binds in this netns (ADR-0032); 0 if unset
	logPath   string
	createdAt time.Time
}

type driver struct {
	cfg    Config
	client *containerd.Client
	cni    gocni.CNI

	mu        sync.Mutex
	instances map[runtime.InstanceID]*worker
}

// New connects to containerd and loads the CNI config, returning a Linux
// containerd-backed runtime.Runtime. It self-provisions its CNI before cni.Load (ADR-0056):
// it writes funcd's conflist into CNIConfDir if absent (never clobbering an existing one) and
// enables ip_forward (best-effort — a missing /proc on a non-standard host is logged, not fatal).
func New(cfg Config) (runtime.Runtime, error) {
	const op = "runtime.containerd.New"
	client, err := containerd.New(cfg.Socket)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "connect to containerd at %q", cfg.Socket)
	}
	// Self-provision the CNI (ADR-0056): write the funcd conflist if absent, enable ip_forward —
	// BEFORE cni.Load reads the conflist.
	if err := ensureConflist(cfg.CNIConfDir, cfg.SubnetCIDR); err != nil {
		_ = client.Close()
		return nil, fault.Wrapf(err, fault.Internal, op, "write funcd cni conflist")
	}
	if err := setIPForward("/proc/sys/net/ipv4/ip_forward"); err != nil {
		slog.Warn("could not enable ip_forward; container networking may not route",
			"op", op, "error", err)
	}
	cni, err := gocni.New(
		gocni.WithMinNetworkCount(2),
		gocni.WithPluginConfDir(cfg.CNIConfDir),
		gocni.WithPluginDir([]string{cfg.CNIBinDir}),
	)
	if err != nil {
		_ = client.Close()
		return nil, fault.Wrapf(err, fault.Internal, op, "init cni")
	}
	if err := cni.Load(gocni.WithLoNetwork, gocni.WithDefaultConf); err != nil {
		_ = client.Close()
		return nil, fault.Wrapf(err, fault.Unavailable, op, "load cni config from %q", cfg.CNIConfDir)
	}
	return &driver{cfg: cfg, client: client, cni: cni, instances: map[runtime.InstanceID]*worker{}}, nil
}

func (d *driver) nsCtx(ctx context.Context, ns v1alpha1.NamespaceName) context.Context {
	return namespaces.WithNamespace(ctx, "funcd-"+string(ns))
}

func (d *driver) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	const op = "runtime.containerd.Create"
	if spec.Image == "" {
		return runtime.Instance{}, fault.Invalidf(op, "spec.Image must not be empty for the containerd driver")
	}
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Replica)
	ctrID := fmt.Sprintf("%s-r%d", spec.Name, spec.Replica)
	cniID := fmt.Sprintf("%s-%s-r%d", spec.Namespace, spec.Name, spec.Replica)
	nctx := d.nsCtx(ctx, spec.Namespace)

	image, err := d.resolveImage(nctx, op, spec.Image)
	if err != nil {
		return runtime.Instance{}, err
	}

	// Honor the WorkerSpec contract: an empty LogPath means the driver picks a temp file. The
	// containerd shim's cio.LogFile needs an ABSOLUTE path, so a "" (or relative) LogPath must be
	// resolved here — otherwise the shim rejects the task with `"." must be absolute`.
	logPath := spec.LogPath
	if logPath == "" {
		f, ferr := os.CreateTemp("", fmt.Sprintf("funcd-%s-r%d-*.log", spec.Name, spec.Replica))
		if ferr != nil {
			return runtime.Instance{}, fault.Wrapf(ferr, fault.Internal, op, "create log file")
		}
		logPath = f.Name()
		_ = f.Close()
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return runtime.Instance{}, fault.Wrapf(err, fault.Internal, op, "create log dir")
	}

	container, err := d.client.NewContainer(nctx, ctrID,
		containerd.WithImage(image),
		containerd.WithNewSnapshot(ctrID+"-snap", image),
		containerd.WithRuntime(runcShim, &runcoptions.Options{BinaryName: ociRuntimeBinary}),
		containerd.WithContainerLabels(map[string]string{
			"funcd/namespace": string(spec.Namespace),
			"funcd/name":      string(spec.Name),
			"funcd/replica":   fmt.Sprintf("%d", spec.Replica),
		}),
		containerd.WithNewSpec(ociOpts(spec, image)...),
	)
	if err != nil {
		return runtime.Instance{}, mapErr(err, op, "create container %q", ctrID)
	}

	task, err := container.NewTask(nctx, cio.LogFile(logPath))
	if err != nil {
		_ = container.Delete(nctx, containerd.WithSnapshotCleanup)
		return runtime.Instance{}, mapErr(err, op, "create task for %q", ctrID)
	}

	netnsPath := fmt.Sprintf("/proc/%d/ns/net", task.Pid())
	result, err := d.cni.Setup(nctx, cniID, netnsPath)
	if err != nil {
		_, _ = task.Delete(nctx)
		_ = container.Delete(nctx, containerd.WithSnapshotCleanup)
		return runtime.Instance{}, mapErr(err, op, "attach netns for %q", cniID)
	}

	sb := &worker{
		ctrID: ctrID, namespace: spec.Namespace, name: spec.Name, replica: spec.Replica,
		cniID: cniID, netnsPath: netnsPath, ip: extractIP(result), port: fixedPort(spec),
		logPath: logPath, createdAt: time.Now(),
	}
	d.mu.Lock()
	d.instances[id] = sb
	d.mu.Unlock()

	return runtime.Instance{
		ID: id, Namespace: spec.Namespace, Name: spec.Name, Replica: spec.Replica,
		PID: int(task.Pid()), State: runtime.StateCreated, IP: sb.ip, Port: sb.port, CreatedAt: sb.createdAt,
	}, nil
}

// fixedPort reads the FUNCD_PORT the shim binds (ADR-0032 container mode); 0 if unset.
func fixedPort(spec runtime.WorkerSpec) int {
	if v := spec.Env["FUNCD_PORT"]; v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return 0
}

func (d *driver) Start(ctx context.Context, id runtime.InstanceID) error {
	const op = "runtime.containerd.Start"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return err
	}
	task, err := d.task(nctx, sb)
	if err != nil {
		return mapErr(err, op, "load task %q", sb.ctrID)
	}
	if err := task.Start(nctx); err != nil {
		return mapErr(err, op, "start task %q", sb.ctrID)
	}
	return nil
}

func (d *driver) Stop(ctx context.Context, id runtime.InstanceID) error {
	const op = "runtime.containerd.Stop"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return err
	}
	container, err := d.client.LoadContainer(nctx, sb.ctrID)
	if err != nil {
		return nil // already gone — idempotent
	}
	task, err := container.Task(nctx, nil)
	if err == nil {
		_ = task.Kill(nctx, syscall.SIGTERM)
		select {
		case <-waitTask(nctx, task):
		case <-time.After(stopGrace):
			_ = task.Kill(nctx, syscall.SIGKILL)
			<-waitTask(nctx, task)
		}
		_, _ = task.Delete(nctx)
	}
	_ = d.cni.Remove(nctx, sb.cniID, sb.netnsPath)
	_ = container.Delete(nctx, containerd.WithSnapshotCleanup)
	return nil
}

// Sweep force-removes EVERY container in the namespace's containerd namespace — including ones
// this process did not create. Unlike Stop, which works off the driver's in-process instance map
// (empty in a freshly-started process), Sweep walks containerd directly (client.Containers), so it
// can recover a namespace left dirty by a prior hard-killed run — killing each task (the reconnected
// shim exits, so orphaned containerd-shim-runc-v2 processes are reaped), tearing down CNI
// best-effort from the container labels (an orphan's netns died with its process), and deleting the
// container + its snapshot (the source of the "<id>-snap already exists" collision). It is the
// recovery primitive that makes `funcd bench --containerd` cleanly re-runnable; idempotent.
func (d *driver) Sweep(ctx context.Context, ns v1alpha1.NamespaceName) (int, error) {
	const op = "runtime.containerd.Sweep"
	nctx := d.nsCtx(ctx, ns)
	cs, err := d.client.Containers(nctx)
	if err != nil {
		return 0, mapErr(err, op, "list containers")
	}
	for _, c := range cs {
		if task, terr := c.Task(nctx, nil); terr == nil {
			_ = task.Kill(nctx, syscall.SIGKILL)
			select {
			case <-waitTask(nctx, task):
			case <-time.After(stopGrace):
			}
			_, _ = task.Delete(nctx)
		}
		if labels, lerr := c.Labels(nctx); lerr == nil {
			cniID := fmt.Sprintf("%s-%s-r%s", labels["funcd/namespace"], labels["funcd/name"], labels["funcd/replica"])
			_ = d.cni.Remove(nctx, cniID, "")
		}
		_ = c.Delete(nctx, containerd.WithSnapshotCleanup)
		d.mu.Lock()
		for id, sb := range d.instances {
			if sb.ctrID == c.ID() {
				delete(d.instances, id)
			}
		}
		d.mu.Unlock()
	}
	return len(cs), nil
}

func (d *driver) Status(ctx context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	const op = "runtime.containerd.Status"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return runtime.Instance{}, err
	}
	inst := runtime.Instance{
		ID: id, Namespace: sb.namespace, Name: sb.name, Replica: sb.replica,
		IP: sb.ip, Port: sb.port, State: runtime.StateCreated, CreatedAt: sb.createdAt,
	}
	task, err := d.task(nctx, sb)
	if err != nil {
		inst.State = runtime.StateStopped
		return inst, nil
	}
	inst.PID = int(task.Pid())
	st, err := task.Status(nctx)
	if err != nil {
		return inst, nil
	}
	inst.State = mapState(st.Status)
	return inst, nil
}

func (d *driver) Logs(ctx context.Context, id runtime.InstanceID) (io.ReadCloser, error) {
	const op = "runtime.containerd.Logs"
	sb, _, err := d.lookup(ctx, id, op)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(sb.logPath) //nolint:gosec // logPath is driver-owned
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "open log file")
	}
	return f, nil
}

func (d *driver) Exec(ctx context.Context, id runtime.InstanceID, cmd []string) error {
	const op = "runtime.containerd.Exec"
	if len(cmd) == 0 {
		return fault.Invalidf(op, "exec command must not be empty")
	}
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return err
	}
	task, err := d.task(nctx, sb)
	if err != nil {
		return mapErr(err, op, "load task %q", sb.ctrID)
	}
	pspec := &specs.Process{Args: cmd, Cwd: "/"}
	proc, err := task.Exec(nctx, fmt.Sprintf("exec-%d", time.Now().UnixNano()), pspec, cio.NullIO)
	if err != nil {
		return mapErr(err, op, "exec %v", cmd)
	}
	defer func() { _, _ = proc.Delete(nctx) }()
	if err := proc.Start(nctx); err != nil {
		return mapErr(err, op, "start exec %v", cmd)
	}
	status := <-waitProc(nctx, proc)
	if status != 0 {
		return fault.Internalf(op, "exec %v exited with code %d", cmd, status)
	}
	return nil
}

func (d *driver) List(ctx context.Context, ns v1alpha1.NamespaceName) ([]runtime.Instance, error) {
	d.mu.Lock()
	sbs := make([]*worker, 0, len(d.instances))
	ids := make([]runtime.InstanceID, 0, len(d.instances))
	for id, sb := range d.instances {
		if sb.namespace == ns {
			sbs = append(sbs, sb)
			ids = append(ids, id)
		}
	}
	d.mu.Unlock()

	out := make([]runtime.Instance, 0, len(sbs))
	for i, sb := range sbs {
		inst := runtime.Instance{
			ID: ids[i], Namespace: sb.namespace, Name: sb.name, Replica: sb.replica,
			IP: sb.ip, Port: sb.port, State: runtime.StateStopped, CreatedAt: sb.createdAt,
		}
		// Reflect the real task status (ADR-0032): never hardcode Running, or a
		// crashed/exited container would mask the shim's shape failure (ADR-0030).
		nctx := d.nsCtx(ctx, sb.namespace)
		if task, err := d.task(nctx, sb); err == nil {
			inst.PID = int(task.Pid())
			if st, serr := task.Status(nctx); serr == nil {
				inst.State = mapState(st.Status)
			}
		}
		out = append(out, inst)
	}
	return out, nil
}

func (d *driver) Close() error {
	return d.client.Close()
}

// lookup resolves an instance id to its worker + namespaced context.
func (d *driver) lookup(ctx context.Context, id runtime.InstanceID, op string) (*worker, context.Context, error) {
	d.mu.Lock()
	sb, ok := d.instances[id]
	d.mu.Unlock()
	if !ok {
		return nil, nil, fault.NotFoundf(op, "instance %q not found", id)
	}
	return sb, d.nsCtx(ctx, sb.namespace), nil
}

func (d *driver) task(nctx context.Context, sb *worker) (containerd.Task, error) {
	container, err := d.client.LoadContainer(nctx, sb.ctrID)
	if err != nil {
		return nil, err
	}
	return container.Task(nctx, nil)
}

func (d *driver) snapshotter() string {
	if d.cfg.Snapshotter == "" {
		return "overlayfs"
	}
	return d.cfg.Snapshotter
}

// resolveImage makes the requested image available in the function's OWN containerd namespace
// (nctx already carries it). containerd images are per-namespace, so the driver — not the
// Manager — owns availability: it first looks the image up in this namespace; if it is absent
// and a curated embedded tar exists for the ref, it imports the embed into this namespace (NO
// registry pull — ADR-0054) and unpacks it; only a non-curated ref (an ImageOverride pointing
// at a real registry) falls back to a Pull.
func (d *driver) resolveImage(nctx context.Context, op, ref string) (containerd.Image, error) {
	image, err := d.client.GetImage(nctx, ref)
	if err == nil {
		return image, nil // already present in this namespace
	}
	if !errdefs.IsNotFound(err) {
		return nil, mapErr(err, op, "look up image %q", ref)
	}

	if tar, ok := embedimg.TarForImageRef(ref); ok {
		// Import brings the curated tar into nctx's namespace. Our curated tars carry exactly ONE
		// image (built --provenance=false --sbom=false), so use the returned image directly — the
		// tar names it "docker.io/funcd/runtime-<rt>:latest", which will not match an exact lookup
		// of spec.Image ("funcd/runtime-<rt>:latest").
		imported, ierr := d.client.Import(nctx, tar)
		if ierr != nil {
			return nil, mapErr(ierr, op, "import embedded image for %q", ref)
		}
		if len(imported) == 0 {
			return nil, fault.Internalf(op, "embedded tar for %q produced no image", ref)
		}
		image = containerd.NewImage(d.client, imported[0])
		// Import does NOT unpack into the snapshotter (Pull did, via WithPullUnpack); do it now so
		// NewContainer's snapshot has a parent.
		if uerr := image.Unpack(nctx, d.snapshotter()); uerr != nil {
			return nil, mapErr(uerr, op, "unpack embedded image for %q", ref)
		}
		return image, nil
	}

	// Non-curated ref (ImageOverride at a real registry): pull it.
	image, err = d.client.Pull(nctx, ref, containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(d.snapshotter()))
	if err != nil {
		return nil, mapErr(err, op, "pull image %q", ref)
	}
	return image, nil
}

// ociOpts builds the conservative OCI spec for a worker.
func ociOpts(spec runtime.WorkerSpec, image containerd.Image) []oci.SpecOpts {
	opts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithHostname(string(spec.Name)),
		oci.WithEnv(envSlice(spec.Env)),
		oci.WithNoNewPrivileges,
		withAbsoluteCwd, // crun rejects a non-absolute cwd; WithImageConfig leaves it "" for a WORKDIR-less image
	}
	if len(spec.Command) > 0 {
		opts = append(opts, oci.WithProcessArgs(spec.Command...))
	}
	if len(spec.Mounts) > 0 {
		opts = append(opts, oci.WithMounts(bindMounts(spec.Mounts)))
	}
	if spec.Limits.MemoryBytes > 0 {
		opts = append(opts, oci.WithMemoryLimit(uint64(spec.Limits.MemoryBytes)))
	}
	if spec.Limits.CPUs > 0 {
		const period = 100000
		opts = append(opts, oci.WithCPUCFS(int64(spec.Limits.CPUs*period), period))
	}
	return opts
}

// withAbsoluteCwd defaults the OCI process cwd to "/" when the image declares no WORKDIR.
// containerd's WithImageConfig copies the image's (possibly empty) WorkingDir over the spec's
// "/" default; crun — unlike runc — rejects a non-absolute cwd ("." must be absolute), so a
// curated image without a WORKDIR would fail to start. (Surfaced by the ADR-0052 footprint lane.)
func withAbsoluteCwd(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
	if s.Process != nil && (s.Process.Cwd == "" || s.Process.Cwd == ".") {
		s.Process.Cwd = "/"
	}
	return nil
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// bindMounts maps spec Mounts to OCI bind mounts (ADR-0032): the artifact is delivered
// read-only into the curated-image worker.
func bindMounts(mounts []runtime.Mount) []specs.Mount {
	out := make([]specs.Mount, 0, len(mounts))
	for _, m := range mounts {
		opts := []string{"rbind"}
		if m.ReadOnly {
			opts = append(opts, "ro")
		} else {
			opts = append(opts, "rw")
		}
		out = append(out, specs.Mount{
			Destination: m.Target,
			Type:        "bind",
			Source:      m.Source,
			Options:     opts,
		})
	}
	return out
}

// extractIP returns the container's own routable IPv4 — the address its shim is reachable on
// (eth0 in the sandbox netns). gocni.Result.Interfaces is a map (randomized iteration) that
// holds EVERY interface the Setup touched: the container's sandbox eth0 (Sandbox != ""), the
// sandbox loopback "lo" (127.0.0.1, contributed by gocni.WithLoNetwork), and the host-side
// bridge/veth ends (Sandbox == ""). Returning the first IP from that map (the old behavior)
// picked a non-deterministic winner — on a fraction of containers the loopback 127.0.0.1 (or a
// host-side interface) won, so the reconciler probed an address nothing serves on and the
// function never reached Ready. Select deterministically: a sandbox interface (Sandbox != "")
// that is NOT loopback, preferring IPv4. (ADR-0032: the shim binds 0.0.0.0:FUNCD_PORT in this
// netns; the reconciler must probe the sandbox eth0 address, never lo or the gateway.)
func extractIP(result *gocni.Result) string {
	if result == nil {
		return ""
	}
	var fallback string
	for name, iface := range result.Interfaces {
		if iface == nil || iface.Sandbox == "" || name == "lo" {
			continue // skip host-side ends (no sandbox) and the sandbox loopback
		}
		for _, ipc := range iface.IPConfigs {
			ip := ipc.IP
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip.To4() != nil {
				return ip.String() // a sandbox IPv4 — the shim's reachable address
			}
			if fallback == "" {
				fallback = ip.String() // an IPv6 sandbox address, only if no IPv4 is found
			}
		}
	}
	return fallback
}
