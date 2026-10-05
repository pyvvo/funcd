//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// --- atomic admission (ADR-0147 Decision 1), through the SDK on the memory and Badger metastores ---

const admissionRacePairs = 40

func admissionRaceNS(i int) v1.NamespaceName { return v1.NamespaceName(fmt.Sprintf("race-%d", i)) }

// bootAdmissionRace boots an in-memory platform with KVStore and Bucket quotas of 3 on the given metastore and
// returns an SDK client whose token reaches one namespace per pair plus "control".
func bootAdmissionRace(t *testing.T, badger bool) *sdk.Client {
	t.Helper()
	nss := []string{"control"}
	for i := range admissionRacePairs {
		nss = append(nss, string(admissionRaceNS(i)))
	}
	opts := []funcd.Option{funcd.InMemory(), funcd.WithKVStoreQuota(3), funcd.WithBucketQuotaForTest(3),
		funcd.WithDevAuth(funcd.DevToken, nss...)}
	if badger {
		eng, err := bstore.Open(filepath.Join(t.TempDir(), "store"), bstore.WithValueLogGCInterval(0))
		require.NoError(t, err)
		st := store.New(eng)
		t.Cleanup(func() { _ = st.Close() })
		opts = append(opts, funcd.WithStore(st))
	}
	p, err := funcd.New(opts...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	return c
}

func metastores(t *testing.T, run func(t *testing.T, c *sdk.Client)) {
	for _, badger := range []bool{false, true} {
		name := "memory"
		if badger {
			name = "badger"
		}
		t.Run(name, func(t *testing.T) { run(t, bootAdmissionRace(t, badger)) })
	}
}

func raceFunction(ns v1.NamespaceName, name, target string) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "oci://example/app:v1"
	if target != "" {
		fn.Spec.Links = []v1.FunctionLink{{Alias: "peer", Target: v1.ObjectName(target)}}
	}
	return fn
}

// applyRetrying applies fn, retrying the read-RV-then-update conflict a reconciler's concurrent status write causes
// (ADR-0018); every other outcome is the admission's.
func applyRetrying(c *sdk.Client, fn *v1.Function) error {
	for {
		_, err := c.Apply(context.Background(), fn)
		if err == nil || !strings.Contains(err.Error(), "resourceVersion mismatch") {
			return err
		}
	}
}

func quotaObject(kind v1.Kind, ns v1.NamespaceName, name string) v1.Object {
	obj, _ := v1.NewObject(kind)
	m := obj.GetObjectMeta()
	m.Name, m.Namespace, m.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	return obj
}

// racePairsAt runs each pair's two writes at once, all pairs released together on a closed channel.
func racePairsAt(pairs [][2]func() error) [][2]error {
	out := make([][2]error, len(pairs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, p := range pairs {
		for j := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				out[i][j] = p[j]()
			}()
		}
	}
	close(start)
	wg.Wait()
	return out
}

func storedLinks(t *testing.T, c *sdk.Client, ns v1.NamespaceName) map[v1.ObjectName][]v1.ObjectName {
	t.Helper()
	objs, err := c.List(context.Background(), v1.KindFunction, ns)
	require.NoError(t, err)
	out := map[v1.ObjectName][]v1.ObjectName{}
	for _, o := range objs {
		f := o.(*v1.Function)
		out[f.Name] = nil
		for _, l := range f.Spec.Links {
			out[f.Name] = append(out[f.Name], l.Target)
		}
	}
	return out
}

