// Package freeport gives a test a free loopback TCP port that no :0 bind is ever given, for a test that frees a
// port and binds it again (#758): another test process's :0 bind can take an ephemeral port in between.
package freeport

import (
	"math/rand/v2"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
)

// The range lies below the ports a :0 bind draws from: 32768-60999 on Linux, 49152-65535 on macOS.
const (
	first = 20000
	size  = 10000
)

// cursor starts at a random place in the range, so test processes that run at once rarely probe the same port, and
// advances on every probe, so one process never gets a port twice.
//
//nolint:gochecknoglobals // per-process probe position
var cursor = func() *atomic.Int64 {
	c := new(atomic.Int64)
	c.Store(rand.Int64N(size))
	return c
}()

// Port returns a 127.0.0.1 port in [20000, 30000) that is free now.
func Port(t testing.TB) int {
	t.Helper()
	for range size {
		port := first + int(cursor.Add(1)%size)
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil && ln.Close() == nil {
			return port
		}
	}
	t.Fatalf("no free 127.0.0.1 port in [%d, %d)", first, first+size)
	return 0
}
