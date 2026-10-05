// Package process implements the runtime.Runtime port by supervising functions as
// plain OS child processes (ADR-0011). It is the cross-platform dev/e2e/CI driver —
// no root, no containerd — and is the driver the InMemory() harness and unit tests
// use. It is not an isolation boundary; production uses the containerd driver.
package process

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// outputWait bounds how long an ended run waits for its last output before it reports a terminal state (ADR-0168).
const outputWait = time.Second

// defaultStopGrace is how long Stop waits after SIGTERM before sending SIGKILL unless runtime.process.stopGrace sets
// it (ADR-0167).
const defaultStopGrace = 3 * time.Second

// instanceFlag prefixes the identity token appended to a worker's argv, which the reap matches (ADR-0167).
const instanceFlag = "--funcd-instance="

// instance tracks one supervised child process.
type instance struct {
	id        runtime.InstanceID
	spec      runtime.WorkerSpec
	portFile  string             // the shim writes its OS-assigned port here once listening (ADR-0030)
	out       *workerpipe.Output // the current or last run's stdout and stderr (ADR-0168); nil before the first Start
	cmd       *exec.Cmd
	pid       int
	state     runtime.State
	stopping  bool
	released  bool // Stop has run and the process is gone, so Remove may forget it (ADR-0143)
	listened  bool // the shim wrote its port since the last Start (ADR-0160)
	exit      runtime.Exit
	done      chan struct{}
	createdAt time.Time
}

// driver is the process-based runtime.Runtime.
type driver struct {
	mu        sync.Mutex
	instances map[runtime.InstanceID]*instance
	capture   runtime.LogCaptureFunc    // optional Path B log-channel hook (ADR-0081); nil = disabled
	outputs   runtime.OutputCaptureFunc // optional Path A raw-output hook (ADR-0168); nil = tail only
	grace     time.Duration
	reg       *procreg.Registry             // the saved worker registry (ADR-0167); nil = in-memory only
	startTime func(pid int) (uint64, error) // procreg.StartTime; a test delays it to widen the exit-before-save window
}

// New returns a process-backed runtime.Runtime (cross-platform; dev/e2e/CI) that saves nothing.
func New() runtime.Runtime {
	return &driver{instances: map[runtime.InstanceID]*instance{}, grace: defaultStopGrace}
}

// Open returns a process driver that saves its workers in <stateDir>/workers.json (ADR-0167). Before it returns it
// reaps the workers a crashed run left there; the reconciler then re-creates the replicas. A stopGrace <= 0 is the
// 3 s default.
func Open(ctx context.Context, stateDir string, stopGrace time.Duration) (runtime.Runtime, error) {
	const op = "runtime.process.Open"
	if stopGrace <= 0 {
		stopGrace = defaultStopGrace
	}
	reg, err := procreg.Open(stateDir, "workers")
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "open worker registry")
	}
	if _, err := reg.Reap(ctx, stopGrace); err != nil {
		_ = reg.Close()
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "reap workers of a previous run")
	}
	return &driver{instances: map[runtime.InstanceID]*instance{}, grace: stopGrace, reg: reg, startTime: procreg.StartTime}, nil
}

// SetLogCapture installs the per-instance structured-log hook (runtime.LogCapturer, ADR-0081). When
// set, Start passes the shim a write pipe as fd 3 (FUNCD_LOG_FD=3) and hands the read end to the hook.
func (d *driver) SetLogCapture(fn runtime.LogCaptureFunc) {
	d.mu.Lock()
	d.capture = fn
	d.mu.Unlock()
}

// SetOutputCapture installs the per-run raw-output hook (runtime.OutputCapturer, ADR-0168); Start calls it just before
// the worker starts.
func (d *driver) SetOutputCapture(fn runtime.OutputCaptureFunc) {
	d.mu.Lock()
	d.outputs = fn
	d.mu.Unlock()
}

