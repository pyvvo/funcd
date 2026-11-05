//go:build linux

package containerd

import (
	"context"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// sandboxSpec applies sandboxOpts to a bare spec.
func sandboxSpec(t *testing.T, spec runtime.WorkerSpec) *specs.Spec {
	t.Helper()
	s := &specs.Spec{Process: &specs.Process{}, Root: &specs.Root{Path: "rootfs"}, Linux: &specs.Linux{}}
	for _, o := range sandboxOpts(spec, "/state/boot/funcd-default/w") {
		require.NoError(t, o(context.Background(), nil, &containers.Container{}, s))
	}
	return s
}

func tmpMount(t *testing.T, s *specs.Spec) specs.Mount {
	t.Helper()
	i := slices.IndexFunc(s.Mounts, func(m specs.Mount) bool { return m.Destination == "/tmp" })
	require.GreaterOrEqual(t, i, 0, "a /tmp mount")
	return s.Mounts[i]
}

// scenario: worker-root-read-only — every worker's root filesystem is read-only.
func TestScenarioWorkerRootReadOnly(t *testing.T) {
	t.Parallel()
	for _, kind := range []v1alpha1.Kind{v1alpha1.KindFunction, v1alpha1.KindLake, v1alpha1.KindSQLDatabase} {
		s := sandboxSpec(t, runtime.WorkerSpec{Name: "w", OwnerKind: kind})
		require.True(t, s.Root.Readonly, string(kind))
		require.True(t, s.Process.NoNewPrivileges, string(kind))
	}
}

// scenario: no-worker-holds-net-raw — every worker holds containerd's default capabilities but CAP_NET_RAW, whatever its
// kind. sandboxOpts reads no egress setting, so this holds with egress on or off.
func TestScenarioNoWorkerHoldsNetRaw(t *testing.T) {
	t.Parallel()
	const netRaw = "CAP_NET_RAW"
	ctx := namespaces.WithNamespace(context.Background(), "funcd-default")
	base, err := oci.GenerateSpec(ctx, nil, &containers.Container{ID: "w"})
	require.NoError(t, err)
	require.Contains(t, base.Process.Capabilities.Bounding, netRaw, "containerd's default list holds it")
	want := slices.DeleteFunc(slices.Clone(base.Process.Capabilities.Bounding), func(c string) bool { return c == netRaw })

	for _, kind := range []v1alpha1.Kind{v1alpha1.KindFunction, v1alpha1.KindLake, v1alpha1.KindSQLDatabase} {
		s, err := oci.GenerateSpec(ctx, nil, &containers.Container{ID: "w"},
			sandboxOpts(runtime.WorkerSpec{Name: "w", OwnerKind: kind}, "/state/boot/funcd-default/w")...)
		require.NoError(t, err)
		caps := s.Process.Capabilities
		require.ElementsMatch(t, want, caps.Bounding, string(kind))
		require.ElementsMatch(t, want, caps.Effective, string(kind))
		require.ElementsMatch(t, want, caps.Permitted, string(kind))
		require.NotContains(t, caps.Inheritable, netRaw, string(kind))
		require.NotContains(t, caps.Ambient, netRaw, string(kind))
	}
}

// A worker cannot change its netns's settings: as root it passes their file modes, so its /proc/sys is read-only for it.
func TestProcSysStaysReadOnly(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(context.Background(), "funcd-default")
	s, err := oci.GenerateSpec(ctx, nil, &containers.Container{ID: "w"},
		sandboxOpts(runtime.WorkerSpec{Name: "w", OwnerKind: v1alpha1.KindFunction}, "/state/boot/funcd-default/w")...)
	require.NoError(t, err)
	require.Contains(t, s.Linux.ReadonlyPaths, "/proc/sys")
}

// scenario: tmp-counts-in-memory — /tmp is a tmpfs of the worker's memory limit, 512 MiB without one, and HOME is /tmp
// unless the spec sets it.
func TestScenarioTmpCountsInMemory(t *testing.T) {
	t.Parallel()
	s := sandboxSpec(t, runtime.WorkerSpec{Name: "w", Limits: runtime.Limits{MemoryBytes: 256 << 20}})
	m := tmpMount(t, s)
	require.Equal(t, "tmpfs", m.Type)
	require.Subset(t, m.Options, []string{"nosuid", "nodev", "mode=1777", "size=" + strconv.Itoa(256<<20)})
	require.Contains(t, s.Process.Env, "HOME=/tmp")

	pooled := tmpMount(t, sandboxSpec(t, runtime.WorkerSpec{Name: "pool"}))
	require.Contains(t, pooled.Options, "size="+strconv.Itoa(512<<20), "a worker with no memory limit gets ADR-0291's default")

	own := sandboxSpec(t, runtime.WorkerSpec{Name: "w", Env: map[string]string{"HOME": "/home/app"}})
	require.Contains(t, own.Process.Env, "HOME=/home/app")
	require.NotContains(t, own.Process.Env, "HOME=/tmp")
}

// scenario: boot-and-log-dirs-bounded (the log half) — the log channel's directory is mounted read-only, and its
// socket still takes a connection.
func TestLogDirReadOnly(t *testing.T) {
	t.Parallel()
	mount, _, ln, dir, err := setupLogChannel("w-1.r0", runtime.WorkerSpec{Name: "w"}, func(_ runtime.WorkerSpec, r io.ReadCloser) { _ = r.Close() })
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close(); _ = os.RemoveAll(dir) })
	require.True(t, mount.ReadOnly)
	require.Equal(t, "/run/funcd-log", mount.Target)
	conn, err := net.Dial("unix", dir+"/log.sock")
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}
