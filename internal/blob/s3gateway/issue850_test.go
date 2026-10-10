package s3gateway_test

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// masterSite is a working directory holding an earlier gateway-off master (#850), a separate data dir, and a
// logger capturing the warnings.
type masterSite struct {
	legacy, dataDir, target string
	logs                    *bytes.Buffer
	logger                  *slog.Logger
}

func newMasterSite(t *testing.T, legacyKey []byte) masterSite {
	t.Helper()
	t.Chdir(t.TempDir())
	legacy, err := filepath.Abs(filepath.Join("s3gateway", "master.key"))
	require.NoError(t, err)
	if legacyKey != nil {
		writeKey(t, legacy, legacyKey)
	}
	dataDir := t.TempDir()
	logs := &bytes.Buffer{}
	return masterSite{
		legacy:  legacy,
		dataDir: dataDir,
		target:  filepath.Join(dataDir, "s3gateway", "master.key"),
		logs:    logs,
		logger:  slog.New(slog.NewTextHandler(logs, nil)),
	}
}

func writeKey(t *testing.T, path string, key []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, key, 0o600))
}

func readKey(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test temp path
	require.NoError(t, err)
	return b
}

// ADR-0204 Decision 7 migration: a gateway-off node's working-directory master is copied 0600 under the data dir with
// a warning naming both paths, the old file is left, and the loaded master is that key.
func TestIssue850_GatewayOffMasterMigrates(t *testing.T) {
	s := newMasterSite(t, []byte("old-key"))
	require.NoError(t, s3gateway.MigrateMaster("", s.dataDir, false, s.logger))

	info, err := os.Stat(s.target)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
	require.Equal(t, []byte("old-key"), readKey(t, s.legacy), "the old file is left to the operator")
	require.Contains(t, s.logs.String(), s.legacy)
	require.Contains(t, s.logs.String(), s.target)

	master, err := s3gateway.LoadOrCreateMaster("", s.dataDir)
	require.NoError(t, err)
	require.Equal(t, []byte("old-key"), master, "the catalog tokens and keypairs stay")
}

// Two different keys: the gateway-off node refuses, naming both paths and s3gateway.masterSecretFile; it never picks.
func TestIssue850_GatewayOffDifferentMastersRefused(t *testing.T) {
	s := newMasterSite(t, []byte("old-key"))
	writeKey(t, s.target, []byte("new-key"))

	err := s3gateway.MigrateMaster("", s.dataDir, false, s.logger)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, s.legacy)
	require.ErrorContains(t, err, s.target)
	require.ErrorContains(t, err, "s3gateway.masterSecretFile")
	require.Equal(t, []byte("new-key"), readKey(t, s.target))
}

// The same key in both places: nothing to copy, the working-directory file is named as ignored.
func TestIssue850_GatewayOffSameMasterKept(t *testing.T) {
	s := newMasterSite(t, []byte("key"))
	writeKey(t, s.target, []byte("key"))

	require.NoError(t, s3gateway.MigrateMaster("", s.dataDir, false, s.logger))
	require.Contains(t, s.logs.String(), s.legacy)
}

// A gateway-on node keeps its data-dir key and warns naming the ignored working-directory file.
func TestIssue850_GatewayOnKeepsMaster(t *testing.T) {
	s := newMasterSite(t, []byte("old-key"))
	writeKey(t, s.target, []byte("new-key"))

	require.NoError(t, s3gateway.MigrateMaster("", s.dataDir, true, s.logger))
	require.Equal(t, []byte("new-key"), readKey(t, s.target))
	require.Contains(t, s.logs.String(), s.legacy)
}

// One file is one key: a working directory equal to the data dir, directly or through a symlink, neither copies,
// refuses nor warns.
func TestIssue850_WorkingDirEqualToDataDirIsOneKey(t *testing.T) {
	s := newMasterSite(t, []byte("key"))
	cwd := filepath.Dir(filepath.Dir(s.legacy))
	link := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.Symlink(cwd, link))

	for _, dataDir := range []string{cwd, link} {
		require.NoError(t, s3gateway.MigrateMaster("", dataDir, false, s.logger))
		master, err := s3gateway.LoadOrCreateMaster("", dataDir)
		require.NoError(t, err)
		require.Equal(t, []byte("key"), master)
	}
	require.Empty(t, s.logs.String())
}

// Default (c): no data dir and no masterSecretFile ⇒ a fresh master in memory, never written, and no migration.
func TestIssue850_NoLocationMasterInMemory(t *testing.T) {
	s := newMasterSite(t, nil)
	require.NoError(t, s3gateway.MigrateMaster("", "", false, s.logger))
	a, err := s3gateway.LoadOrCreateMaster("", "")
	require.NoError(t, err)
	b, err := s3gateway.LoadOrCreateMaster("", "")
	require.NoError(t, err)
	require.Len(t, a, 32)
	require.NotEqual(t, a, b, "each load generates a fresh in-memory master")

	_, err = os.Stat(s.legacy)
	require.ErrorIs(t, err, fs.ErrNotExist, "nothing is written to the working directory")

	writeKey(t, s.legacy, []byte("old-key"))
	require.NoError(t, s3gateway.MigrateMaster("", "", false, s.logger))
	require.Empty(t, s.logs.String())
}

// Default (d): a set masterSecretFile is authoritative and never migrated; a stray working-directory key is only
// named as ignored, and the file itself in the working directory is the key, not a stray.
func TestIssue850_MasterSecretFileNeverMigrates(t *testing.T) {
	s := newMasterSite(t, []byte("old-key"))
	file := filepath.Join(t.TempDir(), "master")
	writeKey(t, file, []byte("operator-key"))

	require.NoError(t, s3gateway.MigrateMaster(file, s.dataDir, false, s.logger))
	_, err := os.Stat(s.target)
	require.ErrorIs(t, err, fs.ErrNotExist, "no copy under the data dir")
	require.Contains(t, s.logs.String(), s.legacy)
	master, err := s3gateway.LoadOrCreateMaster(file, s.dataDir)
	require.NoError(t, err)
	require.Equal(t, []byte("operator-key"), master)

	s.logs.Reset()
	require.NoError(t, s3gateway.MigrateMaster(filepath.Join("s3gateway", "master.key"), s.dataDir, false, s.logger))
	require.Empty(t, s.logs.String())
}
