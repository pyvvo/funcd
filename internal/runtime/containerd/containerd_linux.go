//go:build linux

// Package containerd implements the runtime.Runtime port over containerd + runc on
// Linux (ADR-0011): pull a curated runtime image, run it as a runc container with
// conservative OCI defaults (no added caps, no_new_privileges, optional limits),
// attach a per-worker netns with default-deny lateral (go-cni bridge + firewall),
// and read each task's stdout and stderr through FIFOs (ADR-0168). One driver subpackage; the port stays
// container-lib-free. Integration-tested on the Linux lane (FUNCD_IT=1, root).
package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/embedimg"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// stopGrace is how long Stop waits after SIGTERM before sending SIGKILL.
const stopGrace = 10 * time.Second

// closeGrace is Close's SIGTERM grace: shorter than stopGrace so the stop fits Shutdown's closeTimeout and the log
// Routes and telemetry still flush after it (issue #453).
const closeGrace = 3 * time.Second

// nsPrefix prefixes a funcd namespace's containerd namespace.
const nsPrefix = "funcd-"

// runcShim is the containerd runtime-v2 shim; ociRuntimeBinary selects crun as
// the OCI runtime (C-based — lower per-worker RSS than the Go runc; drop-in
// OCI-compatible via the shim's BinaryName).
const (
	runcShim         = "io.containerd.runc.v2"
	ociRuntimeBinary = "crun"
)

// containerBootDir is where a worker's boot dir is mounted; the shim writes its port to FUNCD_PORTFILE in it (ADR-0160).
const containerBootDir = "/run/funcd-boot"

// ownerKindLabel names the container label that keeps the kind whose reconciler created the worker (ADR-0152).
const ownerKindLabel = "funcd/owner-kind"

// Config configures the containerd driver (the composition root supplies it).
type Config struct {
	Socket      string       // /run/containerd/containerd.sock
	Snapshotter string       // overlayfs
	CNIBinDir   string       // /opt/cni/bin
	CNIConfDir  string       // funcd-written conflist dir
	StateDir    string       // funcd-owned dir for runtime-generated worker files (e.g. resolv.conf); dataDir-relative
	SubnetCIDR  string       // lateral bridge subnet
	Logger      *slog.Logger // nil ⇒ slog.Default()

	// Pullable reports whether an image ref that is not embedded may be pulled (ctrmanager.Config.Pullable);
	// nil ⇒ none may.
	Pullable func(ref string) bool

	// Private marks Socket as funcd's private managed containerd (ADR-0054): no other owner runs workers in it, so
	// Close may sweep every funcd namespace in it. An external containerd may hold another daemon's or a bench's
	// workers (ADR-0055), so there Close stops only the workers this driver runs.
	Private bool
}

// worker tracks the per-instance bookkeeping the port needs but containerd does
// not retain (the netns path/IP and the task's output).
type worker struct {
	ctrID     string
	namespace v1alpha1.NamespaceName
	name      v1alpha1.ObjectName
	ownerKind v1alpha1.Kind
	revision  v1alpha1.ObjectName
	replica   int
	cniID     string
	netnsPath string
	ip        string
	port      int                // the fixed FUNCD_PORT the shim binds in this netns (ADR-0032); 0 if unset
	out       *workerpipe.Output // the task's stdout and stderr, through FIFOs under the driver's fifoDir (ADR-0168)
	taskIO    cio.IO             // the NewTask task's IO; Close removes its FIFO dir
	createdAt time.Time
	released  bool // Stop has released the task, the CNI attachment and the container, so Remove may forget it
	bootDir   string
	listened  bool // the port file was seen since Create (ADR-0160)
	stopping  bool // Stop has begun, so an ended task is ExitByStop

	// Path B structured-log channel (ADR-0081): a per-instance host UDS bind-mounted into the
	// sandbox; the shim connects and writes NDJSON, the accept loop hands each conn to the hook.
	logListener net.Listener
	logDir      string
}

type driver struct {
	cfg        Config
	client     *containerd.Client
	cni        gocni.CNI
	resolvPath string // host path to the shared worker /etc/resolv.conf (bind-mounted into every worker)
	bootRoot   string // 0700 parent of the per-worker boot dirs (ADR-0160)
	netns      netnsPins
	mu         sync.Mutex

	capture   runtime.LogCaptureFunc    // optional Path B hook (runtime.LogCapturer, ADR-0081); nil = disabled
	outputs   runtime.OutputCaptureFunc // optional Path A hook (runtime.OutputCapturer, ADR-0168); nil = tail only
	instances map[runtime.InstanceID]*worker
	fifoDir   string // the parent of each task's FIFO dir (ADR-0168)
	ownFifo   bool   // fifoDir is a private temp dir, which Close removes
}

// outputWait bounds how long Stop and a terminal worker's Logs wait for the run's last output (ADR-0168).
const outputWait = time.Second