// scenario: link-cycle-race-rejected
func TestScenarioLinkCycleRaceRejected(t *testing.T) {
	metastores(t, func(t *testing.T, c *sdk.Client) {
		ctx := context.Background()
		cycle := func(ns v1.NamespaceName) [2]func() error {
			_, err := c.Apply(ctx, raceFunction(ns, "a", ""))
			require.NoError(t, err)
			_, err = c.Apply(ctx, raceFunction(ns, "b", ""))
			require.NoError(t, err)
			return [2]func() error{
				func() error { return applyRetrying(c, raceFunction(ns, "a", "b")) },
				func() error { return applyRetrying(c, raceFunction(ns, "b", "a")) },
			}
		}
		control := cycle("control")
		require.NoError(t, control[0]())
		err := control[1]()
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, "would create a dependency cycle")

		pairs := make([][2]func() error, admissionRacePairs)
		for i := range pairs {
			pairs[i] = cycle(admissionRaceNS(i))
		}
		for i, errs := range racePairsAt(pairs) {
			failed := 0
			for _, err := range errs {
				if err != nil {
					failed++
					assert.Equal(t, fault.Invalid, fault.KindOf(err), "pair %d", i)
					assert.ErrorContains(t, err, "would create a dependency cycle", "pair %d", i)
				}
			}
			assert.Equal(t, 1, failed, "pair %d: exactly one link write succeeds", i)
			links := storedLinks(t, c, admissionRaceNS(i))
			assert.False(t, len(links["a"]) > 0 && len(links["b"]) > 0, "pair %d stored a cycle", i)
		}
	})
}

// scenario: dangling-link-race-rejected
func TestScenarioDanglingLinkRaceRejected(t *testing.T) {
	metastores(t, func(t *testing.T, c *sdk.Client) {
		ctx := context.Background()
		dangle := func(ns v1.NamespaceName) [2]func() error {
			_, err := c.Apply(ctx, raceFunction(ns, "a", ""))
			require.NoError(t, err)
			_, err = c.Apply(ctx, raceFunction(ns, "b", ""))
			require.NoError(t, err)
			return [2]func() error{
				func() error { return applyRetrying(c, raceFunction(ns, "a", "b")) },
				func() error { return c.Delete(ctx, v1.KindFunction, ns, "b") },
			}
		}
		control := dangle("control")
		require.NoError(t, control[0]())
		require.Equal(t, fault.Conflict, fault.KindOf(control[1]()), "link first: the delete fails")
		require.NoError(t, applyRetrying(c, raceFunction("control", "a", "")))
		require.NoError(t, control[1]())
		err := control[0]()
		require.Equal(t, fault.Invalid, fault.KindOf(err), "delete first: the link fails")
		require.ErrorContains(t, err, "does not exist")

		pairs := make([][2]func() error, admissionRacePairs)
		for i := range pairs {
			pairs[i] = dangle(admissionRaceNS(i))
		}
		for i, errs := range racePairsAt(pairs) {
			link, del := errs[0], errs[1]
			switch {
			case link == nil:
				assert.Equal(t, fault.Conflict, fault.KindOf(del), "pair %d: link stored, so the delete fails", i)
			case del == nil:
				assert.Equal(t, fault.Invalid, fault.KindOf(link), "pair %d: b deleted, so the link fails", i)
				assert.ErrorContains(t, link, "does not exist", "pair %d", i)
			default:
				t.Errorf("pair %d: both failed: %v; %v", i, link, del)
			}
			links := storedLinks(t, c, admissionRaceNS(i))
			for from, targets := range links {
				for _, to := range targets {
					_, ok := links[to]
					assert.True(t, ok, "pair %d: %s links the missing %s", i, from, to)
				}
			}
		}
	})
}

