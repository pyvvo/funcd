package funclog

import (
	"context"
	"io"
	"log/slog"

	"github.com/pyvvo/funcd/internal/platform/clock"
)

// RoutePoolWithClock is RoutePool with the clock its drop warning is rate-limited by.
func RoutePoolWithClock(ctx context.Context, r io.Reader, sinks Sinks, pool Resource, isMember func(name string) bool, log *slog.Logger, clk clock.Clock) error {
	return routePool(ctx, r, sinks, pool, isMember, log, clk)
}