// SetOutputCapture installs the per-run raw-output hook (runtime.OutputCapturer, ADR-0168); Create calls it after the
// network setup, before the worker is registered and can start.
func (d *driver) SetOutputCapture(fn runtime.OutputCaptureFunc) {
	d.mu.Lock()
	d.outputs = fn
	d.mu.Unlock()
}

// makeFifoDir makes <stateDir>/fifo, emptied of a previous run's FIFOs, or a private temp dir when stateDir is unset
// (ADR-0168).
func makeFifoDir(stateDir string) (string, bool, error) {
	const op = "runtime.containerd.New"
	if stateDir == "" {
		dir, err := os.MkdirTemp("", "funcd-fifo-")
		if err != nil {
			return "", false, fault.Wrapf(err, fault.Internal, op, "create fifo dir")
		}
		return dir, true, nil
	}
	dir := filepath.Join(stateDir, "fifo")
	if err := os.RemoveAll(dir); err != nil {
		return "", false, fault.Wrapf(err, fault.Internal, op, "empty fifo dir %q", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fault.Wrapf(err, fault.Internal, op, "create fifo dir %q", dir)
	}
	return dir, false, nil
}

// SetLogCapture installs the per-instance structured-log hook (runtime.LogCapturer, ADR-0081). When
// set, Create gives each sandbox a bind-mounted UDS (FUNCD_LOG_SOCK) the shim writes NDJSON to; the
// accept loop hands each connection to the hook, up to the instance's bound (logConnBound), and closes the rest.
func (d *driver) SetLogCapture(fn runtime.LogCaptureFunc) {
	d.mu.Lock()
	d.capture = fn
	d.mu.Unlock()
}

// setupLogChannel creates the per-instance host UDS + accept loop and returns the OCI mount + the
// augmented env (FUNCD_LOG_SOCK at /run/funcd-log/log.sock). The caller stores ln/dir on the worker
// for teardown. capture must be non-nil.
func setupLogChannel(ctrID string, spec runtime.WorkerSpec, capture runtime.LogCaptureFunc) (mount runtime.Mount, env map[string]string, ln net.Listener, dir string, err error) {
	const op = "runtime.containerd.setupLogChannel"
	dir, err = os.MkdirTemp("", "funcd-log-"+ctrID+"-")
	if err != nil {
		return runtime.Mount{}, nil, nil, "", fault.Wrapf(err, fault.Internal, op, "create log dir")
	}
	sock := filepath.Join(dir, "log.sock")
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(dir)
		return runtime.Mount{}, nil, nil, "", fault.Wrapf(err, fault.Internal, op, "listen on log socket")
	}
	// The distroless sandbox runs as a non-root uid; let it connect to the node-local per-instance socket.
	_ = os.Chmod(sock, 0o777) //nolint:gosec // node-local, per-instance ephemeral log socket (ADR-0081)

	go acceptLogConns(ul, spec, capture)

	env = map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
	}
	env["FUNCD_LOG_SOCK"] = "/run/funcd-log/log.sock"
	return runtime.Mount{Source: dir, Target: "/run/funcd-log", ReadOnly: false}, env, ul, dir, nil
}

// soloLogConns bounds the live connections on a solo worker's log socket. The sandbox can dial it at will and each
// captured connection holds a daemon-side reader of up to funclog.MaxLineBytes; a shim opens one per process, so a
// few leave room for a forked process or a reconnect while the old connection drains.
const soloLogConns = 4

// logConnBound is the most live log connections spec's instance keeps: a pool shim opens one more per member.
func logConnBound(spec runtime.WorkerSpec) int {
	return soloLogConns + max(spec.Members, 0)
}

// acceptLogConns hands each connection on ln to capture until ln is closed on teardown. A connection beyond
// logConnBound live ones is closed at once. A failed Accept (EMFILE while the daemon is out of fds) is retried after a
// backoff, so it does not end the instance's log capture.
func acceptLogConns(ln *net.UnixListener, spec runtime.WorkerSpec, capture runtime.LogCaptureFunc) {
	slots := make(chan struct{}, logConnBound(spec))
	var backoff time.Duration
	for {
		conn, err := ln.AcceptUnix()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		select {
		case slots <- struct{}{}:
			capture(spec, &logConn{UnixConn: conn, release: func() { <-slots }})
		default:
			_ = conn.Close()
		}
	}
}

// logConn frees its slot on its first Close. It embeds *net.UnixConn, so the daemon's drain can still
// CloseRead it.
type logConn struct {
	*net.UnixConn
	once    sync.Once
	release func()
}

func (c *logConn) Close() error {
	err := c.UnixConn.Close()
	c.once.Do(c.release)
	return err
}

