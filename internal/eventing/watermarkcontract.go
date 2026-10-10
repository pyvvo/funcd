package eventing

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// WatermarkContract exercises the Watermark port against a driver (ADR-0157, ADR-0201): every driver's test runs
// it, so the event store's seen-list view on disk and in memory and MemWatermark are held to one behavior.
func WatermarkContract(t *testing.T, newWatermark func(t *testing.T) Watermark) {
	t.Helper()
	ctx := context.Background()

	t.Run("missing record loads empty", func(t *testing.T) {
		s, err := newWatermark(t).Load(ctx, "lake", "drops", "arrived")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Seen == nil || len(s.Seen) != 0 || s.UID != "" {
			t.Fatalf("a missing record loads %+v, want an empty one with a non-nil Seen", s)
		}
	})

	t.Run("round trip", func(t *testing.T) {
		wm := newWatermark(t)
		in := SeenList{Bucket: "raw", Prefix: "drop/", UID: "uid-1", Seen: map[string]string{
			"drop/a<b>.parquet": "1759536000000000000-1234",
			"drop/x&y":          "1-2",
			"drop/ctl\x01key":   "3-4",
		}}
		mustSave(t, wm, "drops", "arrived", in)
		out := mustLoad(t, wm, "drops", "arrived")
		if !reflect.DeepEqual(in, out) {
			t.Fatalf("loaded %+v, want %+v", out, in)
		}
		out.Seen["drop/new"] = "5-6"
		if _, ok := mustLoad(t, wm, "drops", "arrived").Seen["drop/new"]; ok {
			t.Fatal("a loaded record is not a copy")
		}
	})

	t.Run("rewrite replaces the record", func(t *testing.T) {
		wm := newWatermark(t)
		mustSave(t, wm, "drops", "arrived", SeenList{Bucket: "raw", Seen: map[string]string{"drop/a": "1-1", "drop/b": "2-2"}})
		want := SeenList{Bucket: "raw", Seen: map[string]string{"drop/b": "2-2"}}
		mustSave(t, wm, "drops", "arrived", want)
		if got := mustLoad(t, wm, "drops", "arrived"); !reflect.DeepEqual(want, got) {
			t.Fatalf("loaded %+v after a rewrite, want %+v", got, want)
		}
	})

	t.Run("nil seen loads empty", func(t *testing.T) {
		wm := newWatermark(t)
		mustSave(t, wm, "drops", "arrived", SeenList{Bucket: "raw"})
		if s := mustLoad(t, wm, "drops", "arrived"); s.Seen == nil {
			t.Fatal(`a record saved with "seen":null loads a nil Seen`)
		}
	})

	t.Run("a 2 MiB record round trips", func(t *testing.T) {
		wm := newWatermark(t)
		in := LargeSeenList(2 << 20)
		mustSave(t, wm, "drops", "arrived", in)
		if out := mustLoad(t, wm, "drops", "arrived"); !reflect.DeepEqual(in, out) {
			t.Fatalf("a 2 MiB record loads %d keys, want %d", len(out.Seen), len(in.Seen))
		}
	})

	t.Run("delete and list sources", func(t *testing.T) {
		wm := newWatermark(t)
		if err := wm.Delete(ctx, "lake", "ghost"); err != nil {
			t.Fatalf("deleting a source with no record: %v", err)
		}
		rec := SeenList{Bucket: "raw", Seen: map[string]string{"drop/a": "1-1"}}
		mustSave(t, wm, "drops", "arrived", rec)
		mustSave(t, wm, "drops", "other", rec)
		mustSave(t, wm, "drops2", "arrived", rec)
		mustSave(t, wm, "drops-x", "arrived", rec)
		requireSources(t, wm, "drops", "drops-x", "drops2")

		if err := wm.Delete(ctx, "lake", "drops"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if gone := mustLoad(t, wm, "drops", "arrived"); len(gone.Seen) != 0 {
			t.Fatalf("a deleted record loads %+v", gone)
		}
		for _, src := range []v1.ObjectName{"drops2", "drops-x"} {
			if kept := mustLoad(t, wm, src, "arrived"); !reflect.DeepEqual(rec, kept) {
				t.Fatalf("deleting drops changed %s: %+v", src, kept)
			}
		}
		requireSources(t, wm, "drops-x", "drops2")
	})
}

// LargeSeenList returns a SeenList whose JSON is at least size bytes.
func LargeSeenList(size int) SeenList {
	s := SeenList{Bucket: "raw", Prefix: "drop/", UID: "uid-1", Seen: map[string]string{}}
	for i := range size/64 + 1 {
		s.Seen[fmt.Sprintf("drop/%08d-%s", i, strings.Repeat("x", 24))] = fmt.Sprintf("1759536000000000000-%08d", i)
	}
	return s
}

func mustSave(t *testing.T, wm Watermark, source, event v1.ObjectName, s SeenList) {
	t.Helper()
	if err := wm.Save(context.Background(), "lake", source, event, s); err != nil {
		t.Fatalf("Save %s/%s: %v", source, event, err)
	}
}

func mustLoad(t *testing.T, wm Watermark, source, event v1.ObjectName) SeenList {
	t.Helper()
	s, err := wm.Load(context.Background(), "lake", source, event)
	if err != nil {
		t.Fatalf("Load %s/%s: %v", source, event, err)
	}
	return s
}

func requireSources(t *testing.T, wm Watermark, want ...v1.ObjectName) {
	t.Helper()
	srcs, err := wm.ListSources(context.Background())
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	var got []v1.ObjectName
	for _, s := range srcs {
		if s.Namespace != "lake" {
			t.Fatalf("ListSources returned %+v", s)
		}
		got = append(got, s.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("ListSources = %v, want each of %v once", got, want)
	}
}
