package funcd

import (
	"context"
	"net"
	"testing"
	"time"
)

// freeLoopbackAddr returns an unused 127.0.0.1:port for the gateway listener.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// scenario: disabled-by-default (ADR-0080) — New(InMemory()) with no S3 config wires no
// gateway and opens no listener.
func TestScenarioS3GatewayDisabledByDefault(t *testing.T) {
	p, err := New(InMemory())
	if err != nil {
		t.Fatalf("New(InMemory): %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	if p.s3gw != nil {
		t.Fatal("the S3 gateway must be absent unless WithS3Gateway is set")
	}
}

// scenario: enabled wires the gateway + opens the listener (ADR-0080/0085) — WithS3Gateway
// builds the server; once Run binds the node-private addr, a TCP dial succeeds. Disabled by
// default is the contrast above.
func TestScenarioS3GatewayEnabledOpensListener(t *testing.T) {
	addr := freeLoopbackAddr(t)
	p, err := New(InMemory(), WithS3Gateway(addr, 0, "", t.TempDir()))
	if err != nil {
		t.Fatalf("New(InMemory, WithS3Gateway): %v", err)
	}
	if p.s3gw == nil {
		t.Fatal("WithS3Gateway must wire the S3 gateway")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = p.Shutdown(context.Background()) })

	// Wait until the gateway listener is ready (bound + accepting).
	select {
	case <-p.s3gw.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("s3 gateway did not become ready")
	}
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial s3 gateway listener: %v", derr)
	}
	_ = conn.Close()
}
