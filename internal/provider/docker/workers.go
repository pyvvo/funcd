package docker

import (
	"archive/tar"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

const (
	workerPrefix = "funcd-dev-worker-"
	// runSuffix names a worker's second volume, mounted at workerRunDir on the host's network (ADR-0346 Decision 2).
	runSuffix    = "-run"
	workerRunDir = "/run/funcd"
	// funcdHost is the name a worker on an engine in a VM reaches funcd's listeners by: the engine's host gateway
	// (ADR-0346 Decision 5).
	funcdHost = "funcd.host"
	// workerOutputWait bounds how long an ended run waits for its last output before it reads as ended (ADR-0168), as
	// the process driver's does.
	workerOutputWait = time.Second
	// workerProbeTimeout bounds one probe of a worker's readiness path on an engine in a VM.
	workerProbeTimeout = time.Second
	// maxPortFileBytes bounds what is read of a worker's port file.
	maxPortFileBytes = 32
	// bootIDPath is the kernel's boot ID, and netnsPath the process's network namespace (ADR-0346 Decision 5).
	bootIDPath = "/proc/sys/kernel/random/boot_id"
	netnsPath  = "/proc/self/ns/net"
)

// workerNet is how the session's workers reach funcd's listeners on the host's 127.0.0.1 (ADR-0346 Decision 5): on the
// host's network, or, with host false, from an engine in a VM by funcdHost. refused says why the engine runs none.
type workerNet struct {
	host    bool
	refused string
}

// netFinder finds the workerNet of eng; h is a helper to run on it.
type netFinder func(ctx context.Context, eng Engine, h Helper) (workerNet, error)

// findNet runs a helper on the engine's host network that reads the kernel's boot ID and its network namespace (ADR-0346
// Decision 5). The host's own boot ID means the engine shares this host's kernel, and then its network, unless its host
// network is another namespace, as a rootless engine's is. A host with no boot ID to read, as macOS, runs every engine in
// a VM.
func findNet(ctx context.Context, eng Engine, h Helper) (workerNet, error) {
	boot, err := os.ReadFile(bootIDPath)
	if errors.Is(err, fs.ErrNotExist) {
		return workerNet{}, nil
	}
	if err != nil {
		return workerNet{}, fault.Wrapf(err, fault.Internal, "docker.NewWorkers", "read this host's boot ID")
	}
	netns, err := os.Readlink(netnsPath)
	if err != nil {
		return workerNet{}, fault.Wrapf(err, fault.Internal, "docker.NewWorkers", "read this process's network namespace")
	}
	h.HostNetwork = true
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	out, err := eng.Exec(ctx, h, "sh", "-c", `cat `+bootIDPath+`; readlink `+netnsPath)
	if err != nil {
		return workerNet{}, err
	}
	return netOf(string(boot), netns, out), nil
}

// netOf is the workerNet of an engine whose host-network helper wrote out, its kernel's boot ID and its network
// namespace, on a host of boot and netns.
func netOf(boot, netns string, out []byte) workerNet {
	got := strings.Fields(string(out))
	switch {
	case len(got) != 2 || got[0] != strings.TrimSpace(boot):
		return workerNet{}
	case got[1] != strings.TrimSpace(netns):
		return workerNet{refused: "the container engine runs on this host's kernel, but its host network is not this host's, " +
			"as a rootless engine's is not: a worker on it could not reach funcd on 127.0.0.1"}
	}
	return workerNet{host: true}
}

// workers is funcdctl dev's worker driver on its container engine (ADR-0346 Decision 4).
type workers struct {
	eng    Engine
	key    string
	net    workerNet
	logger *slog.Logger
	probe  *http.Client
	// follows ends every follow of a worker's output at Close.
	follows  context.Context //nolint:containedctx // the driver's own: a follow outlives the Start that began it
	unfollow context.CancelFunc

	mu        sync.Mutex
	instances map[containerrt.InstanceID]*worker
	outputs   containerrt.OutputCaptureFunc
}

// worker is one instance: its spec, its container's name, and what its runs showed.
type worker struct {
	spec      containerrt.WorkerSpec
	name      string
	run       int // the Starts so far: what a read of an earlier run finds changes nothing
	state     containerrt.State
	pid       int
	port      int
	listened  bool
	stopping  bool
	released  bool // Stop has run, so Remove may forget it (ADR-0143)
	exit      containerrt.Exit
	out       *workerpipe.Output
	createdAt time.Time
	startedAt time.Time
}

var (
	_ containerrt.ImageRunner    = (*workers)(nil)
	_ containerrt.OutputCapturer = (*workers)(nil)
	_ containerrt.StopGracer     = (*workers)(nil)
	_ containerrt.LimitEnforcer  = (*workers)(nil)
)

// NewWorkers runs the Function and Job workers of funcdctl dev's session key on eng (ADR-0346 Decision 4), nerdctl's
// through the same Engine: each one a container from its spec's image, its bundle copied into a volume of its own
// through the helper and mounted read-only, its output read through the engine's log follow. It removes the key's
// leftover workers, their containers and volumes, and finds how a worker reaches funcd (Decision 5) before it returns.
func NewWorkers(ctx context.Context, eng Engine, key string, logger *slog.Logger) (containerrt.Runtime, error) {
	return newWorkers(ctx, eng, key, logger, findNet)
}

func newWorkers(ctx context.Context, eng Engine, key string, logger *slog.Logger, find netFinder) (*workers, error) {
	const op = "docker.NewWorkers"
	if !sessionKey.MatchString(key) {
		return nil, fault.Invalidf(op, "session key %q is not 16 hex digits", key)
	}
	w := &workers{eng: eng, key: key, logger: cmp.Or(logger, slog.Default()).With("component", "dev-workers"),
		probe: httpx.NodeClient(workerProbeTimeout), instances: map[containerrt.InstanceID]*worker{}}
	if err := w.reap(ctx); err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "remove the workers an earlier session of key %s left", key)
	}
	n, err := find(ctx, eng, w.helper(workerPrefix+key+"-net"))
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "find how a worker reaches funcd")
	}
	w.net = n
	w.follows, w.unfollow = context.WithCancel(context.Background())
	return w, nil
}

