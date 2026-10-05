// Package wsupstream is the one WebSocket test upstream (ADR-0181): an httptest server over x/net/websocket that
// records each request's ContentLength and serves the query parameter mode.
//
//   - echo (the default) replies echo:<frame> to every frame;
//   - sink replies echo:<frame> only to a frame starting probe:;
//   - push echoes like echo and also sends push:<n> every `every` (a duration query parameter).
package wsupstream

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// Server is a running WebSocket test upstream, closed by the test's Cleanup.
type Server struct {
	URL string

	mu      sync.Mutex
	lengths []int64
	ended   atomic.Int64
}

// New starts a Server closed by tb.Cleanup.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{}
	ws := websocket.Handler(s.serve)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.lengths = append(s.lengths, r.ContentLength)
		s.mu.Unlock()
		ws.ServeHTTP(w, r)
	}))
	tb.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// ContentLengths returns the ContentLength of every request received, in arrival order.
func (s *Server) ContentLengths() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.lengths...)
}

// Ended returns how many WebSocket connections have ended because their pending read failed.
func (s *Server) Ended() int { return int(s.ended.Load()) }

func (s *Server) serve(c *websocket.Conn) {
	defer s.ended.Add(1)
	q := c.Request().URL.Query()
	mode := q.Get("mode")
	if mode == "push" {
		every, err := time.ParseDuration(q.Get("every"))
		if err != nil || every <= 0 {
			return
		}
		done := make(chan struct{})
		defer close(done)
		go push(c, every, done)
	}
	for {
		var frame string
		if err := websocket.Message.Receive(c, &frame); err != nil {
			return
		}
		if mode == "sink" && !strings.HasPrefix(frame, "probe:") {
			continue
		}
		if err := websocket.Message.Send(c, "echo:"+frame); err != nil {
			return
		}
	}
}

func push(c *websocket.Conn, every time.Duration, done <-chan struct{}) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for n := 1; ; n++ {
		select {
		case <-done:
			return
		case <-tick.C:
			if err := websocket.Message.Send(c, "push:"+strconv.Itoa(n)); err != nil {
				return
			}
		}
	}
}
