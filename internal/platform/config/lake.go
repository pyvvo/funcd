package config

import (
	"fmt"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The bounds of lake.saveInterval, both included (ADR-0274 Decision 1).
const (
	MinLakeSaveInterval = 500 * time.Millisecond
	MaxLakeSaveInterval = time.Hour
)

// LakeSaveInterval is lake.saveInterval parsed: 0 when empty, where every Lake saves its catalog before it answers
// (ADR-0279); fault.Invalid naming the key for a value that is no ADR-0194 duration or lies outside
// [MinLakeSaveInterval, MaxLakeSaveInterval].
func (c Config) LakeSaveInterval() (time.Duration, error) {
	const op = "config.LakeSaveInterval"
	v := c.Lake.SaveInterval
	if v == "" {
		return 0, nil
	}
	d, err := v1.ParseDuration(v)
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", "lake.saveInterval")
	}
	if err := v1.CheckDuration(op, fmt.Sprintf("config key %q", "lake.saveInterval"), d,
		v1.Duration(MinLakeSaveInterval), v1.Duration(MaxLakeSaveInterval)); err != nil {
		return 0, err
	}
	return time.Duration(d), nil
}

// CheckLake refuses a lake.saveInterval that LakeSaveInterval refuses.
func (c Config) CheckLake() []Finding {
	var f findings
	if _, err := c.LakeSaveInterval(); err != nil {
		f.refuse("lake.saveInterval", "%v", err)
	}
	return f.errs
}
