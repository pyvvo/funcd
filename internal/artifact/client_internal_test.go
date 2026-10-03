package artifact

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
)

// A registry target keeps its keep-alive connection when the process-wide http.DefaultTransport's idle connections
// are closed, as every httptest.Server.Close does, with and without a readable credential store: oras-go's default
// client sends through that transport. Not parallel: it sets DOCKER_CONFIG and closes the default transport's idle
// connections.
func TestIssue571_RegistryTargetSurvivesDefaultTransportCloseIdle(t *testing.T) {
	for name, configJSON := range map[string]string{"credential store": "{}", "unreadable credential store": "{"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600))
			t.Setenv("DOCKER_CONFIG", dir)

			conns, requests := new(atomic.Int32), new(atomic.Int32)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusNotFound)
			}))
			srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			srv.Start()
			t.Cleanup(srv.Close)

			target, tag, err := resolveTarget(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/fn:v1")
			require.NoError(t, err)
			repo, ok := target.(*remote.Repository)
			require.True(t, ok)
			repo.PlainHTTP = true
			resolve := func() {
				_, rerr := repo.Resolve(context.Background(), tag)
				require.ErrorIs(t, rerr, errdef.ErrNotFound)
			}
			resolve()
			http.DefaultTransport.(*http.Transport).CloseIdleConnections()
			resolve()
			require.EqualValues(t, 2, requests.Load())
			require.EqualValues(t, 1, conns.Load(), "closing the default transport's idle connections must not touch the target's")
		})
	}
}
