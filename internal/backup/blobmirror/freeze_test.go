package blobmirror_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
)

// With blob.dir a mount point, link(2) fails with EXDEV: the run warns once, reads the live files with the same
// checks, and a file replaced before its copy is read again.
func TestFreezeEXDEVReadsLive(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put("x", "x before")
	f.put("y", "y")
	blobmirror.Set(f.mirror, nil, nil, func(_, newname string) error {
		return &os.LinkError{Op: "link", New: newname, Err: syscall.EXDEV}
	}, &blobmirror.Hooks{BeforeCopy: func() { f.put("x", "x replaced before its copy") }})
	m := f.run()
	require.Equal(t, 1, strings.Count(f.logs.String(), "not on the image's device"))
	require.Equal(t, map[string]string{"x": "x replaced before its copy", "y": "y"}, f.restored(m.Generation, nil))
}

// A torn .attrs freezes the key again; a checkpoint whose bytes miss the recorded MD5 is linked again before its
// prefix is walked; the fourth re-freeze of a key fails the run with fault.Conflict naming it.
func TestAttrsMismatchRefrozen(t *testing.T) {
	t.Parallel()
	t.Run("torn attrs", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		f.put("k", "data")
		attrs := filepath.Join(f.dir, "k.attrs")
		good, err := os.ReadFile(attrs) //nolint:gosec // the test's own store
		require.NoError(t, err)
		links := 0
		blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(key string) {
			links++
			data := good
			if links == 1 {
				data = good[:len(good)/2]
			}
			require.NoError(t, os.WriteFile(attrs, data, 0o600))
		}})
		m := f.run()
		require.Equal(t, 2, links)
		require.Equal(t, map[string]string{"k": "data"}, f.restored(m.Generation, nil))
	})
	t.Run("checkpoint relinked first", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		f.put(parquet("f1"), "rows 1")
		f.put(ck, "f1")
		var events []string
		blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{
			AfterLink: func(key string) {
				events = append(events, "link "+key)
				if key == ck && len(events) == 2 {
					attrs := filepath.Join(f.dir, filepath.FromSlash(ck)+".attrs")
					data, err := os.ReadFile(attrs) //nolint:gosec // the test's own store
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(attrs, []byte(strings.Replace(string(data), `"md5":"`, `"md5":"AAAA`, 1)), 0o600))
				}
				if key == ck && len(events) == 3 {
					f.put(ck, "f1")
				}
			},
			AfterWalk: func(prefix string) { events = append(events, "walk "+prefix) },
		})
		m := f.run()
		require.Equal(t, []string{"walk ", "link " + ck, "link " + ck, "walk " + lake, "link " + parquet("f1")}, events[:5])
		requireCatalogConsistent(t, f.restored(m.Generation, nil))
	})
	t.Run("fourth fails", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		f.put("k", "data")
		links := 0
		blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(string) {
			links++
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "k.attrs"), []byte("{"), 0o600))
		}})
		_, err := f.mirror.Run(context.Background())
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), `"k"`)
		require.Equal(t, 4, links)
		require.NoDirExists(t, f.dir+"-frozen")
		require.Empty(t, f.stub.Keys("blob/"))
	})
}

// A checkpoint replaced while its prefix is walked freezes the prefix again: a second walk of it.
func TestCheckpointChangedAfterListingRelistsPrefix(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put(parquet("f1"), "rows 1")
	f.put(ck, "f1")
	var walks []string
	blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterWalk: func(prefix string) {
		walks = append(walks, prefix)
		if prefix == lake && len(walks) == 2 {
			f.put(parquet("f2"), "rows 2")
			f.put(ck, "f1,f2")
		}
	}})
	m := f.run()
	require.Equal(t, []string{"", lake, lake}, walks)
	got := f.restored(m.Generation, nil)
	require.Equal(t, "f1,f2", got[ck])
	requireCatalogConsistent(t, got)
}

// A Parquet file gone at its link (walked, then cleaned up after a new checkpoint) freezes the prefix again; a key
// gone outside a catalog is left out.
func TestMissingCatalogKeyRefreezesPrefix(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put(parquet("f1"), "rows 1")
	f.put(ck, "f1")
	f.put("other", "o")
	var walks []string
	blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterWalk: func(prefix string) {
		walks = append(walks, prefix)
		switch {
		case prefix == "":
			f.del("other")
		case prefix == lake && len(walks) == 2:
			f.put(parquet("f2"), "rows 2")
			f.put(ck, "f2")
			f.del(parquet("f1"))
		}
	}})
	m := f.run()
	require.Equal(t, []string{"", lake, lake}, walks)
	got := f.restored(m.Generation, nil)
	require.NotContains(t, got, "other")
	require.Equal(t, "f2", got[ck])
	requireCatalogConsistent(t, got)
	require.False(t, slices.Contains(keysOf(got), parquet("f1")))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
