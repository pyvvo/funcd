package activator_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// --- real (non-mock) fakes for the two seams + a controllable clock ---

// fakeEndpoints is an Endpoints whose readiness can flip at runtime.
type fakeEndpoints struct {
	mu       sync.Mutex
	upstream string
	ready    bool
	err      error
}

func (f *fakeEndpoints) Upstream(_ context.Context, _ activator.FunctionRef) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upstream, f.ready, f.err
}

func (f *fakeEndpoints) setReady(upstream string) {
	f.mu.Lock()
	f.upstream, f.ready = upstream, true
	f.mu.Unlock()
}

// fakeScaler counts ScaleTo calls, records targets, and can run a hook on each call.
type fakeScaler struct {
	mu      sync.Mutex
	calls   int
	targets []int
	hook    func(fn activator.FunctionRef, replicas int)
}

func (f *fakeScaler) ScaleTo(_ context.Context, fn activator.FunctionRef, replicas int) error {
	f.mu.Lock()
	f.calls++
	f.targets = append(f.targets, replicas)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(fn, replicas)
	}
	return nil
}

func (f *fakeScaler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeScaler) targetList() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.targets))
	copy(out, f.targets)
	return out
}

// stepClock is a hand-written advancing clock.Clock (no mock framework).
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var _ clock.Clock = (*stepClock)(nil)

func newActivator(t *testing.T, d activator.Deps) *activator.Activator {
	t.Helper()
	a, err := activator.New(d)
	require.NoError(t, err)
	return a
}

func echoUpstream(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
}

func serve(a *activator.Activator, fn activator.FunctionRef) *httptest.ResponseRecorder {
	req := activator.WithFunction(httptest.NewRequest(http.MethodGet, "/", nil), fn)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec
}

// scenario: warm-passthrough — a ready function is proxied immediately, no ScaleTo.
func TestScenarioWarmPassthrough(t *testing.T) {
	t.Parallel()
	backend := echoUpstream("warm-body")
	defer backend.Close()

	ep := &fakeEndpoints{upstream: backend.URL, ready: true}
	sc := &fakeScaler{}
	a := newActivator(t, activator.Deps{Endpoints: ep, Scaler: sc})

	rec := serve(a, activator.FunctionRef{Namespace: "default", Name: "warm"})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "warm-body", rec.Body.String())
	require.Equal(t, 0, sc.count(), "warm path must not call ScaleTo")
}

// An upstream with a path (a pool worker's /function/<name>, ADR-0046) serves the function's root at that path itself,
// and a sub-path below it.
func TestForwardUnderUpstreamPath(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	t.Cleanup(backend.Close)
	a := newActivator(t, activator.Deps{Endpoints: &fakeEndpoints{upstream: backend.URL + "/function/svc", ready: true}, Scaler: &fakeScaler{}})
	fn := activator.FunctionRef{Namespace: "default", Name: "svc"}

	for path, want := range map[string]string{"/": "/function/svc", "/a/b": "/function/svc/a/b"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, activator.WithFunction(httptest.NewRequest(http.MethodPost, path, nil), fn))
		require.Equal(t, want, rec.Body.String(), "request path %s", path)
	}
}

// ADR-0143: with Calls set, a proxied call counts against its upstream until its answer ends.
func TestProxiedCallIsCountedWhileInFlight(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-gate
		_, _ = io.WriteString(w, "done")
	}))
	t.Cleanup(backend.Close)
	t.Cleanup(release)

	calls := activator.NewCallTracker(nil)
	a := newActivator(t, activator.Deps{Endpoints: &fakeEndpoints{upstream: backend.URL, ready: true}, Scaler: &fakeScaler{}, Calls: calls})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(a, activator.FunctionRef{Namespace: "default", Name: "slow"}) }()
	require.Eventually(t, func() bool { return !calls.Idle(backend.URL, 0) }, 2*time.Second, time.Millisecond, "the call counts in flight")

	release()
	require.Equal(t, "done", (<-done).Body.String())
	require.True(t, calls.Idle(backend.URL, 0), "the call ends with its answer")
}

