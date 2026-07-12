package funcd

import (
	"context"
	"testing"
	"time"

	"github.com/green-0-rabbit/funcd/internal/funclog"
)

// fakeSink records Append calls so the tee test can assert logs are still persisted.
type fakeSink struct {
	appended []funclog.Entry
	closed   bool
}

func (f *fakeSink) Append(_ context.Context, _ funclog.Resource, e funclog.Entry) error {
	f.appended = append(f.appended, e)
	return nil
}
func (f *fakeSink) Flush(context.Context, funclog.Resource) (string, error) { return "key", nil }
func (f *fakeSink) Close() error                                            { f.closed = true; return nil }

// teeLogSink streams every Append to the observer (live) AND delegates to the inner sink (persist).
func TestTeeLogSinkStreamsAndPersists(t *testing.T) {
	inner := &fakeSink{}
	var got LogLine
	seen := 0
	tee := &teeLogSink{inner: inner, observe: func(l LogLine) { got = l; seen++ }}

	res := funclog.Resource{Namespace: "default", Function: "ingest", Replica: "0"}
	e := funclog.Entry{
		Time:       time.Date(2026, 7, 12, 9, 8, 7, 0, time.UTC),
		Severity:   funclog.SevInfo,
		Body:       "hello",
		Attrs:      map[string]string{"name": "Ada"},
		Invocation: "inv-1",
	}
	if err := tee.Append(context.Background(), res, e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if seen != 1 {
		t.Fatalf("observer called %d times, want 1", seen)
	}
	if got.Function != "ingest" || got.Severity != "INFO" || got.Body != "hello" ||
		got.Attrs["name"] != "Ada" || got.Invocation != "inv-1" {
		t.Fatalf("observed line mismatch: %+v", got)
	}
	if len(inner.appended) != 1 {
		t.Fatalf("inner sink Append called %d times, want 1 (logs must still persist)", len(inner.appended))
	}

	// Flush/Close delegate to the inner sink.
	if _, err := tee.Flush(context.Background(), res); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := tee.Close(); err != nil || !inner.closed {
		t.Fatalf("Close did not delegate to inner (err=%v closed=%v)", err, inner.closed)
	}
}
