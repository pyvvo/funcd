package freeport

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIssue758_PortIsNeverEphemeral: Port's ports lie below the range a :0 bind draws from, so between a test's
// free and rebind, another test process's :0 bind is never given the port; each is free, and none repeats.
func TestIssue758_PortIsNeverEphemeral(t *testing.T) {
	low := ephemeralFirst(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.GreaterOrEqual(t, ln.Addr().(*net.TCPAddr).Port, low, "a :0 bind draws from the ephemeral range")
	require.NoError(t, ln.Close())

	seen := map[int]bool{}
	for range 3 {
		port := Port(t)
		require.Less(t, port, low, "another test process's :0 bind can be given this port")
		require.False(t, seen[port], "one process never gets a port twice")
		seen[port] = true
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		require.NoError(t, err, "the port is free")
		require.NoError(t, ln.Close())
	}
}

// ephemeralFirst returns the lowest port the OS gives a :0 bind.
func ephemeralFirst(t *testing.T) int {
	t.Helper()
	var out []byte
	var err error
	switch runtime.GOOS {
	case "linux":
		out, err = os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	case "darwin":
		out, err = exec.Command("sysctl", "-n", "net.inet.ip.portrange.first").Output()
	default:
		t.Skipf("no ephemeral port range lookup on %s", runtime.GOOS)
	}
	require.NoError(t, err)
	fields := strings.Fields(string(out))
	require.NotEmpty(t, fields)
	low, err := strconv.Atoi(fields[0])
	require.NoError(t, err)
	return low
}
