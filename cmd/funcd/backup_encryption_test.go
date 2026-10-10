package main

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// scenario: master-key-migrates — at the daemon's start: a gateway-off node's working-directory master is copied
// under storage.dataDir with a warning naming both paths and its catalog tokens stay; two different keys refuse
// naming both paths; a gateway-on node keeps its storage.dataDir key and warns naming the ignored one; a working
// directory equal to storage.dataDir is one key.
func TestScenarioMasterKeyMigrates(t *testing.T) {
	old, other := bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{9}, 32)
	start := func(t *testing.T, cwd, dataDir string, gatewayOn bool) ([]byte, string, string, error) {
		t.Helper()
		t.Chdir(cwd)
		legacy, err := filepath.Abs(filepath.Join("s3gateway", "master.key"))
		require.NoError(t, err)
		var cfg config.Config
		cfg.Storage.DataDir, cfg.S3Gateway.Enabled = dataDir, gatewayOn
		logs := &bytes.Buffer{}
		master, err := loadMaster(cfg, slog.New(slog.NewTextHandler(logs, nil)))
		return master, legacy, logs.String(), err
	}
	put := func(t *testing.T, dir string, key []byte) string {
		t.Helper()
		p := filepath.Join(dir, "s3gateway", "master.key")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, key, 0o600))
		return p
	}

	t.Run("gateway off copies the key", func(t *testing.T) {
		cwd, dataDir := t.TempDir(), shortDataDir(t)
		put(t, cwd, old)
		master, legacy, logs, err := start(t, cwd, dataDir, false)
		require.NoError(t, err)
		target := filepath.Join(dataDir, "s3gateway", "master.key")
		info, err := os.Stat(target)
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
		require.Contains(t, logs, legacy)
		require.Contains(t, logs, target)
		want, err := cataloggw.DeriveCatalogToken(old, "team", "fn")
		require.NoError(t, err)
		got, err := cataloggw.DeriveCatalogToken(master, "team", "fn")
		require.NoError(t, err)
		require.Equal(t, want, got, "the catalog tokens stay")
	})
	t.Run("two different keys refuse", func(t *testing.T) {
		cwd, dataDir := t.TempDir(), shortDataDir(t)
		put(t, cwd, old)
		target := put(t, dataDir, other)
		_, legacy, _, err := start(t, cwd, dataDir, false)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, legacy)
		require.ErrorContains(t, err, target)
		require.ErrorContains(t, err, "s3gateway.masterSecretFile")
	})
	t.Run("gateway on keeps its key", func(t *testing.T) {
		cwd, dataDir := t.TempDir(), shortDataDir(t)
		put(t, cwd, old)
		put(t, dataDir, other)
		master, legacy, logs, err := start(t, cwd, dataDir, true)
		require.NoError(t, err)
		require.Equal(t, other, master)
		require.Contains(t, logs, "level=WARN")
		require.Contains(t, logs, legacy)
	})
	t.Run("working dir equal to data dir is one key", func(t *testing.T) {
		dataDir := shortDataDir(t)
		link := filepath.Join(t.TempDir(), "data")
		require.NoError(t, os.Symlink(dataDir, link))
		put(t, dataDir, old)
		master, _, logs, err := start(t, link, dataDir, false)
		require.NoError(t, err)
		require.Equal(t, old, master)
		require.NotContains(t, logs, "level=WARN")
	})
}

// ADR-0204 Decision 2 at the daemon's start: with backup.target set, a recipients rule violation refuses the start
// naming the key, and a valid configuration logs the keys' fingerprints. ADR-0205 runs the backups.
func TestBackupEncryptionCheckedAtStart(t *testing.T) {
	load := func(t *testing.T, backupYAML string) (config.Config, string) {
		t.Helper()
		dataDir := shortDataDir(t)
		path := filepath.Join(dataDir, "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte(
			"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
				"storage:\n  mode: memory\n  dataDir: \""+dataDir+"\"\n"+
				"backup:\n  target: \"file://"+t.TempDir()+"\"\n"+backupYAML), 0o600))
		cfg, err := config.Load(path, config.Flags{})
		require.NoError(t, err)
		return cfg, dataDir
	}

	cfg, _ := load(t, "")
	_, _, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.DiscardHandler))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "backup.encryption.recipients")

	cfg, _ = load(t, "  encryption:\n    none: true\n")
	_, _, _, _, err = buildOptions(context.Background(), cfg, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "secrets.encryptionKeyFile", "none without a secrets key never starts")

	a, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	b, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recipients := filepath.Join(t.TempDir(), "recipients.txt")
	require.NoError(t, os.WriteFile(recipients, []byte(a.Recipient().String()+"\n"+b.Recipient().String()+"\n"), 0o600))
	cfg, dataDir := load(t, "  encryption:\n    recipients:\n      - \""+recipients+"\"\n")
	logs := &bytes.Buffer{}
	_, closeExec, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	master, err := os.ReadFile(filepath.Join(dataDir, "s3gateway", "master.key")) //nolint:gosec // a test temp path
	require.NoError(t, err)
	require.Contains(t, logs.String(), "backup keys")
	require.Contains(t, logs.String(), envelope.Fingerprint(master))
	require.Contains(t, logs.String(), envelope.Fingerprint([]byte(a.Recipient().String())))
}