// RunsImages is false, naming why, on an engine whose workers could not reach funcd (ADR-0346 Decision 5).
func (w *workers) RunsImages() (bool, string) { return w.net.refused == "", w.net.refused }

// EndpointHost is the loopback on the host's network, else funcdHost.
func (w *workers) EndpointHost() string {
	if w.net.host {
		return containerrt.LoopbackHost
	}
	return funcdHost
}

// SetOutputCapture installs the per-run raw-output hook (ADR-0168), which Start calls before the worker runs.
func (w *workers) SetOutputCapture(fn containerrt.OutputCaptureFunc) {
	w.mu.Lock()
	w.outputs = fn
	w.mu.Unlock()
}

// StopGrace is the engines' grace between SIGTERM and the kill (ADR-0277).
func (w *workers) StopGrace() time.Duration { return stopGrace }

// EnforcesLimits is true: a worker's CPU and memory limits are its container's (ADR-0346 Decision 4).
func (w *workers) EnforcesLimits() bool { return true }

// nameOf is the container and volume name of id: the key and the hash of its namespace, name, revision and replica
// (ADR-0346 Decision 7).
func (w *workers) nameOf(id containerrt.InstanceID) string {
	sum := sha256.Sum256([]byte(id))
	return workerPrefix + w.key + "-" + hex.EncodeToString(sum[:8])
}

func (w *workers) labels(spec containerrt.WorkerSpec) map[string]string {
	return map[string]string{KeyLabel: w.key, roleLabel: roleWorker, namespaceLabel: string(spec.Namespace), nameLabel: string(spec.Name)}
}

