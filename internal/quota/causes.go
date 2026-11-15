package quota

import "github.com/pyvvo/funcd/api/fault"

// The known causes of a refused write (ADR-0330 Decision 2), shared by every client whose writes the quota decides.
var (
	ErrFloorReached = fault.NewKnownCause(fault.InsufficientStorage, "quota.floor-reached",
		"funcd's data filesystem is below storage.minFreeSize, or the write would take it there.",
		"Free space on the data filesystem, then retry; reads and deletes still work.")
	ErrExceeded = fault.NewKnownCause(fault.InsufficientStorage, "quota.exceeded",
		"The write would take what a store or a prefix of a Bucket holds past its quota; the detail names which one.",
		"Delete data to make room, or raise that quota: the store's spec.quota, or the quota of the prefix in its "+
			"Bucket's spec.prefixes.")
	ErrUnreadable = fault.NewKnownCause(fault.Unavailable, "quota.unreadable",
		"funcd could not read the store's quota or measure what the store holds.",
		"Retry the write.")
)
