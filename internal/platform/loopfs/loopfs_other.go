//go:build !linux

// Package loopfs makes, grows, mounts and removes an ext4 filesystem kept in a preallocated file, mounted through a
// loop device: the volume image (ADR-0262) and the Lake engine's scratch (ADR-0294). It needs Linux.
package loopfs

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/pyvvo/funcd/api/fault"
)

// Create fails: a loop-mounted ext4 file needs Linux.
func Create(context.Context, string, int64, string) error {
	return fault.Unavailablef("loopfs.Create", "a loop-mounted filesystem needs Linux")
}

// Grow fails: a loop-mounted ext4 file needs Linux.
func Grow(context.Context, string, int64) error {
	return fault.Unavailablef("loopfs.Grow", "a loop-mounted filesystem needs Linux")
}

// Mount fails: a loop-mounted ext4 file needs Linux.
func Mount(string, string, ...string) error {
	return fault.Unavailablef("loopfs.Mount", "a loop-mounted filesystem needs Linux")
}

// MountedAt finds nothing off Linux.
func MountedAt(string) (string, bool) { return "", false }

// Unmount has nothing to unmount off Linux.
func Unmount(string) error { return nil }

// Remove removes dir and image; off Linux nothing is mounted.
func Remove(image, dir string) error {
	for _, p := range []string{dir, image} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fault.Wrapf(err, fault.Internal, "loopfs.Remove", "remove %s", p)
		}
	}
	return nil
}
