package restore

import (
	"golang.org/x/mod/semver"

	"github.com/pyvvo/funcd/api/fault"
)

// CheckVersion is Decision 4's rule: a generation written by funcd writer restores on funcd binary when writer's
// major.minor is the same or older; a newer one is fault.Invalid naming both. A stamp that is not semver ("dev", or
// git describe's bare hash in a clone without tags) orders nothing, so it restores and Run warns.
func CheckVersion(writer, binary string) error {
	if !ordered(writer, binary) {
		return nil
	}
	if semver.Compare(semver.MajorMinor(writer), semver.MajorMinor(binary)) > 0 {
		return fault.Invalidf("restore.CheckVersion", "the generation was written by funcd %s, newer than this funcd %s: "+
			"restore it with funcd %s or newer", writer, binary, semver.MajorMinor(writer))
	}
	return nil
}

// ordered reports whether both stamps are semver, so CheckVersion compares them.
func ordered(writer, binary string) bool { return semver.IsValid(writer) && semver.IsValid(binary) }
