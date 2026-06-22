package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/config"
)

// kvBadgerCfg is a config selecting the durable Badger KV engine under a temp data dir.
func kvBadgerCfg(t *testing.T) config.Config {
	t.Helper()
	var cfg config.Config
	cfg.Storage.Mode = "file"
	cfg.Storage.DataDir = t.TempDir()
	cfg.Kvstore.Engine = "badger"
	return cfg
}

// scenario: backup-enabled-requires-target (daemon) — kvstore.backup.enabled with an empty target ⇒
// the daemon refuses to start with fault.Invalid (never a silent half-configured backup).
func TestScenarioDaemonBackupEnabledRequiresTarget(t *testing.T) {
	cfg := kvBadgerCfg(t)
	cfg.Kvstore.Backup.Enabled = true // no target
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, _, err := buildKVStore(context.Background(), cfg, logger)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "enable-without-target is fault.Invalid")
}

// scenario: backup-default-off (daemon) — without backup config the durable KV driver is built and the
// start hook is a no-op (no DR loop), confirming the seam costs nothing unless configured.
func TestScenarioDaemonBackupDefaultOff(t *testing.T) {
	cfg := kvBadgerCfg(t)
	kv, start, err := buildKVStore(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	kv, start, err := buildKVStore(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
