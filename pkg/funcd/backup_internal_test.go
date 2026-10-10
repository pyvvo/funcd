package funcd

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	funcdconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
)

// WithPlatformBackup (ADR-0205): New binds the runner to the event store, the metastore and the run state, Run runs it,
// and the control plane mounts its status route, which a developer may not read.
func TestPlatformBackupWired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir, targetDir := eventingDir(t), t.TempDir()
	quiet := slog.New(slog.DiscardHandler)
	tg, err := backup.Open(ctx, backup.Config{Target: gocloud.FileURL(targetDir), DataDir: dir, Retention: backup.Retention{Hourly: 48}, Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tg.Close() })
	sealer, err := envelope.New(envelope.Config{None: true, NoSecrets: true, Logger: quiet})
	require.NoError(t, err)
	r, err := runner.New(runner.Config{Target: tg, Sealer: sealer, Logger: quiet,
		Times: funcdconfig.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: 5 * time.Minute}})
	require.NoError(t, err)
	eng, err := bstore.Open(filepath.Join(dir, "store"), bstore.WithValueLogGCInterval(0))
	require.NoError(t, err)
	p, err := New(InMemory(), WithLogger(quiet), WithoutLogCompaction(), WithStore(store.New(eng)),
		WithRuntime(&recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}),
		WithDeadLetterQueue(filepath.Join(dir, "deadletter"), 3, 0, 0), WithPlatformBackup(r))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	var st runner.Status
	require.Eventually(t, func() bool {
		st, err = r.Status(ctx)
		return err == nil && st.LastSuccessTime != nil
	}, 10*time.Second, 20*time.Millisecond)
	require.True(t, st.Enabled)
	entries, err := tg.List(ctx)
	require.NoError(t, err)
	m, err := backup.ReadManifest(ctx, mustOpen(t, targetDir), entries[0])
	require.NoError(t, err)
	var names []string
	for _, s := range m.Stores {
		names = append(names, s.Name)
	}
	require.Equal(t, []string{"events", "metastore", "runs"}, names)

	srv := httptest.NewServer(p.httpServer.Handler)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/apis/funcd.io/v1alpha1/platformbackup", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+DevToken)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func mustOpen(t *testing.T, dir string) blob.Bucket {
	t.Helper()
	b, err := gocloud.OpenWith(context.Background(), gocloud.FileURL(dir), gocloud.OpenOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}
