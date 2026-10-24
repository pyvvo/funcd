package containerd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/typeurl/v2"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
)

// scenario: unsafe-report-reads-empty (ADR-0276) — an engine's report is untrusted: a symlink, a FIFO and a directory
// read as empty without blocking, a regular file is read up to MaxReportBytes, and a Function worker's is never read.
func TestScenarioUnsafeReportReadsEmpty(t *testing.T) {
	t.Parallel()
	at := func(t *testing.T) (string, string) {
		dir := t.TempDir()
		return dir, filepath.Join(dir, reportFile)
	}
	t.Run("a regular report is read", func(t *testing.T) {
		dir, path := at(t)
		require.NoError(t, os.WriteFile(path, []byte("saved 2026-10-24T05:00:00+00:00"), 0o600))
		require.Equal(t, "saved 2026-10-24T05:00:00+00:00", readReport(dir))
	})
	t.Run("a large report is bounded", func(t *testing.T) {
		dir, path := at(t)
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 2*runtime.MaxReportBytes)), 0o600))
		require.Len(t, readReport(dir), runtime.MaxReportBytes)
	})
	t.Run("a symlink is not followed", func(t *testing.T) {
		dir, path := at(t)
		secret := filepath.Join(t.TempDir(), "host-file")
		require.NoError(t, os.WriteFile(secret, []byte("host secret"), 0o600))
		require.NoError(t, os.Symlink(secret, path))
		require.Empty(t, readReport(dir))
	})
	t.Run("a FIFO does not block", func(t *testing.T) {
		dir, path := at(t)
		require.NoError(t, syscall.Mkfifo(path, 0o600))
		got := make(chan string, 1)
		go func() { got <- readReport(dir) }()
		select {
		case r := <-got:
			require.Empty(t, r)
		case <-time.After(5 * time.Second):
			t.Fatal("reading a FIFO report blocked")
		}
	})
	t.Run("a directory reads empty", func(t *testing.T) {
		dir, path := at(t)
		require.NoError(t, os.Mkdir(path, 0o700))
		require.Empty(t, readReport(dir))
	})
	t.Run("no report reads empty", func(t *testing.T) {
		dir, _ := at(t)
		require.Empty(t, readReport(dir))
	})
	t.Run("a Function worker's report is never read", func(t *testing.T) {
		dir, path := at(t)
		require.NoError(t, os.WriteFile(path, []byte("saved 2026-10-24T05:00:00+00:00"), 0o600))
		require.Empty(t, report(&worker{ownerKind: v1alpha1.KindFunction, bootDir: dir}))
		require.Equal(t, "saved 2026-10-24T05:00:00+00:00", report(&worker{ownerKind: v1alpha1.KindLake, bootDir: dir}))
	})
}

// ADR-0276: only an engine worker's environment names a report file, in its boot dir.
func TestReportFileOnlyForEngines(t *testing.T) {
	ctx := leases.WithLease(context.Background(), "report")
	cs, layer, manifest := fakeImage(t)
	const image = "funcd/report:latest"
	ctrs := &memContainers{records: map[string]containers.Container{}}
	snap := &memSnapshotter{rootfs: t.TempDir(), keys: map[string]bool{layer.String(): true}}
	client := fakeClient(t, cs, images.Image{Name: image, Target: manifest}, ctrs, map[string]snapshots.Snapshotter{"overlayfs": snap})
	d := &driver{cfg: Config{Logger: slog.Default()}, client: client, cni: attachedCNI{}, bootRoot: t.TempDir(), fifoDir: t.TempDir(), instances: map[runtime.InstanceID]*worker{}}
	env := func(spec runtime.WorkerSpec) []string {
		t.Helper()
		_, err := d.Create(ctx, spec)
		require.NoError(t, err)
		ctrID, _ := workerNames(string(spec.Namespace), string(spec.Name), string(spec.Revision), "0")
		v, err := typeurl.UnmarshalAny(ctrs.records[ctrID].Spec)
		require.NoError(t, err)
		return v.(*specs.Spec).Process.Env
	}
	want := runtime.ReportFileEnv + "=" + containerBootDir + "/" + reportFile
	require.True(t, slices.Contains(env(runtime.WorkerSpec{Namespace: "default", Name: "lake", OwnerKind: v1alpha1.KindLake, Image: image}), want))
	fnEnv := env(runtime.WorkerSpec{Namespace: "default", Name: "fn", OwnerKind: v1alpha1.KindFunction, Revision: "fn-1", Image: image})
	require.False(t, slices.ContainsFunc(fnEnv, func(e string) bool { return strings.HasPrefix(e, runtime.ReportFileEnv+"=") }))
}
