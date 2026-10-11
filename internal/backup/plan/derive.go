// Package plan turns recovery objectives into backup settings (ADR-0223): Derive maps them to keys, Apply splices the
// keys into a file's bytes, CheckConfig runs the daemon's loader on the result and CheckRTO bounds the recovery time.
// It holds no rule of the daemon's own; cmd/funcdctl wires the flags and the files.
package plan

import (
	"strconv"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// Component is what a plan sets: the platform backup, the KV export or the blob mirror keys of funcdconfig.yaml, or a
// BackupSchedule.
type Component string

// The components of ADR-0223 Decision 1.
const (
	Platform Component = "platform"
	KV       Component = "kv"
	Blob     Component = "blob"
	Schedule Component = "schedule"
)

// Objectives are the operator's targets; a zero field was not given (Decision 1). Size and Speed are bytes and bytes
// per second.
type Objectives struct {
	RPO, Keep, RTO time.Duration
	Size, Speed    int64
}

// Setting is one derived key: Key the dotted path from the document root, Value the scalar's text.
type Setting struct{ Key, Value string }

// Change is one key Apply edited; Old "" ⇒ the key was absent.
type Change struct{ Key, Old, New string }

// defaultKVRebaseline is kvstore.backup.rebaseline when empty, as cmd/funcd's kvBackup reads it (ADR-0209).
const defaultKVRebaseline = 24 * time.Hour

// Derive maps o to c's keys (Decision 2); cur is config.Parse of the located file, read for the KV floor and the blob
// interval. An absent objective sets no key.
func Derive(c Component, o Objectives, cur config.Config) ([]Setting, error) {
	const op = "plan.Derive"
	var s []Setting
	switch c {
	case Platform:
		if o.RPO > 0 {
			s = append(s, Setting{"backup.interval", duration(o.RPO / 2)}, Setting{"backup.objectives.rpo", duration(o.RPO)})
		}
		if o.Keep > 0 {
			s = append(s, ladderSettings("backup.retention.", o.Keep, 0)...)
		}
	case KV:
		if o.RPO > 0 {
			s = append(s, Setting{"kvstore.backup.interval", duration(o.RPO / 2)})
		}
		if o.Keep > 0 {
			floor, err := kvHourlyFloor(cur)
			if err != nil {
				return nil, err
			}
			s = append(s, ladderSettings("kvstore.backup.retention.", o.Keep, floor)...)
		}
	case Blob:
		interval := o.RPO / 2
		if o.RPO > 0 {
			s = append(s, Setting{"blob.backup.interval", duration(interval)})
		}
		if o.Keep > 0 {
			if o.RPO == 0 {
				t, err := cur.BlobTimes()
				if err != nil {
					return nil, err
				}
				interval = t.Interval
			}
			if o.Keep > time.Duration(v1.MaxDuration)-interval {
				return nil, fault.Invalidf(op, "--keep %s plus blob.backup.interval %s exceeds %s", v1.Duration(o.Keep),
					v1.Duration(interval), v1.MaxDuration)
			}
			s = append(s, Setting{"blob.backup.retention", duration(o.Keep + interval)})
		}
	case Schedule:
		if o.RPO > 0 {
			expr, err := Cron(o.RPO)
			if err != nil {
				return nil, err
			}
			s = append(s, Setting{"spec.schedule", expr})
		}
		if o.Keep > 0 {
			s = append(s, Setting{"spec.ttl", duration(o.Keep)})
		}
	default:
		return nil, fault.Invalidf(op, "component %q: want platform, kv, blob or schedule", c)
	}
	return s, nil
}

// Ladder gives (hourly, daily, weekly) in ADR-0203 Decision 5's units, 0 unused, so the longest kept class covers keep.
func Ladder(keep time.Duration) (hourly, daily, weekly int) {
	switch {
	case keep <= 48*time.Hour:
		return ceilUnits(keep, time.Hour), 0, 0
	case keep <= 720*time.Hour:
		return 48, ceilUnits(keep, 24*time.Hour), 0
	default:
		return 48, 30, ceilUnits(keep, 168*time.Hour)
	}
}

// Cron is the largest period P ≤ rpo ÷ 2 that divides the hour or the day, as a cron expression (ADR-0211 grammar):
// every P minutes, every P hours, or daily at the 24 h cap. rpo ÷ 2 under a minute is fault.Invalid naming --rpo.
func Cron(rpo time.Duration) (string, error) {
	half := rpo / 2
	switch {
	case half < time.Minute:
		return "", fault.Invalidf("plan.Cron", "--rpo %s: a cron fires at most once a minute, so want at least 2m",
			v1.Duration(rpo.Truncate(time.Millisecond)))
	case half >= 24*time.Hour:
		return "0 0 * * *", nil
	}
	for _, h := range []int{12, 8, 6, 4, 3, 2, 1} {
		if half >= time.Duration(h)*time.Hour {
			if h == 1 {
				return "0 * * * *", nil
			}
			return "0 */" + strconv.Itoa(h) + " * * *", nil
		}
	}
	for _, m := range []int{30, 20, 15, 12, 10, 6, 5, 4, 3, 2} {
		if half >= time.Duration(m)*time.Minute {
			return "*/" + strconv.Itoa(m) + " * * * *", nil
		}
	}
	return "* * * * *", nil
}

// ladderSettings is Ladder(keep) under prefix, hourly raised to floor.
func ladderSettings(prefix string, keep time.Duration, floor int) []Setting {
	hourly, daily, weekly := Ladder(keep)
	hourly = max(hourly, floor)
	return []Setting{
		{prefix + "hourly", strconv.Itoa(hourly)},
		{prefix + "daily", strconv.Itoa(daily)},
		{prefix + "weekly", strconv.Itoa(weekly)},
	}
}

// kvHourlyFloor is 24 × ⌊rebaseline ÷ 24h⌋ + 1: the least kvstore.backup.retention.hourly ADR-0209 Decision 5 accepts
// beside cur's rebaseline (empty ⇒ 24h).
func kvHourlyFloor(cur config.Config) (int, error) {
	b := defaultKVRebaseline
	if v := cur.Kvstore.Backup.Rebaseline; v != "" {
		d, err := v1.ParseDuration(v)
		if err != nil {
			return 0, fault.Wrapf(err, fault.Invalid, "plan.Derive", "config key %q", "kvstore.backup.rebaseline")
		}
		b = time.Duration(d)
	}
	return 24*int(b/(24*time.Hour)) + 1, nil
}

// duration is d truncated to the ms in ADR-0194's normalized form.
func duration(d time.Duration) string { return v1.Duration(d.Truncate(time.Millisecond)).String() }

// ceilUnits is ⌈d ÷ unit⌉ for d ≥ 0.
func ceilUnits(d, unit time.Duration) int {
	n := d / unit
	if d%unit != 0 {
		n++
	}
	return int(n)
}
