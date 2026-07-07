package funcd

import (
	"context"
	"testing"
)

// scenario: disabled-config (daemon half) — WithoutLogCompaction() wires no compactor (raw left untouched),
// while a default blob-backed platform wires one. The compaction behavior itself is covered by
// internal/funclog/compact's scenario tests.
func TestScenarioDisabledConfigNoCompactor(t *testing.T) {
	// Default InMemory() has a blob substrate, so compaction is wired on.
	p, err := New(InMemory())
	if err != nil {
		t.Fatalf("New(InMemory): %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	if p.compactor == nil {
		t.Fatal("expected a compactor by default when a blob substrate is present")
	}

	// WithoutLogCompaction disables it.
	p2, err := New(InMemory(), WithoutLogCompaction())
	if err != nil {
		t.Fatalf("New(InMemory, WithoutLogCompaction): %v", err)
	}
	t.Cleanup(func() { _ = p2.Shutdown(context.Background()) })
	if p2.compactor != nil {
		t.Fatal("WithoutLogCompaction() must wire no compactor")
	}
}
