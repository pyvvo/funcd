package gateway_test

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/testkit/sendbuf"
)

const stall = 300 * time.Millisecond

// deadlineWriter is a ResponseWriter that records each write deadline set through http.ResponseController: an arm
// is at most a day ahead, a clear is further. It counts the writes and flushes made while no arm was in force.
type deadlineWriter struct {
	header               http.Header
	arms, clears, writes int
	flushes, biggest     int
	unbounded, zeros     int
	last                 time.Time
}

func (f *deadlineWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func cleared(t time.Time) bool { return t.IsZero() || t.After(time.Now().Add(24*time.Hour)) }

func (f *deadlineWriter) check() {
	if cleared(f.last) {
		f.unbounded++
	}
}

func (f *deadlineWriter) WriteHeader(int) { f.check() }

func (f *deadlineWriter) Write(b []byte) (int, error) {
	f.check()
	f.writes++
	f.biggest = max(f.biggest, len(b))
	return len(b), nil
}

func (f *deadlineWriter) Flush() {
	f.check()
	f.flushes++
}

func (f *deadlineWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

func (f *deadlineWriter) SetWriteDeadline(t time.Time) error {
	switch {
	case t.IsZero():
		f.zeros++
	case cleared(t):
		f.clears++
	default:
		f.arms++
	}
	f.last = t
	return nil
}

func serveStall(fw *deadlineWriter, h http.HandlerFunc) {
	gateway.WriteStall(time.Minute)(h).ServeHTTP(fw, httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestWriteStallArmSequence(t *testing.T) {
	t.Parallel()
	fw := &deadlineWriter{}
	serveStall(fw, func(w http.ResponseWriter, _ *http.Request) {
		require.Equal(t, 1, fw.clears, "the request starts cleared")
		w.WriteHeader(http.StatusOK)
		require.Equal(t, 1, fw.arms, "one arm per WriteHeader")
		n, err := w.Write(make([]byte, 1<<20))
		require.NoError(t, err)
		require.Equal(t, 1<<20, n)
		require.Equal(t, 33, fw.arms, "one arm per 32 KiB piece")
		require.Equal(t, 32, fw.writes)
		require.Equal(t, 32<<10, fw.biggest)
		require.NoError(t, http.NewResponseController(w).Flush())
		require.Equal(t, 34, fw.arms, "one arm per Flush")
		require.Equal(t, 1, fw.flushes)
		require.Equal(t, fw.arms+1, fw.clears, "each arm is cleared when its call returns")
		require.True(t, cleared(fw.last), "no deadline between writes")
	})
	require.Zero(t, fw.unbounded, "a write or flush ran without a deadline")
	require.Zero(t, fw.zeros, "a clear set the zero time")
	require.Equal(t, 35, fw.arms, "one arm after the handler returns")
	require.WithinDuration(t, time.Now().Add(time.Minute), fw.last, time.Second)

	fw = &deadlineWriter{}
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		serveStall(fw, func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	})
	require.Equal(t, 1, fw.arms, "one arm during a panic unwind")
	require.Equal(t, 1, fw.clears)

	fw = &deadlineWriter{}
	serveStall(fw, func(w http.ResponseWriter, _ *http.Request) {
		_, _, err := http.NewResponseController(w).Hijack()
		require.NoError(t, err)
	})
	require.Equal(t, 1, fw.arms+fw.clears+fw.zeros, "no deadline set after a Hijack")
}

// rawGet sends a GET on a raw connection, with a read buffer of rcvbuf bytes when rcvbuf > 0.
func rawGet(t *testing.T, addr string, rcvbuf int) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	if rcvbuf > 0 {
		require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(rcvbuf))
	}
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: funcd.test\r\n\r\n")
	require.NoError(t, err)
	return conn
}

func stallServer(t *testing.T, size int, done chan<- error) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(gateway.WriteStall(stall)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(make([]byte, size))
		done <- err
	})))
	srv.Listener = sendbuf.Listener(srv.Listener)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestWriteStallCutsStalledWrite(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	srv := stallServer(t, 32<<20, done)
	start := time.Now()
	rawGet(t, srv.Listener.Addr().String(), 4<<10)
	select {
	case err := <-done:
		require.Error(t, err)
		require.Less(t, time.Since(start), stall+time.Second)
	case <-time.After(10 * time.Second):
		t.Fatal("the write to a client that reads nothing was not cut")
	}
}

