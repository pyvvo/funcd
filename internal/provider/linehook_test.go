package provider_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/provider"
)

// A LineHook hands an engine's lines, with its spec and stream, to the func FollowLines set, and has nothing to hand
// them to before (ADR-0343).
func TestLineHook(t *testing.T) {
	t.Parallel()
	var h provider.LineHook
	spec := provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "sales"}}
	require.Nil(t, h.LinesOf(spec, "stderr"))
	var got []string
	h.FollowLines(func(s provider.ProviderSpec, stream, line string) {
		got = append(got, string(s.Ref.Name)+" "+stream+" "+line)
	})
	h.LinesOf(spec, "stderr")("ready")
	require.Equal(t, []string{"sales stderr ready"}, got)
}
