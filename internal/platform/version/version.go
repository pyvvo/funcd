// Package version exposes funcd's build identity. Version/Commit/Date are written
// at link time via -ldflags "-X …version.Version=…" (the one sanctioned mutable-globals
// seam — ADR-0002 §5 / ADR-0026); GoVersion + Platform come from the runtime. An
// un-stamped build returns the honest "dev"/"none"/"unknown" defaults so it's obvious.
package version

import (
	"fmt"
	"runtime"
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