// scenario: cold-start-buffer-and-forward — a cold request triggers ScaleTo(1) once,
// is held until a ready upstream appears, then forwarded.
func TestScenarioColdStartBufferAndForward(t *testing.T) {
	t.Parallel()
	backend := echoUpstream("cold-then-warm")
	defer backend.Close()

	ep := &fakeEndpoints{} // starts not-ready
	sc := &fakeScaler{}
	// On the wake, make the upstream ready a moment later (proves the request is buffered).
	sc.hook = func(_ activator.FunctionRef, replicas int) {
		if replicas == 1 {
			go func() {
				time.Sleep(15 * time.Millisecond)
				ep.setReady(backend.URL)
			}()
		}
	}
	a := newActivator(t, activator.Deps{
		Endpoints:         ep,
		Scaler:            sc,
		ActivationTimeout: 2 * time.Second,
		PollInterval:      5 * time.Millisecond,
	})

	rec := serve(a, activator.FunctionRef{Namespace: "default", Name: "cold"})

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "cold-then-warm", rec.Body.String())
	require.Equal(t, 1, sc.count(), "exactly one ScaleTo for the cold start")
	require.Equal(t, []int{1}, sc.targetList(), "scaled up to 1")
}

// scenario: concurrent-activation-singleflight — N concurrent cold requests trigger
// exactly one ScaleTo(1) and all are forwarded once ready.
func TestScenarioConcurrentActivationSingleflight(t *testing.T) {
	t.Parallel()
	backend := echoUpstream("served")
	defer backend.Close()

	ep := &fakeEndpoints{}
	sc := &fakeScaler{}
	sc.hook = func(_ activator.FunctionRef, replicas int) {
		if replicas == 1 {
			go func() {
				time.Sleep(30 * time.Millisecond)
				ep.setReady(backend.URL)
			}()
		}
	}
	a := newActivator(t, activator.Deps{
		Endpoints:         ep,
		Scaler:            sc,
		ActivationTimeout: 2 * time.Second,
		PollInterval:      5 * time.Millisecond,
	})

	const n = 10
	fn := activator.FunctionRef{Namespace: "default", Name: "shared"}
	var wg sync.WaitGroup
	codes := make([]int, n)
	bodies := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := serve(a, fn)
			codes[i] = rec.Code
			bodies[i] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	require.Equal(t, 1, sc.count(), "N concurrent cold requests must single-flight to one ScaleTo")
	for i := range n {
		require.Equal(t, http.StatusOK, codes[i], "waiter %d forwarded", i)
		require.Equal(t, "served", bodies[i], "waiter %d got the upstream body", i)
	}
}

// scenario: activation-timeout — a never-ready function 503s after the timeout and the
// pending activation is cleared (a later request starts a fresh activation).
func TestScenarioActivationTimeout(t *testing.T) {
	t.Parallel()
	ep := &fakeEndpoints{} // never ready
	sc := &fakeScaler{}
	a := newActivator(t, activator.Deps{
		Endpoints:         ep,
		Scaler:            sc,
		ActivationTimeout: 60 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
	})

	fn := activator.FunctionRef{Namespace: "default", Name: "never"}

	rec := serve(a, fn)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "urn:funcd:problem:unavailable")
	require.Equal(t, 1, sc.count(), "one wake attempted")

	// Pending cleared: a second request starts a fresh activation (another ScaleTo).
	rec2 := serve(a, fn)
	require.Equal(t, http.StatusServiceUnavailable, rec2.Code)
	require.Equal(t, 2, sc.count(), "activation was cleared — second request re-activates")
}

