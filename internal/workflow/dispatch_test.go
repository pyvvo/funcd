package workflow

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type fakeEndpoints struct {
	upstream string
	ready    bool
}

func (f fakeEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return f.upstream, f.ready, nil
}

type fakeWaker struct {
	upstream string
	called   bool
}

func (f *fakeWaker) Wake(context.Context, activator.FunctionRef) (string, error) {
	f.called = true
	return f.upstream, nil
}

type fakeGrant struct{ allow bool }

// manualClock is an advancing clock.Clock for driving the activator's idle reclaim.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// zeroScaler records the functions the activator scales to zero.
type zeroScaler struct {
	mu   sync.Mutex
	zero []activator.FunctionRef
}

func (z *zeroScaler) ScaleTo(_ context.Context, fn activator.FunctionRef, replicas int) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if replicas == 0 {
		z.zero = append(z.zero, fn)
	}
	return nil
}

func (z *zeroScaler) reclaimed() []activator.FunctionRef {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]activator.FunctionRef(nil), z.zero...)
}

func (f fakeGrant) Allow(v1.NamespaceName, v1.ObjectName) bool { return f.allow }

func echoServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(attemptHeader) == "" {
			t.Error("missing X-Funcd-Attempt header")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dispatchReq(target string) DispatchRequest {
	return DispatchRequest{Namespace: "default", Run: "r1", Step: "s", Target: v1.ObjectName(target), Attempt: 1, Input: json.RawMessage(`{}`)}
}

// scenario: scale-to-zero-step-wakes — a not-ready function is woken, then invoked.
func TestDispatchWakesColdStep(t *testing.T) {
	srv := echoServer(t, 200, `{"ok":true}`)
	waker := &fakeWaker{upstream: srv.URL}
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{ready: false}, Waker: waker, Grant: fakeGrant{allow: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), dispatchReq("s-fn"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !waker.called {
		t.Fatal("cold function should have been woken")
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("output = %s", out)
	}
}

// scenario: undeclared-target-impossible — an ungranted target is Forbidden, no call.
func TestDispatchFailClosed(t *testing.T) {
	waker := &fakeWaker{upstream: "http://never"}
	d, _ := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: "http://never", ready: true}, Waker: waker, Grant: fakeGrant{allow: false},
	})
	_, err := d.Dispatch(context.Background(), dispatchReq("evil"))
	if fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("want Forbidden, got %v", err)
	}
	if waker.called {
		t.Fatal("must not touch the function when denied")
	}
}

// 4xx ⇒ permanent (not retried); 5xx ⇒ retryable.
func TestDispatchStatusClassification(t *testing.T) {
	t.Run("4xx-permanent", func(t *testing.T) {
		srv := echoServer(t, 422, "bad input")
		d, _ := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true}})
		_, err := d.Dispatch(context.Background(), dispatchReq("s"))
		if !isPermanent(err) {
			t.Fatalf("4xx should be permanent, got %v", err)
		}
	})
	t.Run("5xx-retryable", func(t *testing.T) {
		srv := echoServer(t, 503, "overloaded")
		d, _ := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true}})
		_, err := d.Dispatch(context.Background(), dispatchReq("s"))
		if err == nil || isPermanent(err) {
			t.Fatalf("5xx should be a retryable (non-permanent) error, got %v", err)
		}
	})
}

