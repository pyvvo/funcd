//go:build e2e

package funcd_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// An http Function's started stream through the edge: no deadline of funcd cuts it (ADR-0151 Decision 2), and it
// counts as a call in flight until it ends, so a revision switch keeps it on the old worker until runtime.drainGrace
// (ADR-0143 Decisions 4.1 and 6).

const ticker = "ticker"

// streamModule is an http Function with no build: any GET answers an SSE stream of events that read "<tag> <id>", gap
// ms apart, ended after count of them, or never when count is 0. It tags its export as http(app) does, since a module
// with no build cannot import @funcd-dev/shim.
const streamModule = `const tag = %q;
const encoder = new TextEncoder();
const events = (request) => {
  const url = new URL(request.url);
  const gap = Number(url.searchParams.get('gap'));
  const count = Number(url.searchParams.get('count') ?? '0');
  let timer;
  const body = new ReadableStream({
    start(controller) {
      let id = 0;
      const send = () => {
        controller.enqueue(encoder.encode('id: ' + id + '\ndata: ' + tag + ' ' + id + '\n\n'));
        id += 1;
        if (id === count) controller.close();
        else timer = setTimeout(send, gap);
      };
      send();
    },
    cancel() {
      clearTimeout(timer);
    },
  });
  return new Response(body, { headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' } });
};
events[Symbol.for('funcd.http')] = true;
export const handle = events;
`

// pushStream pushes streamModule with tag under the OCI tag of that name, with no contract, as funcdctl push does for
// an http Function.
func (e *gcEnv) pushStream(t *testing.T, tag string) (ref, digest string) {
	t.Helper()
	writeStep(t, e.src, tag, fmt.Sprintf(streamModule, tag))
	ref = "oci-layout://" + e.layout + ":" + tag
	digest, err := artifact.Push(e.ctx, ref, filepath.Join(e.src, tag+".mjs"), nil, "", "")
	require.NoError(t, err)
	return ref, digest
}

// streamFn is an http Function of the pushed module with one warm worker; a zero timeout leaves spec.timeout unset.
func streamFn(ref, digest string, timeout time.Duration) *v1.Function {
	fn := warmFn(ticker, ref)
	fn.Spec.ImageDigest, fn.Spec.Protocol, fn.Spec.Timeout = digest, v1.ProtocolHTTP, v1.Duration(timeout)
	return fn
}

type sseEvent struct {
	data string
	at   time.Time
}

// openStream sends GET path to the edge and returns the stream's events as they arrive, then the error that ends it:
// io.EOF when the app ends it, io.ErrUnexpectedEOF when the connection is cut.
func (e *gcEnv) openStream(t *testing.T, path string) (<-chan sseEvent, <-chan error) {
	t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.dp+path, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	events, end := make(chan sseEvent, 1024), make(chan error, 1)
	go func() {
		r := bufio.NewReader(resp.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				end <- err
				return
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				events <- sseEvent{data: strings.TrimSuffix(data, "\n"), at: time.Now()}
			}
		}
	}()
	return events, end
}

// readStream returns the stream's events once it ends, with the error that ended it, and fails after within.
func readStream(t *testing.T, events <-chan sseEvent, end <-chan error, within time.Duration) ([]sseEvent, error) {
	t.Helper()
	var got []sseEvent
	deadline := time.After(within)
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
		case err := <-end:
			// The reader sends every event before its end, so the buffer holds the rest.
			for {
				select {
				case ev := <-events:
					got = append(got, ev)
				default:
					return got, err
				}
			}
		case <-deadline:
			t.Fatalf("the stream did not end within %s", within)
		}
	}
}

// longestGap is the longest wait between two events.
func longestGap(events []sseEvent) time.Duration {
	var gap time.Duration
	for i := 1; i < len(events); i++ {
		gap = max(gap, events[i].at.Sub(events[i-1].at))
	}
	return gap
}

// streamTimeout is the Function's spec.timeout and the listener's read timeout in TestHTTPFunctionStreamOutlivesItsTimeout;
// each stream lasts streamLasts, four times longer.
const (
	streamTimeout = 500 * time.Millisecond
	streamLasts   = 4 * streamTimeout
)

