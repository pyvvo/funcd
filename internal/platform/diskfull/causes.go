package diskfull

import "github.com/pyvvo/funcd/api/fault"

// ErrDiskFull is the known cause of a write that fails with ENOSPC (ADR-0330 Decision 1), which Wrapf raises for every
// store that calls it.
var ErrDiskFull = fault.NewKnownCause(fault.InsufficientStorage, "diskfull.disk-full",
	"The disk that holds funcd's data is full.",
	"Free space on that disk, then retry; funcd keeps running meanwhile.")
