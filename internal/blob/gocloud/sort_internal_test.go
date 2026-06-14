package gocloud

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/blob"
)

// Proves List's sort directly (independent of any backend's own ordering): a shuffled
// input must come out lexically sorted. This FAILS if sortByKey is a no-op — unlike the
// end-to-end list-by-prefix test, whose mem/file backends already return sorted output.
func TestSortByKey(t *testing.T) {
	items := []blob.Attributes{{Key: "b/1"}, {Key: "a/2"}, {Key: "a/1"}, {Key: "c"}}
	sortByKey(items)
	want := []string{"a/1", "a/2", "b/1", "c"}
	for i, w := range want {
		if items[i].Key != w {
			t.Fatalf("sortByKey[%d]=%q want %q (full: %+v)", i, items[i].Key, w, items)
		}
	}
}
