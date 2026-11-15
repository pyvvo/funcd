// Package diskfull tells a store's out-of-space error apart (ADR-0297 Decision 3): a write that fails with ENOSPC is
// fault.InsufficientStorage, a 507, while the daemon keeps running.
package diskfull

import (
	"errors"
	"syscall"

	"github.com/pyvvo/funcd/api/fault"
)

// Wrapf wraps a store write's error as fault.Wrapf does: raised from ErrDiskFull, of kind fault.InsufficientStorage,
// when it matches syscall.ENOSPC, and fault.Internal otherwise.
func Wrapf(err error, op, format string, a ...any) *fault.Error {
	if Is(err) {
		return ErrDiskFull.Wrapf(err, op, format, a...)
	}
	return fault.Wrapf(err, fault.Internal, op, format, a...)
}

// Is reports whether err matches syscall.ENOSPC.
func Is(err error) bool { return errors.Is(err, syscall.ENOSPC) }
