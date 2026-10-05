//go:build linux

package containerd

import (
	"context"
	"os"
	"path/filepath"

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

// mapState maps a containerd task status to a runtime.State. A task that exited with a non-zero status is
// Failed, as the port defines it (ADR-0142); a zero exit is Stopped.
func mapState(st containerd.Status) runtime.State {
	if st.Status == containerd.Stopped && st.ExitStatus != 0 {
		return runtime.StateFailed
	}
	switch st.Status {
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

// exitSignalOffset is what a reaper adds to the number of the signal that ended a task (containerd pkg/sys/reaper).
const exitSignalOffset = 128

// exitOf reads how a task ended from its status (ADR-0160): 255 is containerd's unknown exit status, 129–192 a signal.
func exitOf(st containerd.Status) runtime.Exit {
	switch {
	case st.Status != containerd.Stopped || st.ExitStatus == containerd.UnknownExitStatus:
		return runtime.Exit{}
	case st.ExitStatus > exitSignalOffset && st.ExitStatus <= exitSignalOffset+64:
		return runtime.Exit{Cause: runtime.ExitBySignal, Signal: int(st.ExitStatus - exitSignalOffset)}
	}
	return runtime.Exit{Cause: runtime.ExitByCode, Code: int(st.ExitStatus)}
}

// portWritten reports whether a regular file sits at the port path in boot dir dir. It only calls os.Lstat: the
// sandbox writes the dir, so the host never opens what is there (ADR-0160).
func portWritten(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dir, "port"))
	return err == nil && fi.Mode().IsRegular()
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
