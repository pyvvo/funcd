package quarantine_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
)

func TestStampRoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 1, 30, 0, 123_456_789, time.FixedZone("CEST", 2*3600))
	s := quarantine.Stamp(at)
	require.Equal(t, "20261024T233000.123Z", s, "UTC, to the ms")
	got, err := quarantine.ParseStamp(s)
	require.NoError(t, err)
	require.True(t, got.Equal(at.Truncate(time.Millisecond)))
	_, err = quarantine.ParseStamp("20261024T233000Z")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func TestParseState(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]quarantine.State{"": quarantine.Kept, "moving\n": quarantine.Moving,
		"kept": quarantine.Kept, "returning": quarantine.Returning} {
		got, err := quarantine.ParseState(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := quarantine.ParseState("gone")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

func TestExpiredAndUntil(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	e := quarantine.Entry{Since: since, State: quarantine.Kept}
	require.Equal(t, since.Add(168*time.Hour), e.Until(168*time.Hour))
	require.True(t, e.Expired(since.Add(time.Millisecond)))
	require.False(t, e.Expired(since), "an entry exactly one period old is kept until the next sweep")
	for _, st := range []quarantine.State{quarantine.Moving, quarantine.Returning} {
		e.State = st
		require.False(t, e.Expired(since.Add(time.Hour)), "a %s entry never expires", st)
	}
}

func TestPick(t *testing.T) {
	t.Parallel()
	bucket := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindBucket, Namespace: "team-a", Name: "uploads"}}
	vol := quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Namespace: "team-a", Name: "uploads"}, Volume: "cache"}
	t0 := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	entries := []quarantine.Entry{
		{Ref: bucket, Since: t0, State: quarantine.Kept},
		{Ref: bucket, Since: t0.Add(time.Hour), State: quarantine.Kept},
		{Ref: bucket, Since: t0.Add(2 * time.Hour), State: quarantine.Moving},
		{Ref: vol, Since: t0, State: quarantine.Kept},
	}
	got, err := quarantine.Pick(entries, bucket, time.Time{})
	require.NoError(t, err)
	require.Equal(t, t0.Add(time.Hour), got.Since, "the newest entry that is not moving")
	got, err = quarantine.Pick(entries, bucket, t0)
	require.NoError(t, err)
	require.Equal(t, t0, got.Since)
	_, err = quarantine.Pick(entries, bucket, t0.Add(2*time.Hour))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a moving entry is not returned")
	entries[0].State = quarantine.Returning
	got, err = quarantine.Pick(entries, bucket, time.Time{})
	require.NoError(t, err)
	require.Equal(t, quarantine.Returning, got.State, "a cut return resumes first")
	got, err = quarantine.Pick(entries, vol, time.Time{})
	require.NoError(t, err)
	require.Equal(t, "Function team-a/uploads/cache", got.String())
	_, err = quarantine.Pick(entries, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindKVStore, Namespace: "team-a", Name: "uploads"}}, time.Time{})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}
