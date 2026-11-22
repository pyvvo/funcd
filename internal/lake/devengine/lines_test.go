//go:build dev

package devengine

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/provider"
)

// ADR-0343: the writer of a dev Lake engine's stderr hands each line on, its colors and empty lines dropped, the
// last unended one at the end.
func TestPrefixLogWriterHandsEachLineOn(t *testing.T) {
	t.Parallel()
	var got []string
	w := &prefixLogWriter{logger: slog.New(slog.DiscardHandler), provider: "sales", keep: func(l string) { got = append(got, l) }}
	_, _ = w.Write([]byte("one\n\x1b[31mtwo\x1b[0m\n\nthree"))
	w.flush()
	require.Equal(t, []string{"one", "two", "three"}, got)
}

// ADR-0343: an engine started by the runtime hands its stderr lines to the func FollowLines set, with its spec.
func TestRuntimeHandsTheEnginesStderrLinesOn(t *testing.T) {
	t.Setenv(fakeModeEnv, "fail")
	r := fakeRuntime(t)
	var mu sync.Mutex
	var got []string
	r.FollowLines(func(spec provider.ProviderSpec, stream, line string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, string(spec.Ref.Name)+" "+stream+" "+line)
	})
	_, err := r.Converge(context.Background(), testSpec())
	require.ErrorContains(t, err, "no_such_table")
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, got, string(testSpec().Ref.Name)+" stderr Lake Error: Table with name no_such_table does not exist!")
}
