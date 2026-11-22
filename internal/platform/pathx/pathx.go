// Package pathx is the segment-aware path prefix test that the edge router, the embedded gateway, the Site bundle, the
// contract validator and the local API's facet check share (ADR-0310 Decision 5), the traversal test that the edge's
// static backend and its add-on entries share (ADR-0332 Decision 5), and the kept path that an add-on entry and a
// Function's call of an engine share (ADR-0338 Decision 2).
package pathx

import (
	"path"
	"strings"
)

// HasPrefix reports whether p is prefix or lies under it: "/kv" covers "/kv" and "/kv/a", not "/kv-2". It compares
// the strings as given: it cleans nothing and needs no leading "/".
func HasPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// ContainsDotDot reports whether any segment of p, split at / and \, is exactly "..", mirroring net/http's own
// containsDotDot.
func ContainsDotDot(p string) bool {
	if !strings.Contains(p, "..") {
		return false
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// Kept is the path an add-on entry forwards, its base path kept (ADR-0332 Decision 5): a path with a .. segment is
// refused, and so is a cleaned path outside base or under a private path; the rest goes on cleaned, so another spelling
// of a private path finds it. A Function's call of an engine checks its path the same way (ADR-0338 Decision 2).
func Kept(p, base string, private []string) (string, bool) {
	if ContainsDotDot(p) {
		return "", false
	}
	clean := path.Clean(p)
	if base != "/" && !HasPrefix(clean, base) {
		return "", false
	}
	for _, pp := range private {
		if HasPrefix(clean, pp) {
			return "", false
		}
	}
	return clean, true
}
