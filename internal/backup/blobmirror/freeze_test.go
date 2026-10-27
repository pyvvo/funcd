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
	"github.com/pyvvo/funcd/internal/blob"
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

// Attributes that do not decode fail the run at once, naming the key (ADR-0278 Decision 3); a checkpoint whose bytes
// miss the recorded MD5 is linked again before its prefix is walked, and again once a new Put names another file; the
// fourth re-freeze of a key fails the run with fault.Conflict naming it.
func TestAttrsMismatchRefrozen(t *testing.T) {
	t.Parallel()
	t.Run("undecodable attrs", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		f.put("k", "data")
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, "k.attrs"), []byte(`{"md5":`), 0o600))
		links := 0
		blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(string) { links++ }})
		_, err := f.mirror.Run(context.Background())
		require.ErrorIs(t, err, blob.ErrCorrupt)
		require.Equal(t, fault.Internal, fault.KindOf(err))
		require.Contains(t, err.Error(), `"k"`)
		require.Equal(t, 1, links)
		require.NoDirExists(t, f.dir+"-frozen")
		require.Empty(t, f.stub.Keys("blob/"))
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
					corruptMD5(t, f, ck)
				}
				if key == ck && len(events) == 3 {
					f.put(ck, "f1")
				}
			},
			AfterWalk: func(prefix string) { events = append(events, "walk "+prefix) },
		})
		m := f.run()
		require.Equal(t, []string{"walk ", "link " + ck, "link " + ck, "link " + ck, "walk " + lake, "link " + parquet("f1")}, events[:6])
		requireLakeConsistent(t, f.restored(m.Generation, nil), ck)
	})
	t.Run("fourth fails", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, nil)
		f.put(ck, "f1")
		links := 0
		blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(string) {
			links++
			if links == 1 {
				corruptMD5(t, f, ck)
			}
		}})
		_, err := f.mirror.Run(context.Background())
		require.Equal(t, fault.Conflict, fault.KindOf(err))
		require.Contains(t, err.Error(), ck)
		require.Equal(t, 4, links)
		require.NoDirExists(t, f.dir+"-frozen")
		require.Empty(t, f.stub.Keys("blob/"))
	})
}

// corruptMD5 makes key's attributes record another MD5 for the same file.
func corruptMD5(t *testing.T, f *fixture, key string) {
	t.Helper()
	attrs := filepath.Join(f.dir, filepath.FromSlash(key)+".attrs")
	data, err := os.ReadFile(attrs) //nolint:gosec // the test's own store
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(attrs, []byte(strings.Replace(string(data), `"md5":"`, `"md5":"AAAA`, 1)), 0o600))
}

// oracle: docs/design/blob/file-put.fizz#MirrorAgrees
// scenario: mirror-after-torn-pair (ADR-0278) — new data with the attributes of the Put before it, as a power loss
// between a Put's two renames leaves them, is frozen again, then copied unchecked: the run still makes a generation.
func TestScenarioMirrorAfterTornPair(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put("k", "old")
	attrs := filepath.Join(f.dir, "k.attrs")
	old, err := os.ReadFile(attrs) //nolint:gosec // the test's own store
	require.NoError(t, err)
	f.put("k", "new bytes")
	require.NoError(t, os.WriteFile(attrs, old, 0o600))
	links := 0
	blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(string) { links++ }})
	m := f.run()
	require.Equal(t, 4, links, "the first freeze and three re-freezes")
	require.Equal(t, map[string]string{"k": "new bytes"}, f.restored(m.Generation, nil))
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
	requireLakeConsistent(t, got, ck)
}

// A Parquet file gone at its link (walked, then cleaned up after a new checkpoint) freezes the prefix again; a key
// gone outside a lake is left out.
func TestMissingLakeKeyRefreezesPrefix(t *testing.T) {
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
	requireLakeConsistent(t, got, ck)
	require.False(t, slices.Contains(keysOf(got), parquet("f1")))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
