package sensor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// eventing.deliveryBackoffInitial 50ms and deliveryBackoffMax 100ms make attempts 2-4 wait 50, 100 and 100 ms after the
// previous failure (ADR-0163); zero keeps 100ms doubling up to 10s.
func TestDeliveryBackoffFromDeps(t *testing.T) {
	q := newRetryQueue(50*time.Millisecond, 100*time.Millisecond, 0, 0)
	require.Equal(t, []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond},
		[]time.Duration{q.backoff(1), q.backoff(2), q.backoff(3)})
	d := newRetryQueue(0, 0, 0, 0)
	require.Equal(t, retryBaseDelay, d.backoff(1))
	require.Equal(t, retryMaxDelay, d.backoff(10))
}
