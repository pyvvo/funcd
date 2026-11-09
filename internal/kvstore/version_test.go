package kvstore_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/kvstore"
)

// A version is an opaque hash (ADR-0320 Decision 2): 32 hex characters, one per write of a key on a timeline, and
// another for another write, key or timeline.
func TestVersionIsAnOpaqueHash(t *testing.T) {
	t.Parallel()
	tl := kvstore.NewTimeline()
	require.Regexp(t, `^[0-9a-f]{64}$`, tl)
	v := kvstore.Version(tl, "ns/s/t/k", 7)
	require.Regexp(t, `^[0-9a-f]{32}$`, v)
	require.Equal(t, v, kvstore.Version(tl, "ns/s/t/k", 7), "one write of one key gives one version")
	for _, other := range []string{
		kvstore.Version(tl, "ns/s/t/k", 8),
		kvstore.Version(tl, "ns/s/t/j", 7),
		kvstore.Version(kvstore.NewTimeline(), "ns/s/t/k", 7),
	} {
		require.NotEqual(t, v, other)
	}
}
