package store

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// timelineBytes is the timeline's size: 64 random bits, 16 lowercase hex characters (ADR-0202 Decision 3).
const timelineBytes = 8

// Version is a parsed resourceVersion (ADR-0202): "<timeline>-<n>", or a plain "<n>" written before timelines
// (Timeline ""). N orders versions only inside one timeline.
type Version struct {
	Timeline string // lowercase hex
	N        uint64
}

// ParseVersion parses a resourceVersion; an empty or malformed one is fault.Invalid.
func ParseVersion(rv string) (Version, error) {
	const op = "store.ParseVersion"
	timeline, n, found := strings.Cut(rv, "-")
	if !found {
		timeline, n = "", rv
	} else if !isLowerHex(timeline) {
		return Version{}, fault.Invalidf(op, "invalid resourceVersion %q", rv)
	}
	num, err := strconv.ParseUint(n, 10, 64)
	if err != nil {
		return Version{}, fault.Invalidf(op, "invalid resourceVersion %q", rv)
	}
	return Version{Timeline: timeline, N: num}, nil
}

// String formats v: "<timeline>-<n>", or "<n>" when Timeline is "".
func (v Version) String() string {
	n := strconv.FormatUint(v.N, 10)
	if v.Timeline == "" {
		return n
	}
	return v.Timeline + "-" + n
}

// ResumePoint is a watcher's resume point after it saw v: v when its timeline differs from seen's or its N is
// larger, else seen. A timeline change moves the point even backward: a re-watch from another timeline re-lists.
func ResumePoint(seen, v Version) Version {
	if v.Timeline != seen.Timeline || v.N > seen.N {
		return v
	}
	return seen
}

// newTimeline mints a random timeline.
func newTimeline() (string, error) {
	var b [timelineBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Internalf("store.newTimeline", "generate timeline: %v", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
