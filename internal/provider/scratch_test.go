package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// floorFunc is a provider.Floor whose answer a test sets.
type floorFunc func(bytes int64) error

func (f floorFunc) Check(_ context.Context, bytes int64) error { return f(bytes) }

func scratchSpec(ip string, port int) provider.ProviderSpec {
	s := specFor(ip, port, nil)
	s.Scratch = &provider.ScratchSpec{Path: "/var/funcd/ducklake", SizeBytes: 1 << 30}
	return s
}

func newScratchRuntime(t *testing.T, rt *fakeRuntime, floor provider.Floor) (provider.Runtime, *provider.ScratchCalls, string, string) {
	t.Helper()
	root, mountRoot := t.TempDir(), t.TempDir()
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt, OwnerKind: v1.KindLake, ScratchRoot: root,
		ScratchMountRoot: mountRoot, Floor: floor})
	require.NoError(t, err)
	calls := &provider.ScratchCalls{}
	provider.UseScratch(pr, calls)
	return pr, calls, root, mountRoot
}

// scenario: lake-scratch-refused-below-floor — the floor's refusal fails the start before anything is allocated.
func TestScenarioLakeScratchRefusedBelowFloor(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime("", 0)
	var asked int64
	refuse := floorFunc(func(bytes int64) error {
		asked = bytes
		return fault.InsufficientStoragef("quota.Floor", "the data filesystem is below storage.minFreeSize")
	})
	pr, calls, _, _ := newScratchRuntime(t, rt, refuse)
	_, err := pr.Converge(context.Background(), scratchSpec("", 8080))
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "lacks the space for the scratch of default/lake")
	require.Equal(t, int64(1<<30), asked, "the floor is asked for the scratch's size")
	require.Empty(t, calls.Made, "nothing is allocated")
	require.Empty(t, rt.created, "no engine is created")
}

// scenario: lake-scratch-recreated — each engine start gets a new scratch, bound at its path, and a teardown removes
// it.
func TestScenarioLakeScratchRecreated(t *testing.T) {
	t.Parallel()
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	pr, calls, root, mountRoot := newScratchRuntime(t, rt, floorFunc(func(int64) error { return nil }))
	ctx := context.Background()

	_, err := pr.Converge(ctx, scratchSpec(host, port))
	require.NoError(t, err)
	image := filepath.Join(root, "default", "lake.img")
	require.Equal(t, []string{image}, calls.Made)
	require.Equal(t, []containerrt.Mount{{Source: filepath.Join(mountRoot, "default", "lake"), Target: "/var/funcd/ducklake"}},
		rt.created[0].Mounts)

	rt.mu.Lock()
	for id, in := range rt.instances {
		in.State = containerrt.StateFailed
		rt.instances[id] = in
	}
	rt.mu.Unlock()
	_, err = pr.Converge(ctx, scratchSpec(host, port))
	require.NoError(t, err)
	require.Len(t, rt.created, 2, "the dead engine is recreated")
	require.Equal(t, []string{image, image}, calls.Made, "with a new scratch")

	require.True(t, calls.Holds(image))
	require.NoError(t, pr.Teardown(ctx, provider.ProviderRef{Namespace: "default", Name: "lake"}))
	require.False(t, calls.Holds(image), "a teardown removes the scratch")
}

// scenario: lake-scratch-freed-on-restart — a restart that the floor refuses still removes the scratch of the engine
// it replaces, and the next pass makes a new one once the floor admits it.
func TestScenarioRefusedRestartFreesScratch(t *testing.T) {
	t.Parallel()
	host, port, closeFn := engineServer(t, 200)
	defer closeFn()
	rt := newFakeRuntime(host, port)
	var refusing atomic.Bool
	pr, calls, root, _ := newScratchRuntime(t, rt, floorFunc(func(int64) error {
		if refusing.Load() {
			return fault.InsufficientStoragef("quota.Floor", "the data filesystem is below storage.minFreeSize")
		}
		return nil
	}))
	ctx := context.Background()
	_, err := pr.Converge(ctx, scratchSpec(host, port))
	require.NoError(t, err)
	image := filepath.Join(root, "default", "lake.img")
	require.True(t, calls.Holds(image))

	rt.mu.Lock()
	for id, in := range rt.instances {
		in.State = containerrt.StateFailed
		rt.instances[id] = in
	}
	rt.mu.Unlock()
	refusing.Store(true)
	_, err = pr.Converge(ctx, scratchSpec(host, port))
	require.Equal(t, fault.InsufficientStorage, fault.KindOf(err), "%v", err)
	require.False(t, calls.Holds(image), "the ended engine's scratch is removed though the floor refuses a new one")
	require.Len(t, rt.created, 1, "no engine is created")

	refusing.Store(false)
	_, err = pr.Converge(ctx, scratchSpec(host, port))
	require.NoError(t, err)
	require.True(t, calls.Holds(image), "the next pass makes a new scratch")
	require.Len(t, rt.created, 2)
}

func TestScratchNeedsOneReplica(t *testing.T) {
	t.Parallel()
	pr, _, _, _ := newScratchRuntime(t, newFakeRuntime("", 0), nil)
	spec := scratchSpec("", 8080)
	spec.Replicas = 2
	_, err := pr.Converge(context.Background(), spec)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
}

func TestScratchIgnoredWithoutRoot(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime("", 0)
	pr, err := provider.NewRuntime(provider.Deps{Runtime: rt, OwnerKind: v1.KindLake})
	require.NoError(t, err)
	_, err = pr.Converge(context.Background(), scratchSpec("", 8080))
	require.NoError(t, err)
	require.Empty(t, rt.created[0].Mounts, "a runtime with no scratch root, the process runtime's, binds none")
}

func TestRemoveLeftoverScratch(t *testing.T) {
	t.Parallel()
	root, mountRoot := t.TempDir(), t.TempDir()
	for _, p := range []string{filepath.Join(root, "ns1", "a.img"), filepath.Join(root, "ns1", "b.img")} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("image"), 0o600))
	}
	for _, d := range []string{filepath.Join(mountRoot, "ns1", "a"), filepath.Join(mountRoot, "ns2", "c")} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, provider.RemoveLeftoverScratch(root, mountRoot))
	for _, p := range []string{"ns1/a.img", "ns1/b.img"} {
		require.NoFileExists(t, filepath.Join(root, p))
	}
	for _, d := range []string{"ns1/a", "ns2/c"} {
		require.NoDirExists(t, filepath.Join(mountRoot, d))
	}
	require.NoError(t, provider.RemoveLeftoverScratch("", ""), "no scratch root: nothing to do")
}
