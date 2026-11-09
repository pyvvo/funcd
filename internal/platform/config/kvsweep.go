package config

import (
	"fmt"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The default and the floor of kvstore.sweepInterval (ADR-0320).
const (
	DefaultKVSweepInterval = time.Second
	MinKVSweepInterval     = 100 * time.Millisecond
)

// KVSweepInterval is kvstore.sweepInterval parsed, how often the KV service deletes the keys that expired (ADR-0320):
// DefaultKVSweepInterval when empty; fault.Invalid naming the key for a value that is no ADR-0194 duration or lies below
// MinKVSweepInterval.
func (c Config) KVSweepInterval() (time.Duration, error) {
	const op, key = "config.KVSweepInterval", "kvstore.sweepInterval"
	v := c.Kvstore.SweepInterval
	if v == "" {
		return DefaultKVSweepInterval, nil
	}
	d, err := v1.ParseDuration(v)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", key)
	}
	if err := v1.CheckDuration(op, fmt.Sprintf("config key %q", key), d, v1.Duration(MinKVSweepInterval),
		v1.MaxDuration); err != nil {
		return 0, err
	}
	return time.Duration(d), nil
}

// CheckKVSweep refuses a kvstore.sweepInterval that KVSweepInterval refuses.
func (c Config) CheckKVSweep() []Finding {
	var f findings
	if _, err := c.KVSweepInterval(); err != nil {
		f.refuse("kvstore.sweepInterval", "%v", err)
	}
	return f.errs
}
