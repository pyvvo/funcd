package eventing_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/eventing"
)

// scenario: cloudevent-normalized — a named event's envelope carries the source URI + the event name as
// `type`, and the source URI round-trips through ParseSourceURI (the fanout routing key, ADR-0108).
func TestScenarioCloudEventNormalized(t *testing.T) {
	t.Parallel()
	ev, err := eventing.NewNamedEvent("team-a", "nightly", "tick")
	require.NoError(t, err)
	require.Equal(t, "1.0", ev.SpecVersion)
	require.Equal(t, "tick", ev.Type, "type carries the event name")
	require.Equal(t, "funcd://team-a/eventsource/nightly", ev.Source)
	require.NotEmpty(t, ev.ID)
	require.False(t, ev.Time.IsZero())

	// CloudEvents-v1.0-shaped JSON: required context attributes are present.
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, "1.0", m["specversion"])
	require.Equal(t, "tick", m["type"])
	require.Contains(t, m, "id")
	require.Contains(t, m, "source")
	require.Contains(t, m, "time")

	// The source URI round-trips to (ns, source) — the fanout routing key.
	ns, source, ok := eventing.ParseSourceURI(ev.Source)
	require.True(t, ok)
	require.Equal(t, "team-a", string(ns))
	require.Equal(t, "nightly", string(source))
	_, _, ok = eventing.ParseSourceURI("https://example.com/x")
	require.False(t, ok, "a foreign URI is not routable")

	// id is unique per call (fresh random).
	ev2, err := eventing.NewNamedEvent("team-a", "nightly", "tick")
	require.NoError(t, err)
	require.NotEqual(t, ev.ID, ev2.ID)
}
