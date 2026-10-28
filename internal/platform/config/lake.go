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

// DefaultLakeCatalogMaxSize caps one Lake's catalog when lake.catalogMaxSize is empty (ADR-0293 Decision 3, its
// default lowered by ADR-0294).
const DefaultLakeCatalogMaxSize = "256MiB"

// The Lake engine's scratch (ADR-0294 Decision 6) holds the catalog twice, the copy and its checkpoint, plus at least
// lakeScratchMargin for ext4's own overhead and the spills; empty, it is at least MinLakeScratchBytes.
const (
	MinLakeScratchBytes = 1 << 30
	lakeScratchMargin   = 512 << 20
)

// LakeScratchBytes is lake.scratchSize in bytes: empty, the larger of MinLakeScratchBytes and twice the catalog cap
// plus lakeScratchMargin; fault.Invalid names the key for a size that does not parse or is below that sum.
func (c Config) LakeScratchBytes() (int64, error) {
	const op = "config.LakeScratchBytes"
	catalogMax, err := c.LakeCatalogMaxBytes()
	if err != nil {
		return 0, err
	}
	need := 2*catalogMax + lakeScratchMargin
	v := c.Lake.ScratchSize
	if v == "" {
		return max(MinLakeScratchBytes, need), nil
	}
	n, err := v1.ByteSize(v).Bytes()
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", "lake.scratchSize")
	}
	if n < need {
		return 0, fault.Invalidf(op, "config key %q: %s is below twice lake.catalogMaxSize plus 512MiB (%d bytes)",
			"lake.scratchSize", v, need)
	}
	return n, nil
}

// LakeCatalogMaxBytes is lake.catalogMaxSize in bytes; empty, DefaultLakeCatalogMaxSize or s3gateway.maxUploadBytes
// when that is smaller. fault.Invalid names the key for a size that does not parse, is zero, or passes
// s3gateway.maxUploadBytes, which the gateway would refuse first with no word of the setting.
func (c Config) LakeCatalogMaxBytes() (int64, error) {
	const op = "config.LakeCatalogMaxBytes"
	maxUpload := c.S3Gateway.MaxUploadBytes
	if maxUpload <= 0 {
		maxUpload = 1 << 30
	}
	v := c.Lake.CatalogMaxSize
	if v == "" {
		n, _ := v1.ByteSize(DefaultLakeCatalogMaxSize).Bytes()
		return min(n, maxUpload), nil
	}
	n, err := v1.ByteSize(v).Bytes()
	if err != nil {
		return 0, fault.Wrapf(err, fault.Invalid, op, "config key %q", "lake.catalogMaxSize")
	}
	if n <= 0 {
		return 0, fault.Invalidf(op, "config key %q: %q must be above zero", "lake.catalogMaxSize", v)
	}
	if n > maxUpload {
		return 0, fault.Invalidf(op, "config key %q: %s is over s3gateway.maxUploadBytes (%d bytes)", "lake.catalogMaxSize", v, maxUpload)
	}
	return n, nil
}

// CheckLake refuses a lake.saveInterval that LakeSaveInterval refuses, a lake.catalogMaxSize that LakeCatalogMaxBytes
// refuses and a lake.scratchSize that LakeScratchBytes refuses.
func (c Config) CheckLake() []Finding {
	var f findings
	if _, err := c.LakeSaveInterval(); err != nil {
		f.refuse("lake.saveInterval", "%v", err)
	}
	if _, err := c.LakeCatalogMaxBytes(); err != nil {
		f.refuse("lake.catalogMaxSize", "%v", err)
	} else if _, err := c.LakeScratchBytes(); err != nil {
		f.refuse("lake.scratchSize", "%v", err)
	}
	return f.errs
}