// New connects to containerd and loads the CNI config, returning a Linux
// containerd-backed runtime.Runtime. It self-provisions its CNI before cni.Load (ADR-0056):
// it writes funcd's conflist into CNIConfDir if absent (never clobbering an existing one) and
// enables ip_forward (best-effort — a missing /proc on a non-standard host is logged, not fatal).
func New(cfg Config) (runtime.Runtime, error) {
	const op = "runtime.containerd.New"
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
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
		cfg.Logger.Warn("could not enable ip_forward; container networking may not route",
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
	resolvPath, err := writeWorkerResolv(cfg.StateDir, cfg.SubnetCIDR)
	if err != nil {
		_ = client.Close()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "provision worker resolv.conf")
	}
	bootRoot, err := makeStateDir(cfg.StateDir, "boot")
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	netnsDir, err := makeStateDir(cfg.StateDir, "netns")
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	fifoDir, ownFifo, err := makeFifoDir(cfg.StateDir)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &driver{cfg: cfg, client: client, cni: cni, resolvPath: resolvPath, bootRoot: bootRoot, netns: newNetnsPins(netnsDir),
		fifoDir: fifoDir, ownFifo: ownFifo, instances: map[runtime.InstanceID]*worker{}}, nil
}

// makeStateDir makes <stateDir>/<name>, or a private temp dir when stateDir is unset, and enforces 0700 on it, so no
// host user but root reaches a dir the sandbox writes (ADR-0160) or a worker's netns pin.
func makeStateDir(stateDir, name string) (string, error) {
	const op = "runtime.containerd.New"
	if stateDir == "" {
		dir, err := os.MkdirTemp("", "funcd-"+name+"-")
		if err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "create %s dir", name)
		}
		return dir, nil
	}
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create %s dir %q", name, dir)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return "", fault.Internalf(op, "%s dir %q is not a directory", name, dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "restrict %s dir %q", name, dir)
	}
	return dir, nil
}

// makeBootDir makes worker ctrID's boot dir under 0700 parents, removing an earlier one first. Only the leaf is 0777,
// so the sandbox's non-root uid can write its port file there.
func (d *driver) makeBootDir(ns v1alpha1.NamespaceName, ctrID string) (string, error) {
	const op = "runtime.containerd.Create"
	parent := filepath.Join(d.bootRoot, nsPrefix+string(ns))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create boot dir parent %q", parent)
	}
	dir := filepath.Join(parent, ctrID)
	if err := os.RemoveAll(dir); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "remove earlier boot dir %q", dir)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create boot dir %q", dir)
	}
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // the sandbox uid writes its port here; the 0700 parents keep host users out
		_ = os.RemoveAll(dir)
		return "", fault.Wrapf(err, fault.Internal, op, "open boot dir %q to the sandbox", dir)
	}
	return dir, nil
}

// writeWorkerResolv writes the shared /etc/resolv.conf funcd bind-mounts into every worker, pointing at
// the funcd0 gateway (the subnet's .1) on :53. Raw containerd — unlike CRI/kubelet — provisions NO
// resolver, so without this a worker has no /etc/resolv.conf and cannot resolve any domain (the egress
// e2e caught this: all domain EgressPolicy rules were dead because workers never sent DNS). With egress
// enabled, the worker's :53 is REDIRECTed into funcd's DNS forwarder (ADR-0117), so the nameserver value
// is just the redirect entry point — the always-present gateway is the natural target. Returns the host
// path to bind-mount read-only at the worker's /etc/resolv.conf.
func writeWorkerResolv(stateDir, subnetCIDR string) (string, error) {
	const op = "runtime.containerd.writeWorkerResolv"
	_, ipnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return "", fault.Wrapf(err, fault.Invalid, op, "parse subnet %q", subnetCIDR)
	}
	gw := make(net.IP, len(ipnet.IP))
	copy(gw, ipnet.IP)
	gw[len(gw)-1] |= 1 // the subnet's .1 — the funcd0 bridge gateway (isGateway host-local IPAM)
	dir := stateDir
	if dir == "" {
		dir = os.TempDir() // fallback for callers that don't set StateDir (e.g. the bench harness)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // funcd state dir, world-readable is fine
		return "", fault.Wrapf(err, fault.Internal, op, "create state dir %q", dir)
	}
	path := filepath.Join(dir, "worker-resolv.conf")
	body := fmt.Sprintf("nameserver %s\noptions timeout:2 attempts:2\n", gw.String())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // world-readable resolv.conf is expected
		return "", fault.Wrapf(err, fault.Internal, op, "write %q", path)
	}
	return path, nil
}

func (d *driver) nsCtx(ctx context.Context, ns v1alpha1.NamespaceName) context.Context {
	return namespaces.WithNamespace(ctx, nsPrefix+string(ns))
}

