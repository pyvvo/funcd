package deadletter_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
)

// scenario: every-timestamp-utc-millisecond — a record failed at …35.965999999 reads failedAt …35.965Z; a stored
// record in another form no longer decodes (ADR-0196 Decision 4).
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	at := time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("UTC+2", 2*60*60))
	b, err := json.Marshal(deadletter.DeadLetter{ID: "01DL", Namespace: "team-a", FailedAt: v1.NewTimestamp(at)})
	require.NoError(t, err)
	require.Contains(t, string(b), `"failedAt":"2026-10-07T20:03:35.965Z"`)
	var back deadletter.DeadLetter
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, v1.NewTimestamp(at), back.FailedAt)

	err = json.Unmarshal([]byte(`{"id":"01DL","failedAt":"2026-10-07T20:03:35.965999999Z"}`), &back)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}
