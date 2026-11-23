//go:build dev

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: engine-logs-dev-lake-sql — under dev, todo-db's engine lines are read with funcdctl logs
// sqldatabase/todo-db, each kept as stderr, and dev's terminal shows none of them (ADR-0343). Dev's Lake engine
// writes on stderr only on an error, so its writer's test holds the Lake half.
func TestScenarioEngineLogsDevLakeSQL(t *testing.T) {
	requireSQLEngine(t)
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: out}).startDev(ctx, sqlProject(t), devConfig{cacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.Equal(t, 1, addNote(t, inst))

	var lines []logread.Line
	require.Eventually(t, func() bool {
		lines, err = inst.client.EngineLogs(ctx, devNamespace, v1.KindSQLDatabase, "todo-db", sdk.LogsOptions{})
		return err == nil && len(lines) > 0
	}, time.Minute, 500*time.Millisecond, "funcdctl logs sqldatabase/todo-db reads the engine's lines from dev's store")
	terminal := out.String()
	for _, l := range lines {
		require.Equal(t, "stderr", l.Source)
		require.NotContains(t, terminal, l.Body, "no engine line reaches dev's terminal")
	}
}