func (d *driver) Create(ctx context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	const op = "runtime.containerd.Create"
	if spec.Image == "" {
		return runtime.Instance{}, fault.Invalidf(op, "spec.Image must not be empty for the containerd driver")
	}
	if spec.OwnerKind == "" {
		return runtime.Instance{}, fault.Invalidf(op, "spec.OwnerKind must not be empty")
	}
	if d.bootRoot == "" {
		return runtime.Instance{}, fault.Internalf(op, "the driver has no boot root")
	}
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)
	d.mu.Lock()
	held := d.instances[id]
	d.mu.Unlock()
	if held != nil && held.ownerKind != spec.OwnerKind {
		return runtime.Instance{}, fault.Conflictf(op, "instance %q is held by a %s worker", id, held.ownerKind)
	}
	ctrID, cniID := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), strconv.Itoa(spec.Replica))
	nctx := d.nsCtx(ctx, spec.Namespace)

	image, err := d.resolveImage(nctx, op, spec.Image)
	if err != nil {
		return runtime.Instance{}, err
	}

	var logLn net.Listener
	var logDir, bootDir string
	var out *workerpipe.Output
	var taskIO cio.IO
	success := false
	defer func() {
		if !success { // the worker is never registered, so no Remove or Stop would free these
			closeLogChannel(&worker{logListener: logLn, logDir: logDir})
			if taskIO != nil {
				taskIO.Cancel()
				_ = taskIO.Close()
			}
			if out != nil {
				out.Close()
			}
			if bootDir != "" {
				_ = os.RemoveAll(bootDir)
			}
		}
	}()

	// Path B structured-log channel (ADR-0081): when a capture hook is set, give the sandbox a
	// bind-mounted UDS (FUNCD_LOG_SOCK) the shim writes NDJSON to. Torn down on any failure below.
	d.mu.Lock()
	capture := d.capture
	d.mu.Unlock()
	if capture != nil {
		mount, env, ln, dir, lerr := setupLogChannel(ctrID, spec, capture)
		if lerr != nil {
			return runtime.Instance{}, lerr
		}
		spec.Env = env
		spec.Mounts = append(spec.Mounts, mount)
		logLn, logDir = ln, dir
	}

	// Give the worker a resolver: raw containerd provisions no /etc/resolv.conf, so without this the
	// worker cannot resolve any domain (and every domain EgressPolicy rule is dead). Bind-mount the
	// shared funcd resolv.conf (nameserver = the funcd0 gateway; with egress on, :53 is REDIRECTed into
	// the DNS forwarder — ADR-0117). Read-only.
	if d.resolvPath != "" {
		spec.Mounts = append(spec.Mounts, runtime.Mount{Source: d.resolvPath, Target: "/etc/resolv.conf", ReadOnly: true})
	}

	labels := map[string]string{
		"funcd/namespace": string(spec.Namespace),
		"funcd/name":      string(spec.Name),
		"funcd/replica":   fmt.Sprintf("%d", spec.Replica),
		ownerKindLabel:    string(spec.OwnerKind),
	}
	if spec.Revision != "" {
		labels["funcd/revision"] = string(spec.Revision)
	}
	if err := d.reclaim(nctx, op, id, ctrID, spec.OwnerKind); err != nil {
		return runtime.Instance{}, err
	}
	if bootDir, err = d.makeBootDir(spec.Namespace, ctrID); err != nil {
		return runtime.Instance{}, err
	}
	env := make(map[string]string, len(spec.Env)+1)
	maps.Copy(env, spec.Env)
	env["FUNCD_PORTFILE"] = containerBootDir + "/port"
	spec.Env = env
	container, err := d.client.NewContainer(nctx, ctrID,
		containerd.WithImage(image),
		containerd.WithSnapshotter(d.snapshotter()),
		containerd.WithNewSnapshot(ctrID+"-snap", image),
		containerd.WithRuntime(runcShim, &runcoptions.Options{BinaryName: ociRuntimeBinary}),
		containerd.WithContainerLabels(labels),
		containerd.WithNewSpec(ociOpts(spec, image, bootDir)...),
	)
	if err != nil {
		return runtime.Instance{}, mapErr(err, op, "create container %q", ctrID)
	}

	// Raw stdout and stderr (ADR-0168): two FIFOs under fifoDir that the containerd client copies into out as written.
	out = workerpipe.New(workerpipe.Options{Logger: d.cfg.Logger.With(
		"namespace", spec.Namespace, "name", spec.Name, "revision", spec.Revision, "replica", spec.Replica)})
	creator := cio.NewCreator(cio.WithStreams(nil, out.Writer(workerpipe.Stdout), out.Writer(workerpipe.Stderr)), cio.WithFIFODir(d.fifoDir))
	task, err := container.NewTask(nctx, func(id string) (cio.IO, error) {
		created, cerr := creator(id)
		taskIO = created
		return created, cerr
	})
	if err != nil {
		taskIO = nil // NewTask closes the IO it made when it fails
		_ = container.Delete(nctx, containerd.WithSnapshotCleanup)
		return runtime.Instance{}, mapErr(err, op, "create task for %q", ctrID)
	}

	netnsPath, err := d.netns.pin(cniID, task.Pid())
	var result *gocni.Result
	if err == nil {
		if result, err = d.cni.Setup(nctx, cniID, netnsPath); err != nil {
			// Setup does not undo the plugins that succeeded and the CNI spec leaves that DEL to the runtime, so without
			// it the host-local lease and the masquerade rules outlive the failed Create.
			if rerr := d.cni.Remove(nctx, cniID, netnsPath); rerr != nil {
				d.cfg.Logger.Warn("could not release the CNI attachment of a failed network setup",
					"op", op, "attachment", cniID, "error", rerr)
			}
			d.netns.unpin(netnsPath)
		}
	}
	if err != nil {
		// containerd deletes neither a created task that has a pid nor its container until the task is killed.
		_, _ = task.Delete(nctx, containerd.WithProcessKill)
		_ = container.Delete(nctx, containerd.WithSnapshotCleanup)
		return runtime.Instance{}, mapErr(err, op, "attach netns for %q", cniID)
	}

	// Path A hook (ADR-0168): after the network setup, before registration, so it runs before the task can write.
	d.mu.Lock()
	outputs := d.outputs
	d.mu.Unlock()
	if outputs != nil {
		outputs(spec, out)
	}
	sb := &worker{
		ctrID: ctrID, namespace: spec.Namespace, name: spec.Name, ownerKind: spec.OwnerKind, revision: spec.Revision,
		replica: spec.Replica, cniID: cniID, netnsPath: netnsPath, ip: extractIP(result), port: fixedPort(spec),
		out: out, taskIO: taskIO, createdAt: time.Now(),
		logListener: logLn, logDir: logDir, bootDir: bootDir,
	}
	// The run's output ends when the task's FIFOs close: then the last lines are queued and the FIFO dir goes.
	go func() {
		taskIO.Wait()
		out.Close()
		_ = taskIO.Close()
	}()
	d.mu.Lock()
	d.instances[id] = sb
	d.mu.Unlock()

	success = true // keep the output and the channel; Stop and Remove own their teardown now
	return runtime.Instance{
		ID: id, Namespace: spec.Namespace, Name: spec.Name, OwnerKind: spec.OwnerKind, Revision: spec.Revision,
		Replica: spec.Replica, PID: int(task.Pid()), State: runtime.StateCreated, IP: sb.ip, Port: sb.port, CreatedAt: sb.createdAt,
	}, nil
}

