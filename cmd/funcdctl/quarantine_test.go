package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// ADR-0288 Decision 6: return's arguments name a KVStore, a Bucket or a Function's volume, and --since an RFC 3339
// time; anything else is fault.Invalid before any call.
func TestQuarantineReturnArgs(t *testing.T) {
	t.Parallel()
	since := v1.NewTimestamp(time.Date(2026, 10, 25, 1, 30, 0, 123_000_000, time.UTC))
	for _, tc := range []struct {
		kind, ref, since string
		want             hold.QuarantineReturn
	}{
		{"kvstore", "team-a/orders", "", hold.QuarantineReturn{Kind: v1.KindKVStore, Namespace: "team-a", Name: "orders"}},
		{"bucket", "team-a/uploads", "2026-10-25T01:30:00.123Z",
			hold.QuarantineReturn{Kind: v1.KindBucket, Namespace: "team-a", Name: "uploads", Since: &since}},
		{"volume", "team-a/api/cache", "", hold.QuarantineReturn{Kind: v1.KindFunction, Namespace: "team-a", Name: "api", Volume: "cache"}},
	} {
		got, err := quarantineReturn(tc.kind, tc.ref, tc.since)
		require.NoError(t, err, tc.ref)
		require.Equal(t, tc.want, got, tc.ref)
	}
	for _, bad := range [][3]string{{"sqldatabase", "a/b", ""}, {"bucket", "a", ""}, {"volume", "a/b", ""},
		{"kvstore", "a/b/c", ""}, {"bucket", "a/b", "yesterday"}} {
		_, err := quarantineReturn(bad[0], bad[1], bad[2])
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
	}
}