// scenario: quota-race-rejected
func TestScenarioQuotaRaceRejected(t *testing.T) {
	for _, kind := range []v1.Kind{v1.KindKVStore, v1.KindBucket} {
		t.Run(string(kind), func(t *testing.T) {
			metastores(t, func(t *testing.T, c *sdk.Client) {
				ctx := context.Background()
				fill := func(ns v1.NamespaceName) [2]func() error {
					for _, name := range []string{"s1", "s2"} {
						_, err := c.Create(ctx, quotaObject(kind, ns, name))
						require.NoError(t, err)
					}
					return [2]func() error{
						func() error { _, err := c.Create(ctx, quotaObject(kind, ns, "s3")); return err },
						func() error { _, err := c.Create(ctx, quotaObject(kind, ns, "s4")); return err },
					}
				}
				control := fill("control")
				require.NoError(t, control[0]())
				err := control[1]()
				require.Equal(t, fault.Invalid, fault.KindOf(err))
				require.ErrorContains(t, err, "already holds the maximum 3")

				pairs := make([][2]func() error, admissionRacePairs)
				for i := range pairs {
					pairs[i] = fill(admissionRaceNS(i))
				}
				for i, errs := range racePairsAt(pairs) {
					failed := 0
					for _, err := range errs {
						if err != nil {
							failed++
							assert.Equal(t, fault.Invalid, fault.KindOf(err), "pair %d", i)
							assert.ErrorContains(t, err, "already holds the maximum 3", "pair %d", i)
						}
					}
					assert.Equal(t, 1, failed, "pair %d: exactly one create fails", i)
					objs, err := c.List(ctx, kind, admissionRaceNS(i))
					require.NoError(t, err)
					assert.LessOrEqual(t, len(objs), 3, "pair %d", i)
				}
			})
		})
	}
}

// --- nested-call in-flight cap (ADR-0147 Decision 3), on the embedded Node shim ---

// marker returns a file a handler appends one byte to per run, and a count of the runs.
func marker(t *testing.T) (string, func() int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ran")
	return path, func() int {
		b, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
		if os.IsNotExist(err) {
			return 0
		}
		require.NoError(t, err)
		return len(b)
	}
}

// holdSrc waits data.ms (default 2 s) and returns {held: ms}.
func holdSrc(mark string) string {
	return fmt.Sprintf(`import { appendFileSync } from 'node:fs';
export const handle = async (context, event) => {
  appendFileSync(%q, 'x');
  const ms = event.data?.ms ?? 2000;
  await new Promise((r) => setTimeout(r, ms));
  return { held: ms };
};
`, mark)
}

// fanoutSrc calls its h link data.n times at once; a refusal by the nested cap is counted, anything else fails.
const fanoutSrc = `export const handle = async (context, event) => {
  const n = event.data?.n ?? 1;
  const rs = await Promise.allSettled(Array.from({ length: n }, () => context.invoke('h', { data: { ms: 2000 } })));
  let ok = 0, refused = 0, detail = '';
  for (const r of rs) {
    if (r.status === 'fulfilled') { ok++; continue; }
    const m = String(r.reason?.message ?? r.reason);
    if (!m.includes('workernode.local.nested-cap')) throw r.reason;
    refused++;
    detail = m;
  }
  return { ok, refused, detail };
};
`

type fanoutOut struct {
	OK      int    `json:"ok"`
	Refused int    `json:"refused"`
	Detail  string `json:"detail"`
}

func deployFanout(t *testing.T, rig *shimRig) func() int {
	t.Helper()
	mark, ran := marker(t)
	rig.deploy(t, "h", nodeFn(holdSrc(mark)))
	f := nodeFn(fanoutSrc)
	f.links = []v1.FunctionLink{{Alias: "h", Target: "h"}}
	rig.deploy(t, "fanout", f)
	return ran
}

func fanout(t *testing.T, rig *shimRig, n int) fanoutOut {
	t.Helper()
	var r reply
	require.Eventually(t, func() bool {
		r = rig.post("fanout", fmt.Sprintf(`{"data":{"n":%d}}`, n))
		return r.status != 503 && r.status != 404
	}, 30*time.Second, 200*time.Millisecond, "fanout never became ready")
	require.Equal(t, 200, r.status, r.body)
	var out fanoutOut
	require.NoError(t, json.Unmarshal([]byte(r.body), &out), r.body)
	return out
}

// waitAnswering posts body to name until the function answers something other than not-ready.
func waitAnswering(t *testing.T, rig *shimRig, name, body string) {
	t.Helper()
	require.Eventually(t, func() bool {
		r := rig.post(name, body)
		return r.status != 503 && r.status != 404
	}, 30*time.Second, 200*time.Millisecond, "%s never became ready", name)
}

