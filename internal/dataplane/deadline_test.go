package dataplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// workerAfter is a worker that answers body after delay, or never when delay < 0 (until the call is cut). It
// records the X-Funcd-Timeout-Ms of the last call.
func workerAfter(t *testing.T, delay time.Duration, body string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var header atomic.Value
	header.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header.Store(r.Header.Get(activator.TimeoutHeader))
		_, _ = io.Copy(io.Discard, r.Body) // the server notices a cut call only once the body is read
		if delay < 0 {
			<-r.Context().Done()
			return
		}
		select {
		case <-time.After(delay):
			_, _ = io.WriteString(w, body)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &header
}

func seedTimed(t *testing.T, st store.Store, name string, timeout time.Duration) {
	t.Helper()
	fn := &v1.Function{}
	fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	fn.Spec.Timeout = timeout
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func deadlineDataPlane(t *testing.T, ep activator.Endpoints, sc activator.Scaler, rtr router.Router, d time.Duration) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: ep, Scaler: sc})
	require.NoError(t, err)
	return dataplane.Handler(st, act, rtr, nil, nil, nil, d, nil), st
}

func timedCall(h http.Handler, target string) (*httptest.ResponseRecorder, time.Duration) {
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`)))
	return rec, time.Since(start)
}

func requireDeadline504(t *testing.T, rec *httptest.ResponseRecorder, elapsed, limit time.Duration, name, source string) {
	t.Helper()
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, "urn:funcd:problem:deadline-exceeded", p.Type)
	require.Equal(t, "Gateway Timeout", p.Title)
	require.Equal(t, "activator.response-deadline: default/"+name+" did not start its response within "+limit.String()+" ("+source+")", p.Detail)
	require.GreaterOrEqual(t, elapsed, limit, "not before the limit")
	require.Less(t, elapsed, limit+time.Second)
}

// scenario: hung-handler-cut-at-default — no spec.timeout and a never-settling handler, invoked by name or by
// Route ⇒ 504 after D, naming the Function, D and invoke.defaultTimeout.
func TestScenarioHungHandlerCutAtDefault(t *testing.T) {
	t.Parallel()
	up, header := workerAfter(t, -1, "")
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default", Auth: v1.AuthOpen, Host: "app.example.com",
		Rules: []router.CompiledRule{{Path: "/agent", Function: "agent"}},
	}}))
	h, st := deadlineDataPlane(t, fakeEndpoints{upstream: up.URL}, noScaler{}, rtr, time.Second)
	seedTimed(t, st, "agent", 0)

	for _, target := range []string{"/function/agent", "http://app.example.com/agent"} {
		rec, elapsed := timedCall(h, target)
		requireDeadline504(t, rec, elapsed, time.Second, "agent", "invoke.defaultTimeout")
		ms, err := strconv.Atoi(header.Load().(string))
		require.NoError(t, err)
		require.True(t, ms > 0 && ms <= 1000, "the worker is told the time left: %d ms", ms)
	}
}

// scenario: spec-timeout-raises-limit — D = 1 s, spec.timeout 3 s, answer after 2 s ⇒ 200; without ⇒ 504 after 1 s.
func TestScenarioSpecTimeoutRaisesLimit(t *testing.T) {
	t.Parallel()
	up, _ := workerAfter(t, 2*time.Second, "output")
	h, st := deadlineDataPlane(t, fakeEndpoints{upstream: up.URL}, noScaler{}, nil, time.Second)
	seedTimed(t, st, "slow", 3*time.Second)
	seedTimed(t, st, "plain", 0)

	var slow, plain *httptest.ResponseRecorder
	var plainElapsed time.Duration
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); slow, _ = timedCall(h, "/function/slow") }()
	go func() { defer wg.Done(); plain, plainElapsed = timedCall(h, "/function/plain") }()
	wg.Wait()
	require.Equal(t, http.StatusOK, slow.Code, slow.Body.String())
	require.Equal(t, "output", slow.Body.String())
	requireDeadline504(t, plain, plainElapsed, time.Second, "plain", "invoke.defaultTimeout")
}

// wakeEndpoints is cold until ScaleTo(1), then ready readyAfter later; warm when woken in the past.
type wakeEndpoints struct {
	mu         sync.Mutex
	woke       time.Time
	readyAfter time.Duration
	upstream   string
	scaleUps   atomic.Int32
}

func (e *wakeEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.woke.IsZero() || time.Since(e.woke) < e.readyAfter {
		return "", false, nil
	}
	return e.upstream, true, nil
}

func (e *wakeEndpoints) ScaleTo(_ context.Context, _ activator.FunctionRef, replicas int) error {
	if replicas != 1 {
		return nil
	}
	e.scaleUps.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.woke.IsZero() {
		e.woke = time.Now()
	}
	return nil
}

// scenario: cold-wake-counts — scaled to zero, spec.timeout 2 s, ready 1.5 s after the wake, handler 1 s ⇒ 504
// after 2 s; warm ⇒ 200 after 1 s.
func TestScenarioColdWakeCounts(t *testing.T) {
	t.Parallel()
	up, _ := workerAfter(t, time.Second, "output")
	cold := &wakeEndpoints{readyAfter: 1500 * time.Millisecond, upstream: up.URL}
	h, st := deadlineDataPlane(t, cold, cold, nil, time.Minute)
	seedTimed(t, st, "fn", 2*time.Second)
	rec, elapsed := timedCall(h, "/function/fn")
	requireDeadline504(t, rec, elapsed, 2*time.Second, "fn", "spec.timeout")

	warm := &wakeEndpoints{woke: time.Now().Add(-time.Hour), upstream: up.URL}
	h, st = deadlineDataPlane(t, warm, warm, nil, time.Minute)
	seedTimed(t, st, "fn", 2*time.Second)
	rec, elapsed = timedCall(h, "/function/fn")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Less(t, elapsed, 2*time.Second)
}

// scenario: cold-wake-cut-at-limit — spec.timeout 2 s, ready 5 s after the wake ⇒ 504 after 2 s with the
// response-deadline detail, not the wake's 503; a call after 5 s is served warm by the same activation.
func TestScenarioColdWakeCutAtLimit(t *testing.T) {
	t.Parallel()
	up, _ := workerAfter(t, 0, "output")
	ep := &wakeEndpoints{readyAfter: 5 * time.Second, upstream: up.URL}
	h, st := deadlineDataPlane(t, ep, ep, nil, time.Minute)
	seedTimed(t, st, "fn", 2*time.Second)
	start := time.Now()
	rec, elapsed := timedCall(h, "/function/fn")
	requireDeadline504(t, rec, elapsed, 2*time.Second, "fn", "spec.timeout")

	time.Sleep(time.Until(start.Add(5200 * time.Millisecond)))
	rec, elapsed = timedCall(h, "/function/fn")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Less(t, elapsed, time.Second, "served warm: the activation went on after the 504")
	require.Equal(t, int32(1), ep.scaleUps.Load(), "no second scale-up")
}

// scenario: started-response-not-cut — spec.timeout 1 s, headers at 0.5 s, body ends at 2 s ⇒ the status and the
// whole body after 2 s.
func TestScenarioStartedResponseNotCut(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "first ")
		w.(http.Flusher).Flush()
		time.Sleep(1500 * time.Millisecond)
		_, _ = io.WriteString(w, "last")
	}))
	t.Cleanup(up.Close)
	h, st := deadlineDataPlane(t, fakeEndpoints{upstream: up.URL}, noScaler{}, nil, time.Minute)
	seedTimed(t, st, "stream", time.Second)
	rec, elapsed := timedCall(h, "/function/stream")
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, "first last", rec.Body.String())
	require.GreaterOrEqual(t, elapsed, 2*time.Second)
}

// scenario: link-keeps-own-limit — D = 1 s; a link with a 5 s timeout to B answering after 2 s gets the output, and
// B is told the link's time left; a 1 s link to a never-settling B fails 503 after 1 s, as before ADR-0151.
func TestScenarioLinkKeepsOwnLimit(t *testing.T) {
	t.Parallel()
	slow, header := workerAfter(t, 2*time.Second, "output")
	h, st := deadlineDataPlane(t, fakeEndpoints{upstream: slow.URL}, noScaler{}, nil, time.Second)
	seedTimed(t, st, "b", 0)
	inv := local.NewInvoker(h)
	out, err := inv.Invoke(context.Background(), local.Ref{Namespace: "default", Function: "b"}, []byte(`{}`), 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, "output", string(out))
	ms, err := strconv.Atoi(header.Load().(string))
	require.NoError(t, err)
	require.True(t, ms > 4000 && ms <= 5000, "the link's own limit reaches the worker: %d ms", ms)

	hung, _ := workerAfter(t, -1, "")
	h, st = deadlineDataPlane(t, fakeEndpoints{upstream: hung.URL}, noScaler{}, nil, time.Minute)
	seedTimed(t, st, "b", 0)
	start := time.Now()
	_, err = local.NewInvoker(h).Invoke(context.Background(), local.Ref{Namespace: "default", Function: "b"}, []byte(`{}`), time.Second)
	elapsed := time.Since(start)
	var ue *local.UpstreamError
	require.True(t, errors.As(err, &ue), "%v", err)
	require.Equal(t, http.StatusServiceUnavailable, ue.Status)
	require.GreaterOrEqual(t, elapsed, time.Second)
	require.Less(t, elapsed, 2*time.Second)
}
