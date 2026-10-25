package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
)

// TestQuarantineMovesTheDirectory (ADR-0288 Decisions 2 and 5): Move renames an engine's directory, with what it
// holds, to <root>/.quarantine/<kind>/<ns>/<name>/<since> once no container of it exists; the engine listing no longer
// shows it; List shows the entry, kept; Expire removes it only once it is older than the cutoff.
func TestQuarantineMovesTheDirectory(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt, root := newFakeRuntime(host, port), t.TempDir()
	pr := newStorageRuntime(t, rt, root)
	vol := pr.(provider.Volumes)
	ctx := context.Background()
	ref := provider.ProviderRef{Namespace: "default", Name: "lake"}
	qref := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindLake, Namespace: "default", Name: "lake"}}
	q := vol.Quarantine()

	_, err := pr.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(engineDir(root), "db.sqlite"), []byte("rows"), 0o600))
	now := time.Date(2026, 10, 25, 3, 4, 5, 678_900_000, time.UTC)
	_, err = q.Move(ctx, qref, now)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a running engine's directory is never moved")
	require.DirExists(t, engineDir(root))

	require.NoError(t, pr.Teardown(ctx, ref))
	e, err := q.Move(ctx, qref, now)
	require.NoError(t, err)
	require.Equal(t, quarantine.Entry{Ref: qref, Since: now.Truncate(time.Millisecond), State: quarantine.Kept}, e)
	entryDir := filepath.Join(root, ".quarantine", "lake", "default", "lake", "20261025T030405.678Z")
	got, err := os.ReadFile(filepath.Join(entryDir, "db.sqlite"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(got), "the directory moves whole")
	require.NoDirExists(t, engineDir(root))
	refs, err := vol.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs, "the engine listing reads only <root>/<kind>/")

	listed, err := q.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{e}, listed)
	_, err = q.Move(ctx, qref, now.Add(time.Second))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "nothing left to move")

	gone, err := q.Expire(ctx, e.Since)
	require.NoError(t, err)
	require.Empty(t, gone, "an entry exactly as old as the cutoff stays")
	require.DirExists(t, entryDir)
	gone, err = q.Expire(ctx, e.Since.Add(time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, []quarantine.Entry{e}, gone)
	require.NoDirExists(t, filepath.Join(root, ".quarantine", "lake", "default"), "the parents it left empty go too")
}

// TestQuarantineSkipsEmptyEntries (ADR-0288 Decision 2): an entry directory that holds nothing is no entry, which the
// sweep removes without reporting; a name that is no stamp is left alone; another kind's ref is refused.
func TestQuarantineSkipsEmptyEntries(t *testing.T) {
	root := t.TempDir()
	q := provider.NewDirQuarantine(filepath.Join(root, ".quarantine", "sqldatabase"), v1.KindSQLDatabase,
		func(ref provider.ProviderRef) string {
			return filepath.Join(root, "sqldatabase", string(ref.Namespace), string(ref.Name))
		},
		func(context.Context, provider.ProviderRef) (bool, error) { return false, nil })
	ctx := context.Background()
	empty := filepath.Join(root, ".quarantine", "sqldatabase", "orders", "todo-db", "20261020T000000.000Z")
	other := filepath.Join(root, ".quarantine", "sqldatabase", "orders", "todo-db", "notes")
	require.NoError(t, os.MkdirAll(empty, 0o700))
	require.NoError(t, os.MkdirAll(other, 0o700))

	listed, err := q.List(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
	gone, err := q.Expire(ctx, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Empty(t, gone, "an empty entry is removed, not reported")
	require.NoDirExists(t, empty)
	require.DirExists(t, other)

	_, err = q.Move(ctx, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindLake, Namespace: "orders", Name: "todo-db"}}, time.Now())
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	none := provider.NewDirQuarantine("", v1.KindSQLDatabase, nil, nil)
	_, err = none.Move(ctx, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindSQLDatabase, Namespace: "orders", Name: "todo-db"}}, time.Now())
	require.Equal(t, fault.NotFound, fault.KindOf(err), "without durable storage there is nothing to move")
}
