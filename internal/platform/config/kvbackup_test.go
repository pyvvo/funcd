package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// kvOn is the default config with the KV export on and nothing else of it set.
func kvOn(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	c.Kvstore.Backup.Enabled = true
	return c
}

// ADR-0209 Decision 1, a row per rule: each group comes whole from kvstore.backup when its lead key is set there,
// else from backup:; the refusals name the key.
func TestResolveKVBackup(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		set  func(c *config.Config)
		want config.KVBackup
		key  string
	}{
		"both groups inherited": {set: func(c *config.Config) {
			c.Backup.Target, c.Backup.CredentialsFile, c.Backup.SingleWriter = "s3://t", "/etc/c", true
			c.Backup.Encryption.Recipients = []string{"/r"}
		}, want: config.KVBackup{Target: "s3://t", CredentialsFile: "/etc/c", SingleWriter: true, Shared: true, Recipients: []string{"/r"}}},
		"own target, inherited encryption": {set: func(c *config.Config) {
			c.Backup.Target, c.Backup.CredentialsFile = "s3://t", "/etc/c"
			c.Kvstore.Backup.Target = "s3://k"
			c.Backup.Encryption.None = true
		}, want: config.KVBackup{Target: "s3://k", None: true}},
		"own encryption wins whole": {set: func(c *config.Config) {
			c.Backup.Target = "s3://t"
			c.Backup.Encryption.Recipients = []string{"/r"}
			c.Kvstore.Backup.Encryption.None = true
		}, want: config.KVBackup{Target: "s3://t", Shared: true, None: true}},
		"own recipients": {set: func(c *config.Config) {
			c.Kvstore.Backup.Target, c.Kvstore.Backup.SingleWriter = "file:///srv/kv", true
			c.Kvstore.Backup.Encryption.Recipients = []string{"/k"}
		}, want: config.KVBackup{Target: "file:///srv/kv", SingleWriter: true, Recipients: []string{"/k"}}},
		"no target":               {set: func(c *config.Config) { c.Backup.Encryption.None = true }, key: "kvstore.backup.target"},
		"credentials alone":       {set: func(c *config.Config) { c.Backup.Target, c.Kvstore.Backup.CredentialsFile = "s3://t", "/c" }, key: "kvstore.backup.credentialsFile"},
		"single writer alone":     {set: func(c *config.Config) { c.Backup.Target, c.Kvstore.Backup.SingleWriter = "s3://t", true }, key: "kvstore.backup.singleWriter"},
		"target equals platforms": {set: func(c *config.Config) { c.Backup.Target, c.Kvstore.Backup.Target = "s3://t", "s3://t" }, key: "kvstore.backup.target"},
		"no encryption":           {set: func(c *config.Config) { c.Kvstore.Backup.Target = "s3://k" }, key: "kvstore.backup.encryption.recipients"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := kvOn(t)
			tc.set(&c)
			got, err := c.ResolveKVBackup()
			if tc.key != "" {
				require.Equal(t, fault.Invalid, fault.KindOf(err))
				require.ErrorContains(t, err, tc.key)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// The kvstore.backup rows of CheckBackup (ADR-0209): chunkBytes above 8 MiB, the retention ranges and a rebaseline
// that reaches ⌈hourly/24⌉ days refuse naming the key; nothing is checked in memory mode or with the export off; the
// backup.encryption.* keys the KV takes are not reported as ignored.
func TestKVBackupRows(t *testing.T) {
	t.Parallel()
	valid := func() config.Config {
		c := kvOn(t)
		c.Kvstore.Backup.Target = "s3://k"
		c.Kvstore.Backup.Encryption.None = true
		return c
	}
	first := func(c config.Config) config.Finding {
		fs := c.CheckBackup()
		require.NotEmpty(t, fs)
		return fs[0]
	}
	require.Empty(t, valid().CheckBackup())
	for key, set := range map[string]func(c *config.Config){
		"kvstore.backup.chunkBytes":       func(c *config.Config) { c.Kvstore.Backup.ChunkBytes = 8<<20 + 1 },
		"kvstore.backup.retention.hourly": func(c *config.Config) { c.Kvstore.Backup.Retention.Hourly = 0 },
		"kvstore.backup.retention.daily":  func(c *config.Config) { c.Kvstore.Backup.Retention.Daily = -1 },
		"kvstore.backup.retention.weekly": func(c *config.Config) { c.Kvstore.Backup.Retention.Weekly = -1 },
		"kvstore.backup.rebaseline":       func(c *config.Config) { c.Kvstore.Backup.Rebaseline = "48h" },
		"kvstore.backup.interval":         func(c *config.Config) { c.Kvstore.Backup.Interval = "0s" },
		"kvstore.backup.target":           func(c *config.Config) { c.Kvstore.Backup.Target = "" },
	} {
		c := valid()
		set(&c)
		f := first(c)
		require.True(t, f.Error, key)
		require.Equal(t, key, f.Key)
	}
	c := valid()
	c.Kvstore.Backup.Rebaseline = "48h"
	require.Contains(t, first(c).Message, "kvstore.backup.retention.hourly", "the rebaseline row names both keys")
	c.Kvstore.Backup.Rebaseline = "47h"
	require.Empty(t, c.CheckBackup())
	c.Kvstore.Backup.ChunkBytes = 8 << 20
	require.Empty(t, c.CheckBackup(), "8 MiB is the maximum")

	mem := valid()
	mem.Storage.Mode, mem.Kvstore.Backup.ChunkBytes = "memory", 64<<20
	require.Empty(t, mem.CheckBackup())

	inherited := kvOn(t)
	inherited.Kvstore.Backup.Target = "s3://k"
	inherited.Backup.Encryption.None = true
	require.Empty(t, inherited.CheckBackup(), "backup.encryption.none taken by the KV is not ignored")
	inherited.Kvstore.Backup.Enabled = false
	require.Equal(t, "backup.encryption.none", first(inherited).Key, "without the KV export it is")
}
