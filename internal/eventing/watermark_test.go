package eventing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
)

func TestWatermarkContract(t *testing.T) {
	drivers := map[string]func(t *testing.T) Watermark{
		"kv": func(t *testing.T) Watermark {
			t.Helper()
			wm, err := NewKVWatermark(kvmemory.New())
			require.NoError(t, err)
			return wm
		},
		"mem": func(*testing.T) Watermark { return NewMemWatermark() },
	}
	for name, mk := range drivers {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			t.Run("missing record loads empty", func(t *testing.T) {
				s, err := mk(t).Load(ctx, "lake", "drops", "arrived")
				require.NoError(t, err)
				require.NotNil(t, s.Seen)
				require.Empty(t, s.Seen)
				require.Empty(t, s.UID)
			})

			t.Run("round trip", func(t *testing.T) {
				wm := mk(t)
				in := SeenList{Bucket: "raw", Prefix: "drop/", UID: "uid-1", Seen: map[string]string{
					"drop/a<b>.parquet": "1759536000000000000-1234",
					"drop/x&y":          "1-2",
					"drop/ctl\x01key":   "3-4",
				}}
				require.NoError(t, wm.Save(ctx, "lake", "drops", "arrived", in))
				out, err := wm.Load(ctx, "lake", "drops", "arrived")
				require.NoError(t, err)
				require.Equal(t, in, out)
				out.Seen["drop/new"] = "5-6"
				again, err := wm.Load(ctx, "lake", "drops", "arrived")
				require.NoError(t, err)
				require.NotContains(t, again.Seen, "drop/new", "a loaded record is a copy")
			})

			t.Run("nil seen loads empty", func(t *testing.T) {
				wm := mk(t)
				require.NoError(t, wm.Save(ctx, "lake", "drops", "arrived", SeenList{Bucket: "raw"}))
				s, err := wm.Load(ctx, "lake", "drops", "arrived")
				require.NoError(t, err)
				require.NotNil(t, s.Seen)
			})

			t.Run("delete and list sources", func(t *testing.T) {
				wm := mk(t)
				require.NoError(t, wm.Delete(ctx, "lake", "ghost"), "a source with no record is not an error")
				rec := SeenList{Bucket: "raw", Seen: map[string]string{"drop/a": "1-1"}}
				require.NoError(t, wm.Save(ctx, "lake", "drops", "arrived", rec))
				require.NoError(t, wm.Save(ctx, "lake", "drops", "other", rec))
				require.NoError(t, wm.Save(ctx, "lake", "drops2", "arrived", rec))
				srcs, err := wm.ListSources(ctx)
				require.NoError(t, err)
				require.ElementsMatch(t, []SourceRef{{Namespace: "lake", Name: "drops"}, {Namespace: "lake", Name: "drops2"}}, srcs)

				require.NoError(t, wm.Delete(ctx, "lake", "drops"))
				gone, err := wm.Load(ctx, "lake", "drops", "arrived")
				require.NoError(t, err)
				require.Empty(t, gone.Seen)
				kept, err := wm.Load(ctx, "lake", "drops2", "arrived")
				require.NoError(t, err)
				require.Equal(t, rec, kept, "deleting drops leaves drops2")
				srcs, err = wm.ListSources(ctx)
				require.NoError(t, err)
				require.Equal(t, []SourceRef{{Namespace: "lake", Name: "drops2"}}, srcs)
			})
		})
	}
}

func TestKVWatermarkLoadsNullSeenAndOldCursor(t *testing.T) {
	ctx := context.Background()
	kv := kvmemory.New()
	wm, err := NewKVWatermark(kv)
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, watermarkKey("lake", "drops", "null"), []byte(`{"bucket":"raw","prefix":"drop/","uid":"u","seen":null}`)))
	require.NoError(t, kv.Put(ctx, watermarkKey("lake", "drops", "cursor"), []byte(`{"maxModTime":"2026-01-01T00:00:00Z","keysAtMax":["drop/a"]}`)))

	s, err := wm.Load(ctx, "lake", "drops", "null")
	require.NoError(t, err)
	require.NotNil(t, s.Seen)

	old, err := wm.Load(ctx, "lake", "drops", "cursor")
	require.NoError(t, err)
	require.Equal(t, SeenList{Seen: map[string]string{}}, old, "an old Cursor record decodes with no location")
}
