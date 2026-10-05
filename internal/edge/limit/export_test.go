package limit

import (
	"net/http"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// ChainAt is Chain on clk.
func ChainAt(cfg Config, clk clock.Clock) func(http.Handler) http.Handler { return chain(cfg, clk) }

// NewTargetLimiterAt is NewTargetLimiter on clk.
func NewTargetLimiterAt(cfg Config, clk clock.Clock) *TargetLimiter {
	return newTargetLimiter(cfg, clk)
}

// Buckets is the number of buckets the limiter holds.
func (l *TargetLimiter) Buckets() int {
	l.t.mu.Lock()
	defer l.t.mu.Unlock()
	return len(l.t.byKey)
}
