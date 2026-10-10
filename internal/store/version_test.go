package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/store"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		rv   string
		want store.Version
	}{
		{"120", store.Version{N: 120}},
		{"0", store.Version{}},
		{"0123456789abcdef-121", store.Version{Timeline: "0123456789abcdef", N: 121}},
		{"ff-1", store.Version{Timeline: "ff", N: 1}},
	} {
		got, err := store.ParseVersion(tc.rv)
		require.NoError(t, err, tc.rv)
		require.Equal(t, tc.want, got, tc.rv)
		require.Equal(t, tc.rv, got.String(), "round trip")
	}
	for _, rv := range []string{"", "-1", "abc-", "ABC-1", "xyz-1", "0123-1-2", "12a", "-", "0123456789abcdef", " 1", "+1"} {
		_, err := store.ParseVersion(rv)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q is malformed", rv)
	}
}

func TestResumePoint(t *testing.T) {
	t1, t2 := "1111111111111111", "2222222222222222"
	v := func(tl string, n uint64) store.Version { return store.Version{Timeline: tl, N: n} }
	require.Equal(t, v(t1, 5), store.ResumePoint(store.Version{}, v(t1, 5)), "the first version seen")
	require.Equal(t, v(t1, 7), store.ResumePoint(v(t1, 5), v(t1, 7)), "a larger N of the timeline")
	require.Equal(t, v(t1, 7), store.ResumePoint(v(t1, 7), v(t1, 3)), "a smaller N of the timeline")
	require.Equal(t, v(t1, 100), store.ResumePoint(v(t2, 5), v(t1, 100)), "an older timeline after a newer one")
	require.Equal(t, v("", 120), store.ResumePoint(v(t2, 5), v("", 120)), "a plain version after a timeline")
}
