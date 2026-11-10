package config

import (
	"fmt"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The default and the floor of storage.quarantineRetention (ADR-0288 Decision 4).
const (
	DefaultQuarantineRetention = 168 * time.Hour
	MinQuarantineRetention     = time.Hour
)

// QuarantineRetention is storage.quarantineRetention parsed, how long the start-up reclaims keep the data no object
// names (ADR-0288): DefaultQuarantineRetention when empty; fault.Invalid naming the key for a value that is no ADR-0194
// duration or lies below MinQuarantineRetention.
func (c Config) QuarantineRetention() (time.Duration, error) {
	return boundedDuration("config.QuarantineRetention", "storage.quarantineRetention", c.Storage.QuarantineRetention,
		DefaultQuarantineRetention, MinQuarantineRetention)
}

// boundedDuration parses value, the ADR-0194 duration of config key key: def when empty; fault.Invalid naming the key
// for a value that is no duration or lies below floor.
func boundedDuration(op, key, value string, def, floor time.Duration) (time.Duration, error) {
	if value == "" {
		return def, nil
	}
	d, err := v1.ParseDuration(value)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", key)
	}
	if err := v1.CheckDuration(op, fmt.Sprintf("config key %q", key), d, v1.Duration(floor), v1.MaxDuration); err != nil {
		return 0, err
	}
	return time.Duration(d), nil
}

// CheckQuarantine refuses a storage.quarantineRetention that QuarantineRetention refuses.
func (c Config) CheckQuarantine() []Finding {
	var f findings
	if _, err := c.QuarantineRetention(); err != nil {
		f.refuse("storage.quarantineRetention", "%v", err)
	}
	return f.errs
}
