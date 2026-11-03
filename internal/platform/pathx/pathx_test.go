package pathx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHasPrefix: a prefix covers itself and the paths under it, segment by segment, compared as given (ADR-0310
// Decision 5): no clean, no leading "/" required, and "/" is no catch-all.
func TestHasPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		p, prefix string
		want      bool
	}{
		{"/kv", "/kv", true},
		{"/kv/a", "/kv", true},
		{"/kv/a/b", "/kv", true},
		{"/kv/", "/kv", true},
		{"/kv-2", "/kv", false},
		{"/kvx/a", "/kv", false},
		{"/k", "/kv", false},
		{"", "/kv", false},
		{"/function/../x", "/function", true},
		{"//function", "/function", false},
		{"_runtime", "_runtime", true},
		{"_runtime/index.html", "_runtime", true},
		{"_runtimex", "_runtime", false},
		{"file:///s.json#/oneOf/0/properties/a", "file:///s.json#/oneOf/0", true},
		{"file:///s.json#/oneOf/01", "file:///s.json#/oneOf/0", false},
		{"/a", "/", false},
	} {
		require.Equal(t, tc.want, HasPrefix(tc.p, tc.prefix), "HasPrefix(%q, %q)", tc.p, tc.prefix)
	}
}
