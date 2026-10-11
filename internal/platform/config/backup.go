package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The platform backup's run times when a key is empty (ADR-0205 Decision 1).
const (
	defaultBackupInterval      = time.Hour
	defaultBackupRetryInterval = 5 * time.Minute
	defaultBackupRPO           = 2 * time.Hour
)

// BackupTimes are backup.interval, .objectives.rpo and .retryInterval, parsed (ADR-0205 Decision 1).
type BackupTimes struct{ Interval, RPO, RetryInterval time.Duration }

// Finding is one outcome of CheckBackup (ADR-0205 Decision 3): Key is the first key its rule names and Message names
// every key; Error refuses the start, else it is a warning.
type Finding struct {
	Key, Message string
	Error        bool
}

// BackupTimes parses the run times with ADR-0194's grammar within [1ms, MaxDuration]: an empty key is its default, a
// malformed or out-of-bounds one fault.Invalid naming it.
func (c Config) BackupTimes() (BackupTimes, error) {
	var t BackupTimes
	for _, k := range c.backupTimeKeys(&t) {
		if err := k.parse(); err != nil {
			return BackupTimes{}, err
		}
	}
	return t, nil
}

type backupTimeKey struct {
	key, value string
	def        time.Duration
	dst        *time.Duration
}

func (c Config) backupTimeKeys(t *BackupTimes) []backupTimeKey {
	return []backupTimeKey{
		{"backup.interval", c.Backup.Interval, defaultBackupInterval, &t.Interval},
		{"backup.objectives.rpo", c.Backup.Objectives.RPO, defaultBackupRPO, &t.RPO},
		{"backup.retryInterval", c.Backup.RetryInterval, defaultBackupRetryInterval, &t.RetryInterval},
	}
}

func (k backupTimeKey) parse() error {
	const op = "config.BackupTimes"
	if k.value == "" {
		*k.dst = k.def
		return nil
	}
	d, err := v1.ParseDuration(k.value)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "config key %q", k.key)
	}
	if err := v1.CheckDuration(op, fmt.Sprintf("config key %q", k.key), d, v1.Duration(time.Millisecond), v1.MaxDuration); err != nil {
		return err
	}
	*k.dst = time.Duration(d)
	return nil
}

// findings collects CheckBackup's outcomes, errors apart from warnings.
type findings struct{ errs, warns []Finding }

func (f *findings) refuse(key, format string, args ...any) {
	f.errs = append(f.errs, Finding{Key: key, Message: fmt.Sprintf(format, args...), Error: true})
}

func (f *findings) warn(key, format string, args ...any) {
	f.warns = append(f.warns, Finding{Key: key, Message: fmt.Sprintf(format, args...)})
}

// CheckBackup applies ADR-0205 Decision 3's rows that read only the config, errors first; the daemon's Validate and
// F112 run it. The run times are checked always, every other row once backup.target is set; without a target, a
// backup key off its default is reported as ignored. kvstore.backup.* and blob.* are checked elsewhere.
func (c Config) CheckBackup() []Finding {
	var f findings
	var t BackupTimes
	timesOK := true
	for _, k := range c.backupTimeKeys(&t) {
		if err := k.parse(); err != nil {
			f.refuse(k.key, "%v", err)
			timesOK = false
		}
	}
	if c.Backup.Target == "" {
		if keys := c.backupKeysOffDefault(); len(keys) > 0 {
			f.warn(keys[0], "%s set without backup.target: ignored, no platform backup runs", strings.Join(keys, ", "))
		}
		return append(f.errs, f.warns...)
	}
	c.checkBackupTarget(&f)
	retentionOK := c.checkBackupRetention(&f)
	c.checkBackupEncryption(&f)
	if timesOK {
		c.checkBackupTimes(&f, t, retentionOK)
	}
	if c.Storage.Mode == "memory" {
		f.warn("backup.target", "storage.mode is memory: backup.target is ignored, no platform backup runs")
	}
	return append(f.errs, f.warns...)
}

// checkBackupTarget applies ADR-0203 Decision 5's scheme rules.
func (c Config) checkBackupTarget(f *findings) {
	b := c.Backup
	u, err := url.Parse(b.Target)
	switch {
	case err != nil:
		f.refuse("backup.target", "backup.target: %v", err)
	case u.Scheme == "file" && b.CredentialsFile != "":
		f.refuse("backup.credentialsFile", "backup.credentialsFile applies to an s3:// backup.target only")
	case u.Scheme != "s3" && u.Scheme != "file":
		f.refuse("backup.target", "backup.target %q: the scheme is not s3 or file", b.Target)
	}
}

// checkBackupRetention applies ADR-0203 Decision 5's ranges and reports whether they hold.
func (c Config) checkBackupRetention(f *findings) bool {
	r := c.Backup.Retention
	n := len(f.errs)
	for _, k := range []struct {
		key      string
		v, least int
	}{
		{"backup.retention.hourly", r.Hourly, 1},
		{"backup.retention.daily", r.Daily, 0},
		{"backup.retention.weekly", r.Weekly, 0},
		{"backup.retention.verified", r.Verified, 1},
		{"backup.retention.preUpgrade", r.PreUpgrade, 1},
	} {
		if k.v < k.least {
			f.refuse(k.key, "%s is %d: want at least %d", k.key, k.v, k.least)
		}
	}
	return len(f.errs) == n
}

