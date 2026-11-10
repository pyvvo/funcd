package kvstore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
)

// An expiry rounds up to the next second (ADR-0320 Decision 4): a key lives at least its TTL, and less than a second
// more.
func TestExpiresAtRoundsUpToTheNextSecond(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		now  time.Time
		ttl  time.Duration
		want uint64
	}{
		{time.Unix(10, 0), time.Second, 11},
		{time.Unix(10, 1), time.Second, 12},
		{time.Unix(10, 999_999_999), time.Second, 12},
		{time.Unix(10, 500_000_000), 1500 * time.Millisecond, 12},
		{time.Unix(10, 0), 0, 0},
	} {
		require.Equal(t, tc.want, kvstore.ExpiresAt(tc.now, tc.ttl), "%v + %v", tc.now, tc.ttl)
	}
}
