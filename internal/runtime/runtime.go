// Package runtime is the function worker port (ADR-0011): create, start, stop,
// observe, and harvest logs from a worker instance (one replica of a function).
//
// The port is driver-independent and imports no container library. Two drivers
// implement it: a cross-platform process driver (internal/runtime/process — the
// dev/e2e/CI driver) and a Linux-only containerd/runc driver
// (internal/runtime/containerd). The controller maps Function/Revision/RuntimeClass
// (ADR-0003) into a WorkerSpec; this port does not read resources, and functions
// never call it (internal-plane only).
package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
)

// InstanceID identifies one worker instance (a function replica).
type InstanceID string

// State is the lifecycle state of a worker instance.
type State string

const (
	// StateCreated means the worker exists but its process is not started.
	StateCreated State = "created"
	// StateRunning means the worker process is running.
	StateRunning State = "running"
	// StateStopped means the worker process exited or was stopped.
	StateStopped State = "stopped"
	// StateFailed means the worker process exited abnormally.
	StateFailed State = "failed"
)

// Terminal reports whether an instance in this state has exited (Stopped or Failed).
func (s State) Terminal() bool { return s == StateStopped || s == StateFailed }

// Limits bounds a worker's resources (0 = unlimited).
type Limits struct {
	MemoryBytes int64
	CPUs        float64
}

// Mount is a host→container bind mount (ADR-0032). Container drivers (containerd) honor it;
// the process driver ignores it (no container filesystem). Used to deliver the function
// artifact read-only into the curated-image worker.
type Mount struct {
	Source   string // host path
	Target   string // container path
	ReadOnly bool
}

// WorkerSpec is the driver-independent request to run one function replica. The
// controller assembles it from Function/Revision/RuntimeClass (ADR-0003).
type WorkerSpec struct {
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Revision  v1alpha1.ObjectName // the Revision this worker runs (ADR-0143); "" outside the Function lifecycle
	Replica   int
	Image     string            // runtime image (containerd driver)
	Command   []string          // launch command / container args
	Env       map[string]string // typed-flat; no any
	Mounts    []Mount           // host→container bind mounts (container drivers; ADR-0032)
	Limits    Limits
	LogPath   string // file for captured stdout+stderr ("" → driver picks a temp file)
}

// Instance is the observed state of one worker.
type Instance struct {
	ID        InstanceID
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Revision  v1alpha1.ObjectName // as created (ADR-0143)
	Replica   int
	PID       int
	State     State
	IP        string // worker endpoint host: netns IP (containerd) or 127.0.0.1 (process driver, ADR-0030)
	Port      int    // worker HTTP port: the shim's listening port (ADR-0030); 0 until resolved
	CreatedAt time.Time
}

// Runtime is the worker port: create/start/stop/observe one function replica.
// Errors are api/fault; every method is ctx-first; the port imports no container
// library. Implementations are internal-plane only — functions never call them.
type Runtime interface {
	// Create makes a worker from spec without starting it.
	Create(ctx context.Context, spec WorkerSpec) (Instance, error)
	// Start runs the worker's process.
	Start(ctx context.Context, id InstanceID) error
	// Stop terminates the process (SIGTERM, then SIGKILL); it is idempotent.
	Stop(ctx context.Context, id InstanceID) error
	// Status returns the observed instance; fault.NotFound if unknown.
	Status(ctx context.Context, id InstanceID) (Instance, error)
	// Logs returns a reader over the instance's captured stdout+stderr.
	Logs(ctx context.Context, id InstanceID) (io.ReadCloser, error)
	// Exec runs a command in the instance's context (best-effort in V1).
	Exec(ctx context.Context, id InstanceID, cmd []string) error
	// List returns the instances in a namespace.
	List(ctx context.Context, ns v1alpha1.NamespaceName) ([]Instance, error)
	// Remove forgets an instance after Stop has released it, with its per-instance files: Status then reports
	// fault.NotFound and List omits it. An instance Stop has not released — running, created, or exited on its own —
	// is fault.Conflict; an unknown one is a no-op (ADR-0143).
	Remove(ctx context.Context, id InstanceID) error
	// Close releases the driver's resources.
	Close() error
}

// LogCaptureFunc receives one instance's structured telemetry channel (ADR-0081 Path B): r yields
// the NDJSON records the runtime shim writes to its log channel. The composition root supplies it
// (wiring a funclog Pump); the implementation owns reading and closing r. spec identifies the
// instance whose logs these are.
type LogCaptureFunc func(spec WorkerSpec, r io.ReadCloser)

// LogCapturer is the OPTIONAL capability a runtime driver implements to deliver per-instance
// structured logs (ADR-0081). funcd sets the hook after building the funclog sink; a driver that
// does not implement it simply captures no Path B telemetry. Keeping it off the Runtime port keeps
// the port free of the observability concern (the port imports no observability package).
type LogCapturer interface {
	// SetLogCapture installs (or clears, with nil) the per-instance log-channel hook.
	SetLogCapture(LogCaptureFunc)
}

// NewInstanceID builds the canonical id "<ns>/<name>/r<replica>", or "<ns>/<name>/<revision>/r<replica>" for a worker
// of a Revision, so two revisions of one replica can run at once (ADR-0143).
func NewInstanceID(ns v1alpha1.NamespaceName, name, revision v1alpha1.ObjectName, replica int) InstanceID {
	if revision == "" {
		return InstanceID(fmt.Sprintf("%s/%s/r%d", ns, name, replica))
	}
	return InstanceID(fmt.Sprintf("%s/%s/%s/r%d", ns, name, revision, replica))
}
