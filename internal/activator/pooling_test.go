package activator

import (
	"net/http"
	"testing"
)

// scenario: upstream-pooled — the data-plane forward reuses keep-alive connections to the
// function worker; the pooled transport raises MaxIdleConnsPerHost far above the stdlib
// default of 2, so sustained load doesn't churn ports into TIME_WAIT (ADR-0041). The pool is
// keyed by upstream host:port (per-replica unique), so reuse never crosses a tenant boundary.
func TestPooledTransport(t *testing.T) {
	t.Parallel()
	tr := newPooledTransport()
	if tr == nil {
		t.Fatal("newPooledTransport returned nil")
	}
	if tr.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want greater than the stdlib default %d",
			tr.MaxIdleConnsPerHost, http.DefaultMaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout not set — idle connections never reclaimed")
	}
}