func (d *driver) Create(_ context.Context, spec runtime.WorkerSpec) (runtime.Instance, error) {
	const op = "runtime.process.Create"
	if len(spec.Command) == 0 {
		return runtime.Instance{}, fault.Invalidf(op, "spec.Command must not be empty for the process driver")
	}
	if spec.OwnerKind == "" {
		return runtime.Instance{}, fault.Invalidf(op, "spec.OwnerKind must not be empty")
	}
	id := runtime.NewInstanceID(spec.Namespace, spec.Name, spec.Revision, spec.Replica)

	d.mu.Lock()
	defer d.mu.Unlock()
	// An exited instance is replaced (ADR-0142), as containerd allows once Stop has deleted the container,
	// so a replica can be re-created after it stopped or crashed. The replaced instance's files go with it.
	old, replace := d.instances[id]
	if replace && old.spec.OwnerKind != spec.OwnerKind {
		return runtime.Instance{}, fault.Conflictf(op, "instance %q is held by a %s worker", id, old.spec.OwnerKind)
	}
	if replace && !old.state.Terminal() {
		return runtime.Instance{}, fault.Conflictf(op, "instance %q already exists", id)
	}

	// The port file reserves its own name; the shim writes its bound port into it (ADR-0030, ADR-0168).
	f, err := os.CreateTemp("", "funcd-worker-*.port")
	if err != nil {
		return runtime.Instance{}, fault.Wrapf(err, fault.Internal, op, "create port file")
	}
	portFile := f.Name()
	_ = f.Close()
	if replace {
		removeFiles(old)
	}
	inst := &instance{
		id:        id,
		spec:      spec,
		portFile:  portFile,
		state:     runtime.StateCreated,
		done:      make(chan struct{}),
		createdAt: time.Now(),
	}
	d.instances[id] = inst
	return d.snapshotLocked(id, inst), nil
}

func (d *driver) Start(_ context.Context, id runtime.InstanceID) error {
	const op = "runtime.process.Start"
	d.mu.Lock()
	defer d.mu.Unlock()
	inst, ok := d.instances[id]
	if !ok {
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	if inst.state == runtime.StateRunning {
		return nil
	}
	inst.released = false // a restarted instance runs again, so Remove must wait for its next Stop (ADR-0143)

	// Truncated, not removed, so the name stays reserved and an empty file reads as not listened.
	if err := os.WriteFile(inst.portFile, nil, 0o600); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "reset port file")
	}
	// Raw stdout and stderr (ADR-0168): two pipes funcd reads; the parent's write ends close once the child has them.
	out := workerpipe.New(workerpipe.Options{Logger: slog.Default().With(
		"namespace", inst.spec.Namespace, "name", inst.spec.Name, "revision", inst.spec.Revision, "replica", inst.spec.Replica)})
	var pipes []*os.File // every pipe end made so far: an error return closes them
	fail := func(err error, msg string) error {
		for _, f := range pipes {
			_ = f.Close()
		}
		out.Close()
		return fault.Wrapf(err, fault.Internal, op, "%s", msg)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return fail(err, "create stdout pipe")
	}
	pipes = append(pipes, outR, outW)
	errR, errW, err := os.Pipe()
	if err != nil {
		return fail(err, "create stderr pipe")
	}
	pipes = append(pipes, errR, errW)

	args := inst.spec.Command[1:]
	if d.reg != nil {
		args = append(slices.Clone(args), instanceFlag+string(id))
	}
	cmd := exec.Command(inst.spec.Command[0], args...) //nolint:gosec // command is platform-internal, from the controller-built spec
	cmd.Stdout = outW
	cmd.Stderr = errW
	// Its own process group, so Stop, Close and the worker's exit reach everything it starts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// FUNCD_PORTFILE is the driver↔shim port handshake (ADR-0030): the shim binds
	// 127.0.0.1:0 and writes its OS-assigned port here, which Status reads back.
	cmd.Env = append(envSlice(inst.spec.Env), "FUNCD_PORTFILE="+inst.portFile)

	// Path B structured-log channel (ADR-0081): when a capture hook is set, pass the shim a write
	// pipe as fd 3 and hand the read end to the hook. ExtraFiles[0] becomes the child's fd 3.
	var logRead *os.File
	if d.capture != nil {
		pr, pw, perr := os.Pipe()
		if perr != nil {
			return fail(perr, "create log pipe")
		}
		pipes = append(pipes, pr, pw)
		cmd.ExtraFiles = []*os.File{pw}
		cmd.Env = append(cmd.Env, "FUNCD_LOG_FD=3")
		logRead = pr
	}

	// Path A hook (ADR-0168), last before the worker starts: a Pump it starts ends when out is closed.
	if d.outputs != nil {
		d.outputs(inst.spec, out)
	}
	if err := cmd.Start(); err != nil {
		// no process ran, so the instance keeps its state (Created for a new one) and is started again (ADR-0142)
		return fail(err, "start process")
	}
	// The parent's write ends close, so each Drain sees EOF once the worker and what it started have exited.
	_ = outW.Close()
	_ = errW.Close()
	out.Drain(workerpipe.Stdout, outR)
	out.Drain(workerpipe.Stderr, errR)

	if logRead != nil {
		_ = cmd.ExtraFiles[0].Close() // close the parent's copy of the write end so EOF propagates on child exit
		d.capture(inst.spec, logRead) // the hook owns reading + closing the read end
	}

	inst.cmd = cmd
	inst.out = out
	inst.pid = cmd.Process.Pid
	inst.state = runtime.StateRunning
	inst.stopping = false
	inst.listened = false
	inst.exit = runtime.Exit{}
	inst.done = make(chan struct{})
	// Saved before wait can reap the worker: an exited but unreaped worker still has the start time the entry needs.
	serr := d.saveLocked(inst)
	if serr != nil {
		_ = syscall.Kill(-inst.pid, syscall.SIGKILL)
	}
	go d.wait(inst, out)
	if serr != nil {
		return fault.Wrapf(serr, fault.KindOf(serr), op, "save worker %q", id)
	}
	return nil
}

