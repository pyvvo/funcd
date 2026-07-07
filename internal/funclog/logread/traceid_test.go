package logread_test

import (
	"context"
	"testing"

	"github.com/green-0-rabbit/funcd/internal/funclog/compact"
	"github.com/green-0-rabbit/funcd/internal/funclog/logread"
)

// rowTrace builds a compact.Row for a given function + trace-id (ADR-0106 run-scoped read tests).
func rowTrace(tNano int64, body, fn, trace string) compact.Row {
	return compact.Row{
		TimeUnixNano: tNano, SeverityText: "info", SeverityNumber: 9, Body: body,
		Namespace: "default", Function: fn, Replica: "0", Tenant: "default", Source: "console", TraceID: trace,
	}
}

const (
	traceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	traceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// scenario: reader-filters-by-traceid — two trace-ids in one function's logs → only the queried trace's
// records come back.
func TestScenarioReaderFiltersByTraceID(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedCompacted(t, b, "default", "fn", base, []compact.Row{
		rowTrace(base, "run-A line", "fn", traceA),
		rowTrace(base+1, "run-B line", "fn", traceB),
		rowTrace(base+2, "run-A line 2", "fn", traceA),
	})
	r := logread.NewBlobReader(b)
	lines, err := r.Read(context.Background(), logread.Query{Namespace: "default", Function: "fn", TraceID: traceA})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 lines for trace A, got %d", len(lines))
	}
	for _, l := range lines {
		if l.TraceID != traceA {
			t.Fatalf("a foreign trace leaked: %q", l.TraceID)
		}
	}
}

// scenario: namespace-wide-by-traceid — records under two functions, one trace → both come back when
// Function == "" (namespace-wide), and another function's other-trace line is excluded.
func TestScenarioNamespaceWideByTraceID(t *testing.T) {
	b := memBucket(t)
	base := baseTime().UnixNano()
	seedCompacted(t, b, "default", "step-one", base, []compact.Row{
		rowTrace(base, "step-one of run-A", "step-one", traceA),
		rowTrace(base+1, "step-one of run-B", "step-one", traceB),
	})
	seedCompacted(t, b, "default", "step-two", base, []compact.Row{
		rowTrace(base+2, "step-two of run-A", "step-two", traceA),
	})
	r := logread.NewBlobReader(b)
	lines, err := r.Read(context.Background(), logread.Query{Namespace: "default", TraceID: traceA}) // Function == "" ⇒ namespace-wide
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("want 2 run-A lines across both functions, got %d", len(lines))
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if l.TraceID != traceA {
			t.Fatalf("a foreign trace leaked: %q", l.TraceID)
		}
		seen[l.Function] = true
	}
	if !seen["step-one"] || !seen["step-two"] {
		t.Fatalf("namespace-wide read must span both step functions, saw %v", seen)
	}
}

// A bare namespace scan with no trace filter (and no function) is rejected — the guard prevents an
// unfiltered full-namespace dump (ADR-0106).
func TestScenarioBareNamespaceScanRejected(t *testing.T) {
	r := logread.NewBlobReader(memBucket(t))
	if _, err := r.Read(context.Background(), logread.Query{Namespace: "default"}); err == nil {
		t.Fatal("a namespace scan with neither function nor traceId must be rejected")
	}
}
