package controlplane

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

type InlineInner struct {
	Z string `json:"z"`
}

type InlineMid struct {
	InlineInner `json:",inline"`
	Y           string `json:"y"`
}

type InlineOuter struct {
	InlineMid `json:",inline"`
	X         string `json:"x"`
}

// TestIssue166_NestedInlineEmbedsFlatten: flattening a parent first still promotes the fields of an
// `,inline` embed that itself embeds `,inline`, and neither embedded type is left in the components.
func TestIssue166_NestedInlineEmbedsFlatten(t *testing.T) {
	t.Parallel()
	r := newInlineRegistry()
	r.Registry.Schema(reflect.TypeFor[InlineOuter](), true, "")
	r.flatten("InlineOuter")

	s := r.Map()["InlineOuter"]
	require.ElementsMatch(t, []string{"x", "y", "z"}, slices.Collect(maps.Keys(s.Properties)))
	require.ElementsMatch(t, []string{"x", "y", "z"}, s.Required)

	out, err := json.Marshal(r)
	require.NoError(t, err)
	var components map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &components))
	require.ElementsMatch(t, []string{"InlineOuter"}, slices.Collect(maps.Keys(components)))
}
