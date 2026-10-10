// Package version exposes funcd's build identity. Version/Commit/Date are written
// at link time via -ldflags "-X …version.Version=…" (the one sanctioned mutable-globals
// seam — ADR-0002 §5 / ADR-0026); GoVersion + Platform come from the runtime. An
// un-stamped build returns the honest "dev"/"none"/"unknown" defaults so it's obvious.
package version

import (
	"cmp"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// The ldflags-stamp seam. //nolint is required — gochecknoglobals fires on these names
// (same inline form as the lookup-table globals in api/fault/problem.go, pkg/sdk/kinds.go).
//
//nolint:gochecknoglobals // sanctioned ldflags-stamp seam (write-once at link, read-only after) — ADR-0002 §5 exception
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info is the assembled build identity.
type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	Platform  string
}

// Get returns this build's Info: the stamp vars plus the runtime-derived Go version
// and OS/arch (always filled, even on an un-stamped build).
func Get() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the build identity as one human line.
func (i Info) String() string {
	return fmt.Sprintf("funcd %s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.Date, i.GoVersion, i.Platform)
}

// Compare orders two scripts/build.sh stamps (ADR-0207): a trailing -dirty and -<n>-g<hash> are cut, the tags compare
// by semver, equal tags by n (none: 0). ok is false when either is "dev" or no semver tag is left (a bare hash: a
// clone without tags).
func Compare(a, b string) (c int, ok bool) {
	ta, na := cutDescribe(a)
	tb, nb := cutDescribe(b)
	if !semver.IsValid(ta) || !semver.IsValid(tb) {
		return 0, false
	}
	return cmp.Or(semver.Compare(ta, tb), cmp.Compare(na, nb)), true
}

// cutDescribe splits git describe's <tag>-<n>-g<hash>[-dirty] into the tag and n; a stamp without them is its own tag.
func cutDescribe(s string) (tag string, n int) {
	s = strings.TrimSuffix(s, "-dirty")
	rest, hash, ok := cutLast(s, "-g")
	if !ok || hash == "" || strings.Trim(hash, "0123456789abcdef") != "" {
		return s, 0
	}
	tag, count, ok := cutLast(rest, "-")
	if !ok {
		return s, 0
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 0 {
		return s, 0
	}
	return tag, n
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}
