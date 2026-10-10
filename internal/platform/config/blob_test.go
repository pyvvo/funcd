package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// ADR-0208 Decision 1: CheckBlob's rows, errors first, each naming its key; storage.mode memory warns once and
// refuses nothing.
func TestCheckBlobRows(t *testing.T) {
	t.Parallel()
	base, err := config.Load(writeCfg(t, "storage:\n  dataDir: /srv/funcd\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/srv/funcd/blob", base.Blob.Dir, "Load derives blob.dir in file mode")
	require.Empty(t, base.CheckBlob())
	type finding struct {
		key   string
		error bool
	}
	for name, c := range map[string]struct {
		mod  func(c *config.Config)
		want []finding
	}{
		"remote store": {func(c *config.Config) { c.Blob.Dir, c.Blob.Target = "", "s3://b?region=r" }, nil},
		"remote with credentials": {func(c *config.Config) {
			c.Blob.Dir, c.Blob.Target, c.Blob.CredentialsFile = "", "s3://b", "/etc/funcd/blob-credentials"
		}, nil},
		"another scheme":         {func(c *config.Config) { c.Blob.Dir, c.Blob.Target = "", "file:///srv/blob" }, []finding{{"blob.target", true}}},
		"target with dir":        {func(c *config.Config) { c.Blob.Target = "s3://b" }, []finding{{"blob.dir", true}}},
		"credentials alone":      {func(c *config.Config) { c.Blob.CredentialsFile = "/c" }, []finding{{"blob.credentialsFile", true}}},
		"dir is the data dir":    {func(c *config.Config) { c.Blob.Dir = "/srv/funcd" }, []finding{{"blob.dir", true}}},
		"dir above the data dir": {func(c *config.Config) { c.Blob.Dir = "/srv" }, []finding{{"blob.dir", true}}},
		"dir elsewhere":          {func(c *config.Config) { c.Blob.Dir = "/srv/funcd-blob" }, nil},
		"bad duration":           {func(c *config.Config) { c.Blob.VersionRetention = "1d" }, []finding{{"blob.versionRetention", true}}},
		"zero interval":          {func(c *config.Config) { c.Blob.Backup.Interval = "0s" }, []finding{{"blob.backup.interval", true}}},
		"retention below interval": {func(c *config.Config) { c.Blob.Backup.Retention = "30m" },
			[]finding{{"blob.backup.retention", true}}},
		"rebaseline below interval": {func(c *config.Config) { c.Blob.Backup.Interval = "800h" },
			[]finding{{"blob.backup.rebaseline", true}, {"blob.backup.retention", true}}},
		"memory": {func(c *config.Config) {
			c.Storage.Mode, c.Blob.Target, c.Blob.Backup.Interval = "memory", "file:///x", "0s"
		}, []finding{{"blob.target", false}}},
	} {
		cfg := base
		c.mod(&cfg)
		var got []finding
		for _, f := range cfg.CheckBlob() {
			got = append(got, finding{f.Key, f.Error})
		}
		require.Equal(t, c.want, got, name)
	}
}

// scenario: blob-keys-checked (config) — Load refuses a blob.target of another scheme, target with dir, a
// credentials file alone and a dir at or above storage.dataDir, naming the key; storage.mode memory ignores every
// blob key and derives no dir. The daemon's one warning is cmd/funcd's TestScenarioBlobKeysChecked.
func TestScenarioBlobKeysCheckedLoad(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	head := "storage:\n  dataDir: " + dataDir + "\n"
	for key, yaml := range map[string]string{
		"blob.target":          "blob:\n  target: file:///srv/blob\n",
		"blob.dir":             "blob:\n  target: s3://b\n  dir: /srv/blob\n",
		"blob.credentialsFile": "blob:\n  credentialsFile: /etc/c\n",
		"blob.backup.interval": "blob:\n  backup:\n    interval: 1d\n",
	} {
		_, err := config.Load(writeCfg(t, head+yaml), config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), key)
		require.ErrorContains(t, err, key)
	}
	_, err := config.Load(writeCfg(t, head+"blob:\n  dir: "+filepath.Dir(dataDir)+"\n"), config.Flags{})
	require.ErrorContains(t, err, "blob.dir")

	memory := true
	c, err := config.Load(writeCfg(t, head+"blob:\n  target: file:///srv/blob\n"), config.Flags{MemoryOnly: &memory})
	require.NoError(t, err)
	require.Empty(t, c.Blob.Dir)
	require.Len(t, c.CheckBlob(), 1)

	c, err = config.Load(writeCfg(t, head+"blob:\n  dir: rel\n  backup:\n    interval: 30m\n"), config.Flags{})
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(c.Blob.Dir), "a relative blob.dir is made absolute")
	times, err := c.BlobTimes()
	require.NoError(t, err)
	require.Equal(t, config.BlobTimes{Interval: 30 * time.Minute, Rebaseline: 720 * time.Hour, Retention: 720 * time.Hour}, times)
}
