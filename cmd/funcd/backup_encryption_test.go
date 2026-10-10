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
		master, err := envelope.LoadMaster(cfg, slog.New(slog.NewTextHandler(logs, nil)))
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
// naming the key (config.CheckBackup in the config's Validate, ADR-0205), and a valid configuration logs the keys'
// fingerprints.
func TestBackupEncryptionCheckedAtStart(t *testing.T) {
	load := func(t *testing.T, backupYAML string) (config.Config, string, error) {
		t.Helper()
		dataDir := shortDataDir(t)
		path := filepath.Join(dataDir, "funcdconfig.yaml")
		require.NoError(t, os.WriteFile(path, []byte(
			"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
				"storage:\n  mode: memory\n  dataDir: \""+dataDir+"\"\n"+
				"backup:\n  target: \"file://"+t.TempDir()+"\"\n"+backupYAML), 0o600))
		cfg, err := config.Load(path, config.Flags{})
		return cfg, dataDir, err
	}
	start := func(t *testing.T, cfg config.Config) (string, error) {
		t.Helper()
		logs := &bytes.Buffer{}
		_, closeExec, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(logs, nil)), nil)
		if err == nil {
			t.Cleanup(func() { _ = closeExec() })
		}
		return logs.String(), err
	}
	recipientsFile := func(t *testing.T, ids ...*age.X25519Identity) string {
		t.Helper()
		var b bytes.Buffer
		for _, id := range ids {
			b.WriteString(id.Recipient().String() + "\n")
		}
		p := filepath.Join(t.TempDir(), "recipients.txt")
		require.NoError(t, os.WriteFile(p, b.Bytes(), 0o600))
		return p
	}
	a, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	b, err := age.GenerateX25519Identity()
	require.NoError(t, err)

	t.Run("no recipients refuses", func(t *testing.T) {
		_, _, err := load(t, "")
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, "backup.encryption.recipients")
	})
	// scenario: plaintext-secrets-refused — none without a secrets key refuses naming both keys; with one it starts
	// and warns.
	t.Run("none without a secrets key refuses", func(t *testing.T) {
		_, _, err := load(t, "  encryption:\n    none: true\n")
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, "backup.encryption.none")
		require.ErrorContains(t, err, "secrets.encryptionKeyFile")
	})
	t.Run("none with a secrets key starts", func(t *testing.T) {
		key := filepath.Join(t.TempDir(), "secrets.key")
		require.NoError(t, os.WriteFile(key, bytes.Repeat([]byte{3}, 32), 0o600))
		cfg, _, err := load(t, "  encryption:\n    none: true\nsecrets:\n  encryptionKeyFile: \""+key+"\"\n")
		require.NoError(t, err)
		logs, err := start(t, cfg)
		require.NoError(t, err)
		require.Contains(t, logs, "level=WARN")
		require.Contains(t, logs, "backup encryption is off")
	})
	t.Run("recipients from the env start", func(t *testing.T) {
		t.Setenv("FUNCD_BACKUP_ENCRYPTION_RECIPIENTS", recipientsFile(t, a)+","+recipientsFile(t, b))
		cfg, _, err := load(t, "")
		require.NoError(t, err)
		require.Len(t, cfg.Backup.Encryption.Recipients, 2)
		logs, err := start(t, cfg)
		require.NoError(t, err)
		require.Contains(t, logs, envelope.Fingerprint([]byte(a.Recipient().String())))
		require.Contains(t, logs, envelope.Fingerprint([]byte(b.Recipient().String())))
	})
	t.Run("valid recipients log the fingerprints", func(t *testing.T) {
		cfg, dataDir, err := load(t, "  encryption:\n    recipients:\n      - \""+recipientsFile(t, a, b)+"\"\n")
		require.NoError(t, err)
		logs, err := start(t, cfg)
		require.NoError(t, err)
		master, err := os.ReadFile(filepath.Join(dataDir, "s3gateway", "master.key")) //nolint:gosec // a test temp path
		require.NoError(t, err)
		require.Contains(t, logs, "backup keys")
		require.Contains(t, logs, envelope.Fingerprint(master))
		require.Contains(t, logs, envelope.Fingerprint([]byte(a.Recipient().String())))
	})
}
