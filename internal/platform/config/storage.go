package config

import (
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// DefaultStorageMinFreeSize is the free space funcd keeps on the data filesystem when storage.minFreeSize is empty
// (ADR-0296 Decision 1).
const DefaultStorageMinFreeSize = "2GiB"

// StorageMinFreeBytes is storage.minFreeSize in bytes: empty is DefaultStorageMinFreeSize, and 0 turns the floor off.
// fault.Invalid names the key for a size that does not parse.
func (c Config) StorageMinFreeBytes() (int64, error) {
	v := c.Storage.MinFreeSize
	if v == "" {
		v = DefaultStorageMinFreeSize
	}
	n, err := v1.ByteSize(v).Bytes()
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, "config.StorageMinFreeBytes", "config key %q", "storage.minFreeSize")
	}
	return n, nil
}

// CheckStorage refuses a storage.minFreeSize that StorageMinFreeBytes refuses, so a bad value fails the config's load
// instead of a start that safe mode counts.
func (c Config) CheckStorage() []Finding {
	var f findings
	if _, err := c.StorageMinFreeBytes(); err != nil {
		f.refuse("storage.minFreeSize", "%v", err)
	}
	return f.errs
}
