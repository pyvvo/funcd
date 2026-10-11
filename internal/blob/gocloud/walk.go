package gocloud

import (
	"context"
	"path/filepath"
	"strings"
)

// WalkFileKeys calls fn with each key under prefix of the file:// store at dir, in key order, and the path of its
// data file, decoding paths as the store does: no ".attrs" sidecar or ".funcd-tmp" temp file (ADR-0208 Decision 5).
// A directory that cannot be read fails the walk; one gone during it is skipped. fn's error ends the walk.
func WalkFileKeys(ctx context.Context, dir, prefix string, fn func(key, path string) error) error {
	start := fileWalkPrefix(prefix)
	start = start[:strings.LastIndex(start, "/")+1]
	w := fileWalker{prefix: prefix, visit: fn}
	return w.walk(ctx, filepath.Join(dir, filepath.FromSlash(start)), start)
}
