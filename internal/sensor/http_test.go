package sensor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/sensor"
)

func servesHTTP(context.Context, activator.FunctionRef) (v1.FunctionProtocol, error) {
	return v1.ProtocolHTTP, nil
}

// scenario: http-no-events — a Sensor or App hook call to a Function that serves HTTP fails with TargetServesHTTP
// before any request reaches its worker (ADR-0243 Decision 5).
func TestHTTPInvokerRefusesATargetThatServesHTTP(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	t.Cleanup(srv.Close)
	inv := &sensor.HTTPInvoker{Endpoints: readyEndpoints{upstream: srv.URL}, Protocol: servesHTTP}
	err := inv.Invoke(context.Background(), "team-a", "api", eventing.CloudEvent{SpecVersion: "1.0", ID: "e1"})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Contains(t, err.Error(), activator.ReasonTargetServesHTTP)
	require.Zero(t, requests.Load(), "the worker is never called")
}

// scenario: http-no-events — a Sensor whose action names a Function that serves HTTP is NotReady TargetServesHTTP and
// still delivers, so each event it matches is recorded as it fails; a Function event re-runs it, and it turns Ready
// once the Function is an event Function again.
func TestScenarioSensorNamingAnHTTPFunctionIsNotReady(t *testing.T) {
	st, fan, inv, r := harness(t)
	ctx := context.Background()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "api", "team-a", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	fn.Spec.Protocol = v1.ProtocolHTTP
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	createSensor(t, st, "nightly", []v1.Dependency{dep("tick", "clock", "fired")}, []v1.Action{{Name: "purge", On: "tick", Function: "api"}})

	_, err = r.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	got, err := st.Get(ctx, v1.KindSensor.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	cond, _ := got.(*v1.Sensor).Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, activator.ReasonTargetServesHTTP, cond.Reason)
	fire(t, fan, "clock", "fired", "")
	require.Equal(t, 1, inv.count(), "the action is still delivered, for the invoker to refuse and record")

	require.Equal(t, []string{"team-a/nightly"}, requestNames(r.MapFunction(ctx, created)))

	fn = created.(*v1.Function)
	fn.Spec.Protocol = v1.ProtocolCloudEvent
	_, err = st.Update(ctx, fn)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reqOf("nightly"))
	require.NoError(t, err)
	got, err = st.Get(ctx, v1.KindSensor.GVK(), "team-a", "nightly")
	require.NoError(t, err)
	cond, _ = got.(*v1.Sensor).Status.Conditions.Get("Ready")
	require.Equal(t, v1.ConditionTrue, cond.Status)
}

func requestNames(reqs []controller.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, string(req.Namespace)+"/"+string(req.Name))
	}
	return out
}
