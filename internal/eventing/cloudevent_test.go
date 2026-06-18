package eventing_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/internal/eventing"
)

// scenario: cloudevent-normalized.
func TestScenarioCloudEventNormalized(t *testing.T) {
	t.Parallel()
	ev, err := eventing.NewTimerEvent("team-a", "nightly")
	require.NoError(t, err)
	require.Equal(t, "1.0", ev.SpecVersion)
	require.Equal(t, "io.funcd.timer.tick", ev.Type)
	require.Equal(t, "funcd://team-a/eventsource/nightly", ev.Source)
	require.NotEmpty(t, ev.ID)
	require.False(t, ev.Time.IsZero())

	// CloudEvents-v1.0-shaped JSON: required context attributes are present.
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, "1.0", m["specversion"])
	require.Equal(t, "io.funcd.timer.tick", m["type"])
	require.Contains(t, m, "id")
	require.Contains(t, m, "source")
	require.Contains(t, m, "time")

	// id is unique per call (fresh random).
	ev2, err := eventing.NewTimerEvent("team-a", "nightly")
	require.NoError(t, err)
	require.NotEqual(t, ev.ID, ev2.ID)
}
