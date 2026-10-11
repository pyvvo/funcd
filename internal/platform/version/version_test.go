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

// TestCompareVersions orders scripts/build.sh stamps (ADR-0207): describe counts after their tag, -dirty equal, a
// prerelease before its tag, a bare hash and dev unordered; restore.CheckVersion's stamps order as semver.
func TestCompareVersions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v0.8.0", "v0.8.0-9-gabc1234", -1, true},
		{"v0.8.0-9-gabc1234", "v0.8.0-10-gabc1234", -1, true},
		{"v0.8.0-10-gdef5678", "v0.8.0-9-gabc1234", 1, true},
		{"v0.8.0-dirty", "v0.8.0", 0, true},
		{"v0.8.0-3-gabc1234-dirty", "v0.8.0-3-gabc1234", 0, true},
		{"v0.8.0-rc.1", "v0.8.0", -1, true},
		{"v0.8.0-rc.1-2-gabc1234", "v0.8.0-rc.1", 1, true},
		{"v0.9.0", "v0.8.1", 1, true},
		{"v0.7.2", "v0.8.1", -1, true},
		{"v0.8.0", "v0.8.0", 0, true},
		{"abc1234", "v0.8.0", 0, false},
		{"v0.8.0", "abc1234-dirty", 0, false},
		{"dev", "v0.8.0", 0, false},
		{"v0.8.0", "dev", 0, false},
	} {
		got, ok := version.Compare(c.a, c.b)
		require.Equal(t, c.ok, ok, "%s vs %s", c.a, c.b)
		require.Equal(t, c.want, got, "%s vs %s", c.a, c.b)
	}
}
