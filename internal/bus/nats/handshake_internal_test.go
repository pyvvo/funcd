package nats

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// slowInfo hands out in-process connections whose first read (the server's INFO) arrives after delay, as
// on a loaded host.
type slowInfo struct {
	srv   *natsserver.Server
	delay time.Duration
}

func (s slowInfo) InProcessConn() (net.Conn, error) {
	c, err := s.srv.InProcessConn()
	if err != nil {
		return nil, err
	}
	return &slowConn{Conn: c, delay: s.delay}, nil
}

type slowConn struct {
	net.Conn
	delay time.Duration
	once  sync.Once
}

func (c *slowConn) Read(p []byte) (int, error) {
	c.once.Do(func() { time.Sleep(c.delay) })
	return c.Conn.Read(p)
}

// Open reaches its own server in process and gives the handshake the startup budget, not nats.go's 2s
// default: an INFO that is slow on a loaded host does not fail startup (issue #545).
func TestIssue545_SlowHandshakeDoesNotFailOpen(t *testing.T) {
	b, err := Open(context.Background(), Options{Storage: MemoryStorage})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	e := b.(*embedded)

	opts := e.nc.Opts
	opts.InProcessServer = slowInfo{srv: e.srv, delay: nats.DefaultTimeout + 500*time.Millisecond}
	nc, err := opts.Connect()
	if err != nil {
		t.Fatalf("handshake with an INFO slower than %s: %v", nats.DefaultTimeout, err)
	}
	nc.Close()
	if got := e.nc.ConnectedAddr(); got != "pipe" {
		t.Fatalf("bus connected to its server at %q, want in process", got)
	}
}
