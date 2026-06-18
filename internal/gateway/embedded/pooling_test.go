package embedded

import (
	"context"
	"net/http"
	"testing"

	"github.com/green-0-rabbit/funcd/internal/gateway"
)

// scenario: upstream-pooled — the gateway's reverse proxies share one transport that reuses
// upstream connections (MaxIdleConnsPerHost ≫ the stdlib default of 2), so sustained load
// doesn't churn connections into TIME_WAIT and exhaust ephemeral ports (ADR-0041).
func TestUpstreamPooled(t *testing.T) {
	d, ok := New().(*driver)
	if !ok {
		t.Fatal("New() did not return *driver")
	}
	if d.transport == nil {
		t.Fatal("driver has no pooled transport")
	}
	if d.transport.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want greater than the stdlib default %d",
			d.transport.MaxIdleConnsPerHost, http.DefaultMaxIdleConnsPerHost)
	}

	// every compiled route carries the shared pooled transport (not nil → http.DefaultTransport).
	if err := d.ProgramRoutes(context.Background(), []gateway.Route{
		{ID: "r1", PathPrefix: "/function/echo", Upstream: "http://127.0.0.1:9999"},
	}); err != nil {
		t.Fatalf("ProgramRoutes: %v", err)
	}
	for _, cr := range d.table {
		if cr.proxy.Transport != d.transport {
			t.Error("a compiled route does not use the shared pooled transport")
		}
	}
}
