// Package funcdenv sets the FUNCD_* environment of a test, so that a page test builds its command tree with every
// variable the tree might read either unset or set to a value of its own (ADR-0324).
package funcdenv

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Clear unsets every FUNCD_* variable for the rest of t.
func Clear(t testing.TB) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "FUNCD_") {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}
}

// Sentinels clears the FUNCD_* environment, then sets each of names to a value of its own for the rest of t.
func Sentinels(t testing.TB, names []string) {
	t.Helper()
	Clear(t)
	for _, name := range names {
		t.Setenv(name, "sentinel-"+strings.ToLower(name))
	}
}