// scenario: inflight-loop-stopped — an A↔B cycle written straight to the store (admission bypassed), cap 10:
// one request into it ends non-2xx within 10 s after at most 20 nested calls, and a second request stops the same.
func TestScenarioInflightLoopStopped(t *testing.T) {
	st := store.New(memory.New())
	rig := newShimRig(t, "", funcd.WithStore(st))
	mark, ran := marker(t)
	src := fmt.Sprintf(`import { appendFileSync } from 'node:fs';
export const handle = async (context) => {
  appendFileSync(%q, 'x');
  await context.invoke('peer', { data: {} });
  return {};
};
`, mark)
	rig.deploy(t, "b", nodeFn(src))
	a := nodeFn(src)
	a.links = []v1.FunctionLink{{Alias: "peer", Target: "b"}}
	rig.deploy(t, "a", a)
	ctx := context.Background()
	require.Eventually(t, func() bool { // the reconciler's status writes race the read-RV-then-update
		obj, err := st.Get(ctx, v1.KindFunction.GVK(), "default", "b")
		require.NoError(t, err)
		b := obj.(*v1.Function)
		b.Spec.Links = []v1.FunctionLink{{Alias: "peer", Target: "a"}}
		_, err = st.Update(ctx, b)
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "write the b → a link straight to the store")

	baseline := 0
	for run := range 2 {
		require.Eventually(t, func() bool {
			_ = os.Remove(mark)
			start := time.Now()
			r := rig.post("a", `{"data":{}}`)
			if r.status == 503 || r.status == 404 || ran() < 2 {
				return false // not ready, or b has not taken its new link yet
			}
			assert.GreaterOrEqual(t, r.status, 300, "run %d: the loop ends non-2xx", run)
			assert.Less(t, time.Since(start), 10*time.Second, "run %d", run)
			assert.LessOrEqual(t, ran()-1, 20, "run %d: at most 20 nested calls reach a handler", run)
			return true
		}, 60*time.Second, 300*time.Millisecond)
		if run == 0 {
			time.Sleep(time.Second)
			baseline = runtime.NumGoroutine()
		}
	}
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= baseline+20 }, 10*time.Second, 100*time.Millisecond,
		"goroutines return to the baseline after the loop is stopped")
}

// scenario: inflight-external-load-through-link — cap 10, single calls h once (h holds 2 s): 10 external requests
// all get through; with 11, single's handler catches one 429 from context.invoke and every caller gets 200.
func TestScenarioInflightExternalLoadThroughLink(t *testing.T) {
	rig := newShimRig(t, "")
	mark, ran := marker(t)
	rig.deploy(t, "h", nodeFn(holdSrc(mark)))
	single := nodeFn(`export const handle = async (context) => {
  try {
    await context.invoke('h', { data: { ms: 2000 } });
    return { refused: 0 };
  } catch (e) {
    if (String(e?.message).includes('429') && String(e?.message).includes('workernode.local.nested-cap')) return { refused: 1 };
    throw e;
  }
};
`)
	single.links = []v1.FunctionLink{{Alias: "h", Target: "h"}}
	rig.deploy(t, "single", single)
	waitAnswering(t, rig, "h", `{"data":{"ms":0}}`)
	waitAnswering(t, rig, "single", `{"data":{}}`)

	for _, n := range []int{10, 11} {
		refused := 0
		for _, r := range rig.postConcurrently("single", `{"data":{}}`, n)() {
			require.Equal(t, 200, r.status, r.body)
			if strings.Contains(r.body, `"refused":1`) {
				refused++
			}
		}
		assert.Equal(t, n-10, refused, "%d external requests", n)
	}
	assert.Positive(t, ran())
}

// scenario: inflight-fanout-at-cap — cap 10, fanout calls h 10 times at once: all succeed.
func TestScenarioInflightFanoutAtCap(t *testing.T) {
	rig := newShimRig(t, "")
	ran := deployFanout(t, rig)
	out := fanout(t, rig, 10)
	assert.Equal(t, fanoutOut{OK: 10}, out)
	assert.Equal(t, 10, ran())
}

