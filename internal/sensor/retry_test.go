package sensor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A unit whose backoff elapsed while both workers were busy is only queued, not in flight: once the queue shuts
// down it is not started, so shutdown waits for the attempts in flight alone (ADR-0118 §6, issue #347).
func TestIssue347_ShutdownStartsNoDueUnit(t *testing.T) {
	q := newRetryQueue(time.Hour, time.Hour)
	q.reschedule("due", delivery{}, 1)
	q.ready("due")
	q.shutDown()
	id, _, _, shutdown := q.get()
	require.True(t, shutdown, "get handed out queued unit %q after the queue shut down", id)
}