// checkBackupEncryption applies ADR-0204 Decision 2's rules.
func (c Config) checkBackupEncryption(f *findings) {
	e, keyless := c.Backup.Encryption, c.Secrets.EncryptionKeyFile == ""
	switch {
	case e.None && len(e.Recipients) > 0:
		f.refuse("backup.encryption.none", "backup.encryption.none is true: backup.encryption.recipients must be empty")
	case e.None && keyless:
		f.refuse("backup.encryption.none", "backup.encryption.none is true and secrets.encryptionKeyFile is not set: a "+
			"backup would carry Secret values in plaintext; set secrets.encryptionKeyFile or backup.encryption.recipients")
	case e.None:
		f.warn("backup.encryption.none", "backup.encryption.none is true: every record but Secret values leaves in plaintext")
	case len(e.Recipients) == 0:
		f.refuse("backup.encryption.recipients", "backup.encryption.recipients is empty: set at least 2 age recipients, "+
			"or backup.encryption.none")
	case keyless:
		f.warn("backup.encryption.recipients", "backup.encryption.recipients without secrets.encryptionKeyFile: the "+
			"backup envelope alone protects Secret values")
	}
}

// checkBackupTimes applies Q1's rules: a ladder that expires a generation before the next run and an rpo below the
// interval are errors; an interval above half the rpo, and verified copies that expire before their original is rpo
// old, are warnings.
func (c Config) checkBackupTimes(f *findings, t BackupTimes, retentionOK bool) {
	r := c.Backup.Retention
	if retentionOK {
		key, hours := "backup.retention.hourly", int64(r.Hourly)
		for _, k := range []struct {
			key   string
			hours int64
		}{{"backup.retention.daily", int64(r.Daily) * 24}, {"backup.retention.weekly", int64(r.Weekly) * 168}} {
			if k.hours > hours {
				key, hours = k.key, k.hours
			}
		}
		if hours < ceilDiv(t.Interval, time.Hour) {
			f.refuse(key, "%s keeps a generation %dh, below backup.interval %s: each generation expires before the "+
				"next run", key, hours, v1.Duration(t.Interval))
		}
	}
	if t.RPO < t.Interval {
		f.refuse("backup.objectives.rpo", "backup.objectives.rpo %s is below backup.interval %s: no generation can be "+
			"verified within the rpo", v1.Duration(t.RPO), v1.Duration(t.Interval))
		return
	}
	if t.Interval > t.RPO/2 {
		f.warn("backup.interval", "backup.interval %s is above half of backup.objectives.rpo %s: one failed run lets "+
			"the newest generation pass the rpo", v1.Duration(t.Interval), v1.Duration(t.RPO))
	}
	if gap := t.RPO - t.Interval; retentionOK && int64(r.Verified) < ceilDiv(gap, 24*time.Hour) {
		f.warn("backup.retention.verified", "backup.retention.verified %d days is below backup.objectives.rpo − "+
			"backup.interval (%s): a verified copy can expire before its original is rpo old", r.Verified, v1.Duration(gap))
	}
}

// ceilDiv is ⌈d / unit⌉ for d ≥ 0.
func ceilDiv(d, unit time.Duration) int64 {
	return int64((d + unit - 1) / unit)
}

// backupKeysOffDefault names the backup.* keys besides target set off their defaults; an empty duration is its default.
func (c Config) backupKeysOffDefault() []string {
	b, d := c.Backup, defaults().Backup
	var keys []string
	for _, k := range []struct {
		key string
		off bool
	}{
		{"backup.credentialsFile", b.CredentialsFile != d.CredentialsFile},
		{"backup.singleWriter", b.SingleWriter != d.SingleWriter},
		{"backup.retention.hourly", b.Retention.Hourly != d.Retention.Hourly},
		{"backup.retention.daily", b.Retention.Daily != d.Retention.Daily},
		{"backup.retention.weekly", b.Retention.Weekly != d.Retention.Weekly},
		{"backup.retention.verified", b.Retention.Verified != d.Retention.Verified},
		{"backup.retention.preUpgrade", b.Retention.PreUpgrade != d.Retention.PreUpgrade},
		{"backup.encryption.recipients", len(b.Encryption.Recipients) > 0},
		{"backup.encryption.none", b.Encryption.None},
		{"backup.interval", offDuration(b.Interval, defaultBackupInterval)},
		{"backup.objectives.rpo", offDuration(b.Objectives.RPO, defaultBackupRPO)},
		{"backup.retryInterval", offDuration(b.RetryInterval, defaultBackupRetryInterval)},
	} {
		if k.off {
			keys = append(keys, k.key)
		}
	}
	return keys
}

// offDuration reports a duration key set off its default; empty is the default.
func offDuration(v string, def time.Duration) bool {
	d, err := v1.ParseDuration(v)
	return v != "" && (err != nil || time.Duration(d) != def)
}
