// Package diskfull tells a store's out-of-space error apart (ADR-0297 Decision 3): a write that fails with ENOSPC is
// fault.InsufficientStorage, a 507, while the daemon keeps running.
package diskfull

import (
	"errors"
	"syscall"

	"github.com/pyvvo/funcd/api/fault"
)

// Wrapf wraps a store write's error as fault.Wrapf does: fault.InsufficientStorage when it matches syscall.ENOSPC,
// fault.Internal otherwise.
func Wrapf(err error, op, format string, a ...any) *fault.Error {
	kind := fault.Internal
	if Is(err) {
		kind = fault.InsufficientStorage
	}
	return fault.Wrapf(err, kind, op, format, a...)
}

// Is reports whether err matches syscall.ENOSPC.
func Is(err error) bool { return errors.Is(err, syscall.ENOSPC) }