// reclaim deletes the container and snapshot that hold a worker's name when this driver runs no worker under it.
// containerd keeps both when funcd stops or dies, and the restarted driver starts with no instances, so without this
// every re-create of the worker fails with "already exists". A leftover labelled with another kind is kept and refused
// with fault.Conflict; an unlabelled one, from before ADR-0152, is reclaimed. An ID this driver still runs is
// fault.Conflict, before Create touches its boot dir (ADR-0160).
func (d *driver) reclaim(nctx context.Context, op string, id runtime.InstanceID, ctrID string, kind v1alpha1.Kind) error {
	d.mu.Lock()
	sb, ok := d.instances[id]
	live := ok && !sb.released
	d.mu.Unlock()
	if live {
		return fault.Conflictf(op, "instance %q already exists", id)
	}
	c, err := d.client.LoadContainer(nctx, ctrID)
	switch {
	case err == nil:
		labels, lerr := c.Labels(nctx)
		if lerr != nil {
			return mapErr(lerr, op, "read leftover container %q labels", ctrID)
		}
		if owner := labels[ownerKindLabel]; owner != "" && owner != string(kind) {
			return fault.Conflictf(op, "leftover container %q belongs to a %s worker", ctrID, owner)
		}
		if derr := d.discard(nctx, c); derr != nil {
			return mapErr(derr, op, "delete leftover container %q", ctrID)
		}
	case !errdefs.IsNotFound(err):
		return mapErr(err, op, "load leftover container %q", ctrID)
	}
	if err := d.client.SnapshotService(d.snapshotter()).Remove(nctx, ctrID+"-snap"); err != nil && !errdefs.IsNotFound(err) {
		return mapErr(err, op, "remove leftover snapshot %q", ctrID+"-snap")
	}
	return nil
}

// workerNames returns a worker's container ID and CNI attachment ID (ADR-0143, ADR-0179). A revisioned worker joins its
// parts with '.', which a DNS-1123 name cannot contain, so a revisioned name never equals an unrevisioned one. The CNI ID
// is node-global: without a revision '_', which a DNS-1123 name cannot contain either, ends the namespace.
func workerNames(ns, name, revision, replica string) (ctrID, cniID string) {
	if revision == "" {
		return name + "-r" + replica, ns + "_" + name + "-r" + replica
	}
	return revision + ".r" + replica, ns + "." + revision + ".r" + replica
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
	return d.stop(ctx, id, stopGrace)
}

