package eventing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/eventing"
)

// scenario: fanout-routes-by-key — a published named event reaches only the subscriber(s) whose
// (ns, source, event) key matches, derived from the envelope's source URI + type (the inverse of
// NewNamedEvent); a foreign/unsubscribed source is a no-op; cancel deregisters (ADR-0108).
func TestFanoutRoutesByKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := eventing.NewFanout()

	var tickHits, otherHits int
	cancel := f.Subscribe("team-a", "nightly", "tick", func(context.Context, eventing.CloudEvent) { tickHits++ })
	f.Subscribe("team-a", "nightly", "other", func(context.Context, eventing.CloudEvent) { otherHits++ })

	ev, err := eventing.NewNamedEvent("team-a", "nightly", "tick")
	require.NoError(t, err)
	require.NoError(t, f.Publish(ctx, ev))
	require.Equal(t, 1, tickHits, "the matching subscriber fires")
	require.Equal(t, 0, otherHits, "a different event name does not fire")

	// publish-to-nobody (no subscriber for this source) is a no-op, not an error.
	nobody, _ := eventing.NewNamedEvent("team-a", "absent", "tick")
	require.NoError(t, f.Publish(ctx, nobody))

	// a foreign (non-funcd) source URI is dropped.
	require.NoError(t, f.Publish(ctx, eventing.CloudEvent{Source: "https://example.com/x", Type: "tick"}))

	// cancel deregisters.
	cancel()
	require.NoError(t, f.Publish(ctx, ev))
	require.Equal(t, 1, tickHits, "a cancelled subscriber no longer fires")
}