// An SSE stream of an http Function, with an event every 100 ms or silent for three times its timeout, outlives its
// spec.timeout and the listener's read timeout: the deadline ends once the headers arrive, and nothing times a started
// response.
func TestHTTPFunctionStreamOutlivesItsTimeout(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithGatewayReadTimeoutForTest(streamTimeout))
	ref, digest := e.pushStream(t, "v1")
	e.apply(t, streamFn(ref, digest, streamTimeout))
	waitReady(t, e.c, ticker)
	const beat = 100 * time.Millisecond
	for _, tc := range []struct {
		name       string
		gap, quiet time.Duration
		count      int
	}{
		{"with-traffic", beat, 0, int(streamLasts/beat) + 1},
		{"silent", streamLasts, 3 * streamTimeout, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opened := time.Now()
			events, end := e.openStream(t, fmt.Sprintf("/function/%s/events?gap=%d&count=%d", ticker, tc.gap.Milliseconds(), tc.count))
			got, err := readStream(t, events, end, streamLasts+10*time.Second)
			require.ErrorIs(t, err, io.EOF, "the stream ends where the app ends it")
			require.Len(t, got, tc.count)
			require.Equal(t, fmt.Sprintf("v1 %d", tc.count-1), got[len(got)-1].data)
			require.GreaterOrEqual(t, got[len(got)-1].at.Sub(opened), streamLasts-50*time.Millisecond, "the stream outlived its timeout")
			if tc.quiet > 0 {
				require.GreaterOrEqual(t, longestGap(got), tc.quiet, "and stayed open while silent")
			}
		})
	}
}

// switchGrace is runtime.drainGrace in TestHTTPFunctionStreamAtRevisionSwitch.
const switchGrace = 2 * time.Second

// An open SSE stream at a revision switch stays on the old worker while new calls reach the new revision, and is cut
// runtime.drainGrace after the switch, when that worker stops; the client sees a cut connection, not an end.
func TestHTTPFunctionStreamAtRevisionSwitch(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithPacing(funcd.Pacing{DrainGrace: switchGrace, HandOutSettle: 200 * time.Millisecond}))
	ref, digest := e.pushStream(t, "v1")
	e.apply(t, streamFn(ref, digest, 0))
	waitReady(t, e.c, ticker)
	events, end := e.openStream(t, "/function/"+ticker+"/events?gap=100")
	select {
	case first := <-events:
		require.Equal(t, "v1 0", first.data)
	case <-time.After(10 * time.Second):
		t.Fatal("the stream sent no event")
	}

	ref, digest = e.pushStream(t, "v2")
	e.apply(t, streamFn(ref, digest, 0))
	since := e.waitSwitch(t, ticker, ticker+"-2")
	fresh, freshEnd := e.openStream(t, "/function/"+ticker+"/events?gap=100&count=1")
	got, err := readStream(t, fresh, freshEnd, 10*time.Second)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, []string{"v2 0"}, eventData(got), "a new stream reaches v2")

	got, err = readStream(t, events, end, switchGrace+10*time.Second)
	cut := time.Now()
	require.ErrorIs(t, err, io.ErrUnexpectedEOF, "the stream is cut, not ended")
	for _, ev := range got {
		require.True(t, strings.HasPrefix(ev.data, "v1 "), "every event of the open stream comes from v1: %q", ev.data)
	}
	require.NotEmpty(t, got)
	require.True(t, got[len(got)-1].at.After(since.Add(switchGrace/2)), "the open stream goes on after the switch")
	require.GreaterOrEqual(t, cut.Sub(since), switchGrace-50*time.Millisecond, "it is cut no sooner than runtime.drainGrace")
	require.Less(t, cut.Sub(since), switchGrace+3*time.Second, "and when its worker stops")
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindFunction, "default", ticker)
		return err == nil && obj.(*v1.Function).Status.DrainingRevision == ""
	}, 10*time.Second, 50*time.Millisecond, "v1 drained")
}

// waitSwitch waits until name's calls switch to revision and returns when the switch happened, status.drainingSince.
func (e *gcEnv) waitSwitch(t *testing.T, name, revision string) time.Time {
	t.Helper()
	var since time.Time
	require.Eventually(t, func() bool {
		obj, err := e.c.Get(e.ctx, v1.KindFunction, "default", v1.ObjectName(name))
		if err != nil {
			return false
		}
		st := obj.(*v1.Function).Status
		if st.ServingRevision != revision || st.DrainingSince == nil {
			return false
		}
		since = time.Time(*st.DrainingSince)
		return true
	}, 30*time.Second, 20*time.Millisecond, "the calls of %s switch to %s", name, revision)
	return since
}

func eventData(events []sseEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.data
	}
	return out
}
