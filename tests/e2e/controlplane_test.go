package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// startPlatform boots an InMemory platform, Run-s it in the background, and returns
// an SDK client pointed at its control-plane Addr (authenticated with DevToken).
// Cleanup cancels Run and waits for a graceful stop — all through the PUBLIC surface
// (pkg/funcd + pkg/sdk + api), no internal/ import (depguard e2e-boundary).
func startPlatform(t *testing.T) *sdk.Client {
	t.Helper()
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	require.NotEmpty(t, p.Addr())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return c
}

// newFunction builds a minimal VALID Function (admission + the shape gate require
// namespace=default + resourceGroup + runtime + handler + artifact.uri; Replicas=1
// provisions one worker replica so the reconciler reaches Ready).
func newFunction(name string) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "app.handler"
	fn.Spec.Artifact.URI = "mem://artifact.js"
	fn.Spec.Replicas = 1
	return fn
}

// scenario: addr-available-after-new.
func TestE2EAddrAvailableAfterNew(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	require.NotEmpty(t, p.Addr(), "Addr() is the bound control-plane address, ready at New (before Run)")
	require.Contains(t, p.Addr(), "127.0.0.1:")
}

// scenario: run-serves-control-plane.
func TestE2ERunServesControlPlane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := startPlatform(t)

	applied, err := c.Apply(ctx, newFunction("served"))
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("served"), applied.GetName())

	got, err := c.Get(ctx, v1.KindFunction, "default", "served")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("served"), got.GetName(), "the served API round-trips through the SDK")
}

// scenario: run-reconciles-function-to-ready.
func TestE2ERunReconcilesFunctionToReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := startPlatform(t)

	_, err := c.Apply(ctx, newFunction("ready"))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		got, gerr := c.Get(ctx, v1.KindFunction, "default", "ready")
		if gerr != nil {
			return false
		}
		return got.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 50*time.Millisecond, "the controller's Function reconciler should drive it to Ready")
}

// scenario: run-shutdown-graceful.
func TestE2ERunShutdownGraceful(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	cancel()

	select {
	case runErr := <-done:
		require.NoError(t, runErr, "Run returns nil on graceful ctx cancel")
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	require.NoError(t, p.Shutdown(context.Background()), "second Shutdown is idempotent")
}
