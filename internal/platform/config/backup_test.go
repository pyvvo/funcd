package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// The run times default to 1h, 2h and 5m, from the file or the env; an empty key is its default; a malformed or
// out-of-bounds one is fault.Invalid naming it.
func TestBackupTimesDefaults(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "1h", c.Backup.Interval)
	got, err := c.BackupTimes()
	require.NoError(t, err)
	require.Equal(t, config.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: 5 * time.Minute}, got)

	var zero config.Config
	got, err = zero.BackupTimes()
	require.NoError(t, err)
	require.Equal(t, config.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: 5 * time.Minute}, got)

	t.Setenv("FUNCD_BACKUP_INTERVAL", "30m")
	t.Setenv("FUNCD_BACKUP_RETRY_INTERVAL", "1m")
	c, err = config.Load(writeCfg(t, "backup:\n  objectives:\n    rpo: 3h\n"), config.Flags{})
	require.NoError(t, err)
	got, err = c.BackupTimes()
	require.NoError(t, err)
	require.Equal(t, config.BackupTimes{Interval: 30 * time.Minute, RPO: 3 * time.Hour, RetryInterval: time.Minute}, got)

	for _, bad := range []string{"1d", "0s", "1.5h"} {
		c.Backup.RetryInterval = bad
		_, err = c.BackupTimes()
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
		require.ErrorContains(t, err, "backup.retryInterval")
	}
}

// CheckBackup holds Decision 3's config rows: errors first, each naming its key; the run times checked always, the
// rest once backup.target is set.
func TestCheckBackupRules(t *testing.T) {
	on := func() config.Config {
		c, err := config.Load(writeCfg(t, "backup:\n  target: file:///srv/backup\n  encryption:\n    recipients:\n"+
			"      - /etc/funcd/r.txt\nsecrets:\n  encryptionKeyFile: /etc/funcd/secrets.key\n"), config.Flags{})
		require.NoError(t, err)
		return c
	}
	type finding struct {
		key   string
		error bool
	}
	keys := func(fs []config.Finding) []finding {
		var out []finding
		for _, f := range fs {
			require.Contains(t, f.Message, f.Key)
			out = append(out, finding{f.Key, f.Error})
		}
		return out
	}
	for _, tc := range []struct {
		name string
		mod  func(*config.Config)
		want []finding
	}{
		{"defaults with a target", func(*config.Config) {}, nil},
		{"recipients without a target", func(c *config.Config) { c.Backup.Target = "" }, []finding{{"backup.encryption.recipients", false}}},
		{"keys without a target", func(c *config.Config) {
			c.Backup.Target, c.Backup.Encryption.Recipients = "", nil
			c.Backup.Interval = "30m"
		},
			[]finding{{"backup.interval", false}}},
		{"a malformed time without a target", func(c *config.Config) {
			c.Backup.Target, c.Backup.Encryption.Recipients = "", nil
			c.Backup.Objectives.RPO = "2d"
		},
			[]finding{{"backup.objectives.rpo", true}, {"backup.objectives.rpo", false}}},
		{"rpo below interval", func(c *config.Config) { c.Backup.Objectives.RPO = "30m" }, []finding{{"backup.objectives.rpo", true}}},
		{"a ladder shorter than the interval", func(c *config.Config) {
			c.Backup.Retention.Hourly, c.Backup.Retention.Daily, c.Backup.Retention.Weekly = 2, 0, 0
			c.Backup.Interval = "3h"
		}, []finding{{"backup.retention.hourly", true}, {"backup.objectives.rpo", true}}},
		{"the weekly class is the longest", func(c *config.Config) {
			c.Backup.Retention.Daily, c.Backup.Retention.Weekly = 0, 1
			c.Backup.Interval, c.Backup.Objectives.RPO = "200h", "400h"
		}, []finding{{"backup.retention.weekly", true}, {"backup.retention.verified", false}}},
		{"another scheme", func(c *config.Config) { c.Backup.Target = "gs://bucket" }, []finding{{"backup.target", true}}},
		{"a credentials file beside a directory", func(c *config.Config) { c.Backup.CredentialsFile = "/etc/funcd/aws" },
			[]finding{{"backup.credentialsFile", true}}},
		{"retention out of range", func(c *config.Config) {
			c.Backup.Retention.Hourly, c.Backup.Retention.Daily, c.Backup.Retention.Weekly, c.Backup.Retention.Verified = 0, -1, -1, 0
		}, []finding{{"backup.retention.hourly", true}, {"backup.retention.daily", true}, {"backup.retention.weekly", true}, {"backup.retention.verified", true}}},
		{"no recipients", func(c *config.Config) { c.Backup.Encryption.Recipients = nil }, []finding{{"backup.encryption.recipients", true}}},
		{"none beside recipients", func(c *config.Config) { c.Backup.Encryption.None = true }, []finding{{"backup.encryption.none", true}}},
		{"none without a secrets key", func(c *config.Config) {
			c.Backup.Encryption.None, c.Backup.Encryption.Recipients, c.Secrets.EncryptionKeyFile = true, nil, ""
		}, []finding{{"backup.encryption.none", true}}},
		{"none with a secrets key", func(c *config.Config) { c.Backup.Encryption.None, c.Backup.Encryption.Recipients = true, nil },
			[]finding{{"backup.encryption.none", false}}},
		{"recipients without a secrets key", func(c *config.Config) { c.Secrets.EncryptionKeyFile = "" },
			[]finding{{"backup.encryption.recipients", false}}},
		{"an interval above half the rpo", func(c *config.Config) { c.Backup.Objectives.RPO = "90m" }, []finding{{"backup.interval", false}}},
		{"verified copies shorter than rpo − interval", func(c *config.Config) { c.Backup.Objectives.RPO = "60h" },
			[]finding{{"backup.retention.verified", false}}},
		{"memory mode", func(c *config.Config) { c.Storage.Mode = "memory" }, []finding{{"backup.target", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := on()
			tc.mod(&c)
			require.Equal(t, tc.want, keys(c.CheckBackup()))
			err := c.Validate()
			if len(tc.want) > 0 && tc.want[0].error {
				require.Equal(t, fault.Invalid, fault.KindOf(err))
				require.ErrorContains(t, err, tc.want[0].key)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
