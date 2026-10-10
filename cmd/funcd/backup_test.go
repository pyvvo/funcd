package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// backupConfig writes a funcdconfig.yaml in mode with a short data dir, a recipients file of two recipients, a
// secrets key and the YAML given, and returns its path.
func backupConfig(t *testing.T, mode, yaml string) string {
	t.Helper()
	dataDir, keys := shortDataDir(t), t.TempDir()
	var rs bytes.Buffer
	for range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		rs.WriteString(id.Recipient().String() + "\n")
	}
	recipients, key := filepath.Join(keys, "recipients.txt"), filepath.Join(keys, "secrets.key")
	require.NoError(t, os.WriteFile(recipients, rs.Bytes(), 0o600))
	require.NoError(t, os.WriteFile(key, bytes.Repeat([]byte{3}, 32), 0o600))
	path := filepath.Join(dataDir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
		"storage:\n  mode: "+mode+"\n  dataDir: \""+dataDir+"\"\nsecrets:\n  encryptionKeyFile: \""+key+"\"\n"+
		strings.ReplaceAll(yaml, "<recipients>", recipients)), 0o600))
	return path
}

// startBackup runs the daemon's backup start steps on the config at path: the findings, the sealer and the runner.
func startBackup(t *testing.T, path string) (*runner.Runner, string) {
	t.Helper()
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	logBackupFindings(cfg, log)
	sealer, err := backupSealer(cfg, bytes.Repeat([]byte{7}, 32), log)
	require.NoError(t, err)
	r, target, err := backupRunner(context.Background(), cfg, sealer, backupMeter(nil), log)
	require.NoError(t, err)
	if target != nil {
		t.Cleanup(func() { _ = target.Close() })
	}
	return r, logs.String()
}

// platformBackupStatus reads the route the control plane mounts, with r as pkg/funcd passes it.
func platformBackupStatus(t *testing.T, r *runner.Runner) runner.Status {
	t.Helper()
	deps := controlplane.Deps{Store: store.New(memory.New()), Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{"admin": {Subject: "admin", Role: auth.RoleAdmin}})}
	if r != nil {
		deps.Backup = r
	}
	h, err := controlplane.NewServer(deps)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken("admin"))
	require.NoError(t, err)
	st, err := c.PlatformBackup(context.Background())
	require.NoError(t, err)
	return st
}

// scenario: backup-off-by-default — no backup.target, or storage.mode memory with one: no run happens, the status
// reads enabled: false, and the memory case logs one warning.
func TestScenarioBackupOffByDefault(t *testing.T) {
	t.Parallel()
	t.Run("no backup.target", func(t *testing.T) {
		r, logs := startBackup(t, backupConfig(t, "file", ""))
		require.Nil(t, r)
		require.NotContains(t, logs, "level=WARN")
		require.False(t, platformBackupStatus(t, r).Enabled)

		r, logs = startBackup(t, backupConfig(t, "file", "backup:\n  encryption:\n    recipients:\n      - \"<recipients>\"\n"))
		require.Nil(t, r)
		require.Equal(t, 1, strings.Count(logs, "level=WARN"), logs)
		require.Contains(t, logs, "backup.encryption.recipients set without backup.target: ignored")
	})
	t.Run("storage.mode memory with one", func(t *testing.T) {
		dir := t.TempDir()
		r, logs := startBackup(t, backupConfig(t, "memory",
			"backup:\n  target: \""+gocloud.FileURL(dir)+"\"\n  encryption:\n    recipients:\n      - \"<recipients>\"\n"))
		require.Nil(t, r)
		require.Equal(t, 1, strings.Count(logs, "level=WARN"), logs)
		require.Contains(t, logs, "storage.mode is memory: backup.target is ignored")
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries, "nothing touches the target")
		require.False(t, platformBackupStatus(t, r).Enabled)
	})
}

// scenario: impossible-settings-refused — objectives.rpo 30m with interval 1h, or retention 2/0/0 with interval 3h:
// the daemon exits with fault.Invalid naming the key, and config.CheckBackup's first finding is that error.
func TestScenarioImpossibleSettingsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, yaml, key string
	}{
		{"rpo below interval", "  interval: 1h\n  objectives:\n    rpo: 30m\n", "backup.objectives.rpo"},
		{"generations expire before the next run", "  interval: 3h\n  retention:\n    hourly: 2\n    daily: 0\n    weekly: 0\n",
			"backup.retention.hourly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := "backup:\n  target: \"" + gocloud.FileURL(t.TempDir()) + "\"\n  encryption:\n    recipients:\n      - \"<recipients>\"\n"
			err := serve(context.Background(), backupConfig(t, "file", base+tc.yaml), nil, io.Discard)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, tc.key)

			cfg, err := config.Load(backupConfig(t, "file", base), config.Flags{})
			require.NoError(t, err)
			require.NoError(t, yaml.Unmarshal([]byte("backup:\n"+tc.yaml), &cfg))
			fs := cfg.CheckBackup()
			require.NotEmpty(t, fs)
			require.Equal(t, tc.key, fs[0].Key)
			require.True(t, fs[0].Error)
			require.ErrorContains(t, cfg.Validate(), fs[0].Message)
		})
	}
}

// scenario: tight-settings-warn — interval 1h and objectives.rpo 90m: the daemon starts its backup and logs one
// warning naming both keys, the one finding config.CheckBackup returns.
func TestScenarioTightSettingsWarn(t *testing.T) {
	t.Parallel()
	path := backupConfig(t, "file", "backup:\n  target: \""+gocloud.FileURL(t.TempDir())+"\"\n  encryption:\n    recipients:\n"+
		"      - \"<recipients>\"\n  interval: 1h\n  objectives:\n    rpo: 90m\n")
	r, logs := startBackup(t, path)
	require.NotNil(t, r)
	warnings := strings.Count(logs, "level=WARN")
	require.Equal(t, 1, warnings, logs)
	line := logs[strings.Index(logs, "level=WARN"):]
	line = line[:strings.Index(line, "\n")]
	require.Contains(t, line, "backup.interval")
	require.Contains(t, line, "backup.objectives.rpo")

	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	fs := cfg.CheckBackup()
	require.Len(t, fs, 1)
	require.Equal(t, config.Finding{Key: "backup.interval", Message: fs[0].Message}, fs[0])
	require.Contains(t, line, fs[0].Message)
}
