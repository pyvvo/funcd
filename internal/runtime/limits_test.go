package runtime_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/runtime"
)

func TestLimitsFrom(t *testing.T) {
	t.Parallel()
	require.Equal(t, runtime.Limits{CPUs: 0.5, MemoryBytes: 256 << 20}, runtime.LimitsFrom("0.5", "256Mi"))
	require.Equal(t, runtime.Limits{CPUs: 2, MemoryBytes: 1 << 30}, runtime.LimitsFrom(" 2 ", "1GiB"))
	require.Equal(t, runtime.Limits{}, runtime.LimitsFrom("", ""), "empty is no limit")
	require.Equal(t, runtime.Limits{}, runtime.LimitsFrom("NaN", "1G"), "a value that does not parse is no limit")
}

// enforcing is a Runtime that claims LimitEnforcer; only its type matters to LimitWarner.
type enforcing struct{ runtime.Runtime }

func (enforcing) EnforcesLimits() bool { return true }

// LimitWarner logs once per owner, only when the runtime is no LimitEnforcer and a limit is set (ADR-0291).
func TestLimitWarnerLogsOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	var w runtime.LimitWarner
	ctx := context.Background()
	l := runtime.Limits{CPUs: 1}

	w.Warn(ctx, logger, nil, "default/api", runtime.Limits{})
	require.Empty(t, buf.String(), "no limit, nothing to warn about")
	w.Warn(ctx, logger, enforcing{}, "default/api", l)
	require.Empty(t, buf.String(), "an enforcing runtime is trusted")
	w.Warn(ctx, logger, nil, "default/api", l)
	w.Warn(ctx, logger, nil, "default/api", l)
	w.Warn(ctx, logger, nil, "default/web", l)
	require.Equal(t, 2, strings.Count(buf.String(), "does not enforce"), "once per owner")
}
