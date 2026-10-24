package s3gateway_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// Issue #1244: the node master secret derives every S3 key pair and catalog token, and a restore installs the one its
// backup escrowed (ADR-0204). LoadOrCreateMaster makes a new master when the file is empty, so a power loss that left
// it empty would rotate them all. The master is written durably: the parent of its new dir is synced, then the file
// is synced, renamed into place and its dir synced.
func TestIssue1244_MasterSecretIsWrittenDurably(t *testing.T) {
	t.Parallel()
	type step struct {
		op   durable.Op
		path string
	}
	var steps []step
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "s3gateway", "master.key")
	secret := []byte("0123456789abcdef0123456789abcdef")
	require.NoError(t, s3gateway.WriteMasterTraced(path, secret, func(op durable.Op, p string) { steps = append(steps, step{op, p}) }))

	require.Len(t, steps, 4, "%v", steps)
	require.Equal(t, step{durable.SyncDir, dataDir}, steps[0], "the new dir's entry is synced")
	require.Equal(t, durable.SyncFile, steps[1].op)
	require.True(t, strings.HasPrefix(steps[1].path, path+".tmp-"), steps[1].path)
	require.Equal(t, step{durable.Rename, path}, steps[2])
	require.Equal(t, step{durable.SyncDir, filepath.Dir(path)}, steps[3])
	got, err := os.ReadFile(path) //nolint:gosec // the test's own temp dir
	require.NoError(t, err)
	require.Equal(t, secret, got)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
