package activator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// sentHeader sends req through DeadlineTransport and returns the TimeoutHeader the inner transport saw.
func sentHeader(t *testing.T, req *http.Request) (string, bool) {
	t.Helper()
	var got string
	var present bool
	rt := DeadlineTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got, present = r.Header.Get(TimeoutHeader), r.Header.Get(TimeoutHeader) != ""
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	}))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return got, present
}

func requireMsWithin(t *testing.T, header string, lo, hi int64) {
	t.Helper()
	ms, err := strconv.ParseInt(header, 10, 64)
	require.NoError(t, err)
	require.True(t, ms > lo && ms <= hi, "header %d ms, want in (%d, %d]", ms, lo, hi)
}

func deadlineReq(t *testing.T, ctx context.Context, rd *ResponseDeadline) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://worker.invalid/", nil).WithContext(ctx)
	req = WithFunction(req, FunctionRef{Namespace: "default", Name: "agent"})
	if rd != nil {
		req = WithResponseDeadline(req, *rd)
	}
	return req
}

func TestDeadlineTransportHeader(t *testing.T) {
	t.Parallel()
	ctx5, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx1, cancel1 := context.WithTimeout(context.Background(), time.Second)
	defer cancel1()
	in := func(d time.Duration) *ResponseDeadline {
		return &ResponseDeadline{At: time.Now().Add(d), Limit: d, Source: "spec.timeout"}
	}

	h, ok := sentHeader(t, deadlineReq(t, ctx5, nil))
	require.True(t, ok, "a context deadline (link, step, Sensor client) is sent")
	requireMsWithin(t, h, 4000, 5000)

	h, _ = sentHeader(t, deadlineReq(t, context.Background(), in(3*time.Second)))
	requireMsWithin(t, h, 2000, 3000)

	h, _ = sentHeader(t, deadlineReq(t, ctx5, in(2*time.Second)))
	requireMsWithin(t, h, 1000, 2000)
	h, _ = sentHeader(t, deadlineReq(t, ctx1, in(3*time.Second)))
	requireMsWithin(t, h, 0, 1000)

	spoofed := deadlineReq(t, context.Background(), nil)
	spoofed.Header.Set(TimeoutHeader, "999999")
	_, ok = sentHeader(t, spoofed)
	require.False(t, ok, "without a deadline the caller's header is deleted")
	require.Equal(t, "999999", spoofed.Header.Get(TimeoutHeader), "the caller's request is not modified")

	replaced := deadlineReq(t, ctx5, nil)
	replaced.Header.Set(TimeoutHeader, "999999")
	h, _ = sentHeader(t, replaced)
	requireMsWithin(t, h, 4000, 5000)

	require.Equal(t, int64(2), timeoutMs(1500*time.Microsecond), "rounded up")
	require.Equal(t, int64(2), timeoutMs(2*time.Millisecond))
	require.Equal(t, int64(1), timeoutMs(0), "at least 1")
	require.Equal(t, int64(1), timeoutMs(-time.Second))
}

func TestDeadlineTransportCancelsBeforeHeaders(t *testing.T) {
	t.Parallel()
	rt := DeadlineTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	start := time.Now()
	_, err := rt.RoundTrip(deadlineReq(t, context.Background(), &ResponseDeadline{At: start.Add(300 * time.Millisecond), Limit: 300 * time.Millisecond, Source: "spec.timeout"}))
	elapsed := time.Since(start)
	require.Equal(t, fault.DeadlineExceeded, fault.KindOf(err))
	require.EqualError(t, err, "activator.response-deadline: default/agent did not start its response within 300ms (spec.timeout)")
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
	require.Less(t, elapsed, time.Second, "the inner context is cancelled at d.At")
}

type closeSpy struct {
	io.Reader
	closed atomic.Bool
}

func (c *closeSpy) Close() error { c.closed.Store(true); return nil }

func TestDeadlineTransportClosesLateResponse(t *testing.T) {
	t.Parallel()
	calls := NewCallTracker(nil)
	body := &closeSpy{Reader: strings.NewReader("late")}
	rt := DeadlineTransport(calls.Wrap(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		time.Sleep(300 * time.Millisecond) // ignores its context: the response arrives after d.At
		return &http.Response{StatusCode: http.StatusOK, Body: body, Request: r}, nil
	})))
	_, err := rt.RoundTrip(deadlineReq(t, context.Background(), &ResponseDeadline{At: time.Now().Add(100 * time.Millisecond), Limit: 100 * time.Millisecond, Source: "invoke.defaultTimeout"}))
	require.Equal(t, fault.DeadlineExceeded, fault.KindOf(err))
	require.True(t, body.closed.Load(), "a response after d.At is closed")
	require.True(t, calls.Idle("http://worker.invalid", 0), "closing it ends the counted call")
}

func TestDeadlineTransportDoesNotCutStartedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "head")
		w.(http.Flusher).Flush()
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, "tail")
	}))
	defer srv.Close()
	req := deadlineReq(t, context.Background(), &ResponseDeadline{At: time.Now().Add(200 * time.Millisecond), Limit: 200 * time.Millisecond, Source: "spec.timeout"})
	req.URL = mustURL(t, srv.URL)
	req.RequestURI = ""
	resp, err := DeadlineTransport(newPooledTransport()).RoundTrip(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "a body still streaming after d.At is not cut")
	require.Equal(t, "headtail", string(b))
}

func TestDeadlineTransportTunnelsUpgrade(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	defer srv.Close()

	tr := NewCallTracker(nil)
	req := deadlineReq(t, context.Background(), &ResponseDeadline{At: time.Now().Add(5 * time.Second), Limit: 5 * time.Second, Source: "spec.timeout"})
	req.URL = mustURL(t, srv.URL)
	req.RequestURI = ""
	req.Method = http.MethodGet
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")
	resp, err := DeadlineTransport(tr.Wrap(nil)).RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	conn, ok := resp.Body.(io.ReadWriteCloser)
	require.True(t, ok, "the inner response is returned unwrapped, so the upgraded body stays writable")
	_, err = conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, len("ping"))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, "ping", string(buf))
	require.NoError(t, conn.Close())
	require.True(t, tr.Idle(srv.URL, 0))
}

type countingEndpoints struct {
	calls    atomic.Int32
	upstream string
}

func (e *countingEndpoints) Upstream(context.Context, FunctionRef) (string, bool, error) {
	e.calls.Add(1)
	return e.upstream, e.upstream != "", nil
}

type nopScaler struct{}

func (nopScaler) ScaleTo(context.Context, FunctionRef, int) error { return nil }

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) fault.Problem {
	t.Helper()
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return p
}

func TestServeHTTPPastDeadlineDoesNotWake(t *testing.T) {
	t.Parallel()
	ep := &countingEndpoints{}
	a, err := New(Deps{Endpoints: ep, Scaler: nopScaler{}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, deadlineReq(t, context.Background(), &ResponseDeadline{At: time.Now().Add(-time.Millisecond), Limit: time.Second, Source: "spec.timeout"}))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.Equal(t, int32(0), ep.calls.Load(), "a deadline already past answers without a wake")
}

func TestServeHTTPActivationTimeoutBeforeDeadlineIs503(t *testing.T) {
	t.Parallel()
	a, err := New(Deps{Endpoints: &countingEndpoints{}, Scaler: nopScaler{}, ActivationTimeout: 500 * time.Millisecond})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	start := time.Now()
	a.ServeHTTP(rec, deadlineReq(t, context.Background(), &ResponseDeadline{At: start.Add(2 * time.Second), Limit: 2 * time.Second, Source: "spec.timeout"}))
	elapsed := time.Since(start)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "an earlier ActivationTimeout keeps its 503")
	require.Contains(t, problemOf(t, rec).Detail, "did not become ready within 500ms")
	require.Less(t, elapsed, 1500*time.Millisecond)
}

func TestForwardErrorHandlerDeadlineFaultVersusPlainDeadline(t *testing.T) {
	t.Parallel()
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	newAct := func(logs *bytes.Buffer) *Activator {
		a, err := New(Deps{Endpoints: &countingEndpoints{upstream: hang.URL}, Scaler: nopScaler{}, Logger: slog.New(slog.NewTextHandler(logs, nil))})
		require.NoError(t, err)
		return a
	}

	var plainLogs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	newAct(&plainLogs).ServeHTTP(rec, deadlineReq(t, ctx, nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "a plain context deadline (a link's) stays 503")
	require.Contains(t, plainLogs.String(), "upstream call failed")

	var dlLogs bytes.Buffer
	rec = httptest.NewRecorder()
	newAct(&dlLogs).ServeHTTP(rec, deadlineReq(t, context.Background(), &ResponseDeadline{At: time.Now().Add(300 * time.Millisecond), Limit: 300 * time.Millisecond, Source: "spec.timeout"}))
	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	require.True(t, strings.HasPrefix(problemOf(t, rec).Detail, DeadlineOp+": default/agent"))
	require.NotContains(t, dlLogs.String(), "upstream call failed", "the deadline fault is written as is, without the Warn")
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