// helper is a helper of the worker name with mounts, named after it with a suffix of its own.
func (w *workers) helper(name string, mounts ...Mount) Helper {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return Helper{Name: name + "-helper-" + hex.EncodeToString(b[:]), Image: HelperImage,
		Labels: map[string]string{KeyLabel: w.key, roleLabel: roleHelper}, Mounts: mounts}
}

// Create pulls spec's image and copies its bundle, the one read-only mount a worker takes, into the worker's volume
// through the helper; a worker on the host's network gets its run volume too. An exited instance of the same kind is
// replaced with its container and volumes (ADR-0142).
func (w *workers) Create(ctx context.Context, spec containerrt.WorkerSpec) (containerrt.Instance, error) {
	const op = "docker.workers.Create"
	switch {
	case spec.OwnerKind == "":
		return containerrt.Instance{}, fault.Invalidf(op, "spec.OwnerKind must not be empty")
	case spec.Image == "":
		return containerrt.Instance{}, fault.Invalidf(op, "spec.Image must not be empty for a container worker")
	case len(spec.Mounts) > 1 || len(spec.Mounts) == 1 && !spec.Mounts[0].ReadOnly:
		return containerrt.Instance{}, fault.Invalidf(op, "a container worker of funcdctl dev takes one read-only mount, its bundle")
	case w.net.refused != "":
		return containerrt.Instance{}, fault.Wrapf(containerrt.ErrImageUnavailable, fault.NotFound, op, "%s", w.net.refused)
	}
	id := containerrt.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)
	w.mu.Lock()
	old, replace := w.instances[id]
	switch {
	case replace && old.spec.OwnerKind != spec.OwnerKind:
		w.mu.Unlock()
		return containerrt.Instance{}, fault.Conflictf(op, "instance %q is held by a %s worker", id, old.spec.OwnerKind)
	case replace && !old.state.Terminal():
		w.mu.Unlock()
		return containerrt.Instance{}, fault.Conflictf(op, "instance %q already exists", id)
	}
	w.mu.Unlock()
	name := w.nameOf(id)
	if err := w.discard(ctx, name); err != nil {
		return containerrt.Instance{}, fault.Wrapf(err, fault.KindOf(err), op, "remove the earlier worker %s", id)
	}
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	if err := w.eng.Pull(pctx, spec.Image); err != nil {
		return containerrt.Instance{}, fault.Wrapf(containerrt.ErrImageUnavailable, fault.NotFound, op, "%v", err)
	}
	if err := w.prepare(ctx, spec, name); err != nil {
		_ = w.discard(context.WithoutCancel(ctx), name)
		return containerrt.Instance{}, fault.Wrapf(err, fault.KindOf(err), op, "prepare the volumes of worker %s", id)
	}
	inst := &worker{spec: spec, name: name, state: containerrt.StateCreated, createdAt: time.Now()}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.instances[id] = inst
	return w.snapshotLocked(id, inst), nil
}

// prepare creates name's volumes: its bundle's, filled with the mount's folder through the helper, and on the host's
// network its run volume.
func (w *workers) prepare(ctx context.Context, spec containerrt.WorkerSpec, name string) error {
	if len(spec.Mounts) == 1 {
		if err := w.createVolume(ctx, name, spec); err != nil {
			return err
		}
		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			pw.CloseWithError(tarTree(spec.Mounts[0].Source, pw))
		}()
		err := w.eng.WriteTar(ctx, w.helper(name, Mount{Volume: name, Target: helperRoot}), helperRoot, 0, 0, pr)
		_ = pr.Close()
		<-done
		if err != nil {
			return err
		}
	}
	if w.net.host {
		return w.createVolume(ctx, name+runSuffix, spec)
	}
	return nil
}

func (w *workers) createVolume(ctx context.Context, name string, spec containerrt.WorkerSpec) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return w.eng.CreateVolume(ctx, name, w.labels(spec))
}

