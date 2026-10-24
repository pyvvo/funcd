package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
	mu       sync.Mutex
	engines  []string
	refuse   bool
	hold     int32
	calls    atomic.Int32
	canceled atomic.Bool
	called   chan struct{}
	release  chan struct{}
	freed    sync.Once
}

func newSaveStub(engines ...string) *saveStub {
	return &saveStub{engines: engines, called: make(chan struct{}, 16), release: make(chan struct{})}
}

// free lets every held save call answer; barrierFront runs it before the servers close, so no call outlives a test.
func (s *saveStub) free() { s.freed.Do(func() { close(s.release) }) }

func (s *saveStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := s.calls.Add(1)
		s.called <- struct{}{}
		if s.hold > 0 && n >= s.hold {
			select {
			case <-s.release:
			case <-r.Context().Done():
				s.canceled.Store(true)
				return
			}
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
	t.Cleanup(save.free)
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
// waits with its headers. The answer is larger than the proxy's write buffer, so bytes sent early would arrive.
func TestBarrierHoldsTheLastBytes(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{"an answer": strings.Repeat("0123456789", 20<<10), "an empty answer": ""} {
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
			save.free()
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
			if body == "" || a.err == nil {
				require.NoError(t, a.err, "an empty answer's headers wait, so its 503 goes out whole")
				require.Equal(t, http.StatusServiceUnavailable, a.status, "%+v", a)
				require.Contains(t, a.body, "lake catalog not saved")
			}
			require.Equal(t, int32(3), save.calls.Load(), "the refused save call was not retried")
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

// ADR-0279 Decision 3: the save call ends with the client's request.
func TestBarrierSaveCallEndsWithTheRequest(t *testing.T) {
	t.Parallel()
	save := newSaveStub("A")
	save.hold = 2
	url := barrierFront(t, "rows", save)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("query"))
		if err == nil {
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
		}
		done <- err
	}()
	for range 2 {
		select {
		case <-save.called:
		case <-time.After(10 * time.Second):
			t.Fatal("no save call")
		}
	}
	cancel()
	require.Error(t, <-done)
	require.Eventually(t, save.canceled.Load, 10*time.Second, 10*time.Millisecond, "the save call outlived the client's request")
}

// scriptedBody hands out reads one per call: each chunk with its error.
type scriptedBody struct {
	reads []scriptedRead
}

type scriptedRead struct {
	data string
	err  error
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if len(b.reads) == 0 {
		return 0, io.EOF
	}
	r := b.reads[0]
	b.reads = b.reads[1:]
	return copy(p, r.data), r.err
}

func (*scriptedBody) Close() error { return nil }

// ADR-0279 Decision 3: heldTail gives an answer's last byte only once the release succeeded, whether the body's last
// bytes come with io.EOF or on a read before it; a failed release ends the body with its error, the last byte unsent.
func TestHeldTailGivesTheLastByteAfterTheRelease(t *testing.T) {
	t.Parallel()
	for name, reads := range map[string][]scriptedRead{
		"the last bytes with EOF":   {{"0123", nil}, {"4567", io.EOF}},
		"the last bytes before EOF": {{"0123", nil}, {"4567", nil}, {"", io.EOF}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, fail := range []bool{false, true} {
				released := false
				h := &heldTail{body: &scriptedBody{reads: slices.Clone(reads)}, release: func() error {
					released = true
					if fail {
						return errNotSaved
					}
					return nil
				}}
				var got []byte
				var err error
				for err == nil {
					buf := make([]byte, 16)
					var n int
					n, err = h.Read(buf)
					got = append(got, buf[:n]...)
					require.True(t, released || len(got) < 8, "the last byte went out before the release")
				}
				if fail {
					require.ErrorIs(t, err, errNotSaved)
					require.Equal(t, "0123456", string(got), "a failed release keeps the last byte")
					continue
				}
				require.ErrorIs(t, err, io.EOF)
				require.Equal(t, "01234567", string(got))
			}
		})
	}
}