// captured is a slog handler that keeps every Warn-or-above message.
type captured struct {
	mu   *sync.Mutex
	msgs *[]string
}

func (c captured) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (c captured) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.msgs = append(*c.msgs, r.Message)
	return nil
}
func (c captured) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c captured) WithGroup(string) slog.Handler      { return c }

func (c captured) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range *c.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// scenario: inflight-fanout-over-cap — 11 at once under the default cap: 10 succeed, 1 is refused 429
// resource-exhausted naming default/h, 10 and invoke.maxNestedInFlight; h ran 10 times; one Warn line; the
// refusal counter +1. (The missing Retry-After is asserted on the local API in TestNestedCapRefusalResponseAndLog:
// a handler sees no response headers.)
func TestScenarioInflightFanoutOverCap(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	tel := observability.NewFromProviders(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), tracenoop.NewTracerProvider())
	logs := captured{mu: &sync.Mutex{}, msgs: &[]string{}}
	rig := newShimRig(t, "", funcd.WithTelemetry(tel), funcd.WithLogger(slog.New(logs)))
	ran := deployFanout(t, rig)

	out := fanout(t, rig, 11)
	assert.Equal(t, 10, out.OK)
	assert.Equal(t, 1, out.Refused)
	assert.Contains(t, out.Detail, "429")
	assert.Contains(t, out.Detail, "urn:funcd:problem:resource-exhausted")
	assert.Contains(t, out.Detail, "workernode.local.nested-cap: default/h has 10 nested calls in flight, the cap set by invoke.maxNestedInFlight")
	assert.Equal(t, 10, ran(), "h ran 10 times")
	assert.Equal(t, 1, logs.count("fn-to-fn invoke refused: nested in-flight cap"))
	assert.Zero(t, logs.count("fn-to-fn invoke failed"))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var points []metricdata.DataPoint[int64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "funcd.invoke.nested.refused" {
				points = append(points, sum.DataPoints...)
			}
		}
	}
	require.Len(t, points, 1)
	assert.Equal(t, int64(1), points[0].Value)
	assert.Equal(t, attribute.NewSet(attribute.String("namespace", "default"), attribute.String("function", "h")), points[0].Attributes)
}

// scenario: inflight-cap-raised — with the cap at 20, 11 at once all succeed.
func TestScenarioInflightCapRaised(t *testing.T) {
	rig := newShimRig(t, "", funcd.WithNestedInFlightCap(20))
	ran := deployFanout(t, rig)
	assert.Equal(t, fanoutOut{OK: 11}, fanout(t, rig, 11))
	assert.Equal(t, 11, ran())
}

// scenario: inflight-external-not-counted — cap 2, fanout holds 2 nested calls to h while 5 external requests
// reach h on the data-plane listener: all 7 succeed.
func TestScenarioInflightExternalNotCounted(t *testing.T) {
	rig := newShimRig(t, "", funcd.WithNestedInFlightCap(2))
	ran := deployFanout(t, rig)
	waitAnswering(t, rig, "h", `{"data":{"ms":0}}`)
	waitAnswering(t, rig, "fanout", `{"data":{"n":0}}`)
	before := ran()

	nested := make(chan reply, 1)
	go func() { nested <- rig.post("fanout", `{"data":{"n":2}}`) }()
	require.Eventually(t, func() bool { return ran() >= before+2 }, 10*time.Second, 20*time.Millisecond, "the 2 nested calls are held")
	for _, r := range rig.postConcurrently("h", `{"data":{"ms":500}}`, 5)() {
		assert.Equal(t, 200, r.status, r.body)
	}
	r := <-nested
	require.Equal(t, 200, r.status, r.body)
	var out fanoutOut
	require.NoError(t, json.Unmarshal([]byte(r.body), &out))
	assert.Equal(t, fanoutOut{OK: 2}, out)
}
