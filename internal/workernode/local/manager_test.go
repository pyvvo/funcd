package local_test

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// Issue #41: a namespace and a name of 63 characters each are valid, and the function still gets a
// socket it can dial.
func TestIssue41_MaxLengthNamesGetALocalAPISocket(t *testing.T) {
	// not t.TempDir(): it embeds the test name, which alone nears the macOS socket path limit
	dir, err := os.MkdirTemp("", "i41")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	m := local.NewManager(dir, fakeStore{}, fakeInvoker{}, nil, nil, nil, nil)
	t.Cleanup(m.Close)

	sock, err := m.SocketFor(v1.NamespaceName(strings.Repeat("n", 63)), v1.ObjectName(strings.Repeat("f", 63)))
	require.NoError(t, err)
	conn, err := net.Dial("unix", sock)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

// Issue #433: after Close, SocketFor must fail and create nothing (a reconciler still running during
// Platform.Shutdown would otherwise re-create the removed socket dir), and Close must return only once
// every listener is closed.
func TestIssue433_SocketForAfterCloseFailsAndCreatesNothing(t *testing.T) {
	dir, err := os.MkdirTemp("", "i433")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	m := local.NewManager(dir, fakeStore{}, fakeInvoker{}, nil, nil, nil, nil)
	t.Cleanup(m.Close)

	sock, err := m.SocketFor("team-a", "a")
	require.NoError(t, err)
	m.Close()
	if conn, err := net.Dial("unix", sock); err == nil {
		_ = conn.Close()
		t.Fatal("a local API listener still accepts connections after Close returned")
	}

	require.NoError(t, os.RemoveAll(dir))
	_, err = m.SocketFor("team-a", "b")
	require.Error(t, err, "SocketFor must fail after Close")
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.NoDirExists(t, dir, "SocketFor after Close must not re-create the socket dir")
}