// scenario: idle-reclaim — a minReplicas==0 function idle past IdleTimeout is scaled to
// zero; a recently-active one, or a minReplicas>=1 one, is not.
func TestScenarioIdleReclaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	const idle = time.Hour

	t.Run("stale-is-reclaimed", func(t *testing.T) {
		t.Parallel()
		st := store.New(memory.New())
		createFunction(t, st, "stale", v1.Scaling{MinReplicas: 0, IdleTimeout: idle})
		clk := &stepClock{t: base}
		sc := &fakeScaler{}
		a := newActivator(t, activator.Deps{Store: st, Endpoints: &fakeEndpoints{}, Scaler: sc, Clock: clk})

		require.NoError(t, a.ReclaimIdle(ctx))
		require.Equal(t, 0, sc.count(), "first pass seeds a full grace window, no reclaim")

		clk.advance(2 * idle)
		require.NoError(t, a.ReclaimIdle(ctx))
		require.Equal(t, 1, sc.count(), "idle past IdleTimeout → reclaimed")
		require.Equal(t, []int{0}, sc.targetList(), "scaled to zero")
	})

	t.Run("recently-active-is-kept", func(t *testing.T) {
		t.Parallel()
		backend := echoUpstream("ok")
		defer backend.Close()
		st := store.New(memory.New())
		createFunction(t, st, "active", v1.Scaling{MinReplicas: 0, IdleTimeout: idle})
		clk := &stepClock{t: base}
		sc := &fakeScaler{}
		ep := &fakeEndpoints{upstream: backend.URL, ready: true}
		a := newActivator(t, activator.Deps{Store: st, Endpoints: ep, Scaler: sc, Clock: clk})

		// A served request records activity at `base`; reclaim at the same instant keeps it.
		rec := serve(a, activator.FunctionRef{Namespace: "default", Name: "active"})
		require.Equal(t, http.StatusOK, rec.Code)
		require.NoError(t, a.ReclaimIdle(ctx))
		require.Equal(t, 0, sc.count(), "recent activity → not reclaimed")
	})

	t.Run("min-replicas-pinned-is-kept", func(t *testing.T) {
		t.Parallel()
		st := store.New(memory.New())
		createFunction(t, st, "pinned", v1.Scaling{MinReplicas: 1, IdleTimeout: time.Millisecond})
		clk := &stepClock{t: base}
		sc := &fakeScaler{}
		a := newActivator(t, activator.Deps{Store: st, Endpoints: &fakeEndpoints{}, Scaler: sc, Clock: clk})

		require.NoError(t, a.ReclaimIdle(ctx))
		clk.advance(time.Hour)
		require.NoError(t, a.ReclaimIdle(ctx))
		require.Equal(t, 0, sc.count(), "minReplicas>=1 is never scaled to zero")
	})
}

// createFunction stores a minimal Function with the given scaling spec.
func createFunction(t *testing.T, st store.Store, name string, scaling v1.Scaling) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "default"
	fn.ResourceGroup = "rg1"
	fn.Spec.Scaling = scaling
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

// scenario: wake-warm-returns-upstream (ADR-0033) — Wake on a ready function returns its
// upstream with no ScaleTo.
func TestScenarioWakeWarmReturnsUpstream(t *testing.T) {
	t.Parallel()
	ep := &fakeEndpoints{upstream: "http://10.0.0.5:8080", ready: true}
	sc := &fakeScaler{}
	a := newActivator(t, activator.Deps{Endpoints: ep, Scaler: sc})

	up, err := a.Wake(context.Background(), activator.FunctionRef{Namespace: "default", Name: "echo"})
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.5:8080", up)
	require.Zero(t, sc.count(), "a warm function is not scaled")
}

// scenario: wake-cold-scales-and-returns (ADR-0033) — Wake on a cold function single-flights
// ScaleTo(1), waits for readiness, and returns the now-ready upstream.
func TestScenarioWakeColdScalesAndReturns(t *testing.T) {
	t.Parallel()
	ep := &fakeEndpoints{ready: false}
	sc := &fakeScaler{hook: func(_ activator.FunctionRef, replicas int) {
		if replicas == 1 {
			ep.setReady("http://10.0.0.9:8080") // the reconciler would provision; the fake flips ready
		}
	}}
	a := newActivator(t, activator.Deps{Endpoints: ep, Scaler: sc, PollInterval: time.Millisecond})

	up, err := a.Wake(context.Background(), activator.FunctionRef{Namespace: "default", Name: "cold"})
	require.NoError(t, err)
	require.Equal(t, "http://10.0.0.9:8080", up)
	require.Equal(t, 1, sc.count(), "cold wake scales to 1 exactly once")
	require.Equal(t, []int{1}, sc.targetList())
}
