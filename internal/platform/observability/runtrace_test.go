package observability_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/observability"
)

// ADR-0102 and ADR-0105: a trace context is a 32-hex trace ID and a 16-hex span ID, new on each call.
func TestNewTraceContext(t *testing.T) {
	t.Parallel()
	trace, span := observability.NewTraceContext()
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), trace)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), span)
	again, _ := observability.NewTraceContext()
	require.NotEqual(t, trace, again)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), observability.NewSpanID())
}

// ADR-0100: a status keeps an error's first line, cut at MaxStatusError bytes with an ellipsis.
func TestStatusError(t *testing.T) {
	t.Parallel()
	require.Equal(t, "boom", observability.StatusError("boom\n  at handler.js:3"))
	long := strings.Repeat("x", observability.MaxStatusError+10)
	require.Equal(t, long[:observability.MaxStatusError]+"…", observability.StatusError(long))
	require.Equal(t, 512, observability.MaxStatusError)
}
