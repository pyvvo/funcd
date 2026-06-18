// Package e2e_test is the L3b embed-e2e harness (ADR-0025): it drives the whole
// platform through the PUBLIC pkg/funcd surface only — the depguard e2e-boundary rule
// forbids any internal/ import, so this proves "platform as a library, zero infra"
// with no internal reach-in. The internal pkg/funcd tests can't prove that (they
// import internal/); the boundary is the new fact this tier establishes.
package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
)

// scenario: e2e-embed-inmemory-boots.
func TestE2EEmbedInMemoryBoots(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	require.NotNil(t, p)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	// missing-dep through the public surface: New() with no preset → fault.Invalid.
	_, err = funcd.New()
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "New() with no preset names a missing required port")
}

// scenario: e2e-embed-run-shutdown-crash-only.
func TestE2EEmbedRunShutdownCrashOnly(t *testing.T) {
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
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	require.NoError(t, p.Shutdown(context.Background()), "second Shutdown is idempotent")
}

// scenario: e2e-embed-multiple-platforms.
func TestE2EEmbedMultiplePlatforms(t *testing.T) {
	t.Parallel()
	p1, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	p2, err := funcd.New(funcd.InMemory())
	require.NoError(t, err)
	// two independent platforms in one process (no global state) shut down cleanly.
	require.NoError(t, p1.Shutdown(context.Background()))
	require.NoError(t, p2.Shutdown(context.Background()))
}
