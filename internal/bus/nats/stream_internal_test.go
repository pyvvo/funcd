package nats

import (
	"context"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/bus"
)

// Issue #702: EnsureStream carries MaxAge to the JetStream stream, so a stream that keeps every publish
// (the KV change feed's) stays bounded by its retention window.
func TestIssue702_EnsureStreamSetsMaxAge(t *testing.T) {
	ctx := context.Background()
	b, err := Open(ctx, Options{Storage: MemoryStorage})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if err := b.EnsureStream(ctx, bus.StreamConfig{Name: "AGE", Subjects: []bus.Subject{"age.>"}, MaxAge: time.Hour}); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	s, err := b.(*embedded).js.Stream(ctx, "AGE")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := s.CachedInfo().Config.MaxAge; got != time.Hour {
		t.Fatalf("stream MaxAge = %v, want %v", got, time.Hour)
	}
}
