package main

import (
	"context"
	"io"
	"log/slog"
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