// tarTree writes the files under dir to out as a tar, owned by root and readable by any user, so the image's user reads
// them in the read-only bundle.
func tarTree(dir string, out io.Writer) error {
	tw := tar.NewWriter(out)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h := &tar.Header{Name: filepath.ToSlash(rel), ModTime: info.ModTime(), Mode: 0o644}
		switch {
		case d.IsDir():
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, h.Name+"/", 0o755
		case d.Type()&fs.ModeSymlink != 0:
			if h.Linkname, err = os.Readlink(p); err != nil {
				return err
			}
			h.Typeflag = tar.TypeSymlink
		case d.Type().IsRegular():
			h.Typeflag, h.Size = tar.TypeReg, info.Size()
			if info.Mode()&0o111 != 0 {
				h.Mode = 0o755
			}
		default:
			return nil
		}
		if err := tw.WriteHeader(h); err != nil || h.Typeflag != tar.TypeReg {
			return err
		}
		f, err := os.Open(p) //nolint:gosec // a file of the worker's own bundle
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "docker.tarTree", "archive the bundle %s", dir)
	}
	return tw.Close()
}

// Start runs the worker's container from its spec: the image's entrypoint, or its Command, with FUNCD_LOG_STDOUT, so the
// shim prints each record to stdout (ADR-0346 Decision 4); on the host's network with its port file in its run volume,
// else with FUNCD_PORT published on 127.0.0.1 and funcdHost resolving to the engine's host gateway (Decision 5).
// What it writes is followed from its start (ADR-0168).
func (w *workers) Start(ctx context.Context, id containerrt.InstanceID) error {
	const op = "docker.workers.Start"
	w.mu.Lock()
	inst, ok := w.instances[id]
	if !ok {
		w.mu.Unlock()
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	if inst.state == containerrt.StateRunning {
		w.mu.Unlock()
		return nil
	}
	spec, name, hook := inst.spec, inst.name, w.outputs
	w.mu.Unlock()

	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	err := w.eng.Remove(rctx, name) // an earlier run's container: an engine names one container name once
	cancel()
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "remove the earlier run of worker %s", id)
	}
	run := w.runSpec(spec, name)
	if w.net.host {
		// The port file of an earlier run is gone before this one starts, and its folder takes any user's file.
		if _, err := w.exec(ctx, w.helper(name, Mount{Volume: name + runSuffix, Target: helperRoot}), "sh", "-c",
			`rm -f "$1/port" && chmod 777 "$1"`, "sh", helperRoot); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "prepare the run volume of worker %s", id)
		}
	}
	out := workerpipe.New(workerpipe.Options{Logger: w.logger.With(
		"namespace", spec.Namespace, "name", spec.Name, "revision", spec.Revision, "replica", spec.Replica)})
	if hook != nil {
		hook(spec, out)
	}
	pctx, pcancel := context.WithTimeout(ctx, pullTimeout)
	defer pcancel()
	if err := w.eng.Run(pctx, run); err != nil {
		out.Close()
		return fault.Wrapf(err, fault.KindOf(err), op, "run worker %s", id)
	}
	started := time.Now()
	w.follow(name, out)
	w.mu.Lock()
	defer w.mu.Unlock()
	inst.run++
	inst.state, inst.out, inst.startedAt = containerrt.StateRunning, out, started
	inst.pid, inst.port, inst.listened, inst.stopping, inst.released, inst.exit = 0, 0, false, false, false, containerrt.Exit{}
	return nil
}

