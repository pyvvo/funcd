package local_test

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