// saveLocked records a started worker in the registry; caller holds d.mu.
func (d *driver) saveLocked(inst *instance) error {
	if d.reg == nil {
		return nil
	}
	st, err := d.startTime(inst.pid)
	if err != nil {
		return err
	}
	return d.reg.Put(procreg.Entry{
		ID: string(inst.id), PID: inst.pid, PGID: inst.pid, StartTime: st, Token: instanceFlag + string(inst.id),
		Files: []string{inst.portFile},
	})
}

// wait reaps the child and records its terminal state. It is the sole caller of
// cmd.Wait (Stop never calls Wait — it waits on inst.done instead).
func (d *driver) wait(inst *instance, out *workerpipe.Output) {
	err := inst.cmd.Wait()
	// The worker's exit reclaims what it started, as a container's exit tears down its PID namespace (ADR-0011 C4).
	_ = syscall.Kill(-inst.pid, syscall.SIGKILL)
	// The terminal state waits for the run's last output, so Logs has the line a load error ends with (ADR-0168); a
	// descendant that still holds the pipes costs at most outputWait.
	out.Close()
	select {
	case <-out.Done():
	case <-time.After(outputWait):
	}
	_, wrote := readPortFile(inst.portFile)

	d.mu.Lock()
	if d.reg != nil {
		_ = d.reg.Delete(string(inst.id))
	}
	inst.listened = inst.listened || wrote
	inst.exit = exitOf(inst.cmd.ProcessState)
	switch {
	case inst.stopping:
		inst.state = runtime.StateStopped
		inst.exit = runtime.Exit{Cause: runtime.ExitByStop}
	case err != nil:
		inst.state = runtime.StateFailed
	default:
		inst.state = runtime.StateStopped
	}
	done := inst.done
	d.mu.Unlock()
	close(done)
}

