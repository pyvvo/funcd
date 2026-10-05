package local

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/pyvvo/funcd/api/fault"
)

// NestedCapOp is the fault Op of a nested call refused by the per-target in-flight cap (ADR-0147).
const NestedCapOp = "workernode.local.nested-cap"

// nestedCapInvoker bounds the nested fn-to-fn calls in flight to one target Function across all callers
// (ADR-0147), so a link cycle stored despite admission stops at the cap instead of recursing unbounded.
type nestedCapInvoker struct {
	inner   Invoker
	max     int
	refused metric.Int64Counter

	mu       sync.Mutex
	inFlight map[Ref]int // entry deleted at 0
}

// NewNestedCapInvoker wraps inner with the per-target nested-call in-flight cap (ADR-0147): a call is counted
// from before the data-plane hand-off until it returns (a cold wake included). With maxPerTarget (≥ 1) calls to
// the target in flight, the next is refused at once without calling inner: fault.ResourceExhausted (429, no
// Retry-After: a slot frees on return, not on a clock), Op NestedCapOp, and the Int64Counter
// "funcd.invoke.nested.refused" +1 with the target's namespace and function.
func NewNestedCapInvoker(inner Invoker, maxPerTarget int, meter metric.Meter) Invoker {
	refused, err := meter.Int64Counter("funcd.invoke.nested.refused",
		metric.WithDescription("Nested fn-to-fn calls refused by the per-target in-flight cap (ADR-0147)."))
	if err != nil {
		refused = metricnoop.Int64Counter{}
	}
	return &nestedCapInvoker{inner: inner, max: maxPerTarget, refused: refused, inFlight: map[Ref]int{}}
}

func (n *nestedCapInvoker) Invoke(ctx context.Context, target Ref, input []byte, timeout time.Duration) ([]byte, error) {
	n.mu.Lock()
	if n.inFlight[target] >= n.max {
		n.mu.Unlock()
		n.refused.Add(ctx, 1, metric.WithAttributes(
			attribute.String("namespace", string(target.Namespace)),
			attribute.String("function", string(target.Function))))
		return nil, fault.ResourceExhaustedf(NestedCapOp,
			"%s has %d nested calls in flight, the cap set by invoke.maxNestedInFlight", target, n.max)
	}
	n.inFlight[target]++
	n.mu.Unlock()
	defer n.done(target)
	return n.inner.Invoke(ctx, target, input, timeout)
}

func (n *nestedCapInvoker) done(target Ref) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.inFlight[target]--; n.inFlight[target] == 0 {
		delete(n.inFlight, target)
	}
}
