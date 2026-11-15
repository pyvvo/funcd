package quota

import "github.com/pyvvo/funcd/api/fault"

// The known causes of a refused write (ADR-0330 Decision 2), shared by every client whose writes the quota decides.
var (
	ErrFloorReached = fault.NewKnownCause(fault.InsufficientStorage, "quota.floor-reached",
		"funcd's data filesystem is below storage.minFreeSize, or the write would take it there.",
		"Free space on the data filesystem, then retry; reads and deletes still work.")
	ErrExceeded = fault.NewKnownCause(fault.InsufficientStorage, "quota.exceeded",
		"The write would take what its store holds past spec.quota.",
		"Delete data to make room, or raise the store's spec.quota.")
	ErrUnreadable = fault.NewKnownCause(fault.Unavailable, "quota.unreadable",
		"funcd could not read the store's quota or measure what the store holds.",
		"Retry the write.")
)