// runSpec is the container of spec named name.
func (w *workers) runSpec(spec containerrt.WorkerSpec, name string) RunSpec {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	env["FUNCD_LOG_STDOUT"] = "1"
	run := RunSpec{Name: name, Image: spec.Image, Cmd: spec.Command, Env: env, Labels: w.labels(spec), StopGrace: stopGrace,
		NanoCPUs: int64(spec.Limits.CPUs * 1e9), MemoryBytes: spec.Limits.MemoryBytes}
	if len(spec.Mounts) == 1 {
		run.Mounts = append(run.Mounts, Mount{Volume: name, Target: spec.Mounts[0].Target, ReadOnly: true})
	}
	if w.net.host {
		run.HostNetwork = true
		env["FUNCD_PORTFILE"] = workerRunDir + "/port"
		run.Mounts = append(run.Mounts, Mount{Volume: name + runSuffix, Target: workerRunDir})
		return run
	}
	run.Port, _ = strconv.Atoi(spec.Env["FUNCD_PORT"])
	run.ExtraHosts = []string{funcdHost + ":host-gateway"}
	return run
}

// follow drains what the container name writes into out, until it stops, then closes out.
func (w *workers) follow(name string, out *workerpipe.Output) {
	stdout, stderr, err := w.eng.Follow(w.follows, name)
	if err != nil {
		w.logger.Warn("cannot follow a worker's output", "name", name, "err", err)
		out.Close()
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	out.Drain(workerpipe.Stdout, closeNotifier{ReadCloser: stdout, done: wg.Done})
	out.Drain(workerpipe.Stderr, closeNotifier{ReadCloser: stderr, done: wg.Done})
	go func() {
		wg.Wait()
		out.Close()
	}()
}

// closeNotifier calls done once it is closed.
type closeNotifier struct {
	io.ReadCloser
	done func()
}

func (c closeNotifier) Close() error {
	err := c.ReadCloser.Close()
	c.done()
	return err
}

// Stop stops the worker's container with SIGTERM, then kills it after the grace, and waits for its last output.
func (w *workers) Stop(ctx context.Context, id containerrt.InstanceID) error {
	const op = "docker.workers.Stop"
	w.mu.Lock()
	inst, ok := w.instances[id]
	if !ok {
		w.mu.Unlock()
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	if inst.state != containerrt.StateRunning {
		inst.state, inst.exit, inst.released = containerrt.StateStopped, containerrt.Exit{Cause: containerrt.ExitByStop}, true
		w.mu.Unlock()
		return nil
	}
	inst.stopping = true
	run, name, out := inst.run, inst.name, inst.out
	w.mu.Unlock()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace+requestTimeout)
	defer cancel()
	if err := w.eng.Stop(sctx, name, stopGrace); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "stop worker %s", id)
	}
	drained(out)
	w.mu.Lock()
	defer w.mu.Unlock()
	if inst.run == run {
		inst.state, inst.exit, inst.released = containerrt.StateStopped, containerrt.Exit{Cause: containerrt.ExitByStop}, true
	}
	return nil
}

// drained closes out and waits, at most workerOutputWait, for its last line, so Logs has the line a run ends with.
func drained(out *workerpipe.Output) {
	if out == nil {
		return
	}
	out.Close()
	select {
	case <-out.Done():
	case <-time.After(workerOutputWait):
	}
}

