//go:build e2e

package funcd_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The ADR-0360 scenarios: devices-api, the http-api example of funcd-typescript, takes WebSocket connections on its
// route /relay, which sends each message to every other socket of the route, through the edge. The x/net client reads
// any close as io.EOF, so these tests check when a socket ends, and the shim's own tests check its close codes.

const relayPath = "/function/" + devicesAPI + "/relay"

// relayWithin bounds the wait for one relayed message.
const relayWithin = 10 * time.Second

// wsDial opens a WebSocket to path through the edge, closed when the test ends.
func (e *gcEnv) wsDial(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(e.dp, "http")+path, e.dp)
	require.NoError(t, err)
	ws, err := websocket.DialConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// wsNext returns ws's next text message, or the error that ends the socket, within d.
func wsNext(ws *websocket.Conn, d time.Duration) (string, error) {
	if err := ws.SetReadDeadline(time.Now().Add(d)); err != nil {
		return "", err
	}
	var msg string
	err := websocket.Message.Receive(ws, &msg)
	return msg, err
}

// requireRelays checks that a message from one socket reaches the other, and that the sender's next message is the
// other's, not its own.
func requireRelays(t *testing.T, a, b *websocket.Conn) {
	t.Helper()
	require.NoError(t, websocket.Message.Send(a, "left"))
	got, err := wsNext(b, relayWithin)
	require.NoError(t, err)
	require.Equal(t, "left", got)
	require.NoError(t, websocket.Message.Send(b, "right"))
	got, err = wsNext(a, relayWithin)
	require.NoError(t, err)
	require.Equal(t, "right", got, "the sender gets no echo")
}

// scenario: ws-relay — with spec.timeout 1 s, a client on /function/devices-api/relay and one on the Route's /api/relay
// meet on the one worker, and a message sent past that timeout reaches the other socket and not the sender.
func TestScenarioWSRelay(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	ref, digest := e.pushDevices(t, "devices", nil)
	fn := devicesFn(t, devicesAPI, ref, digest)
	fn.Spec.Timeout = v1.Duration(time.Second)
	e.apply(t, fn)
	waitReady(t, e.c, devicesAPI)
	var rt v1.Route
	exampleObject(t, "route.yaml", &rt)
	e.apply(t, &rt)
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindRoute, "default", rt.Name)
		if err != nil {
			return false
		}
		c, ok := obj.(*v1.Route).Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue
	}, 15*time.Second, 50*time.Millisecond, "the Route is programmed")

	a, b := e.wsDial(t, relayPath), e.wsDial(t, "/api/relay")
	time.Sleep(time.Duration(fn.Spec.Timeout) + 500*time.Millisecond) // spec.timeout bounds only the handshake
	requireRelays(t, a, b)
}

// scenario: ws-not-taken — an upgrade request to /devices, a route that takes no socket, gets the app's 200 and its
// headers with an empty body; a GET of /relay with no Upgrade header reaches the next handler, the app's 404.
func TestScenarioWSNotTaken(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	resp, body := e.edge(t, http.MethodGet, "/function/"+devicesAPI+"/devices", nil, map[string]string{
		"Connection":            "Upgrade",
		"Upgrade":               "websocket",
		"Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key":     "dGhlIHNhbXBsZSBub25jZQ==",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json", "the app's headers")
	require.Empty(t, body)

	resp, _ = e.edge(t, http.MethodGet, relayPath, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// scenario: ws-message-cap — a message over 1 MiB closes its socket, and new sockets still relay.
func TestScenarioWSMessageCap(t *testing.T) {
	t.Parallel()
	e := startDevices(t)
	big := e.wsDial(t, relayPath)
	require.NoError(t, websocket.Message.Send(big, strings.Repeat("x", 1<<20+1)))
	_, err := wsNext(big, relayWithin)
	require.ErrorIs(t, err, io.EOF, "the shim closed the socket")
	requireRelays(t, e.wsDial(t, relayPath), e.wsDial(t, relayPath))
}

// scenario: ws-keeps-worker — a scale-to-zero devices-api with scaling.idleTimeout 1 s stays Ready while its sockets
// stay open 3 s, and goes Idle once they close.
func TestScenarioWSKeepsWorker(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithPacing(funcd.Pacing{ReclaimInterval: 200 * time.Millisecond}))
	ref, digest := e.pushDevices(t, "devices", nil)
	fn := devicesFn(t, devicesAPI, ref, digest)
	fn.Spec.Replicas, fn.Spec.Scaling = 0, v1.Scaling{IdleTimeout: v1.Duration(time.Second)}
	e.apply(t, fn)
	a, b := e.wsDial(t, relayPath), e.wsDial(t, relayPath)
	time.Sleep(3 * time.Duration(fn.Spec.Scaling.IdleTimeout))
	require.Equal(t, v1.PhaseReady, phaseOf(t, e.c, devicesAPI), "an open socket is a call in flight")
	requireRelays(t, a, b)

	require.NoError(t, a.Close())
	require.NoError(t, b.Close())
	require.Eventually(t, func() bool { return phaseOf(t, e.c, devicesAPI) == v1.PhaseIdle }, 15*time.Second,
		100*time.Millisecond, "the Function goes Idle once its sockets close")
}

// scenario: ws-at-switch — sockets open on revision 1 keep relaying after revision 2 is applied, apart from the new
// sockets, which meet on revision 2's worker; revision 1's sockets end when its worker stops, runtime.drainGrace after
// the switch.
func TestScenarioWSAtSwitch(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithPacing(funcd.Pacing{DrainGrace: switchGrace, HandOutSettle: 200 * time.Millisecond}))
	ref, digest := e.pushDevices(t, "devices", nil)
	e.apply(t, devicesFn(t, devicesAPI, ref, digest))
	waitReady(t, e.c, devicesAPI)
	old1, old2 := e.wsDial(t, relayPath), e.wsDial(t, relayPath)

	ref, digest = e.pushDevices(t, "devices-v2", nil)
	e.apply(t, devicesFn(t, devicesAPI, ref, digest))
	since := e.waitSwitch(t, devicesAPI, devicesAPI+"-2")
	new1, new2 := e.wsDial(t, relayPath), e.wsDial(t, relayPath)
	require.NoError(t, websocket.Message.Send(new1, "new"))
	got, err := wsNext(new2, relayWithin)
	require.NoError(t, err)
	require.Equal(t, "new", got)
	require.NoError(t, websocket.Message.Send(old1, "old"))
	got, err = wsNext(old2, relayWithin)
	require.NoError(t, err)
	require.Equal(t, "old", got, "revision 1's sockets still relay, and revision 2's message never reached them")

	_, err = wsNext(old1, switchGrace+relayWithin)
	cut := time.Now()
	require.ErrorIs(t, err, io.EOF, "the socket ends with revision 1's worker")
	require.GreaterOrEqual(t, cut.Sub(since), switchGrace-50*time.Millisecond, "no sooner than runtime.drainGrace")
	require.Less(t, cut.Sub(since), switchGrace+3*time.Second, "and when the worker stops")
}
