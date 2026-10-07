//go:build e2e

package funcd_test

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"golang.org/x/net/websocket"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/observ"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/shape"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/wsupstream"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// scenario: cors-preflight (e2e, F78/ADR-0114) — a real funcd with WithEdgeShaping answers an OPTIONS
// preflight AT THE EDGE (shape is innermost but still ahead of the activator dial): 204 + the CORS
// headers, the upstream never woken. Proves the shaping middleware is wired into the real binary chain.
func TestScenarioE2EEdgeCorsPreflight(t *testing.T) {
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)

	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New(nil)),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithEdgeShaping(shape.Config{CORS: &shape.CORS{
			AllowOrigins: []string{"https://app.example"}, AllowMethods: []string{"GET", "POST"},
			AllowHeaders: []string{"Authorization"}, MaxAgeSeconds: 600,
		}}),
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	req, _ := http.NewRequest(http.MethodOptions, "http://"+p.DataPlaneAddr()+"/function/api", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "the preflight is answered at the edge (no activator wake)")
	require.Equal(t, "https://app.example", resp.Header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	require.Equal(t, "600", resp.Header.Get("Access-Control-Max-Age"))
}

// scenario: full-edge-chain-streaming (e2e, F76+F78/ADR-0114 — the M1 on-chain guard) — the REAL
// observ+limit+shape chain, assembled in funcd's exact order (Recover → RequestID → observ → limit →
// shape → handler via gateway.Chain), fronted by a live server so the ResponseWriter can flush and
// hijack. It proves the whole middleware stack, together, (a) applies CORS + gzip to a normal response,
// (b) records the RED metric with the dataplane-filled function label, and — the M1 fold — (c) lets an
// SSE stream flush unbuffered and (d) a WebSocket upgrade round-trip end to end through the real
// dataplane.Handler without the recorder / gzipWriter breaking the stream or the upgrade (ADR-0181).
func TestScenarioE2EFullEdgeChainStreaming(t *testing.T) {
	reader := metric.NewManualReader()
	tel := observability.NewFromProviders(metric.NewMeterProvider(metric.WithReader(reader)), nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv, _ := edgeChainServer(t, tel, logger)

	// (a)+(b) normal request: CORS echoed, security header set, body gzipped, metric+label recorded.
	t.Run("normal-gzip-cors-label", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/function/orders", nil)
		req.Header.Set("Origin", "https://caller.example")
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, "https://caller.example", resp.Header.Get("Access-Control-Allow-Origin"))
		require.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
		require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
		gz, err := gzip.NewReader(resp.Body)
		require.NoError(t, err)
		body, err := io.ReadAll(gz)
		require.NoError(t, err)
		require.Contains(t, string(body), "edge chain body", "the gzipped body decompresses through the full chain")

		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		require.True(t, hasFunctionLabel(rm, "orders"), "the RED metric carries the dataplane-filled function label through the chain")
	})

	// (c) SSE: the stream flushes unbuffered through observ.recorder + shape (never gzipped, never buffered).
	t.Run("sse-flushes-unbuffered", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/function/sse", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Empty(t, resp.Header.Get("Content-Encoding"), "an SSE stream is never gzipped by the chain")
		br := bufio.NewReader(resp.Body)
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "data: tick\n", line, "the first SSE frame arrives flushed, before the handler finishes")
	})

	// (d) WS: the upgrade round-trips through the full chain (observ Hijack + shape Hijack forwarded) and the real
	// dataplane.Handler and activator.
	t.Run("edge-chain-websocket-real-handler", func(t *testing.T) {
		conn := dialEdgeWS(t, srv, "mode=echo")
		require.NoError(t, websocket.Message.Send(conn, "hi"))
		var reply string
		require.NoError(t, websocket.Message.Receive(conn, &reply))
		require.Equal(t, "echo:hi", reply, "the WS frame round-trips through the full edge chain")
	})
}

// edgeChainServer serves the real edge chain in funcd's order (Recover, RequestID → observ → limit → shape) on a
// live server. Its terminal sends /function/ws to a real dataplane.Handler over activator.New, whose only Function
// default/ws is warm on a wsupstream server; only the SSE and normal paths use the stand-in, which fills the
// observ.Target holder as the data plane does.
func edgeChainServer(tb testing.TB, tel *observability.Telemetry, logger *slog.Logger) (*httptest.Server, *wsupstream.Server) {
	tb.Helper()
	up := wsupstream.New(tb)
	st := store.New(memory.New())
	fn := &v1.Function{}
	fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	fn.Name, fn.Namespace, fn.ResourceGroup = "ws", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	_, err := st.Create(context.Background(), fn)
	require.NoError(tb, err)
	act, err := activator.New(activator.Deps{Store: st, Endpoints: warmEndpoint(up.URL), Scaler: failScaler{}, Logger: logger})
	require.NoError(tb, err)
	plane := dataplane.Handler(st, act, router.New(), nil, nil, nil, 0, logger)

	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/function/ws" {
			plane.ServeHTTP(w, r)
			return
		}
		if tgt, ok := observ.TargetFrom(r.Context()); ok {
			tgt.Namespace, tgt.Function = "team", "orders"
		}
		if r.URL.Path == "/function/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			f, ok := w.(http.Flusher)
			require.True(tb, ok, "the chain forwards http.Flusher to the SSE handler")
			for i := 0; i < 3; i++ {
				_, _ = io.WriteString(w, "data: tick\n\n")
				f.Flush() // must reach the client unbuffered — no gzip/recorder swallowing it
			}
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("edge chain body ", 500))
	})

	chain := gateway.Chain(terminal,
		gateway.Recover(logger), gateway.RequestID,
		observ.Chain(observ.Config{Metrics: true, AccessLog: true, Trace: true}, tel, logger),
		limit.Chain(limit.Config{}), // limits off (pass-through) — this test is about shaping+streaming, not rejects
		shape.Chain(shape.Config{
			CORS:        &shape.CORS{AllowOrigins: []string{"*"}},
			Headers:     &shape.Headers{Set: map[string]string{"X-Frame-Options": "DENY"}},
			Compression: true,
		}),
	)
	srv := httptest.NewServer(chain)
	tb.Cleanup(srv.Close)
	return srv, up
}

func dialEdgeWS(tb testing.TB, srv *httptest.Server, query string) *websocket.Conn {
	tb.Helper()
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/function/ws?"+query, "", srv.URL)
	require.NoError(tb, err, "the WebSocket upgrade hijacks through observ + shape")
	tb.Cleanup(func() { _ = conn.Close() })
	return conn
}

// warmEndpoint reports every Function ready on one upstream.
type warmEndpoint string

func (e warmEndpoint) Upstream(context.Context, activator.FunctionRef) (string, bool, error) {
	return string(e), true, nil
}

// failScaler fails every wake: the edge-chain Function is always warm.
type failScaler struct{}

func (failScaler) ScaleTo(context.Context, activator.FunctionRef, int) error {
	return fault.Internalf("failScaler", "no wake expected")
}

func hasFunctionLabel(rm metricdata.ResourceMetrics, fn string) bool {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					if v, present := dp.Attributes.Value("function"); present && v.AsString() == fn {
						return true
					}
				}
			}
		}
	}
	return false
}
