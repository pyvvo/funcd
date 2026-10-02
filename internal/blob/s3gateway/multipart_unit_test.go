package s3gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIssue30_AbandonedMultipartUploadExpires: an upload whose client never completes or
// aborts it (a crashed DuckDB) is dropped once it has gone multipartIdleExpiry without a
// part, so its buffered parts do not stay in daemon RAM until a restart.
func TestIssue30_AbandonedMultipartUploadExpires(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	m := newMultipartStore()
	m.now = func() time.Time { return now }

	abandoned := m.create("lakehouse", "bronze/a.parquet")
	require.NoError(t, m.putPart(abandoned, 1, []byte("held"), 1024))

	now = now.Add(multipartIdleExpiry / 2)
	live := m.create("lakehouse", "bronze/b.parquet")
	now = now.Add(multipartIdleExpiry / 2)
	require.NoError(t, m.putPart(live, 1, []byte("fresh"), 1024))

	now = now.Add(time.Second)
	m.create("lakehouse", "bronze/c.parquet")

	_, ok := m.parts(abandoned)
	require.False(t, ok, "an upload idle past multipartIdleExpiry must be dropped")
	_, ok = m.parts(live)
	require.True(t, ok, "an upload that received a part within the window is kept")
}
