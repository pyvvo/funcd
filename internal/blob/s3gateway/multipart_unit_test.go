package s3gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// settableClock is a clock.Clock the test moves by hand.
type settableClock struct{ t time.Time }

func (c *settableClock) Now() time.Time { return c.t }

var _ clock.Clock = (*settableClock)(nil)

// TestIssue30_AbandonedMultipartUploadExpires: an upload whose client never completes or
// aborts it (a crashed DuckDB) is dropped once it has gone multipartIdleExpiry without a
// part, so its buffered parts do not stay in daemon RAM until a restart. A part refreshes
// the window, so a slow upload that keeps sending parts is never dropped mid-transfer.
func TestIssue30_AbandonedMultipartUploadExpires(t *testing.T) {
	t.Parallel()
	clk := &settableClock{t: time.Unix(1_700_000_000, 0)}
	m := newMultipartStore()
	m.clock = clk

	abandoned := m.create("lakehouse", "bronze/a.parquet")
	require.NoError(t, m.putPart(abandoned, 1, []byte("held"), 1024))
	live := m.create("lakehouse", "bronze/b.parquet")

	clk.t = clk.t.Add(multipartIdleExpiry / 2)
	idle := m.create("lakehouse", "bronze/c.parquet")

	clk.t = clk.t.Add(multipartIdleExpiry/2 - time.Second)
	require.NoError(t, m.putPart(live, 1, []byte("fresh"), 1024))

	clk.t = clk.t.Add(2 * time.Second)
	m.create("lakehouse", "bronze/d.parquet")

	_, ok := m.parts(abandoned)
	require.False(t, ok, "an upload idle past multipartIdleExpiry must be dropped")
	_, ok = m.parts(live)
	require.True(t, ok, "a part within the window refreshes it, even when the upload was created before it")
	_, ok = m.parts(idle)
	require.True(t, ok, "an upload created within the window is kept")
}
