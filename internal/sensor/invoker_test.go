package sensor_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

type readyEndpoints struct{ upstream string }

func (e readyEndpoints) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return e.upstream, true, nil
}

// ADR-0143: a Sensor action made through a CallTracker's transport is counted until its answer has been read.
func TestInvokeIsCountedWhileInFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	calls := activator.NewCallTracker(nil)
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Client: &http.Client{Transport: calls.Wrap(nil)}}

	done := make(chan error, 1)
	go func() {
		done <- inv.Invoke(context.Background(), "default", "target", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"})
	}()
	require.Eventually(t, func() bool { return !calls.Idle(srv.URL, 0) }, 2*time.Second, time.Millisecond, "the action is counted")
	close(release)
	require.NoError(t, <-done)
	require.True(t, calls.Idle(srv.URL, 0), "the action no longer counts once its answer was read")
}

func TestIssue174_InvokeErrorCarriesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"event data does not match the input contract","details":["name is required"]}`))
	}))
	t.Cleanup(srv.Close)
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}}

	err := inv.Invoke(context.Background(), "default", "strict", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "returned status 422")
	require.Contains(t, err.Error(), "name is required", "the function's answer explains the failure")
}

// Deliveries to one function reuse a keep-alive connection, so a busy event source does not churn ephemeral
// ports into TIME_WAIT (ADR-0041).
func TestIssue348_InvokeReusesConnection(t *testing.T) {
	conns := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Client: &http.Client{Transport: transport}}

	for range 10 {
		require.NoError(t, inv.Invoke(context.Background(), "default", "target", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"}))
	}
	require.EqualValues(t, 1, conns.Load(), "10 deliveries must share one keep-alive connection")
}

// recordingScaler records the replica targets the activator asks for.
type recordingScaler struct{ targets []int }

func (s *recordingScaler) ScaleTo(_ context.Context, _ activator.FunctionRef, replicas int) error {
	s.targets = append(s.targets, replicas)
	return nil
}

// manualClock is a clock the test moves by hand.
type manualClock struct{ now time.Time }

func (c *manualClock) Now() time.Time { return c.now }

// Issue #48: a Sensor action at a warm function counts as its activity, so idle reclaim never fires
// while actions keep arriving.
func TestIssue48_WarmInvokeCountsAsActivity(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(srv.Close)
	st := store.New(memory.New())
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "busy", "default", "rg1"
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 0, IdleTimeout: v1.Duration(time.Minute)}
	_, err := st.Create(ctx, fn)
	require.NoError(t, err)
	ep := readyEndpoints{upstream: srv.URL}
	clk := &manualClock{now: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	sc := &recordingScaler{}
	act, err := activator.New(activator.Deps{Store: st, Endpoints: ep, Scaler: sc, Clock: clk})
	require.NoError(t, err)
	inv := &sensor.HTTPInvoker{Endpoints: ep, Waker: act}

	require.NoError(t, act.ReclaimIdle(ctx))
	for range 3 {
		clk.now = clk.now.Add(40 * time.Second)
		require.NoError(t, inv.Invoke(ctx, "default", "busy", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"}))
		require.NoError(t, act.ReclaimIdle(ctx))
	}
	require.Empty(t, sc.targets, "a function invoked every 40s with a 1m idle timeout is never reclaimed")
}

// An HTTPInvoker without a Client builds its default client once, so concurrent first deliveries and later ones
// reuse its connections: a client per call would dial every delivery.
func TestIssue564_SensorInvokerReusesItsDefaultClient(t *testing.T) {
	conns := new(atomic.Int32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}}
	invoke := func() error {
		return inv.Invoke(context.Background(), "default", "target", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"})
	}

	const concurrency = 16
	start := make(chan struct{})
	errs := make(chan error, concurrency)
	for range concurrency {
		go func() {
			<-start
			errs <- invoke()
		}()
	}
	close(start)
	for range concurrency {
		require.NoError(t, <-errs)
	}
	for range 10 {
		require.NoError(t, invoke())
	}
	require.LessOrEqual(t, conns.Load(), int32(concurrency), "the deliveries after the concurrent ones reuse its connections")
}

// burstWorker is a Function worker that holds each round of deliveries until all wide of them have arrived, so every
// delivery of a round is in flight at once, and counts the connections it accepts. Without the barrier a call that
// ends while another's dial is pending hands that call its connection, and the dialed one stays idle.
func burstWorker(t *testing.T, wide int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	conns := new(atomic.Int32)
	var mu sync.Mutex
	arrived, release := 0, make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		round := release
		if arrived++; arrived == wide {
			close(release)
			arrived, release = 0, make(chan struct{})
		}
		mu.Unlock()
		select {
		case <-round:
		case <-time.After(10 * time.Second):
			t.Errorf("a round never reached %d deliveries in flight", wide)
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

// scenario: sensor-burst-reuses-connections — 25 rounds of four deliveries in flight at once to one Function, through
// the client pkg/funcd builds, open 4 connections, all in the first round (ADR-0155).
func TestScenarioSensorBurstReusesConnections(t *testing.T) {
	const rounds, wide = 25, 4
	srv, conns := burstWorker(t, wide)
	client := &http.Client{Transport: activator.DeadlineTransport(activator.NewCallTracker(nil).Wrap(nil)), Timeout: 30 * time.Second}
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Client: client}
	for round := range rounds {
		var wg sync.WaitGroup
		for range wide {
			wg.Go(func() {
				if err := inv.Invoke(context.Background(), "default", "target", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"}); err != nil {
					t.Errorf("invoke: %v", err)
				}
			})
		}
		wg.Wait()
		require.EqualValues(t, wide, conns.Load(), "connections accepted after round %d", round+1)
	}
}
