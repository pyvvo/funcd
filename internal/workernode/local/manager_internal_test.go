package local

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIssue163_LocalAPIReapsIdleKeepAliveConns: a worker can send one request per connection and then
// hold each keep-alive connection idle; without an idle deadline every held connection pins a daemon
// goroutine and its buffers for as long as the worker lives.
func TestIssue163_LocalAPIReapsIdleKeepAliveConns(t *testing.T) {
	dir, err := os.MkdirTemp("", "i163") // short: a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	m := NewManager(dir, nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.Close)
	_, err = m.SocketFor("team-a", "a")
	require.NoError(t, err)

	m.mu.Lock()
	srv := m.active["team-a/a"].srv
	m.mu.Unlock()
	require.Positive(t, srv.IdleTimeout, "the local API must close a keep-alive connection left idle")
}
