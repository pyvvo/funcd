package fault

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testCause = "dial tcp 10.63.0.7:8080: connect: connection refused"

// serveLogged writes err's problem under LogCauses and returns the answer and the log.
func serveLogged(t *testing.T, err error) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	h := LogCauses(slog.New(slog.NewTextHandler(&logs, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteProblem(w, err)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec, logs.String()
}

// The log line of a 5xx holds the whole error, once: a 500 at Error, any other 5xx at Warn.
func TestLogCausesLogsTheFullErrorOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		detail string
		level  string
	}{
		{"a 500", Wrapf(errors.New(testCause), Internal, "store.Get", "read x"), 500, "store.Get: read x", "level=ERROR"},
		{"a plain error", errors.New(testCause), 500, "internal error: the cause is in funcd's log", "level=ERROR"},
		{"a 503", Wrapf(errors.New(testCause), Unavailable, "activator.Wake", "resolve upstream"), 503, "activator.Wake: resolve upstream", "level=WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, logs := serveLogged(t, tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decode the problem: %v", err)
			}
			if p.Detail != tc.detail {
				t.Fatalf("detail = %q, want %q", p.Detail, tc.detail)
			}
			if strings.Count(logs, testCause) != 1 || !strings.Contains(logs, tc.level) {
				t.Fatalf("log = %q, want the cause once at %s", logs, tc.level)
			}
		})
	}
}

// A problem whose detail is the whole error has nothing for the log.
func TestLogCausesLogsNothingWhenNothingIsLeftOut(t *testing.T) {
	for name, err := range map[string]error{
		"a 500 with no cause":  Internalf("activator.ServeHTTP", "no function in request context"),
		"a 503 with no cause":  Unavailablef("edge.limit", "in-flight concurrency ceiling reached"),
		"a 400 with its cause": Wrapf(errors.New("line 3: unknown field"), Invalid, "admission", "Policy"),
		"a 503 over a fault":   Wrapf(Unavailablef("storescaler", "function default/f is Failed"), Unavailable, "activator.activate", "scale up"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, logs := serveLogged(t, err); logs != "" {
				t.Fatalf("log = %q, want none", logs)
			}
		})
	}
}

// With no LogCauses around it, WriteProblem still answers.
func TestWriteProblemAnswersWithNoOwner(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, Wrapf(errors.New(testCause), Internal, "store.Get", "read x"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), testCause) {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
}

// hijackWriter is a ResponseWriter that hijacks, as net/http's does.
type hijackWriter struct {
	http.ResponseWriter
	hijacked bool
}

func (h *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// http.ResponseController reaches through LogCauses, so a stream still flushes and an upgrade still hijacks.
func TestLogCausesKeepsTheResponseController(t *testing.T) {
	rec := httptest.NewRecorder()
	under := &hijackWriter{ResponseWriter: rec}
	flush := LogCauses(slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
	}))
	flush.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !rec.Flushed {
		t.Fatal("the flush did not reach the writer")
	}
	hijack := LogCauses(slog.Default())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, _, err := http.NewResponseController(w).Hijack(); err != nil {
			t.Errorf("hijack: %v", err)
		}
	}))
	hijack.ServeHTTP(under, httptest.NewRequest(http.MethodGet, "/", nil))
	if !under.hijacked {
		t.Fatal("the hijack did not reach the writer")
	}
}
