package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// saveStub is an engine's save endpoint: each call answers the instance engines[call] (the last one past the end),
// 200 unless refuse is set; calls from the hold-th on wait for release.
type saveStub struct {
	mu      sync.Mutex
	engines []string
	refuse  bool
	hold    int32
	calls   atomic.Int32
	called  chan struct{}
	release chan struct{}
}

func newSaveStub(engines ...string) *saveStub {
	return &saveStub{engines: engines, called: make(chan struct{}, 16), release: make(chan struct{})}
}

func (s *saveStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		n := s.calls.Add(1)
		s.called <- struct{}{}
		if s.hold > 0 && n >= s.hold {
			<-s.release
		}
		s.mu.Lock()
		engine := s.engines[min(int(n)-1, len(s.engines)-1)]
		refuse := s.refuse
		s.mu.Unlock()
		if refuse {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(saveReport{Engine: engine, Error: "AccessDenied"})
			return
		}
		_ = json.NewEncoder(w).Encode(saveReport{Engine: engine, Saved: int64(n)})
	}
}

// barrierFront serves body from an engine behind a proxy whose barrier calls save; it returns the proxy's URL.
func barrierFront(t *testing.T, body string, save *saveStub) string {
	t.Helper()
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(engine.Close)
	saves := httptest.NewServer(save.handler())
	t.Cleanup(saves.Close)
	target := EngineTarget{Lake: auth.EntityRef{Type: v1.KindLake, Namespace: "data", Name: "lake"}, Upstream: engine.URL,
		EngineToken: engineToken, SaveURL: saves.URL + "/save"}
	front := httptest.NewServer(NewLakeProxy(nil, nil, target, nil))
	t.Cleanup(front.Close)
	return front.URL
}

// answer is one query's outcome through the proxy.
type answer struct {
	status int
	body   string
	err    error
}

// query sends a non-handshake request, which the proxy forwards with no PEP decision, and reads the whole answer.
func query(url string) answer {
	resp, err := http.Post(url, "application/octet-stream", strings.NewReader("query"))
	if err != nil {
		return answer{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return answer{status: resp.StatusCode, body: string(body), err: err}
}

// ADR-0279 Decision 3: no answer completes before the save call that follows it answers; an answer with no body
// waits with its headers.
func TestBarrierHoldsTheLastBytes(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"an answer": "012345678", "an empty answer": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			save := newSaveStub("A")
			save.hold = 2
			url := barrierFront(t, body, save)
			done := make(chan answer, 1)
			go func() { done <- query(url) }()
			for _, call := range []string{"the call that learns the instance", "the answer's save call"} {
				select {
				case <-save.called:
				case a := <-done:
					t.Fatalf("the answer completed before %s: %+v", call, a)
				case <-time.After(10 * time.Second):
					t.Fatalf("no %s", call)
				}
			}
			select {
			case a := <-done:
				t.Fatalf("the answer completed before its save call answered: %+v", a)
			case <-time.After(100 * time.Millisecond):
			}
			close(save.release)
			a := <-done
			require.NoError(t, a.err)
			require.Equal(t, http.StatusOK, a.status)
			require.Equal(t, body, a.body)
		})
	}
}

// scenario: refused-save-is-an-error (ADR-0279) — a refused save fails the answer: 503 "lake catalog not saved"
// while its headers wait, a broken answer once they went out; never a whole 200 answer.
func TestScenarioRefusedSaveIsAnError(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"an answer": "012345678", "an empty answer": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			save := newSaveStub("A")
			url := barrierFront(t, body, save)
			require.Equal(t, http.StatusOK, query(url).status, "the first query learns the instance and is saved")
			save.mu.Lock()
			save.refuse = true
			save.mu.Unlock()
			a := query(url)
			if a.err == nil {
				require.Equal(t, http.StatusServiceUnavailable, a.status, "%+v", a)
				require.Contains(t, a.body, "lake catalog not saved")
			}
		})
	}
}

// ADR-0279 Decision 3: an answer is released only by the instance the proxy knew when it forwarded the request, with
// one save call; a report from another instance fails the answer and teaches the proxy the new one.
func TestBarrierReleasesOnlyTheSameInstance(t *testing.T) {
	t.Parallel()
	save := newSaveStub("A", "A", "B", "B")
	url := barrierFront(t, "rows", save)
	require.Equal(t, answer{status: http.StatusOK, body: "rows"}, query(url), "learned A, then A saved")
	a := query(url)
	require.False(t, a.err == nil && a.status == http.StatusOK && a.body == "rows", "B reported a save for A's answer: %+v", a)
	require.Equal(t, answer{status: http.StatusOK, body: "rows"}, query(url), "B is known now")
	require.Equal(t, int32(4), save.calls.Load(), "one call to learn A, then one per answer, never retried")
}

// ADR-0279 Decision 3: the first request gets 503 when the call that learns the engine instance fails.
func TestBarrierFirstRequestFailsWhenTheInstanceIsUnknown(t *testing.T) {
	t.Parallel()
	save := newSaveStub("A")
	save.refuse = true
	a := query(barrierFront(t, "rows", save))
	require.NoError(t, a.err)
	require.Equal(t, http.StatusServiceUnavailable, a.status)
	require.Contains(t, a.body, "lake catalog not saved")
}