// stop sends the worker's task SIGTERM, then SIGKILL after grace, and releases the worker once its container is gone.
// It keeps the worker and returns the error when it cannot load the task or delete the container, since the worker may
// still run.
func (d *driver) stop(ctx context.Context, id runtime.InstanceID, grace time.Duration) error {
	const op = "runtime.containerd.Stop"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return err
	}
	d.mu.Lock()
	sb.stopping = true
	d.mu.Unlock()
	defer func() { _ = os.RemoveAll(sb.bootDir) }()
	container, err := d.client.LoadContainer(nctx, sb.ctrID)
	if errdefs.IsNotFound(err) {
		if d.netns.pinned(sb.cniID) != "" {
			d.release(nctx, sb.cniID)
		}
		closeOutput(sb)
		d.markReleased(sb)
		return nil // already gone — idempotent
	}
	if err != nil {
		return mapErr(err, op, "load container %q", sb.ctrID)
	}
	task, err := container.Task(nctx, nil)
	if err != nil && !errdefs.IsNotFound(err) {
		return mapErr(err, op, "load task %q", sb.ctrID)
	}
	// Without the worker's netns the bridge plugin's DEL frees the IP but not the masquerade rules. A /proc netns goes
	// when the task exits; the pin keeps it for a task that already exited until the DEL is sent.
	_ = d.cni.Remove(nctx, sb.cniID, sb.netnsPath)
	d.netns.unpin(sb.netnsPath)
	if err == nil {
		_ = task.Kill(nctx, syscall.SIGTERM)
		select {
		case <-waitTask(nctx, task):
		case <-time.After(grace):
			_ = task.Kill(nctx, syscall.SIGKILL)
			<-waitTask(nctx, task)
		}
		if sb.out != nil {
			select {
			case <-sb.out.Done():
			case <-time.After(outputWait):
			}
		}
		_, _ = task.Delete(nctx)
	}
	if err := container.Delete(nctx, containerd.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return mapErr(err, op, "delete container %q", sb.ctrID)
	}
	closeLogChannel(sb) // close the Path B UDS listener + remove its dir (ADR-0081)
	closeOutput(sb)
	d.markReleased(sb)
	return nil
}

// closeOutput closes a stopped worker's task IO, which removes its FIFO dir (ADR-0168); the waiter then closes its
// Output. Idempotent / nil-safe.
func closeOutput(sb *worker) {
	if sb.taskIO != nil {
		_ = sb.taskIO.Close()
	}
}

func (d *driver) markReleased(sb *worker) {
	d.mu.Lock()
	sb.released = true
	d.mu.Unlock()
}

// Remove forgets an instance Stop has released (ADR-0143).
func (d *driver) Remove(_ context.Context, id runtime.InstanceID) error {
	d.mu.Lock()
	sb, ok := d.instances[id]
	if !ok {
		d.mu.Unlock()
		return nil
	}
	if !sb.released {
		d.mu.Unlock()
		return fault.Conflictf("runtime.containerd.Remove", "instance %q has not been stopped", id)
	}
	delete(d.instances, id)
	d.mu.Unlock()
	return nil
}

// closeLogChannel tears down a worker's Path B log channel (ADR-0081): closes the accept loop's
// listener and removes the per-instance socket dir. Idempotent / nil-safe.
func closeLogChannel(sb *worker) {
	if sb.logListener != nil {
		_ = sb.logListener.Close()
	}
	if sb.logDir != "" {
		_ = os.RemoveAll(sb.logDir)
	}
}

// Sweep force-removes EVERY container in the namespace's containerd namespace — including ones
// this process did not create. Unlike Stop, which works off the driver's in-process instance map
// (empty in a freshly-started process), Sweep walks containerd directly (client.Containers), so it
// can recover a namespace left dirty by a prior hard-killed run — killing each task (the reconnected
// shim exits, so orphaned containerd-shim-runc-v2 processes are reaped), tearing down CNI
// best-effort from the container labels (an orphan's netns died with its process), and deleting the
// container + its snapshot (the source of the "<id>-snap already exists" collision). It is the
// recovery primitive that makes `funcd bench --containerd` cleanly re-runnable; idempotent. It reports every
// container it could not discard.
func (d *driver) Sweep(ctx context.Context, ns v1alpha1.NamespaceName) (int, error) {
	const op = "runtime.containerd.Sweep"
	nctx := d.nsCtx(ctx, ns)
	cs, err := d.client.Containers(nctx)
	if err != nil {
		return 0, mapErr(err, op, "list containers")
	}
	var errs []error
	for _, c := range cs {
		if err := d.discard(nctx, c); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, mapErr(err, op, "discard container %q", c.ID()))
		}
		d.mu.Lock()
		for id, sb := range d.instances {
			if sb.ctrID == c.ID() {
				closeLogChannel(sb)
				delete(d.instances, id)
			}
		}
		d.mu.Unlock()
	}
	return len(cs), errors.Join(errs...)
}

