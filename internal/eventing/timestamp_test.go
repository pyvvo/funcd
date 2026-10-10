package eventing

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// scenario: every-timestamp-utc-millisecond — a blob object modified at …35.965999999 (UTC+2) fires an event whose
// data.time reads …35.965Z, and every CloudEvent envelope time (timer and blob) is in the ADR-0196 form.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	re := regexp.MustCompile(`"time":"` + v1.TimestampPattern[1:len(v1.TimestampPattern)-1] + `"`)
	pub := &capturePub{}
	wm := NewMemWatermark()
	lister := &fakeLister{}
	w := newWatcher(t, lister, pub, wm)
	mt := time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("UTC+2", 2*60*60))
	lister.set(blob.Attributes{Key: "drop/a.parquet", Size: 42, ModTime: mt})
	before := time.Now().Truncate(time.Millisecond)
	w.poll(context.Background())
	timer, err := NewNamedEvent("lake", "clock", "tick")
	require.NoError(t, err)
	after := time.Now()

	require.Equal(t, 1, pub.count())
	blobEv := pub.snapshot()[0]
	require.Contains(t, string(blobEv.Data), `"time":"2026-10-07T20:03:35.965Z"`)
	for _, ev := range []CloudEvent{blobEv, timer} {
		b, err := json.Marshal(ev)
		require.NoError(t, err)
		require.Regexp(t, re, string(b))
		at := time.Time(ev.Time)
		require.False(t, at.Before(before) || at.After(after), "%v not in [%v, %v]", at, before, after)
	}
}
