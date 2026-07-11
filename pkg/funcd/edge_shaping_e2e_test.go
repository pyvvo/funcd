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

	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/edge/limit"
	"github.com/green-0-rabbit/funcd/internal/edge/observ"
	"github.com/green-0-rabbit/funcd/internal/edge/shape"
	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/platform/observability"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
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
		funcd.WithStore(store.New(memory.New())), funcd.WithRuntime(process.New()),
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
// SSE stream flush unbuffered and (d) a WebSocket upgrade round-trip end to end without the recorder /
// gzipWriter breaking the stream or the upgrade.
func TestScenarioE2EFullEdgeChainStreaming(t *testing.T) {
	reader := metric.NewManualReader()
	tel := observability.NewFromProviders(metric.NewMeterProvider(metric.WithReader(reader)), nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	// The terminal handler stands in for dataplane.Handler: it fills the observ.Target holder exactly as
	// the real data plane does after resolving the Route, then serves normal / SSE / WS by path.
	wsEcho := websocket.Handler(func(c *websocket.Conn) {
		var msg string
		for {
			if err := websocket.Message.Receive(c, &msg); err != nil {
				return
			}
			_ = websocket.Message.Send(c, "echo:"+msg)
		}
	})
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tgt, ok := observ.TargetFrom(r.Context()); ok {
			tgt.Namespace, tgt.Function = "team", "orders"
		}
		switch r.URL.Path {
		case "/function/ws":
			wsEcho.ServeHTTP(w, r)
		case "/function/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			f, ok := w.(http.Flusher)
			require.True(t, ok, "the chain forwards http.Flusher to the SSE handler")
			for i := 0; i < 3; i++ {
				_, _ = io.WriteString(w, "data: tick\n\n")
				f.Flush() // must reach the client unbuffered — no gzip/recorder swallowing it
			}
		default:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, strings.Repeat("edge chain body ", 500))
		}
	})

	// Assemble the REAL chain in funcd's exact order (funcd.go): Recover, RequestID (outer) → observ →
	// limit → shape (inner) → terminal.
	chain := gateway.Chain(terminal,
		gateway.Recover, gateway.RequestID,
		observ.Chain(observ.Config{Metrics: true, AccessLog: true, Trace: true}, tel, logger),
		limit.Chain(limit.Config{}), // limits off (pass-through) — this test is about shaping+streaming, not rejects
		shape.Chain(shape.Config{
			CORS:        &shape.CORS{AllowOrigins: []string{"*"}},
			Headers:     &shape.Headers{Set: map[string]string{"X-Frame-Options": "DENY"}},
			Compression: true,
		}),
	)
	srv := httptest.NewServer(chain)
	defer srv.Close()

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

	// (d) WS: the upgrade round-trips through the full chain (observ Hijack + shape Hijack forwarded).
	t.Run("ws-upgrade-roundtrips", func(t *testing.T) {
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/function/ws"
		conn, err := websocket.Dial(wsURL, "", srv.URL)
		require.NoError(t, err, "the WebSocket upgrade hijacks through observ + shape")
		defer func() { _ = conn.Close() }()
		require.NoError(t, websocket.Message.Send(conn, "hi"))
		var reply string
		require.NoError(t, websocket.Message.Receive(conn, &reply))
		require.Equal(t, "echo:hi", reply, "the WS frame round-trips through the full edge chain")
	})
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