// One 4 MiB Write to a client that reads at most 64 KiB every S/10 takes several S, so it completes only if each
// piece gets its own deadline. Both socket buffers are fixed at 64 KiB: the kernel wakes a writer blocked on a full
// send buffer only after a large share of it drains, and a self-tuning one grows to several MiB, which this client
// takes longer than S to drain.
func TestWriteStallLiveClientNotCut(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	srv := stallServer(t, 4<<20, done)
	start := time.Now()
	conn := rawGet(t, srv.Listener.Addr().String(), 64<<10)
	buf := make([]byte, 64<<10)
	deadline := time.After(30 * time.Second)
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			require.Greater(t, time.Since(start), 3*stall, "the write did not outlast one deadline")
			return
		case <-deadline:
			t.Fatal("the write to a reading client did not complete")
		case <-time.After(stall / 10):
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(stall/10)))
			_, _ = conn.Read(buf)
		}
	}
}

func TestWriteStallNoDeadlineSupport(t *testing.T) {
	t.Parallel()
	body := bytes.Repeat([]byte("r"), 100<<10)
	rec := httptest.NewRecorder()
	gateway.WriteStall(stall)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(body)
		require.NoError(t, err)
		require.NoError(t, http.NewResponseController(w).Flush())
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, body, rec.Body.Bytes())
	require.True(t, rec.Flushed)
}

type plainHandler struct{}

func (*plainHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestWriteStallNonPositiveReturnsNext(t *testing.T) {
	t.Parallel()
	next := &plainHandler{}
	require.Same(t, next, gateway.WriteStall(0)(next))
	require.Same(t, next, gateway.WriteStall(-time.Second)(next))
}

// h2Conn opens an HTTP/2 connection to srv with a raw framer and a connection window large enough for the test.
func h2Conn(t *testing.T, srv *httptest.Server) (*http2.Framer, net.Conn) {
	t.Helper()
	cfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	cfg.NextProtos = []string{"h2"}
	conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol)
	_, err = io.WriteString(conn, http2.ClientPreface)
	require.NoError(t, err)
	fr := http2.NewFramer(conn, conn)
	require.NoError(t, fr.WriteSettings())
	require.NoError(t, fr.WriteWindowUpdate(0, 1<<30))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
	return fr, conn
}

// readUntilEnd reads frames until stream id ends and fails on any RST_STREAM. With id 0 it reads until the read
// deadline passes.
func readUntilEnd(t *testing.T, fr *http2.Framer, id uint32) {
	t.Helper()
	for {
		f, err := fr.ReadFrame()
		if id == 0 && errors.Is(err, os.ErrDeadlineExceeded) {
			return
		}
		require.NoError(t, err)
		switch f := f.(type) {
		case *http2.RSTStreamFrame:
			t.Fatalf("stream %d was reset (%v) after it ended", f.StreamID, f.ErrCode)
		case *http2.SettingsFrame:
			if !f.IsAck() {
				require.NoError(t, fr.WriteSettingsAck())
			}
		case *http2.DataFrame:
			if f.StreamID == id && f.StreamEnded() {
				return
			}
		}
	}
}

// Over HTTP/2, a response that writes, flushes and ends gets no RST_STREAM after its END_STREAM, also once S has
// passed. net/http applies each deadline on its serve loop, in no fixed order against the stream's last frame, so the
// test sends many responses on one connection.
func TestWriteStallEndedStreamNotReset(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(gateway.WriteStall(stall)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 1<<10))
		_ = http.NewResponseController(w).Flush()
	})))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	fr, conn := h2Conn(t, srv)

	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	for id := uint32(1); id < 200; id += 2 {
		block.Reset()
		for _, f := range []hpack.HeaderField{
			{Name: ":method", Value: http.MethodGet},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "funcd.test"},
			{Name: ":path", Value: "/"},
		} {
			require.NoError(t, enc.WriteField(f))
		}
		require.NoError(t, fr.WriteHeaders(http2.HeadersFrameParam{
			StreamID: id, BlockFragment: block.Bytes(), EndStream: true, EndHeaders: true,
		}))
		readUntilEnd(t, fr, id)
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*stall)))
	readUntilEnd(t, fr, 0)
}
