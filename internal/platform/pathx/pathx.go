// Package pathx is the segment-aware path prefix test that the edge router, the embedded gateway, the Site bundle, the
// contract validator and the local API's facet check share (ADR-0310 Decision 5).
package pathx

import "strings"

// HasPrefix reports whether p is prefix or lies under it: "/kv" covers "/kv" and "/kv/a", not "/kv-2". It compares
// the strings as given: it cleans nothing and needs no leading "/".
func HasPrefix(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}
