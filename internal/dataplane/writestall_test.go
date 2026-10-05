package dataplane_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/edge/static"
	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/sendbuf"
)

// stall is the write stall limit S of these tests; production uses gateway.WriteStallTimeout.
const stall = 300 * time.Millisecond

const (
	bigBody   = 32 << 20
	routeHost = "app.example.com"
)

// listenerChain composes outer, Recover, RequestID and limit.Chain(lim) around core in the order of the data-plane
// listener chain in pkg/funcd.
func listenerChain(core http.Handler, lim limit.Config, outer ...gateway.Middleware) http.Handler {
	mw := append(append([]gateway.Middleware{}, outer...), gateway.Recover(nil), gateway.RequestID, limit.Chain(lim))
	return gateway.Chain(core, mw...)
}

func edgeChain(core http.Handler, lim limit.Config) http.Handler {
	return listenerChain(core, lim, gateway.WriteStall(stall))
}

// serveListener serves h with the data-plane listener's read timeouts and a fixed send buffer, over TLS with HTTP/2
// when h2 is set.
func serveListener(t *testing.T, h http.Handler, h2 bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = sendbuf.Listener(srv.Listener)
	srv.Config.ReadHeaderTimeout, srv.Config.ReadTimeout = 10*time.Second, 10*time.Second
	if h2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// functionPlane is a data plane whose Function f is warm at up, also reachable through a Route at routeHost/big,
// with an activator that counts its calls.
func functionPlane(t *testing.T, up string) (http.Handler, *activator.CallTracker) {
	t.Helper()
	st := store.New(memory.New())
	seedFunction(t, st, "f")
	calls := activator.NewCallTracker(nil)
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: up}, Scaler: noScaler{}, Calls: calls})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default", Auth: v1.AuthOpen, Host: routeHost,
		Rules: []router.CompiledRule{{Path: "/big", Function: "f"}},
	}}))
	return dataplane.Handler(st, act, rtr, nil, nil, nil, 0, nil), calls
}

// stalledClient sends a GET on a raw connection with a 64 KiB read buffer and reads nothing. A read buffer below the
// loopback segment size would hold the drain after the cut to the sender's zero-window probes, tens of KiB a second.
func stalledClient(t *testing.T, addr, host, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(64<<10))
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", path, host)
	require.NoError(t, err)
	return conn
}

// requireCut drains a stalled client and checks funcd closed the connection before the whole body arrived.
func requireCut(t *testing.T, conn net.Conn, body int) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	n, err := io.Copy(io.Discard, conn)
	var ne net.Error
	require.False(t, errors.As(err, &ne) && ne.Timeout(), "the stalled connection is still open: %v", err)
	require.Less(t, n, int64(body), "the stalled client got the whole body")
}

func getBody(t *testing.T, c *http.Client, url, host string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b
}

func requireWhole(t *testing.T, status int, b []byte, want int) {
	t.Helper()
	if status != http.StatusOK {
		require.Equal(t, http.StatusOK, status, "%s", b)
	}
	require.Len(t, b, want)
}

// scenario: stalled-reader-is-cut — maxInFlight 1, a 32 MiB response, client A reads nothing and B calls the
// same path S + 2 s later ⇒ B gets the whole body, A's connection is closed (its stream reset on HTTP/2), A's upstream
// handler has returned and the worker's call count is 0.
func TestScenarioStalledReaderIsCut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, host, path string
		h2               bool
	}{
		{name: "by name", host: "funcd.test", path: "/function/f"},
		{name: "by route", host: routeHost, path: "/big"},
		{name: "http2", host: "funcd.test", path: "/function/f", h2: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var active atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				active.Add(1)
				defer active.Add(-1)
				piece := bytes.Repeat([]byte("x"), 32<<10)
				for range bigBody / len(piece) {
					if _, err := w.Write(piece); err != nil {
						return
					}
				}
			}))
			t.Cleanup(up.Close)
			core, calls := functionPlane(t, up.URL)
			srv := serveListener(t, edgeChain(core, limit.Config{MaxInFlight: 1}), tc.h2)

			var drainA func()
			if tc.h2 {
				req, err := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
				require.NoError(t, err)
				req.Host = tc.host
				resp, err := srv.Client().Do(req)
				require.NoError(t, err)
				t.Cleanup(func() { _ = resp.Body.Close() })
				require.Equal(t, 2, resp.ProtoMajor)
				drainA = func() {
					_, err := io.Copy(io.Discard, resp.Body)
					require.ErrorContains(t, err, "stream error", "A's stream was reset")
				}
			} else {
				a := stalledClient(t, srv.Listener.Addr().String(), tc.host, tc.path)
				drainA = func() { requireCut(t, a, bigBody) }
			}

			time.Sleep(stall + 2*time.Second)
			status, b := getBody(t, srv.Client(), srv.URL+tc.path, tc.host)
			requireWhole(t, status, b, bigBody)
			drainA()
			require.Eventually(t, func() bool { return active.Load() == 0 && calls.Idle(up.URL, 0) },
				5*time.Second, 20*time.Millisecond, "A's upstream call still runs")
		})
	}
}

