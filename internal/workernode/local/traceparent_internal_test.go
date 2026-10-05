package local

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// staticResolver resolves every alias to target.
type staticResolver struct{ target Ref }

func (s staticResolver) Resolve(context.Context, Ref, string) (Ref, time.Duration, error) {
	return s.target, 5 * time.Second, nil
}

// recordingDataPlane answers every synthesized POST /function/<target> with body and keeps its headers.
func recordingDataPlane(got *[]http.Header, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = append(*got, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// brokerInvoke sends POST /invoke/greeter through NewHandler over inv, with a traceparent when tp is set.
func brokerInvoke(t *testing.T, inv Invoker, tp string) *httptest.ResponseRecorder {
	t.Helper()
	caller, target := Ref{Namespace: "default", Function: "front"}, Ref{Namespace: "default", Function: "greeter"}
	h := NewHandler(caller, staticResolver{target: target}, inv, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/invoke/greeter", strings.NewReader(`{"name":"x"}`))
	if tp != "" {
		req.Header.Set(traceparentHeader, tp)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Contract (ADR-0165 Decision 3): the broker sets the caller's traceparent verbatim on the synthesized data-plane
// request, also through the nested in-flight cap wrapper (ADR-0147). The callee's shim validates it, so a value
// the broker cannot parse is forwarded unchanged too.
func TestBrokerForwardsTraceparent(t *testing.T) {
	wrappers := map[string]func(Invoker) Invoker{
		"invoker":    func(i Invoker) Invoker { return i },
		"nested cap": func(i Invoker) Invoker { return NewNestedCapInvoker(i, 4, metricnoop.NewMeterProvider().Meter("t")) },
	}
	for name, wrap := range wrappers {
		for _, tp := range []string{"00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "not-a-traceparent"} {
			t.Run(name+"/"+tp, func(t *testing.T) {
				var got []http.Header
				rec := brokerInvoke(t, wrap(NewInvoker(recordingDataPlane(&got, `{"greeting":"hi"}`))), tp)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Len(t, got, 1)
				require.Equal(t, []string{tp}, got[0].Values(traceparentHeader))
				require.Equal(t, "default", got[0].Get(namespaceHeader))
				require.Equal(t, "application/json", got[0].Get("Content-Type"))
			})
		}
	}
}

// scenario: no-header-behaves-as-today — a raw POST /invoke/{alias} without traceparent gets the same reply, and
// the forwarded call carries no traceparent, so the callee mints a new trace (ADR-0101 mint-root-trace).
func TestScenarioNoHeaderBehavesAsToday(t *testing.T) {
	var got []http.Header
	inv := NewInvoker(recordingDataPlane(&got, `{"greeting":"hi"}`))
	plain := brokerInvoke(t, inv, "")
	traced := brokerInvoke(t, inv, "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	require.Equal(t, http.StatusOK, plain.Code, plain.Body.String())
	require.Len(t, got, 2)
	require.Empty(t, got[0].Values(traceparentHeader), "no header in, none forwarded")
	require.Equal(t, traced.Code, plain.Code)
	require.Equal(t, traced.Body.String(), plain.Body.String())
	require.Equal(t, traced.Header().Get("Content-Type"), plain.Header().Get("Content-Type"))
	got[1].Del(traceparentHeader)
	require.Equal(t, got[1], got[0], "the forwarded request differs only by the traceparent")
}
