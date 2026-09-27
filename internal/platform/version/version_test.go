package version_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/version"
)

// scenario: version-defaults-unstamped.
func TestScenarioVersionDefaultsUnstamped(t *testing.T) {
	t.Parallel()
	i := version.Get()
	require.Equal(t, "dev", i.Version, "an un-stamped build reports the honest default")
	require.NotEmpty(t, i.Commit)
	require.NotEmpty(t, i.Date)
	require.NotEmpty(t, i.GoVersion, "GoVersion is always filled from the runtime")
	require.Contains(t, i.Platform, "/", "Platform is GOOS/GOARCH")
}

// scenario: version-string-well-formed.
func TestScenarioVersionStringWellFormed(t *testing.T) {
	t.Parallel()
	i := version.Info{Version: "v1.2.3", Commit: "abc123", Date: "2026-06-15", GoVersion: "go1.26", Platform: "linux/amd64"}
	s := i.String()
	require.True(t, strings.HasPrefix(s, "funcd "), "one human line starting with funcd")
	for _, want := range []string{"v1.2.3", "abc123", "2026-06-15", "go1.26", "linux/amd64"} {
		require.Contains(t, s, want)
	}
}
