package dataplane_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/wsupstream"
)

// wsDataPlane serves dataplane.Handler for a warm Function f in default whose upstream is a wsupstream server.
func wsDataPlane(t *testing.T, timeout time.Duration) (http.Handler, *httptest.Server, *wsupstream.Server) {
	t.Helper()
	up := wsupstream.New(t)
	st := store.New(memory.New())
	seedFn(t, st, "default", "f")
	act, err := activator.New(activator.Deps{
		Store:     st,
		Endpoints: readyEndpoints{warm: map[v1.ObjectName]string{"f": ""}, upstream: up.URL},
		Scaler:    &spyScaler{},
	})
	require.NoError(t, err)
	h := dataplane.Handler(st, act, router.New(), nil, nil, nil, timeout, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv, up
}

func dialFunction(t *testing.T, srv *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/function/f?"+query, "", srv.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func requireEcho(t *testing.T, conn *websocket.Conn, frame string) {
	t.Helper()
	require.NoError(t, websocket.Message.Send(conn, frame))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	var reply string
	require.NoError(t, websocket.Message.Receive(conn, &reply))
	require.Equal(t, "echo:"+frame, reply)
}

// scenario: external-websocket-roundtrips (issue #727) — an external WebSocket to a warm Function round-trips, and
// the upstream saw the upgrade GET with Content-Length 0 (no ADR-0134 envelope).
func TestScenarioExternalWebSocketRoundtrips(t *testing.T) {
	t.Parallel()
	_, srv, up := wsDataPlane(t, 0)
	conn := dialFunction(t, srv, "mode=echo")
	require.Equal(t, []int64{0}, up.ContentLengths(), "the upstream saw the upgrade GET with Content-Length 0")
	requireEcho(t, conn, "hi")
}

// scenario: upgrade-tunnel-outlives-deadline — the response deadline bounds the 101, not the tunnel after it.
func TestScenarioUpgradeTunnelOutlivesDeadline(t *testing.T) {
	t.Parallel()
	_, srv, _ := wsDataPlane(t, 300*time.Millisecond)
	conn := dialFunction(t, srv, "mode=echo")
	time.Sleep(700 * time.Millisecond)
	requireEcho(t, conn, "late")
}

// scenario: upgrade-with-body-still-capped — with no edge body cap, a 3 MiB upgrade-headed POST, sized or chunked,
// answers 413 under maxNormalizeBytes and reaches no upstream.
func TestScenarioUpgradeWithBodyStillCapped(t *testing.T) {
	t.Parallel()
	h, _, up := wsDataPlane(t, 0)
	big := bytes.Repeat([]byte("x"), 3<<20)
	for _, tc := range []struct {
		name string
		body io.Reader
		cl   int64
	}{
		{"sized", bytes.NewReader(big), 3 << 20},
		{"chunked", io.MultiReader(bytes.NewReader(big)), -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/function/f", tc.body)
			require.Equal(t, tc.cl, req.ContentLength)
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "x")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
			var p fault.Problem
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			require.Equal(t, "urn:funcd:problem:payload-too-large", p.Type)
		})
	}
	require.Empty(t, up.ContentLengths(), "the upstream received nothing")
}

// scenario: bodiless-get-still-normalized — a GET with no body and no upgrade headers still forwards an envelope
// whose data is null (ADR-0134).
func TestScenarioBodilessGetStillNormalized(t *testing.T) {
	t.Parallel()
	var got string
	up := echoUpstream(t, &got)
	h, st := newHandler(t, up.URL)
	seedFunction(t, st, "echo")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/function/echo", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var ce struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &ce))
	require.Equal(t, "null", string(ce.Data))
}