// discard kills a container's task, tears its CNI attachment down from its labels, in its pinned netns when a pin is
// left, and deletes it with its snapshot. It keeps a container whose task it cannot load, since that task may still run.
func (d *driver) discard(nctx context.Context, c containerd.Container) error {
	task, err := c.Task(nctx, nil)
	switch {
	case err == nil:
		_ = task.Kill(nctx, syscall.SIGKILL)
		select {
		case <-waitTask(nctx, task):
		case <-time.After(stopGrace):
		}
		_, _ = task.Delete(nctx)
	case !errdefs.IsNotFound(err):
		return err
	}
	if labels, lerr := c.Labels(nctx); lerr == nil {
		ns, name, rep := labels["funcd/namespace"], labels["funcd/name"], labels["funcd/replica"]
		_, cniID := workerNames(ns, name, labels["funcd/revision"], rep)
		d.release(nctx, cniID)
		// An unrevisioned worker created before ADR-0179 is attached under <ns>-<name>-r<replica>.
		if labels["funcd/revision"] == "" && ns != "" && name != "" && rep != "" {
			_ = d.cni.Remove(nctx, ns+"-"+name+"-r"+rep, "")
		}
	}
	if ns, ok := namespaces.Namespace(nctx); ok && d.bootRoot != "" {
		_ = os.RemoveAll(filepath.Join(d.bootRoot, ns, c.ID()))
	}
	return c.Delete(nctx, containerd.WithSnapshotCleanup)
}

// release sends the DEL of an attachment whose container is gone in its pinned netns, or with no netns when no pin is
// left, and drops the pin.
func (d *driver) release(ctx context.Context, cniID string) {
	pin := d.netns.pinned(cniID)
	_ = d.cni.Remove(ctx, cniID, pin)
	d.netns.unpin(pin)
}

func (d *driver) Status(ctx context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	const op = "runtime.containerd.Status"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return runtime.Instance{}, err
	}
	inst := runtime.Instance{
		ID: id, Namespace: sb.namespace, Name: sb.name, OwnerKind: sb.ownerKind, Revision: sb.revision,
		Replica: sb.replica, IP: sb.ip, Port: sb.port, State: runtime.StateCreated, CreatedAt: sb.createdAt,
	}
	task, err := d.task(nctx, sb)
	if err != nil {
		d.ended(sb, &inst, nil)
	} else {
		inst.PID = int(task.Pid())
		if st, serr := task.Status(nctx); serr == nil {
			d.ended(sb, &inst, &st)
		}
	}
	inst.Listened = d.listened(sb)
	return inst, nil
}

// ended fills inst's State and Exit from its task status st, nil once the task is gone. Stop sets stopping first, so a
// task it signalled reads Stopped and ExitByStop once ended; a gone task Stop did not end is ExitUnknown (ADR-0160).
func (d *driver) ended(sb *worker, inst *runtime.Instance, st *containerd.Status) {
	d.mu.Lock()
	stopping := sb.stopping
	d.mu.Unlock()
	switch {
	case st != nil && st.Status != containerd.Stopped:
		inst.State = mapState(*st)
	case stopping:
		inst.State, inst.Exit = runtime.StateStopped, runtime.Exit{Cause: runtime.ExitByStop}
	case st == nil:
		inst.State, inst.Exit = runtime.StateStopped, runtime.Exit{}
	default:
		inst.State, inst.Exit = mapState(*st), exitOf(*st)
	}
}

// listened latches whether the worker's shim has written its port file (ADR-0160).
func (d *driver) listened(sb *worker) bool {
	wrote := portWritten(sb.bootDir)
	d.mu.Lock()
	defer d.mu.Unlock()
	sb.listened = sb.listened || wrote
	return sb.listened
}

func (d *driver) Logs(ctx context.Context, id runtime.InstanceID) (io.ReadCloser, error) {
	const op = "runtime.containerd.Logs"
	sb, nctx, err := d.lookup(ctx, id, op)
	if err != nil {
		return nil, err
	}
	// A terminal worker's tail waits for the run's last line, the one a load error ends with (ADR-0168).
	terminal := true
	if task, terr := d.task(nctx, sb); terr == nil {
		if st, serr := task.Status(nctx); serr == nil && st.Status != containerd.Stopped {
			terminal = false
		}
	}
	if terminal {
		select {
		case <-sb.out.Done():
		case <-time.After(outputWait):
		}
	}
	return sb.out.Tail(), nil
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
			ID: ids[i], Namespace: sb.namespace, Name: sb.name, OwnerKind: sb.ownerKind, Revision: sb.revision,
			Replica: sb.replica, IP: sb.ip, Port: sb.port, State: runtime.StateStopped, CreatedAt: sb.createdAt,
		}
		// Reflect the real task status (ADR-0032): never hardcode Running, or a
		// crashed/exited container would mask the shim's shape failure (ADR-0030).
		nctx := d.nsCtx(ctx, sb.namespace)
		if task, err := d.task(nctx, sb); err != nil {
			d.ended(sb, &inst, nil)
		} else {
			inst.PID = int(task.Pid())
			if st, serr := task.Status(nctx); serr == nil {
				d.ended(sb, &inst, &st)
			}
		}
		inst.Listened = d.listened(sb)
		out = append(out, inst)
	}
	return out, nil
}