// Status reads a running worker's container: its process, its port once it listens, and its end.
func (w *workers) Status(ctx context.Context, id containerrt.InstanceID) (containerrt.Instance, error) {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()
	if !ok {
		return containerrt.Instance{}, fault.NotFoundf("docker.workers.Status", "instance %q not found", id)
	}
	if err := w.refresh(ctx, inst); err != nil {
		return containerrt.Instance{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshotLocked(id, inst), nil
}

// refresh reads inst's container while inst runs. It listens once its port file holds its port on the host's network,
// as on the other drivers (ADR-0160), and on an engine in a VM, which writes none, once its readiness path answers with
// any status (ADR-0346 Decision 4).
func (w *workers) refresh(ctx context.Context, inst *worker) error {
	w.mu.Lock()
	if inst.state != containerrt.StateRunning {
		w.mu.Unlock()
		return nil
	}
	run, name, listened, spec := inst.run, inst.name, inst.listened, inst.spec
	w.mu.Unlock()
	ictx, cancel := context.WithTimeout(ctx, requestTimeout)
	c, exists, err := w.eng.Inspect(ictx, name)
	cancel()
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "docker.workers.Status", "inspect worker %s", name)
	}
	port := 0
	if !listened {
		port = w.listenPort(ctx, name, spec, c)
	}
	if exists && c.Running {
		w.mu.Lock()
		defer w.mu.Unlock()
		if inst.run == run {
			inst.pid = cmp.Or(c.Pid, inst.pid)
			if port > 0 {
				inst.port, inst.listened = port, true
			}
		}
		return nil
	}
	w.mu.Lock()
	out := inst.out
	w.mu.Unlock()
	drained(out)
	w.mu.Lock()
	defer w.mu.Unlock()
	if inst.run != run || inst.state != containerrt.StateRunning {
		return nil
	}
	if port > 0 {
		inst.listened = true
	}
	switch {
	case inst.stopping:
		inst.state, inst.exit = containerrt.StateStopped, containerrt.Exit{Cause: containerrt.ExitByStop}
	case !exists:
		inst.state = containerrt.StateFailed // its container is gone: nothing tells how it ended
	default:
		inst.exit = exitOf(c.ExitCode)
		inst.state = containerrt.StateStopped
		if c.ExitCode != 0 {
			inst.state = containerrt.StateFailed
		}
	}
	return nil
}

// exitOf is how a container that ended with code ended: by the signal code-128 above 128 (ADR-0346 Decision 4).
func exitOf(code int) containerrt.Exit {
	if code > 128 {
		return containerrt.Exit{Cause: containerrt.ExitBySignal, Signal: code - 128}
	}
	return containerrt.Exit{Cause: containerrt.ExitByCode, Code: code}
}

