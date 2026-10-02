package sensor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/sensor"
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