// scenario: stalled-static-asset-is-cut — maxInFlight 1, a static Route serving a 32 MiB asset, A reads nothing
// and B requests it S + 2 s later ⇒ B gets the whole asset.
func TestScenarioStalledStaticAssetIsCut(t *testing.T) {
	t.Parallel()
	const asset = bigBody
	b, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Put(context.Background(), "web/big.bin", bytes.Repeat([]byte("s"), asset), blob.PutOptions{}))
	sh, err := static.New(static.Deps{Buckets: func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool) {
		return b, ns == "default" && bucket == "site"
	}})
	require.NoError(t, err)
	rtr := router.New()
	require.NoError(t, rtr.Program(context.Background(), []router.Entry{{
		Namespace: "default", Auth: v1.AuthOpen, Host: routeHost,
		Rules: []router.CompiledRule{{Path: "/", Static: &v1.StaticBackend{Bucket: "site", Prefix: "web/"}}},
	}}))
	st := store.New(memory.New())
	act, err := activator.New(activator.Deps{Store: st, Endpoints: fakeEndpoints{upstream: "http://unused"}, Scaler: noScaler{}})
	require.NoError(t, err)
	core := dataplane.Handler(st, act, rtr, nil, nil, sh, 0, nil)
	srv := serveListener(t, edgeChain(core, limit.Config{MaxInFlight: 1}), false)

	a := stalledClient(t, srv.Listener.Addr().String(), routeHost, "/big.bin")
	time.Sleep(stall + 2*time.Second)
	status, body := getBody(t, srv.Client(), srv.URL+"/big.bin", routeHost)
	requireWhole(t, status, body, asset)
	requireCut(t, a, asset)
}

// streamPlane serves the listener chain, over TLS with HTTP/2 when h2 is set, in front of a Function whose upstream
// is handle.
func streamPlane(t *testing.T, handle http.HandlerFunc, h2 bool) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(handle)
	t.Cleanup(up.Close)
	core, _ := functionPlane(t, up.URL)
	return serveListener(t, edgeChain(core, limit.Config{}), h2)
}

func sendEvent(w http.ResponseWriter, i int) {
	ev := fmt.Sprintf("data: %d\n\n", i)
	_, _ = io.WriteString(w, strings.Repeat(" ", 1024-len(ev))+ev)
	_ = http.NewResponseController(w).Flush()
}

// readEvents reads 1 KiB events from srv's Function until the response ends and returns how many arrived.
func readEvents(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/function/f")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	ev, n := make([]byte, 1024), 0
	for {
		_, err := io.ReadFull(resp.Body, ev)
		if errors.Is(err, io.EOF) {
			return n
		}
		require.NoError(t, err)
		n++
	}
}

// scenario: slow-live-stream-not-cut — a 1 KiB event every S/2 for 8 S, read as it arrives ⇒ all 16 events and the
// end of the response.
func TestScenarioSlowLiveStreamNotCut(t *testing.T) {
	t.Parallel()
	srv := streamPlane(t, func(w http.ResponseWriter, _ *http.Request) {
		for i := range 16 {
			sendEvent(w, i)
			time.Sleep(stall / 2)
		}
	}, false)
	require.Equal(t, 16, readEvents(t, srv))
}

// scenario: quiet-stream-not-cut — one event, nothing for 3 S, a second event and the end ⇒ both events, also over
// HTTP/2, whose server resets a stream when its write deadline fires even with nothing to write.
func TestScenarioQuietStreamNotCut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		h2   bool
	}{
		{name: "http1"},
		{name: "http2", h2: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := streamPlane(t, func(w http.ResponseWriter, _ *http.Request) {
				sendEvent(w, 0)
				time.Sleep(3 * stall)
				sendEvent(w, 1)
			}, tc.h2)
			require.Equal(t, 2, readEvents(t, srv))
		})
	}
}

// echoUpgrade answers an upgrade request with 101 and echoes each line back, and any other request with 64 KiB.
func echoUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") == "" {
		_, _ = w.Write(bytes.Repeat([]byte("k"), 64<<10))
		return
	}
	_, _ = io.Copy(io.Discard, r.Body)
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	for {
		if err := brw.Flush(); err != nil {
			return
		}
		line, err := brw.ReadString('\n')
		if err != nil {
			return
		}
		_, _ = brw.WriteString("echo:" + line)
	}
}

// scenario: upgrade-tunnel-not-cut — a keep-alive connection that first received a 64 KiB response, then an upgrade
// to an echo upstream with a frame every S/2 for 4 S ⇒ every echo arrives.
func TestScenarioUpgradeTunnelNotCut(t *testing.T) {
	t.Parallel()
	srv := streamPlane(t, echoUpgrade, false)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	br := bufio.NewReader(conn)

	_, err = io.WriteString(conn, "GET /function/f HTTP/1.1\r\nHost: funcd.test\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	first, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Len(t, first, 64<<10)

	_, err = io.WriteString(conn, "GET /function/f HTTP/1.1\r\nHost: funcd.test\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	require.NoError(t, err)
	resp, err = http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for i := range 8 {
		time.Sleep(stall / 2)
		_, err := fmt.Fprintf(conn, "frame-%d\n", i)
		require.NoError(t, err)
		line, err := br.ReadString('\n')
		require.NoError(t, err, "frame %d", i)
		require.Equal(t, fmt.Sprintf("echo:frame-%d\n", i), line)
	}
}