// listenPort is the port the worker name listens on, 0 until it does: its port file's on the host's network, else, while
// it runs, its published FUNCD_PORT once its readiness path answers.
func (w *workers) listenPort(ctx context.Context, name string, spec containerrt.WorkerSpec, c Container) int {
	if w.net.host {
		return w.portFile(ctx, name)
	}
	fixed := atoi(spec.Env["FUNCD_PORT"])
	addr := c.Published[portKey(fixed)]
	if fixed == 0 || !c.Running || addr == "" || !w.answers(ctx, addr) {
		return 0
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	return atoi(p)
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

// portFile reads the port the shim wrote to its port file in name's run volume, through the helper; 0 when none.
func (w *workers) portFile(ctx context.Context, name string) int {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	rc, err := w.eng.ReadTar(ctx, w.helper(name, Mount{Volume: name + runSuffix, Target: helperRoot, ReadOnly: true}), helperRoot+"/port")
	if err != nil {
		return 0
	}
	body, err := tarFile(rc)
	if err != nil {
		return 0
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(io.LimitReader(body, maxPortFileBytes))
	if err != nil {
		return 0
	}
	return atoi(string(b))
}

// answers reports whether the worker at addr answers its readiness path, with any status.
func (w *workers) answers(ctx context.Context, addr string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health/readiness", nil)
	if err != nil {
		return false
	}
	resp, err := w.probe.Do(req)
	if err != nil {
		return false
	}
	httpx.CloseBody(resp.Body)
	return true
}

func (w *workers) snapshotLocked(id containerrt.InstanceID, inst *worker) containerrt.Instance {
	out := containerrt.Instance{ID: id, Namespace: inst.spec.Namespace, Name: inst.spec.Name, OwnerKind: inst.spec.OwnerKind,
		Revision: inst.spec.Revision, Replica: inst.spec.Replica, PID: inst.pid, State: inst.state, CreatedAt: inst.createdAt,
		StartedAt: inst.startedAt, Listened: inst.listened, Exit: inst.exit}
	if inst.state == containerrt.StateRunning && inst.port > 0 {
		out.IP, out.Port = containerrt.LoopbackHost, inst.port
	}
	return out
}

// Logs is the tail of the current or last run's output (ADR-0168), empty before the first Start.
func (w *workers) Logs(_ context.Context, id containerrt.InstanceID) (io.ReadCloser, error) {
	w.mu.Lock()
	inst, ok := w.instances[id]
	var out *workerpipe.Output
	if ok {
		out = inst.out
	}
	w.mu.Unlock()
	switch {
	case !ok:
		return nil, fault.NotFoundf("docker.workers.Logs", "instance %q not found", id)
	case out == nil:
		return io.NopCloser(strings.NewReader("")), nil
	}
	return out.Tail(), nil
}

// Exec runs cmd in a helper that mounts the worker's volumes where the worker has them (best-effort, as the port allows).
func (w *workers) Exec(ctx context.Context, id containerrt.InstanceID, cmd []string) error {
	const op = "docker.workers.Exec"
	if len(cmd) == 0 {
		return fault.Invalidf(op, "exec command must not be empty")
	}
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()
	if !ok {
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	mounts := w.runSpec(inst.spec, inst.name).Mounts
	if _, err := w.exec(ctx, w.helper(inst.name, mounts...), cmd...); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "exec %v", cmd)
	}
	return nil
}

func (w *workers) exec(ctx context.Context, h Helper, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	return w.eng.Exec(ctx, h, argv...)
}

// List refreshes and returns the instances of ns.
func (w *workers) List(ctx context.Context, ns v1.NamespaceName) ([]containerrt.Instance, error) {
	w.mu.Lock()
	var ids []containerrt.InstanceID
	for id, inst := range w.instances {
		if inst.spec.Namespace == ns {
			ids = append(ids, id)
		}
	}
	w.mu.Unlock()
	out := make([]containerrt.Instance, 0, len(ids))
	for _, id := range ids {
		in, err := w.Status(ctx, id)
		switch {
		case fault.KindOf(err) == fault.NotFound:
		case err != nil:
			return nil, err
		default:
			out = append(out, in)
		}
	}
	return out, nil
}

// Remove forgets an instance Stop has released, with its container and volumes (ADR-0143).
func (w *workers) Remove(ctx context.Context, id containerrt.InstanceID) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	switch {
	case !ok:
		w.mu.Unlock()
		return nil
	case !inst.released:
		w.mu.Unlock()
		return fault.Conflictf("docker.workers.Remove", "instance %q has not been stopped", id)
	}
	name := inst.name
	w.mu.Unlock()
	if err := w.discard(ctx, name); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "docker.workers.Remove", "remove worker %s", id)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.instances[id] == inst {
		delete(w.instances, id)
	}
	return nil
}

// discard removes name's container, then its volumes; each is no error when it is gone.
func (w *workers) discard(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := w.eng.Remove(ctx, name); err != nil {
		return err
	}
	return errors.Join(w.eng.RemoveVolume(ctx, name), w.eng.RemoveVolume(ctx, name+runSuffix))
}

// Close stops every running worker at once, ends the follows, and removes the key's worker containers and volumes.
func (w *workers) Close() error {
	w.mu.Lock()
	var running []containerrt.InstanceID
	for id, inst := range w.instances {
		if inst.state == containerrt.StateRunning {
			running = append(running, id)
		}
	}
	w.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range running {
		wg.Go(func() { _ = w.Stop(context.Background(), id) })
	}
	wg.Wait()
	w.unfollow()
	w.mu.Lock()
	w.instances = map[containerrt.InstanceID]*worker{}
	w.mu.Unlock()
	return w.reap(context.Background())
}

// reap removes the key's worker containers and volumes: only its own role (ADR-0346 Decision 7).
func (w *workers) reap(ctx context.Context) error {
	own := map[string]string{KeyLabel: w.key, roleLabel: roleWorker}
	return reapKey(ctx, w.eng, own, own)
}
