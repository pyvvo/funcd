package funcd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// lockedWriter is a log sink the platform's goroutines and the test can share.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestIssue454_ServerErrorsUseTheConfiguredLogger: net/http logs its own server errors (a TLS handshake
// error, an accept error, a recovered panic) through the http.Server's ErrorLog, and through the stdlib log
// package when it is nil, so they bypassed the configured logger and its format.
func TestIssue454_ServerErrorsUseTheConfiguredLogger(t *testing.T) {
	var logs lockedWriter
	p, err := New(InMemory(), WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	for name, srv := range map[string]*http.Server{"control-plane": p.httpServer, "data-plane": p.dataPlaneServer} {
		require.NotNil(t, srv.ErrorLog, "the %s server's net/http errors must go through the configured logger", name)
		srv.ErrorLog.Print(name + " probe")
		require.Contains(t, logs.String(), `"level":"WARN","msg":"`+name+` probe"`)
	}
}