func (d *driver) Stop(_ context.Context, id runtime.InstanceID) error {
	const op = "runtime.process.Stop"
	d.mu.Lock()
	inst, ok := d.instances[id]
	if !ok {
		d.mu.Unlock()
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	if inst.state != runtime.StateRunning || inst.cmd == nil {
		// idempotent: already exited or never started. The instance reads Stopped afterwards, as containerd
		// reports once Stop has deleted the container (ADR-0142: a reclaimed replica is Stopped, never Failed).
		inst.state = runtime.StateStopped
		inst.exit = runtime.Exit{Cause: runtime.ExitByStop}
		inst.released = true
		d.mu.Unlock()
		return nil
	}
	inst.stopping = true
	pid := inst.pid
	done := inst.done
	d.mu.Unlock()

	terminate(pid, done, d.grace)
	d.mu.Lock()
	inst.released = true
	d.mu.Unlock()
	return nil
}

func (d *driver) Status(_ context.Context, id runtime.InstanceID) (runtime.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inst, ok := d.instances[id]
	if !ok {
		return runtime.Instance{}, fault.NotFoundf("runtime.process.Status", "instance %q not found", id)
	}
	return d.snapshotLocked(id, inst), nil
}

func (d *driver) Logs(_ context.Context, id runtime.InstanceID) (io.ReadCloser, error) {
	const op = "runtime.process.Logs"
	d.mu.Lock()
	inst, ok := d.instances[id]
	var out *workerpipe.Output
	if ok {
		out = inst.out
	}
	d.mu.Unlock()
	if !ok {
		return nil, fault.NotFoundf(op, "instance %q not found", id)
	}
	if out == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return out.Tail(), nil
}

func (d *driver) Exec(ctx context.Context, id runtime.InstanceID, cmd []string) error {
	const op = "runtime.process.Exec"
	if len(cmd) == 0 {
		return fault.Invalidf(op, "exec command must not be empty")
	}
	d.mu.Lock()
	_, ok := d.instances[id]
	d.mu.Unlock()
	if !ok {
		return fault.NotFoundf(op, "instance %q not found", id)
	}
	// Best-effort (V1): run the command as a child. The process driver does not
	// share namespaces with the instance; the containerd driver execs inside it.
	if err := exec.CommandContext(ctx, cmd[0], cmd[1:]...).Run(); err != nil { //nolint:gosec // cmd is platform-internal
		return fault.Wrapf(err, fault.Internal, op, "exec %v", cmd)
	}
	return nil
}

func (d *driver) List(_ context.Context, ns v1alpha1.NamespaceName) ([]runtime.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []runtime.Instance
	for id, inst := range d.instances {
		if inst.spec.Namespace == ns {
			out = append(out, d.snapshotLocked(id, inst))
		}
	}
	return out, nil
}

// Remove forgets an instance Stop has released, with its driver-owned port file (ADR-0143).
func (d *driver) Remove(_ context.Context, id runtime.InstanceID) error {
	d.mu.Lock()
	inst, ok := d.instances[id]
	if !ok {
		d.mu.Unlock()
		return nil
	}
	if !inst.released {
		d.mu.Unlock()
		return fault.Conflictf("runtime.process.Remove", "instance %q has not been stopped", id)
	}
	delete(d.instances, id)
	d.mu.Unlock()
	removeFiles(inst)
	return nil
}

// removeFiles deletes an instance's driver-owned port file.
func removeFiles(inst *instance) {
	_ = os.Remove(inst.portFile)
}

// Close stops every running instance at once, so shutdown takes one stop grace however many ignore SIGTERM, then deletes
// every instance's driver-owned files, which no later driver knows to remove.
func (d *driver) Close() error {
	d.mu.Lock()
	var running []runtime.InstanceID
	for id, inst := range d.instances {
		if inst.state == runtime.StateRunning && inst.cmd != nil {
			running = append(running, id)
		}
	}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range running {
		wg.Go(func() { _ = d.Stop(context.Background(), id) })
	}
	wg.Wait()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, inst := range d.instances {
		removeFiles(inst)
	}
	if d.reg != nil {
		return d.reg.Close()
	}
	return nil
}

// terminate sends the worker's process group SIGTERM, then SIGKILL after grace, and returns once the worker
// has exited (done closed).
func terminate(pid int, done <-chan struct{}, grace time.Duration) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(grace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
}

// snapshotLocked builds an Instance from internal state and latches Listened; caller holds d.mu.
func (d *driver) snapshotLocked(id runtime.InstanceID, inst *instance) runtime.Instance {
	var port int
	if inst.state == runtime.StateRunning {
		if p, ok := readPortFile(inst.portFile); ok {
			port = p
			inst.listened = true
		}
	}
	out := runtime.Instance{
		ID:        id,
		Namespace: inst.spec.Namespace,
		Name:      inst.spec.Name,
		OwnerKind: inst.spec.OwnerKind,
		Revision:  inst.spec.Revision,
		Replica:   inst.spec.Replica,
		PID:       inst.pid,
		State:     inst.state,
		CreatedAt: inst.createdAt,
		Listened:  inst.listened,
		Exit:      inst.exit,
	}
	// Surface the shim's endpoint once it has reported its port (ADR-0030).
	if port > 0 {
		out.IP = "127.0.0.1"
		out.Port = port
	}
	return out
}

// exitOf reads how a reaped worker ended (ADR-0160).
func exitOf(ps *os.ProcessState) runtime.Exit {
	if ps == nil {
		return runtime.Exit{}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return runtime.Exit{Cause: runtime.ExitBySignal, Signal: int(ws.Signal())}
	}
	if ps.Exited() {
		return runtime.Exit{Cause: runtime.ExitByCode, Code: ps.ExitCode()}
	}
	return runtime.Exit{}
}

// readPortFile reads the shim's bound port from the handshake file; ok=false until the
// shim has written a valid port.
func readPortFile(path string) (int, bool) {
	b, err := os.ReadFile(path) //nolint:gosec // path is driver-owned
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// envSlice renders an env map as KEY=VALUE entries (process env) after the host variables a worker inherits.
func envSlice(env map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); inheritedHostVar(k) {
			out = append(out, kv)
		}
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// inheritedHostVar reports whether a worker inherits the host variable k: only what a process needs to find
// its tools and run. The daemon's own configuration and credentials (FUNCD_TOKEN, backend keys) never reach a
// worker, whose env is otherwise its spec, as in the containerd driver (ADR-0028, ADR-0057).
func inheritedHostVar(k string) bool {
	switch k {
	case "PATH", "HOME", "TMPDIR", "TZ", "LANG":
		return true
	}
	return strings.HasPrefix(k, "LC_")
}
