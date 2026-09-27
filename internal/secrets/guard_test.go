package secrets_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/secrets"
)

// scenario: merge-env-guarded — the single reserved-key guard drops FUNCD_-prefixed keys, keeps the
// rest, applies src over dst for non-reserved keys, and tolerates a nil logger (ADR-0092).
func TestMergeEnvGuarded(t *testing.T) {
	t.Parallel()

	require.True(t, secrets.IsReservedKey("FUNCD_PORT"))
	require.True(t, secrets.IsReservedKey("FUNCD_ANYTHING_FUTURE"))
	require.False(t, secrets.IsReservedKey("API_KEY"))

	dst := map[string]string{"EXISTING": "keep", "OVERRIDE_ME": "old"}
	src := map[string]string{
		"API_KEY":        "secret", // kept
		"OVERRIDE_ME":    "new",    // src wins for a non-reserved key
		"FUNCD_ARTIFACT": "/evil",  // dropped (reserved)
		"FUNCD_PORT":     "9999",   // dropped (reserved)
	}

	// nil logger must be tolerated (no deref).
	secrets.MergeEnvGuarded(dst, src, nil)

	require.Equal(t, "keep", dst["EXISTING"])
	require.Equal(t, "secret", dst["API_KEY"])
	require.Equal(t, "new", dst["OVERRIDE_ME"], "a non-reserved src key overrides dst")
	require.NotContains(t, dst, "FUNCD_ARTIFACT", "a reserved FUNCD_ key is dropped")
	require.NotContains(t, dst, "FUNCD_PORT", "a reserved FUNCD_ key is dropped")
}