// ADR-0143: a step call made through a CallTracker's transport is counted until its answer has been read.
func TestDispatchIsCountedWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	calls := activator.NewCallTracker(nil)
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true},
		Client: &http.Client{Transport: calls.Wrap(nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, derr := d.Dispatch(context.Background(), dispatchReq("s-fn"))
		done <- derr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for calls.Idle(srv.URL, 0) {
		if time.Now().After(deadline) {
			t.Fatal("the step call is never counted")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !calls.Idle(srv.URL, 0) {
		t.Fatal("the step call still counts after its answer was read")
	}
}

// streamedBody is a response body of size bytes that counts how many of them were read.
type streamedBody struct{ left, read int64 }

func (b *streamedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.left)
	clear(p[:n])
	b.left -= n
	b.read += n
	return int(n), nil
}

func (b *streamedBody) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestIssue126_DispatchReadsOnlyWhatTheStepNeeds: the payload limit bounds the daemon's memory, not
// only the stored output — the dispatcher reads an answer up to one byte past the limit, only the
// head of a 4xx that the error keeps, and none of a 5xx it throws away; past that it discards at
// most httpx.DrainLimit before close.
func TestIssue126_DispatchReadsOnlyWhatTheStepNeeds(t *testing.T) {
	const limit = 1 << 20
	answering := func(t *testing.T, status int) (*HTTPDispatcher, *streamedBody) {
		t.Helper()
		body := &streamedBody{left: 8 << 20}
		d, err := NewHTTPDispatcher(DispatchDeps{
			Endpoints: fakeEndpoints{upstream: "http://step.invalid", ready: true}, Grant: fakeGrant{allow: true},
			Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: body, Request: r}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		return d, body
	}

	t.Run("an over-limit output", func(t *testing.T) {
		d, body := answering(t, http.StatusOK)
		e := newTestEngine(t, d, Config{PayloadLimit: limit})
		rec, err := e.Execute(context.Background(), "default", "run-big", "wf", spec(step("a", "")), json.RawMessage(`{}`), StartOptions{})
		if err == nil || rec.Phase != runFailed {
			t.Fatalf("an over-limit output must fail the run: phase=%s err=%v", rec.Phase, err)
		}
		if body.read > limit+1+httpx.DrainLimit {
			t.Fatalf("read %d bytes of the answer under a %d-byte payload limit", body.read, limit)
		}
	})
	t.Run("a 4xx", func(t *testing.T) {
		d, body := answering(t, http.StatusUnprocessableEntity)
		req := dispatchReq("s")
		req.MaxOutput = limit
		if _, err := d.Dispatch(context.Background(), req); !isPermanent(err) {
			t.Fatalf("4xx should be permanent, got %v", err)
		}
		if body.read > errBodyMax+1+httpx.DrainLimit {
			t.Fatalf("read %d bytes of a rejection whose error keeps %d", body.read, errBodyMax)
		}
	})
	t.Run("a 5xx", func(t *testing.T) {
		d, body := answering(t, http.StatusInternalServerError)
		req := dispatchReq("s")
		req.MaxOutput = limit
		if _, err := d.Dispatch(context.Background(), req); err == nil || isPermanent(err) {
			t.Fatalf("5xx should be retryable, got %v", err)
		}
		if body.read > httpx.DrainLimit {
			t.Fatalf("read %d bytes of a 5xx answer that is thrown away", body.read)
		}
	})
}

// scenario: retried-step-reuses-connection — an answer the dispatcher does not keep in full (a retryable 5xx, the
// tail of an over-limit output or of a rejection) is drained before close, so the next dispatch — a retry of a
// failing step — reuses the keep-alive connection instead of dialing a new one (ADR-0041, ADR-0155).
func TestScenarioRetriedStepReusesConnection(t *testing.T) {
	const dispatches, maxOutput = 5, 64
	answer := strings.Repeat("x", 1<<10)
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"a 503 with a body", http.StatusServiceUnavailable},
		{"a 2xx over MaxOutput", http.StatusOK},
		{"a 4xx over the error head", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int64
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, answer)
			}))
			srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					dials.Add(1)
				}
			}
			srv.Start()
			t.Cleanup(srv.Close)
			d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true}})
			if err != nil {
				t.Fatal(err)
			}
			req := dispatchReq("s")
			req.MaxOutput = maxOutput
			for range dispatches {
				_, _ = d.Dispatch(context.Background(), req)
			}
			if n := dials.Load(); n != 1 {
				t.Fatalf("%d dispatches dialed %d connections, want 1", dispatches, n)
			}
		})
	}
}

// Issue #48: a step dispatched to a warm function counts as its activity, so idle reclaim never fires
// while steps keep arriving.
func TestIssue48_WarmDispatchCountsAsActivity(t *testing.T) {
	ctx := context.Background()
	srv := echoServer(t, 200, `{"ok":true}`)
	st := store.New(memory.New())
	obj, ok := v1.NewObject(v1.KindFunction)
	if !ok {
		t.Fatal("no Function kind")
	}
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "busy", "default", "rg1"
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 0, IdleTimeout: time.Minute}
	if _, err := st.Create(ctx, fn); err != nil {
		t.Fatal(err)
	}
	ep := fakeEndpoints{upstream: srv.URL, ready: true}
	clk := &manualClock{t: time.Unix(1000, 0)}
	sc := &zeroScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: ep, Scaler: sc, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewHTTPDispatcher(DispatchDeps{Endpoints: ep, Waker: act, Grant: fakeGrant{allow: true}})
	if err != nil {
		t.Fatal(err)
	}

	if err := act.ReclaimIdle(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		clk.advance(40 * time.Second)
		if _, err := d.Dispatch(ctx, dispatchReq("busy")); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if err := act.ReclaimIdle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(sc.reclaimed()) != 0 {
		t.Fatalf("a function dispatched every 40s with a 1m idle timeout was scaled to %v", sc.reclaimed())
	}
}

// burstWorker is a step function that holds each wave of calls until all wide of them have arrived, so every call of
// a wave is in flight at once, and counts the connections it accepts. Without the barrier a call that ends while
// another's dial is pending hands that call its connection, and the dialed one stays idle.
func burstWorker(t *testing.T, wide int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	conns := new(atomic.Int32)
	var mu sync.Mutex
	arrived, release := 0, make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		wave := release
		if arrived++; arrived == wide {
			close(release)
			arrived, release = 0, make(chan struct{})
		}
		mu.Unlock()
		select {
		case <-wave:
		case <-time.After(10 * time.Second):
			t.Errorf("a wave never reached %d calls in flight", wide)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, conns
}

// scenario: workflow-burst-reuses-connections — 15 waves of 32 steps dispatched at once to one worker, through the
// client pkg/funcd builds, open 32 connections, all in the first wave (ADR-0155).
func TestScenarioWorkflowBurstReusesConnections(t *testing.T) {
	const waves, wide = 15, 32
	srv, conns := burstWorker(t, wide)
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true},
		Client: &http.Client{Transport: activator.DeadlineTransport(activator.NewCallTracker(nil).Wrap(nil))},
	})
	if err != nil {
		t.Fatal(err)
	}
	for wave := range waves {
		var wg sync.WaitGroup
		for range wide {
			wg.Go(func() {
				if _, err := d.Dispatch(context.Background(), dispatchReq("s")); err != nil {
					t.Errorf("dispatch: %v", err)
				}
			})
		}
		wg.Wait()
		if n := conns.Load(); n != wide {
			t.Fatalf("after wave %d the worker accepted %d connections, want %d", wave+1, n, wide)
		}
	}
}
