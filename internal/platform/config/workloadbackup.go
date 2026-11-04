package config

import (
	"cmp"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The workloadBackup keys' defaults.
const (
	DefaultWorkloadBackupMaxListedKeys    = 1 << 20
	DefaultWorkloadBackupMaxListedKeySize = "256MiB"
	DefaultWorkloadBackupMaxCopyMembers   = 1024
)

// WorkloadBackupMaxListedKeyBytes is workloadBackup.maxListedKeySize in bytes; empty, its default. fault.Invalid names the
// key for a size that does not parse or is zero.
func (c Config) WorkloadBackupMaxListedKeyBytes() (int64, error) {
	const op = "config.WorkloadBackupMaxListedKeyBytes"
	v := cmp.Or(c.WorkloadBackup.MaxListedKeySize, DefaultWorkloadBackupMaxListedKeySize)
	n, err := v1.ByteSize(v).Bytes()
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", "workloadBackup.maxListedKeySize")
	}
	if n <= 0 {
		return 0, fault.Invalidf(op, "config key %q: %q must be above zero", "workloadBackup.maxListedKeySize", v)
	}
	return n, nil
}

// CheckWorkloadBackup refuses a workloadBackup.maxListedKeySize that WorkloadBackupMaxListedKeyBytes refuses.
func (c Config) CheckWorkloadBackup() []Finding {
	var f findings
	if _, err := c.WorkloadBackupMaxListedKeyBytes(); err != nil {
		f.refuse("workloadBackup.maxListedKeySize", "%v", err)
	}
	return f.errs
}
