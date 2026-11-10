package config

import (
	"time"
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
	return boundedDuration("config.KVSweepInterval", "kvstore.sweepInterval", c.Kvstore.SweepInterval,
		DefaultKVSweepInterval, MinKVSweepInterval)
}

// CheckKVSweep refuses a kvstore.sweepInterval that KVSweepInterval refuses.
func (c Config) CheckKVSweep() []Finding {
	var f findings
	if _, err := c.KVSweepInterval(); err != nil {
		f.refuse("kvstore.sweepInterval", "%v", err)
	}
	return f.errs
}
