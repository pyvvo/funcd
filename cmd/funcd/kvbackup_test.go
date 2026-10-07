package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// newMemBus opens an in-memory NATS bus for the daemon CDC tests.
func newMemBus(t *testing.T) bus.Bus {
	t.Helper()
	b, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// kvBadgerCfg is a config selecting the durable Badger KV engine under a temp data dir.
func kvBadgerCfg(t *testing.T) config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Storage.Mode = "file"
	cfg.Storage.DataDir = t.TempDir()
	cfg.Kvstore.Engine = "badger"
	cfg.Kvstore.DataDir = filepath.Join(cfg.Storage.DataDir, "kv") // config.Load derives this; set it here since this helper bypasses Load
	return cfg
}

// scenario: backup-enabled-requires-target (daemon) — kvstore.backup.enabled with an empty target ⇒
// the daemon refuses to start with fault.Invalid (never a silent half-configured backup).
func TestScenarioDaemonBackupEnabledRequiresTarget(t *testing.T) {
	cfg := kvBadgerCfg(t)
	cfg.Kvstore.Backup.Enabled = true // no target
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _, err := buildKVStore(context.Background(), cfg, nil, logger)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "enable-without-target is fault.Invalid")
}

// scenario: backup-default-off (daemon) — without backup config the durable KV driver is built and the
// start hook is a no-op (no DR loop), confirming the seam costs nothing unless configured.
func TestScenarioDaemonBackupDefaultOff(t *testing.T) {
	cfg := kvBadgerCfg(t)
	kv, start, err := buildKVStore(context.Background(), cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NotNil(t, kv)
	require.NotNil(t, start)
	start(context.Background()) // no-op; must not panic
	if c, ok := kv.(io.Closer); ok {
		require.NoError(t, c.Close())
	}
}

// scenario: backup-enabled-boots (daemon) — kvstore.backup.enabled with a real (in-memory) target boots
// the durable KV driver with the DR export wired; the start hook launches the loop without error.
func TestScenarioDaemonBackupEnabledBoots(t *testing.T) {
	cfg := kvBadgerCfg(t)
	cfg.Kvstore.Backup.Enabled = true
	cfg.Kvstore.Backup.Target = "file://" + filepath.ToSlash(t.TempDir())
	cfg.Kvstore.Backup.Interval = "100ms"
	kv, start, err := buildKVStore(context.Background(), cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NotNil(t, kv)
	ctx, cancel := context.WithCancel(context.Background())
	start(ctx) // launches RunBackup; cancel stops it
	require.NoError(t, kv.Put(context.Background(), "a/b/c", []byte("v")))
	cancel()
	if c, ok := kv.(io.Closer); ok {
		require.NoError(t, c.Close())
	}
}

// scenario: cdc-enabled-requires-sink (daemon) — kvstore.cdc.enabled with an empty sink ⇒ the daemon
// refuses to start with fault.Invalid.
func TestScenarioDaemonCDCEnabledRequiresSink(t *testing.T) {
	cfg := kvBadgerCfg(t)
	cfg.Kvstore.Cdc.Enabled = true // no sink
	bus := newMemBus(t)
	_, _, err := buildKVStore(context.Background(), cfg, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "enable-without-sink is fault.Invalid")
}

// scenario: cdc-enabled-boots (daemon) — kvstore.cdc.enabled with a sink + a bus boots the durable KV
// driver with the change-feed wired; the start hook launches the tailer without error.
func TestScenarioDaemonCDCEnabledBoots(t *testing.T) {
	cfg := kvBadgerCfg(t)
	cfg.Kvstore.Cdc.Enabled = true
	cfg.Kvstore.Cdc.Sink = "kv.changes"
	bus := newMemBus(t)
	kv, start, err := buildKVStore(context.Background(), cfg, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NotNil(t, kv)
	ctx, cancel := context.WithCancel(context.Background())
	start(ctx) // launches RunCDC; cancel stops it
	require.NoError(t, kv.Put(context.Background(), "a/b/c", []byte("v")))
	cancel()
	if c, ok := kv.(io.Closer); ok {
		require.NoError(t, c.Close())
	}
}

// A malformed or non-positive kvstore.backup/cdc duration fails startup with fault.Invalid naming the key,
// never a silent fall back to the default (issue #190).
func TestIssue190_InvalidKVDurationRejected(t *testing.T) {
	cases := []struct {
		key string
		set func(*config.Config, string)
	}{
		{"kvstore.backup.interval", func(c *config.Config, v string) { c.Kvstore.Backup.Interval = v }},
		{"kvstore.backup.rebaseline", func(c *config.Config, v string) { c.Kvstore.Backup.Rebaseline = v }},
		{"kvstore.backup.rebaselineRetry", func(c *config.Config, v string) { c.Kvstore.Backup.RebaselineRetry = v }},
		{"kvstore.cdc.retention", func(c *config.Config, v string) { c.Kvstore.Cdc.Retention = v }},
	}
	b := newMemBus(t)
	for _, tc := range cases {
		for _, bad := range []string{"5 minutes", "forever", "-3h", "0s"} {
			t.Run(tc.key+"="+bad, func(t *testing.T) {
				cfg := kvBadgerCfg(t)
				cfg.Kvstore.Backup.Enabled = true
				cfg.Kvstore.Backup.Target = "file://" + filepath.ToSlash(t.TempDir())
				cfg.Kvstore.Cdc.Enabled = true
				cfg.Kvstore.Cdc.Sink = "kv.changes"
				tc.set(&cfg, bad)
				kv, _, err := buildKVStore(context.Background(), cfg, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if c, ok := kv.(io.Closer); ok {
					_ = c.Close()
				}
				require.Error(t, err)
				require.Equal(t, fault.Invalid, fault.KindOf(err))
				require.ErrorContains(t, err, tc.key)
			})
		}
	}
}

// --memory overrides kvstore.engine: badger, as it does the metastore, workflow and DLQ stores: the KV
// stays in memory and nothing is written under <dataDir>/kv (ADR-0043: --memory is fully ephemeral).
func TestIssue191_MemoryFlagKeepsKVOffDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  dataDir: \""+dir+"\"\nkvstore:\n  engine: badger\n"), 0o600))
	memoryOnly := true
	cfg, err := config.Load(path, config.Flags{MemoryOnly: &memoryOnly})
	require.NoError(t, err)

	kv, _, err := buildKVStore(context.Background(), cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	if c, ok := kv.(io.Closer); ok {
		t.Cleanup(func() { _ = c.Close() })
	}
	require.NoError(t, kv.Put(context.Background(), "a/b/c", []byte("v")))
	require.NoDirExists(t, cfg.Kvstore.DataDir, "--memory must not open a durable Badger KV")
}

// kvstore.backup/cdc are validated whatever the engine: enabled without a target or sink fails, enabled on
// the in-memory engine fails (it has no seam to serve them), and storage.mode memory ignores them with a
// warning, as it does kvstore.engine badger.
func TestIssue303_KVSeamsNotSilentlyIgnoredOffBadger(t *testing.T) {
	file := func(engine string) config.Config {
		var cfg config.Config
		cfg.Storage.Mode = "file"
		cfg.Storage.DataDir = t.TempDir()
		cfg.Kvstore.Engine = engine
		return cfg
	}
	memoryMode := func() config.Config {
		cfg := kvBadgerCfg(t)
		cfg.Storage.Mode = "memory"
		return cfg
	}
	rejected := []struct {
		name string
		cfg  config.Config
		set  func(*config.Config)
		want string
	}{
		{"default engine, backup without target", file(""), func(c *config.Config) { c.Kvstore.Backup.Enabled = true }, "kvstore.backup.target"},
		{"memory engine, cdc without sink", file("memory"), func(c *config.Config) { c.Kvstore.Cdc.Enabled = true }, "kvstore.cdc.sink"},
		{"memory mode, backup without target", memoryMode(), func(c *config.Config) { c.Kvstore.Backup.Enabled = true }, "kvstore.backup.target"},
		{"memory mode, cdc without sink", memoryMode(), func(c *config.Config) { c.Kvstore.Cdc.Enabled = true }, "kvstore.cdc.sink"},
		{"memory engine, backup with target", file("memory"), func(c *config.Config) {
			c.Kvstore.Backup.Enabled = true
			c.Kvstore.Backup.Target = "file://" + filepath.ToSlash(t.TempDir())
		}, "kvstore.engine"},
		{"default engine, cdc with sink", file(""), func(c *config.Config) {
			c.Kvstore.Cdc.Enabled = true
			c.Kvstore.Cdc.Sink = "kv.changes"
		}, "kvstore.engine"},
	}
	b := newMemBus(t)
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			tc.set(&tc.cfg)
			_, _, err := buildKVStore(context.Background(), tc.cfg, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("memory mode warns that backup and cdc are ignored", func(t *testing.T) {
		cfg := memoryMode()
		cfg.Kvstore.Backup.Enabled = true
		cfg.Kvstore.Backup.Target = "file://" + filepath.ToSlash(t.TempDir())
		cfg.Kvstore.Cdc.Enabled = true
		cfg.Kvstore.Cdc.Sink = "kv.changes"
		var logs bytes.Buffer
		kv, _, err := buildKVStore(context.Background(), cfg, b, slog.New(slog.NewTextHandler(&logs, nil)))
		require.NoError(t, err)
		require.NotNil(t, kv)
		require.NoDirExists(t, cfg.Kvstore.DataDir)
		require.Contains(t, logs.String(), "kvstore.backup")
		require.Contains(t, logs.String(), "kvstore.cdc")
	})
}
