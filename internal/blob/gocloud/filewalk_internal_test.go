package gocloud

import (
	"context"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/pyvvo/funcd/internal/blob"
)

// ADR-0184: fileWalk returns exactly the stored keys under prefix after `after`, in key order, also for keys
// whose stored path is escaped ("//", "../", a trailing "/", a control rune) or whose directory sorts apart
// from its sibling files ("a-", "a.", "a/").
func TestFileWalk_MatchesSortedReference(t *testing.T) {
	ctx := context.Background()
	runes := rapid.SampledFrom([]string{"a", "b", "-", ".", "/", "\x01"})
	text := func(lo, hi int) *rapid.Generator[string] {
		return rapid.Custom(func(rt *rapid.T) string {
			return strings.Join(rapid.SliceOfN(runes, lo, hi).Draw(rt, "runes"), "")
		})
	}
	rapid.Check(t, func(rt *rapid.T) {
		b, err := Open(ctx, FileURL(t.TempDir()))
		if err != nil {
			rt.Fatalf("Open: %v", err)
		}
		defer func() { _ = b.Close() }()
		k := b.(*bucket)
		var stored []string
		for _, key := range rapid.SliceOfN(text(1, 6), 1, 12).Draw(rt, "keys") {
			if k.Put(ctx, key, []byte(key), blob.PutOptions{}) == nil {
				stored = append(stored, key)
			}
		}
		slices.Sort(stored)
		stored = slices.Compact(stored)
		prefix, after := text(0, 4).Draw(rt, "prefix"), text(0, 4).Draw(rt, "after")
		limit := rapid.SampledFrom([]int{-1, 1, 2, 3, 5}).Draw(rt, "limit")

		var want []string
		for _, key := range stored {
			if strings.HasPrefix(key, prefix) && key > after {
				want = append(want, key)
			}
		}
		wantMore := limit >= 0 && len(want) > limit
		if wantMore {
			want = want[:limit]
		}
		items, more, err := k.fileWalk(ctx, prefix, after, limit)
		if err != nil {
			rt.Fatalf("fileWalk: %v", err)
		}
		got := make([]string, 0, len(items))
		for _, it := range items {
			got = append(got, it.Key)
		}
		if !slices.Equal(got, want) || more != wantMore {
			rt.Fatalf("fileWalk(%q, %q, %d) over %q: got (%q, more %v) want (%q, more %v)", prefix, after, limit, stored, got, more, want, wantMore)
		}
	})
}
