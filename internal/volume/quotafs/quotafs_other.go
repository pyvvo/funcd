//go:build !linux

// Package quotafs is the volume Limiter of the containerd runtime (ADR-0262 Decision 3), which runs on Linux only.
package quotafs

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// Config names the image file, where it is mounted, and its size.
type Config struct {
	Image         string
	Mount         string
	CapacityBytes int64
}

// FS is the mounted image; it exists only on Linux.
type FS struct{}

// Open fails: project quotas on a loop image need Linux.
func Open(context.Context, Config) (*FS, error) {
	return nil, fault.Unavailablef("quotafs.Open", "the volume image needs Linux")
}

// Limit is never reached off Linux.
func (*FS) Limit(string, uint32, int64) error {
	return fault.Unavailablef("quotafs.Limit", "the volume image needs Linux")
}

// Enforced reports a kernel limit.
func (*FS) Enforced() bool { return true }

// Free is never reached off Linux.
func (*FS) Free() (int64, error) {
	return 0, fault.Unavailablef("quotafs.Free", "the volume image needs Linux")
}

// Used is never reached off Linux.
func (*FS) Used(uint32) (int64, error) {
	return 0, fault.Unavailablef("quotafs.Used", "the volume image needs Linux")
}

// Root is where the volumes live.
func (*FS) Root() string { return "" }

// Close does nothing off Linux.
func (*FS) Close() error { return nil }
