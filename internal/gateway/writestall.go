package gateway

import (
	"bufio"
	"net"
	"net/http"
	"time"
)

// WriteStallTimeout is how long a data-plane response write may wait for the client to accept data.
const WriteStallTimeout = 60 * time.Second

// stallChunk is the largest piece of a Write sent under one deadline: the io.Copy buffer size that
// httputil.ReverseProxy and http.ServeContent already write in.
const stallChunk = 32 << 10

// clearAhead is how far ahead a clear sets the deadline: far enough that it never fires.
const clearAhead = 100 * 365 * 24 * time.Hour

// WriteStall returns a middleware that bounds each response write by d: a client that accepts no data for
// d while the response has data to send fails the write, and net/http then closes the connection (HTTP/1.1) or resets
// the stream (HTTP/2). The deadline is in force only while a piece is being written or flushed, so a live stream of
// any length and a quiet stream are not cut. It is armed once more when next returns, also during a panic unwind, to
// bound net/http's final flush; net/http clears it on HTTP/1.1 once the response is finished, and the end of an HTTP/2
// stream stops the stream's timer. d <= 0 returns next unchanged.
func WriteStall(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &stallWriter{ResponseWriter: w, d: d}
			sw.clear()
			defer sw.arm()
			next.ServeHTTP(sw, r)
		})
	}
}

// stallWriter sets the connection's write deadline to now + d around each WriteHeader, each stallChunk piece of a
// Write, and each Flush, and clears it when that call returns: net/http's HTTP/2 server resets a stream whose deadline
// fires even when nothing is waiting to be written.
//
// A clear sets the deadline clearAhead from now, never to the zero time, and the request starts cleared. net/http's
// HTTP/2 server applies each deadline later on the connection's serve loop, in no fixed order against the stream's
// last frame, and a zero deadline drops the stream's timer: an arm applied after the stream ended would start a new
// timer that nothing stops, and its firing would reset the ended stream. With a timer from the start, the end of the
// stream stops it, and a deadline applied after that finds it stopped and is ignored.
//
// A deadline error (a writer without a connection, such as httptest.ResponseRecorder) is ignored. After a Hijack it
// sets nothing: net/http has cleared the deadlines, and an upgraded tunnel is not bounded here (ADR-0181). It
// implements http.Flusher and http.Hijacker directly, as inner wrappers type-assert them, and Unwrap for
// http.ResponseController.
type stallWriter struct {
	http.ResponseWriter
	d        time.Duration
	hijacked bool
}

func (s *stallWriter) arm() { s.setDeadline(time.Now().Add(s.d)) }

func (s *stallWriter) clear() { s.setDeadline(time.Now().Add(clearAhead)) }

func (s *stallWriter) setDeadline(t time.Time) {
	if !s.hijacked {
		_ = http.NewResponseController(s.ResponseWriter).SetWriteDeadline(t)
	}
}

func (s *stallWriter) WriteHeader(code int) {
	s.arm()
	s.ResponseWriter.WriteHeader(code)
	s.clear()
}

// Write returns the bytes written so far and the first error.
func (s *stallWriter) Write(b []byte) (int, error) {
	written := 0
	for {
		piece := b[:min(len(b), stallChunk)]
		s.arm()
		n, err := s.ResponseWriter.Write(piece)
		s.clear()
		written += n
		b = b[len(piece):]
		if err != nil || len(b) == 0 {
			return written, err
		}
	}
}

func (s *stallWriter) Flush() {
	s.arm()
	_ = http.NewResponseController(s.ResponseWriter).Flush()
	s.clear()
}

func (s *stallWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil {
		s.hijacked = true
	}
	return conn, brw, err
}

func (s *stallWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
