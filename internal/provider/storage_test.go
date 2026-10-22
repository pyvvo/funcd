package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// storageSpec is specFor with durable storage owned by the test's own user, so the non-root path applies.
func storageSpec(ip string, port int) provider.ProviderSpec {
	s := specFor(ip, port, nil)
	s.Storage = &provider.StorageSpec{Path: "/data", UID: os.Geteuid(), GID: os.Getegid()}
	return s
}

func newStorageRuntime(t *testing.T, rt *fakeRuntime, root string) provider.Runtime {
	t.Helper()
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt, OwnerKind: v1.KindLake, StorageRoot: root})
	require.NoError(t, err)
	return pr
}

func engineDir(root string) string { return filepath.Join(root, "lake", "default", "lake") }

// scenario: engine-data-survives-recreation — a recreated engine mounts the same directory, and what was written
// there is still there.
func TestScenarioEngineDataSurvivesRecreation(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt, root := newFakeRuntime(host, port), t.TempDir()
	pr := newStorageRuntime(t, rt, root)
	ctx := context.Background()

	_, err := pr.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	dir := engineDir(root)
	require.Equal(t, []containerrt.Mount{{Source: dir, Target: "/data"}}, rt.created[0].Mounts)
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "db"), []byte("rows"), 0o600))

	rt.mu.Lock()
	for id, in := range rt.instances {
		in.State = containerrt.StateFailed
		rt.instances[id] = in
	}
	rt.mu.Unlock()
	_, err = pr.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	require.Len(t, rt.created, 2, "the dead engine is recreated")
	require.Equal(t, rt.created[0].Mounts, rt.created[1].Mounts)
	got, err := os.ReadFile(filepath.Join(dir, "db"))
	require.NoError(t, err)
	require.Equal(t, "rows", string(got))
}

// scenario: engine-data-survives-teardown — Teardown, a later Converge and a new runtime on the same root keep it.
func TestScenarioEngineDataSurvivesTeardown(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt, root := newFakeRuntime(host, port), t.TempDir()
	pr := newStorageRuntime(t, rt, root)
	ctx := context.Background()
	ref := provider.ProviderRef{Namespace: "default", Name: "lake"}

	_, err := pr.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	file := filepath.Join(engineDir(root), "db")
	require.NoError(t, os.WriteFile(file, []byte("rows"), 0o600))
	require.NoError(t, pr.Teardown(ctx, ref))
	require.FileExists(t, file)

	again := newStorageRuntime(t, rt, root)
	_, err = again.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	require.FileExists(t, file)
}

// scenario: reclaim-removes-the-directory — Reclaim removes it once no container exists, refuses while one does.
func TestScenarioReclaimRemovesTheDirectory(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt, root := newFakeRuntime(host, port), t.TempDir()
	pr := newStorageRuntime(t, rt, root)
	vol, ok := pr.(provider.Volumes)
	require.True(t, ok, "the engine runtime implements provider.Volumes")
	ctx := context.Background()
	ref := provider.ProviderRef{Namespace: "default", Name: "lake"}

	_, err := pr.Converge(ctx, storageSpec(host, port))
	require.NoError(t, err)
	refs, err := vol.List(ctx)
	require.NoError(t, err)
	require.Equal(t, []provider.ProviderRef{ref}, refs)

	err = vol.Reclaim(ctx, ref)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a running engine's directory is never removed")
	require.DirExists(t, engineDir(root))

	require.NoError(t, pr.Teardown(ctx, ref))
	require.NoError(t, vol.Reclaim(ctx, ref))
	require.NoDirExists(t, engineDir(root))
	refs, err = vol.List(ctx)
	require.NoError(t, err)
	require.Empty(t, refs)
	require.NoError(t, vol.Reclaim(ctx, ref), "Reclaim is idempotent")
}

// scenario: storage-without-root-refused — storage with no root, more than one replica, a relative path, a symlinked
// leaf or, on a non-root daemon, a foreign uid fail Invalid before any container is created.
func TestScenarioStorageWithoutRootRefused(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	ctx := context.Background()
	refused := func(t *testing.T, pr provider.Runtime, rt *fakeRuntime, spec provider.ProviderSpec) {
		t.Helper()
		_, err := pr.Converge(ctx, spec)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.Empty(t, rt.created, "no container is created")
	}

	t.Run("no root", func(t *testing.T) {
		rt := newFakeRuntime(host, port)
		refused(t, newStorageRuntime(t, rt, ""), rt, storageSpec(host, port))
	})
	t.Run("two replicas", func(t *testing.T) {
		rt := newFakeRuntime(host, port)
		s := storageSpec(host, port)
		s.Replicas = 2
		refused(t, newStorageRuntime(t, rt, t.TempDir()), rt, s)
	})
	t.Run("relative path", func(t *testing.T) {
		rt := newFakeRuntime(host, port)
		s := storageSpec(host, port)
		s.Storage.Path = "data"
		refused(t, newStorageRuntime(t, rt, t.TempDir()), rt, s)
	})
	t.Run("symlinked leaf", func(t *testing.T) {
		rt, root := newFakeRuntime(host, port), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Dir(engineDir(root)), 0o700))
		require.NoError(t, os.Symlink(t.TempDir(), engineDir(root)))
		refused(t, newStorageRuntime(t, rt, root), rt, storageSpec(host, port))
	})
	t.Run("foreign uid on a non-root daemon", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("the daemon runs as root here, so any uid is allowed")
		}
		rt := newFakeRuntime(host, port)
		s := storageSpec(host, port)
		s.Storage.UID = os.Geteuid() + 1
		refused(t, newStorageRuntime(t, rt, t.TempDir()), rt, s)
	})
}

// A spec without storage produces the same WorkerSpec as before ADR-0231: no mount.
func TestConverge_noStorageNoMount(t *testing.T) {
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr := newStorageRuntime(t, rt, t.TempDir())
	_, err := pr.Converge(context.Background(), specFor(host, port, nil))
	require.NoError(t, err)
	require.Nil(t, rt.created[0].Mounts)
}

// When the daemon runs as root, the leaf is chowned to the spec's uid and gid, and only the leaf.
func TestConverge_chownsTheLeafAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the chown applies only when the daemon runs as root")
	}
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt, root := newFakeRuntime(host, port), t.TempDir()
	s := specFor(host, port, nil)
	s.Storage = &provider.StorageSpec{Path: "/data", UID: 65532, GID: 65532}
	_, err := newStorageRuntime(t, rt, root).Converge(context.Background(), s)
	require.NoError(t, err)
	leaf, err := os.Stat(engineDir(root))
	require.NoError(t, err)
	require.Equal(t, uint32(65532), leaf.Sys().(*syscall.Stat_t).Uid)
	require.Equal(t, uint32(65532), leaf.Sys().(*syscall.Stat_t).Gid)
	parent, err := os.Stat(filepath.Dir(engineDir(root)))
	require.NoError(t, err)
	require.Equal(t, uint32(0), parent.Sys().(*syscall.Stat_t).Uid, "the parents stay the daemon's")
}
