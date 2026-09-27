//go:build linux

package containerd

import (
	"context"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/errdefs"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime"
)

// mapErr maps a containerd error to an api/fault kind (NotFound / Unavailable /
// Internal), wrapping it with the operation context.
func mapErr(err error, op, format string, a ...any) error {
	switch {
	case errdefs.IsNotFound(err):
		return fault.Wrapf(err, fault.NotFound, op, format, a...)
	case errdefs.IsAlreadyExists(err):
		return fault.Wrapf(err, fault.Conflict, op, format, a...)
	case errdefs.IsUnavailable(err):
		return fault.Wrapf(err, fault.Unavailable, op, format, a...)
	default:
		return fault.Wrapf(err, fault.Internal, op, format, a...)
	}
}

// mapState maps a containerd process status to a runtime.State.
func mapState(s containerd.ProcessStatus) runtime.State {
	switch s {
	case containerd.Created:
		return runtime.StateCreated
	case containerd.Running, containerd.Pausing, containerd.Paused:
		return runtime.StateRunning
	case containerd.Stopped:
		return runtime.StateStopped
	default:
		return runtime.StateFailed
	}
}

// waitTask returns a channel that receives the task's exit code once it exits.
func waitTask(ctx context.Context, task containerd.Task) <-chan uint32 {
	out := make(chan uint32, 1)
	go func() {
		ch, err := task.Wait(ctx)
		if err != nil {
			out <- 1
			return
		}
		status := <-ch
		out <- status.ExitCode()
	}()
	return out
}

// waitProc returns a channel that receives an exec'd process's exit code.
func waitProc(ctx context.Context, proc containerd.Process) <-chan uint32 {
	out := make(chan uint32, 1)
	go func() {
		ch, err := proc.Wait(ctx)
		if err != nil {
			out <- 1
			return
		}
		status := <-ch
		out <- status.ExitCode()
	}()
	return out
}
