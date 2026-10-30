package sensor_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/sensor"
	"github.com/pyvvo/funcd/internal/testkit/plugintest"
)

type countingWaker struct {
	upstream string
	wakes    atomic.Int32
}

func (w *countingWaker) Wake(context.Context, activator.FunctionRef) (string, error) {
	w.wakes.Add(1)
	return w.upstream, nil
}

func answering(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	t.Cleanup(srv.Close)
	return srv
}

func event() eventing.CloudEvent { return eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"} }

// scenario: a-refused-call-wakes-nothing (the sensor entry)
func TestScenarioARefusedCallWakesNothing(t *testing.T) {
	t.Parallel()
	waker := &countingWaker{upstream: "http://unused.invalid"}
	inv := &sensor.HTTPInvoker{Waker: waker, Chain: invoke.NewChain(plugintest.Refuser{})}
	err := inv.Invoke(context.Background(), "default", "fn", event())
	var rf *invoke.Refusal
	require.True(t, errors.As(err, &rf), "the error carries the refusal: %v", err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.Zero(t, waker.wakes.Load(), "a refused action wakes nothing")
}

// scenario: every-caller-runs-the-chain (the sensor entry: Sensor actions and App hooks)
func TestScenarioEveryCallerRunsTheChain(t *testing.T) {
	t.Parallel()
	rec := &plugintest.Recorder{}
	srv := answering(t, "")
	chain := invoke.NewChain(rec)
	action := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Chain: chain}
	hook := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Chain: chain, Caller: invoke.AppHook}
	require.NoError(t, action.Invoke(context.Background(), "default", "fn", event()))
	require.NoError(t, hook.Invoke(context.Background(), "default", "fn", event()))
	require.Equal(t, []invoke.Caller{invoke.SensorAction, invoke.AppHook}, rec.Callers())
}

// scenario: an-after-plugin-replaces-the-answer (the sensor entry)
func TestScenarioAnAfterPluginReplacesTheAnswer(t *testing.T) {
	t.Parallel()
	chain := invoke.NewChain(plugintest.BadAnswer{})
	bad := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: answering(t, plugintest.BadBody).URL}, Chain: chain}
	err := bad.Invoke(context.Background(), "default", "fn", event())
	var rf *invoke.Refusal
	require.True(t, errors.As(err, &rf), "the error carries the refusal: %v", err)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	good := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: answering(t, `{"ok":true}`).URL}, Chain: chain}
	require.NoError(t, good.Invoke(context.Background(), "default", "fn", event()))
}

// scenario: every-path-checks-the-input (the sensor entry: Sensor actions and App hooks)
func TestScenarioEveryPathChecksTheInput(t *testing.T) {
	t.Parallel()
	waker := &countingWaker{upstream: "http://unused.invalid"}
	chain := invoke.NewChain(contractcheck.Plugins(plugintest.CompileContract(t, plugintest.EmailInput, `{}`))...)
	bad := eventing.CloudEvent{SpecVersion: "1.0", ID: "e1", Data: json.RawMessage(`{"email": "a b@example.com"}`)}
	for _, caller := range []invoke.Caller{invoke.SensorAction, invoke.AppHook} {
		inv := &sensor.HTTPInvoker{Waker: waker, Chain: chain, Caller: caller}
		err := inv.Invoke(context.Background(), "default", "fn", bad)
		require.Error(t, err)
		require.Contains(t, err.Error(), "returned status 422", "a %v call gets the contract's 422", caller)
		require.Contains(t, err.Error(), "/email")
	}
	require.Zero(t, waker.wakes.Load(), "a refused call wakes nothing")
}
