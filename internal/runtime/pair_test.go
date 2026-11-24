package runtime_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/runtimecontract"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// ADR-0346 Decision 3: Pair passes the worker port's contract, here over two process drivers, its image side standing
// in for an engine: the contract's specs name an image, so every one of them runs on the image side through Pair.
func TestPairContract(t *testing.T) {
	t.Parallel()
	runtimecontract.RunContract(t, func(t *testing.T) runtime.Runtime {
		t.Helper()
		return runtime.Pair(process.New(nil), process.New(nil))
	})
}

func sleeper(name string, image string) runtime.WorkerSpec {
	return runtime.WorkerSpec{Namespace: "default", Name: v1.ObjectName(name), OwnerKind: v1.KindFunction, Image: image,
		Command: []string{"sleep", "30"}}
}

func names(t *testing.T, rt runtime.Runtime) []v1.ObjectName {
	t.Helper()
	list, err := rt.List(context.Background(), "default")
	require.NoError(t, err)
	var out []v1.ObjectName
	for _, in := range list {
		out = append(out, in.Name)
	}
	return out
}

// ADR-0346 Decision 3: a spec with an Image runs on the image side, any other on the process side; List merges both,
// and each instance is stopped and removed on the driver that holds it.
func TestPairRoutesByImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	procs, imgs := process.New(nil), process.New(nil)
	p := runtime.Pair(procs, imgs)
	t.Cleanup(func() { _ = p.Close() })
	onProcess, err := p.Create(ctx, sleeper("node", ""))
	require.NoError(t, err)
	onImages, err := p.Create(ctx, sleeper("deno", "registry.example.com/deno@sha256:aa"))
	require.NoError(t, err)
	require.Equal(t, []v1.ObjectName{"node"}, names(t, procs))
	require.Equal(t, []v1.ObjectName{"deno"}, names(t, imgs))
	require.ElementsMatch(t, []v1.ObjectName{"node", "deno"}, names(t, p))

	for _, id := range []runtime.InstanceID{onProcess.ID, onImages.ID} {
		require.NoError(t, p.Start(ctx, id))
		require.NoError(t, p.Stop(ctx, id))
		require.NoError(t, p.Remove(ctx, id))
	}
	require.Empty(t, names(t, procs))
	require.Empty(t, names(t, imgs))
}

// ADR-0346 Decision 3: an instance Pair did not create, as one a driver knew before, is found by asking each driver's
// Status, then driven there; an id neither knows is NotFound, and its Remove a no-op.
func TestPairFindsAnInstanceItDidNotRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	procs, imgs := process.New(nil), process.New(nil)
	p := runtime.Pair(procs, imgs)
	t.Cleanup(func() { _ = p.Close() })
	inst, err := imgs.Create(ctx, sleeper("deno", "registry.example.com/deno@sha256:aa"))
	require.NoError(t, err)
	require.NoError(t, p.Start(ctx, inst.ID))
	got, err := p.Status(ctx, inst.ID)
	require.NoError(t, err)
	require.Equal(t, runtime.StateRunning, got.State)
	require.NoError(t, p.Stop(ctx, inst.ID))
	require.NoError(t, p.Remove(ctx, inst.ID))
	require.Empty(t, names(t, imgs))

	_, err = p.Status(ctx, "default/nope/r0")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.NoError(t, p.Remove(ctx, "default/nope/r0"))
}

// capRuntime is a process driver with the optional capabilities a test sets, recording what reaches them.
type capRuntime struct {
	runtime.Runtime
	grace         time.Duration
	enforces      bool
	output, logs  bool
	ok            bool
	why, endpoint string
}

func (c *capRuntime) StopGrace() time.Duration                      { return c.grace }
func (c *capRuntime) EnforcesLimits() bool                          { return c.enforces }
func (c *capRuntime) SetOutputCapture(fn runtime.OutputCaptureFunc) { c.output = fn != nil }
func (c *capRuntime) SetLogCapture(fn runtime.LogCaptureFunc)       { c.logs = fn != nil }
func (c *capRuntime) RunsImages() (bool, string)                    { return c.ok, c.why }
func (c *capRuntime) EndpointHost() string                          { return c.endpoint }

// ADR-0346 Decision 3: Pair runs images as its image side does, hands raw output capture to both drivers and
// structured logs to the process side alone, stops with the longer grace, and enforces limits only when both do.
func TestPairForwardsCapabilities(t *testing.T) {
	t.Parallel()
	procs := &capRuntime{Runtime: process.New(nil), grace: 5 * time.Second, enforces: false}
	imgs := &capRuntime{Runtime: process.New(nil), grace: 10 * time.Second, enforces: true, ok: true, endpoint: "funcd.host"}
	p := runtime.Pair(procs, imgs)
	t.Cleanup(func() { _ = p.Close() })

	p.(runtime.OutputCapturer).SetOutputCapture(func(runtime.WorkerSpec, *workerpipe.Output) {})
	require.True(t, procs.output)
	require.True(t, imgs.output)
	p.(runtime.LogCapturer).SetLogCapture(func(runtime.WorkerSpec, io.ReadCloser) {})
	require.True(t, procs.logs)
	require.False(t, imgs.logs, "a container worker has no structured-log channel")
	require.Equal(t, 10*time.Second, p.(runtime.StopGracer).StopGrace())
	require.False(t, p.(runtime.LimitEnforcer).EnforcesLimits(), "the process side enforces none")
	procs.enforces = true
	require.True(t, p.(runtime.LimitEnforcer).EnforcesLimits())

	ok, _ := p.(runtime.ImageRunner).RunsImages()
	require.True(t, ok)
	require.Equal(t, "funcd.host", p.(runtime.ImageRunner).EndpointHost())
}

// ADR-0346 Decision 1: NoImages runs no image and says why; a Pair over it says so too, and still runs a worker with no
// Image on its process side.
func TestNoImages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const why = "no container engine runs add-on engines: no Docker engine answers"
	none := runtime.NoImages(why)
	ok, got := none.(runtime.ImageRunner).RunsImages()
	require.False(t, ok)
	require.Equal(t, why, got)
	_, err := none.Create(ctx, sleeper("deno", "registry.example.com/deno@sha256:aa"))
	require.True(t, errors.Is(err, runtime.ErrImageUnavailable))
	require.Equal(t, fault.NotFound, fault.KindOf(err))

	p := runtime.Pair(process.New(nil), none)
	t.Cleanup(func() { _ = p.Close() })
	ok, got = p.(runtime.ImageRunner).RunsImages()
	require.False(t, ok)
	require.Equal(t, why, got)
	inst, err := p.Create(ctx, sleeper("node", ""))
	require.NoError(t, err)
	require.NoError(t, p.Start(ctx, inst.ID))
	require.NoError(t, p.Stop(ctx, inst.ID))
}