// Close stops every worker Stop has not released, all at once so shutdown takes one closeGrace however many ignore
// SIGTERM, and releases the client. On the private containerd it also sweeps every funcd namespace, which catches the
// workers an earlier hard-killed run left behind: no driver tracks them and the private containerd keeps them running.
// A worker left running would serve on after the daemon stops, outside the egress fence (ADR-0115), so Close reports
// every worker it could not stop and Shutdown then keeps the fence.
func (d *driver) Close() error {
	d.mu.Lock()
	var live []runtime.InstanceID
	for id, sb := range d.instances {
		if !sb.released {
			live = append(live, id)
		}
	}
	d.mu.Unlock()
	errs := make([]error, len(live), len(live)+2)
	var wg sync.WaitGroup
	for i, id := range live {
		wg.Go(func() { errs[i] = d.stop(context.Background(), id, closeGrace) })
	}
	wg.Wait()
	if d.cfg.Private {
		errs = append(errs, d.SweepAll(context.Background()))
	}
	if d.ownFifo {
		errs = append(errs, os.RemoveAll(d.fifoDir))
	}
	return errors.Join(append(errs, d.client.Close())...)
}

// SweepAll sweeps every funcd containerd namespace at once, then releases the netns pins no worker holds: Close on the
// private containerd, and the boot sweep before any controller starts (ADR-0167).
func (d *driver) SweepAll(ctx context.Context) error {
	names, err := d.client.NamespaceService().List(ctx)
	if err != nil {
		return mapErr(err, "runtime.containerd.SweepAll", "list namespaces")
	}
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		if ns, ok := strings.CutPrefix(name, nsPrefix); ok {
			wg.Go(func() { _, errs[i] = d.Sweep(ctx, v1alpha1.NamespaceName(ns)) })
		}
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		// A container discard kept may still run in its pinned netns.
		return err
	}
	held := map[string]bool{}
	d.mu.Lock()
	for _, sb := range d.instances {
		if !sb.released {
			held[sb.netnsPath] = true
		}
	}
	d.mu.Unlock()
	// What is left is the pin of a worker whose container went without a DEL, or of a daemon that died between a DEL
	// and its unpin.
	for _, cniID := range d.netns.unheld(held) {
		d.release(ctx, cniID)
	}
	return nil
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
// Manager — owns availability: it first looks the image up in this namespace and unpacks it if
// the configured snapshotter lacks its layers; if it is absent and a curated embedded tar exists
// for the ref, it imports the embed into this namespace (NO registry pull — ADR-0054) and unpacks
// it; any other ref is pulled only when Config.Pullable allows it (an ImageOverride or a custom
// imagePrefix). A ref it may not pull, and a pull that finds no such image, wrap runtime.ErrImageUnavailable
// (ADR-0149 Decision 3); any other pull failure keeps its mapErr kind.
func (d *driver) resolveImage(nctx context.Context, op, ref string) (containerd.Image, error) {
	name := normalizedRef(ref) // Import stores a curated tar under it, and Pull must resolve it
	image, err := d.client.GetImage(nctx, ref)
	if errdefs.IsNotFound(err) && name != ref {
		image, err = d.client.GetImage(nctx, name)
	}
	if err == nil {
		// The image record outlives a failed unpack and a snapshotter change, and WithNewSnapshot
		// does not unpack.
		unpacked, uerr := image.IsUnpacked(nctx, d.snapshotter())
		if uerr == nil && !unpacked {
			uerr = image.Unpack(nctx, d.snapshotter())
		}
		if uerr != nil {
			return nil, mapErr(uerr, op, "unpack image %q", ref)
		}
		return image, nil
	}
	if !errdefs.IsNotFound(err) {
		return nil, mapErr(err, op, "look up image %q", ref)
	}

	if tar, ok := embedimg.TarForImageRef(ref); ok {
		// Import brings the curated tar into nctx's namespace. Our curated tars carry exactly ONE
		// image (built --provenance=false --sbom=false), so use the returned image directly — the
		// tar names it "docker.io/funcd/runtime-<rt>:latest", the normalized name the lookup above
		// finds on the next Create.
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

	if d.cfg.Pullable == nil || !d.cfg.Pullable(ref) {
		return nil, fault.Wrapf(runtime.ErrImageUnavailable, fault.NotFound, op, "runtime image %q is not embedded; set runtime.containerd.imageOverride for its runtime to pull it", ref)
	}
	image, err = d.client.Pull(nctx, name, containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(d.snapshotter()))
	if err != nil {
		if imageAbsent(err) {
			return nil, fault.Wrapf(fmt.Errorf("%w: %w", runtime.ErrImageUnavailable, err), fault.NotFound, op, "pull image %q", ref)
		}
		return nil, mapErr(err, op, "pull image %q", ref)
	}
	return image, nil
}

// ociOpts builds the conservative OCI spec for a worker.
func ociOpts(spec runtime.WorkerSpec, image containerd.Image, bootDir string) []oci.SpecOpts {
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
	opts = append(opts, oci.WithMounts(append(bindMounts(spec.Mounts), specs.Mount{
		Destination: containerBootDir,
		Type:        "bind",
		Source:      bootDir,
		Options:     []string{"rbind", "rw", "nosuid", "nodev", "noexec"},
	})))
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
