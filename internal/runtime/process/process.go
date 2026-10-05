// Package process implements the runtime.Runtime port by supervising functions as
// plain OS child processes (ADR-0011). It is the cross-platform dev/e2e/CI driver —
// no root, no containerd — and is the driver the InMemory() harness and unit tests
// use. It is not an isolation boundary; production uses the containerd driver.
package process

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// stopGrace is how long Stop waits after SIGTERM before sending SIGKILL.
const stopGrace = 3 * time.Second

// instance tracks one supervised child process.
type instance struct {
	spec      runtime.WorkerSpec
	logPath   string
	portFile  string // the shim writes its OS-assigned port here once listening (ADR-0030)
	cmd       *exec.Cmd
	pid       int
	state     runtime.State
	stopping  bool
	released  bool // Stop has run and the process is gone, so Remove may forget it (ADR-0143)
	done      chan struct{}
	createdAt time.Time
}

// driver is the process-based runtime.Runtime.
type driver struct {
	mu        sync.Mutex
	instances map[runtime.InstanceID]*instance
	capture   runtime.LogCaptureFunc // optional Path B log-channel hook (ADR-0081); nil = disabled
}

// New returns a process-backed runtime.Runtime (cross-platform; dev/e2e/CI).
func New() runtime.Runtime {
	return &driver{instances: map[runtime.InstanceID]*instance{}}
}

// SetLogCapture installs the per-instance structured-log hook (runtime.LogCapturer, ADR-0081). When
// set, Start passes the shim a write pipe as fd 3 (FUNCD_LOG_FD=3) and hands the read end to the hook.
func (d *driver) SetLogCapture(fn runtime.LogCaptureFunc) {
	d.mu.Lock()
	d.capture = fn
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

	logPath := spec.LogPath
	if logPath == "" {
		f, err := os.CreateTemp("", "funcd-worker-*.log")
		if err != nil {
			return runtime.Instance{}, fault.Wrapf(err, fault.Internal, op, "create log file")
		}
		logPath = f.Name()
		_ = f.Close()
	}
	if replace {
		removeFiles(old)
	}
	inst := &instance{
		spec:      spec,
		logPath:   logPath,
		portFile:  logPath + ".port", // driver-owned; the shim writes its bound port here
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

	if err := os.MkdirAll(filepath.Dir(inst.logPath), 0o750); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create log dir")
	}
	logFile, err := os.OpenFile(inst.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open log file")
	}

	_ = os.Remove(inst.portFile)                                        // clear any stale port from a prior start
	cmd := exec.Command(inst.spec.Command[0], inst.spec.Command[1:]...) //nolint:gosec // command is platform-internal, from the controller-built spec
	cmd.Stdout = logFile
	cmd.Stderr = logFile
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
			_ = logFile.Close()
			return fault.Wrapf(perr, fault.Internal, op, "create log pipe")
		}
		cmd.ExtraFiles = []*os.File{pw}
		cmd.Env = append(cmd.Env, "FUNCD_LOG_FD=3")
		logRead = pr
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		if logRead != nil {
			_ = logRead.Close()
			_ = cmd.ExtraFiles[0].Close()
		}
		// no process ran, so the instance keeps its state (Created for a new one) and is started again (ADR-0142)
		return fault.Wrapf(err, fault.Internal, op, "start process")
	}

	if logRead != nil {
		_ = cmd.ExtraFiles[0].Close() // close the parent's copy of the write end so EOF propagates on child exit
		d.capture(inst.spec, logRead) // the hook owns reading + closing the read end
	}

	inst.cmd = cmd
	inst.pid = cmd.Process.Pid
	inst.state = runtime.StateRunning
	inst.stopping = false
	inst.done = make(chan struct{})
	go d.wait(inst, logFile)
	return nil
}

// wait reaps the child and records its terminal state. It is the sole caller of
// cmd.Wait (Stop never calls Wait — it waits on inst.done instead).
func (d *driver) wait(inst *instance, logFile *os.File) {
	err := inst.cmd.Wait()
	// The worker's exit reclaims what it started, as a container's exit tears down its PID namespace (ADR-0011 C4).
	_ = syscall.Kill(-inst.pid, syscall.SIGKILL)
	_ = logFile.Close()

	d.mu.Lock()
	switch {
	case inst.stopping:
		inst.state = runtime.StateStopped
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
		inst.released = true
		d.mu.Unlock()
		return nil
	}
	inst.stopping = true
	pid := inst.pid
	done := inst.done
	d.mu.Unlock()

	terminate(pid, done)
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
	logPath := ""
	if ok {
		logPath = inst.logPath
	}
	d.mu.Unlock()
	if !ok {
		return nil, fault.NotFoundf(op, "instance %q not found", id)
	}
	f, err := os.Open(logPath) //nolint:gosec // logPath is driver-owned
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "open log file")
	}
	return f, nil
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

// Remove forgets an instance Stop has released, with the driver-owned port file and, when the driver created it, the
// log file (ADR-0143).
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

// removeFiles deletes an instance's driver-owned port file and, when the driver created it, its log file.
func removeFiles(inst *instance) {
	_ = os.Remove(inst.portFile)
	if inst.spec.LogPath == "" {
		_ = os.Remove(inst.logPath)
	}
}

// Close stops every running instance at once, so shutdown takes one stopGrace however many ignore SIGTERM, then deletes
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
	for _, inst := range d.instances {
		removeFiles(inst)
	}
	d.mu.Unlock()
	return nil
}

// terminate sends the worker's process group SIGTERM, then SIGKILL after stopGrace, and returns once the worker
// has exited (done closed).
func terminate(pid int, done <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	}
}

// snapshotLocked builds an Instance from internal state; caller holds d.mu.
func (d *driver) snapshotLocked(id runtime.InstanceID, inst *instance) runtime.Instance {
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
	}
	// Surface the shim's endpoint once it has reported its port (ADR-0030).
	if inst.state == runtime.StateRunning {
		if port, ok := readPortFile(inst.portFile); ok {
			out.IP = "127.0.0.1"
			out.Port = port
		}
	}
	return out
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
