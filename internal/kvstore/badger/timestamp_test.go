package badger

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// scenario: every-timestamp-utc-millisecond — a re-baseline writes the manifest's at in the ADR-0196 form, and a base
// stamped at …35.965999999 reads …35.965Z.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	ctx := context.Background()
	bucket := newFakeBucket()
	b, err := NewBackup(openRawDB(t, t.TempDir()), bucket, BackupConfig{})
	require.NoError(t, err)
	before := time.Now().Truncate(time.Millisecond)
	require.NoError(t, b.(*backup).Rebaseline(ctx))
	after := time.Now()
	raw, err := bucket.Get(ctx, manifestKey)
	require.NoError(t, err)
	at := regexp.MustCompile(`"at":"([^"]*)"`).FindStringSubmatch(string(raw))
	require.Len(t, at, 2, "%s", raw)
	require.Regexp(t, v1.TimestampPattern, at[1])
	parsed, err := time.Parse(v1.TimestampLayout, at[1])
	require.NoError(t, err)
	require.False(t, parsed.Before(before) || parsed.After(after), "%v not in [%v, %v]", parsed, before, after)

	stamped := time.Date(2026, 10, 7, 20, 3, 35, 965999999, time.UTC)
	bk := &backup{bucket: bucket}
	require.NoError(t, bk.saveManifest(ctx, manifest{
		Base: &segment{Prefix: "base/x", To: 5, Parts: 1, At: manifestTime(v1.NewTimestamp(stamped))},
		Incs: []segment{{Prefix: "inc/y", Since: 5, To: 7, Parts: 1}},
	}))
	raw, err = bucket.Get(ctx, manifestKey)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"at":"2026-10-07T20:03:35.965Z"`)
	require.Equal(t, 1, strings.Count(string(raw), `"at"`), "an incremental carries no at")
}

// A manifest written before ADR-0196 (v0.7.3: RFC3339Nano, any offset) still loads, its at read as a Timestamp
// (Decision 10); the next write is in the form.
func TestManifestReadsRFC3339Nano(t *testing.T) {
	ctx := context.Background()
	for raw, want := range map[string]string{
		`{"base":{"prefix":"base/x","since":0,"to":5,"parts":1,"at":"2026-10-07T20:03:35.965999999Z"}}`:                                              "2026-10-07T20:03:35.965Z",
		`{"base":{"prefix":"base/x","since":0,"to":5,"parts":1,"at":"2026-10-07T22:03:35.965123+02:00"}}`:                                            "2026-10-07T20:03:35.965Z",
		`{"base":{"prefix":"base/x","since":0,"to":5,"parts":1,"at":"2026-10-07T20:03:35Z"},"incs":[{"prefix":"inc/y","since":5,"to":7,"parts":1}]}`: "2026-10-07T20:03:35.000Z",
	} {
		bucket := newFakeBucket()
		require.NoError(t, bucket.Put(ctx, manifestKey, []byte(raw), blob.PutOptions{}))
		bk := &backup{bucket: bucket}
		man, err := bk.loadManifest(ctx)
		require.NoError(t, err, raw)
		require.Equal(t, want, v1.Timestamp(man.Base.At).String(), raw)
		require.NoError(t, bk.saveManifest(ctx, man))
		out, err := bucket.Get(ctx, manifestKey)
		require.NoError(t, err)
		require.Contains(t, string(out), `"at":"`+want+`"`)
	}
}
