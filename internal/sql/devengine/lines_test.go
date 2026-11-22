//go:build dev

package devengine

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// ADR-0343: the writer of a dev SQL engine's two streams hands each line on, empty lines dropped, the last unended one
// at the end, while it still reads the listen line.
func TestEngineOutputHandsEachLineOn(t *testing.T) {
	t.Parallel()
	var got []string
	o := newEngineOutput(slog.New(slog.DiscardHandler), "notes", "127.0.0.1:1")
	o.keep = func(l string) { got = append(got, l) }
	_, _ = o.Write([]byte("[rqlited] starting\n\n[http] service listening on 127.0.0.1:1\npartial"))
	o.flush()
	require.Equal(t, []string{"[rqlited] starting", "[http] service listening on 127.0.0.1:1", "partial"}, got)
	select {
	case <-o.listening:
	default:
		t.Fatal("the listen line still closes listening")
	}
}
