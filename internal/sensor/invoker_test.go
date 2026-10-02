package sensor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 0, IdleTimeout: time.Minute}
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
