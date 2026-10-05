package dataplane_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/platform/observability"
)

// ADR-0114 Decision §1: the edge metric labels a request with its function only once the function
// is resolved; a request for a function that does not exist keeps the "-" label, so client-chosen
// names never become metric series.
func TestEdgeMetricsLabelMissingFunctionsAsUnresolved(t *testing.T) {
	t.Parallel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "handled")
	}))
	t.Cleanup(up.Close)

	reader := metric.NewManualReader()
	tel := observability.NewFromProviders(metric.NewMeterProvider(metric.WithReader(reader)), nil)
	dp, st := newHandler(t, up.URL)
	seedFunction(t, st, "echo")
	h := observ.Chain(observ.Config{Metrics: true}, tel, nil)(dp)

	const missing = 50
	for i := range missing {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/function/nope-%d", i), strings.NewReader(`{}`))
		req.Header.Set("X-Funcd-Namespace", fmt.Sprintf("ns-%d", i))
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/function/echo", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusOK, rec.Code)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "funcd.edge.requests" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				fn, _ := dp.Attributes.Value("function")
				ns, _ := dp.Attributes.Value("namespace")
				sc, _ := dp.Attributes.Value("status_class")
				counts[fn.AsString()+"|"+ns.AsString()+"|"+sc.AsString()] += dp.Value
			}
		}
	}
	require.Equal(t, map[string]int64{
		"-||4xx":           missing,
		"echo|default|2xx": 1,
	}, counts, "a missing function's client-chosen names must not become metric labels")
}
