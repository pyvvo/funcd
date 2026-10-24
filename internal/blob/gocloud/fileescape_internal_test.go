package gocloud

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// ADR-0278 Decision 1: funcd's writer stores a key at fileblob's path for it, escaped keys included.
func TestFileEscapeKey_MatchesFileblob(t *testing.T) {
	ctx := context.Background()
	runes := rapid.SampledFrom([]string{"a", "b", ".", "/", "\x01", "é"})
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		b, err := Open(ctx, FileURL(dir))
		if err != nil {
			rt.Fatalf("Open: %v", err)
		}
		defer func() { _ = b.Close() }()
		key := strings.Join(rapid.SliceOfN(runes, 1, 8).Draw(rt, "runes"), "")
		if b.(*bucket).checkKey("test", key) != nil {
			rt.Skip("a key the file backend refuses")
		}
		if err := b.(*bucket).b.WriteAll(ctx, key, []byte(key), nil); err != nil {
			rt.Skip("a key fileblob cannot store")
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(fileEscapeKey(key)))) //nolint:gosec // the test's own temp dir
		if err != nil || string(got) != key {
			rt.Fatalf("key %q: not stored at %q: %v", key, fileEscapeKey(key), err)
		}
	})
}
