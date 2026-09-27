package embedded_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
)

// scenario: streaming-passthrough (embedded driver only — Lura is exempt) — the
// gateway flushes chunks to the client as the upstream emits them, without buffering
// the whole response, so agents can stream tokens / MCP can use SSE.
func TestScenarioStreamingPassthrough(t *testing.T) {
	t.Parallel()

	// Upstream emits chunk1, then blocks until released, then chunk2. If the gateway
	// buffered the full response, the client could not read chunk1 before releasing —
	// so reading chunk1 first proves incremental flushing.
	released := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		_, _ = io.WriteString(w, "chunk1\n")
		flusher.Flush()
		<-released
		_, _ = io.WriteString(w, "chunk2\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	gw := embedded.New()
	defer func() { _ = gw.Close() }()
	require.NoError(t, gw.ProgramRoutes(context.Background(), []gateway.Route{
		{ID: "default/stream", PathPrefix: "/stream", Upstream: upstream.URL},
	}))
	front := httptest.NewServer(gw.Handler())
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream") //nolint:noctx // test client
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)

	// chunk1 must arrive before we release the upstream — read it with a timeout so a
	// buffering regression fails fast instead of deadlocking.
	first := make(chan string, 1)
	go func() {
		line, _ := br.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		require.Contains(t, line, "chunk1", "first chunk streamed before upstream finished")
	case <-time.After(3 * time.Second):
		t.Fatal("gateway buffered the response — chunk1 did not stream through")
	}

	close(released)
	line2, err := br.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, line2, "chunk2")
}
