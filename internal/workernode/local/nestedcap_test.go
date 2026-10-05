package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

func ref(fn v1.ObjectName) local.Ref { return local.Ref{Namespace: "default", Function: fn} }

// loopInvoker stands in for a stored A↔B link cycle: each call into one Function makes a nested call to the
// other through the capped Invoker, so only the cap ends the recursion.
type loopInvoker struct {
	capped  local.Invoker
	reached atomic.Int64
}

func (l *loopInvoker) Invoke(ctx context.Context, target local.Ref, input []byte, timeout time.Duration) ([]byte, error) {
	l.reached.Add(1)
	next := ref("A")
	if target == ref("A") {
		next = ref("B")
	}
	return l.capped.Invoke(ctx, next, input, timeout)
}

// blockingInvoker holds every call until release is closed.
type blockingInvoker struct {
	started chan struct{}
	release chan struct{}
	ran     atomic.Int64
}

func newBlocking() *blockingInvoker {
	return &blockingInvoker{started: make(chan struct{}, 64), release: make(chan struct{})}
}

func (b *blockingInvoker) Invoke(context.Context, local.Ref, []byte, time.Duration) ([]byte, error) {
	b.ran.Add(1)
	b.started <- struct{}{}
	<-b.release
	return []byte(`{}`), nil
}

// fanOut makes n concurrent calls to target, waits until want of them are held in inner and the other n-want
// have returned, then releases the held ones and returns every call's error.
func fanOut(t *testing.T, inv local.Invoker, inner *blockingInvoker, n, want int) []error {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	var returned atomic.Int64
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = inv.Invoke(context.Background(), ref("H"), nil, time.Second)
			returned.Add(1)
		}()
	}
	for range want {
		select {
		case <-inner.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the held calls did not start")
		}
	}
	require.Eventually(t, func() bool { return returned.Load() == int64(n-want) }, 2*time.Second, 5*time.Millisecond)
	close(inner.release)
	wg.Wait()
	return errs
}

func refusals(errs []error) []error {
	var out []error
	for _, err := range errs {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}

// scenario: inflight-loop-stopped — a call into a stored A↔B cycle stops after 20 nested calls (cap 10 per
// target), and a second call stops the same: every count returned to 0.
func TestScenarioInflightLoopStopped(t *testing.T) {
	loop := &loopInvoker{}
	loop.capped = local.NewNestedCapInvoker(loop, 10, metricnoop.Meter{})
	for run := range 2 {
		loop.reached.Store(0)
		_, err := loop.capped.Invoke(context.Background(), ref("B"), nil, time.Second)
		require.Error(t, err, "run %d", run)
		assert.Equal(t, fault.ResourceExhausted, fault.KindOf(err), "run %d", run)
		assert.Equal(t, int64(20), loop.reached.Load(), "run %d: exactly 20 nested calls admitted", run)
	}
}

// scenario: inflight-fanout-at-cap — 10 concurrent nested calls to one target under cap 10 all succeed.
func TestScenarioInflightFanoutAtCap(t *testing.T) {
	inner := newBlocking()
	errs := fanOut(t, local.NewNestedCapInvoker(inner, 10, metricnoop.Meter{}), inner, 10, 10)
	assert.Empty(t, refusals(errs))
	assert.Equal(t, int64(10), inner.ran.Load())
}

// scenario: inflight-fanout-over-cap — the 11th concurrent call is refused without reaching the target:
// ResourceExhausted, Op workernode.local.nested-cap, the ADR's message, and the refusal counter +1.
func TestScenarioInflightFanoutOverCap(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("funcd.invoke")
	inner := newBlocking()
	errs := fanOut(t, local.NewNestedCapInvoker(inner, 10, meter), inner, 11, 10)

	refused := refusals(errs)
	require.Len(t, refused, 1)
	var fe *fault.Error
	require.ErrorAs(t, refused[0], &fe)
	assert.Equal(t, fault.ResourceExhausted, fe.Kind)
	assert.Equal(t, local.NestedCapOp, fe.Op)
	assert.Equal(t, "workernode.local.nested-cap: default/H has 10 nested calls in flight, the cap set by invoke.maxNestedInFlight", fe.Error())
	assert.Equal(t, int64(10), inner.ran.Load(), "H ran 10 times")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	sum := refusedCounter(t, rm)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(1), sum.DataPoints[0].Value)
	assert.Equal(t, attribute.NewSet(attribute.String("namespace", "default"), attribute.String("function", "H")), sum.DataPoints[0].Attributes)
}

func refusedCounter(t *testing.T, rm metricdata.ResourceMetrics) metricdata.Sum[int64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "funcd.invoke.nested.refused" {
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				return sum
			}
		}
	}
	t.Fatal("no funcd.invoke.nested.refused metric")
	return metricdata.Sum[int64]{}
}

// scenario: inflight-cap-raised — under cap 20, 11 concurrent calls all succeed.
func TestScenarioInflightCapRaised(t *testing.T) {
	inner := newBlocking()
	errs := fanOut(t, local.NewNestedCapInvoker(inner, 20, metricnoop.Meter{}), inner, 11, 11)
	assert.Empty(t, refusals(errs))
	assert.Equal(t, int64(11), inner.ran.Load())
}

// The cap counts per target: a target at its cap leaves another target's calls admitted, and a slot frees on
// return, so the count goes back to 0.
func TestNestedCapPerTargetAndReleased(t *testing.T) {
	inner := newBlocking()
	inv := local.NewNestedCapInvoker(inner, 1, metricnoop.Meter{})
	done := make(chan error, 1)
	go func() { _, err := inv.Invoke(context.Background(), ref("H"), nil, time.Second); done <- err }()
	<-inner.started
	_, err := inv.Invoke(context.Background(), ref("H"), nil, time.Second)
	require.Equal(t, fault.ResourceExhausted, fault.KindOf(err), "H is at its cap")
	go func() { _, _ = inv.Invoke(context.Background(), ref("A"), nil, time.Second) }()
	<-inner.started // A is admitted while H is at its cap
	close(inner.release)
	require.NoError(t, <-done)
	_, err = inv.Invoke(context.Background(), ref("H"), nil, time.Second)
	require.NoError(t, err, "H's slot freed on return")
}

// A refusal is answered 429 resource-exhausted with no Retry-After, logged once at Warn as the refusal line
// and never as "fn-to-fn invoke failed".
func TestNestedCapRefusalResponseAndLog(t *testing.T) {
	inner := newBlocking()
	inv := local.NewNestedCapInvoker(inner, 1, metricnoop.Meter{})
	go func() { _, _ = inv.Invoke(context.Background(), ref("H"), nil, time.Second) }()
	<-inner.started
	defer close(inner.release)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := local.NewHandler(ref("A"), &fakeResolver{target: ref("H"), timeout: time.Second}, inv, nil, nil, nil, logger)
	rec := post(t, h, "h", `{}`)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"))
	var problem struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	assert.Equal(t, "urn:funcd:problem:resource-exhausted", problem.Type)
	assert.Contains(t, problem.Detail, "workernode.local.nested-cap: default/H has 1 nested calls in flight")
	assert.Equal(t, 1, strings.Count(logs.String(), "fn-to-fn invoke refused: nested in-flight cap"), logs.String())
	assert.NotContains(t, logs.String(), "fn-to-fn invoke failed")
	assert.Contains(t, logs.String(), "level=WARN")
}
