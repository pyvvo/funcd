package gocloud

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gcblob "gocloud.dev/blob"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// fileBucketAt opens a file:// bucket on dir; trace, when set, gets each durable step of its writer.
func fileBucketAt(t *testing.T, dir string, trace func(op durable.Op, path string)) *bucket {
	t.Helper()
	b, err := Open(context.Background(), FileURL(dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	k := b.(*bucket)
	k.disk = durable.Disk{Trace: trace}
	return k
}

type step struct {
	op   durable.Op
	path string
}

func tempOf(path string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(path) + `\.[0-9a-f]{16}\.funcd-tmp$`)
}

// damage overwrites path's bytes in place and keeps its mtime, as damage on disk does.
func damage(t *testing.T, path string, data []byte) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
}

// oracle: docs/design/blob/file-put.fizz#AckedPutSurvives
// scenario: acknowledged-put-survives-power-loss (ADR-0278) — before a Put returns, the directories it made are synced,
// its data and attributes are synced under temp names, renamed onto the key in that order, and their directory synced.
func TestScenarioAcknowledgedPutSurvivesPowerLoss(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var steps []step
	k := fileBucketAt(t, dir, func(op durable.Op, path string) { steps = append(steps, step{op, path}) })
	require.NoError(t, k.Put(context.Background(), "a/b/obj", []byte("payload"), blob.PutOptions{ContentType: "text/plain"}))

	obj := filepath.Join(dir, "a", "b", "obj")
	require.Len(t, steps, 7, "%v", steps)
	require.Equal(t, []step{{durable.SyncDir, dir}, {durable.SyncDir, filepath.Join(dir, "a")}}, steps[:2])
	require.Equal(t, durable.SyncFile, steps[2].op)
	require.Regexp(t, tempOf(obj), steps[2].path)
	require.Equal(t, durable.SyncFile, steps[3].op)
	require.Regexp(t, tempOf(obj+fileAttrsSuffix), steps[3].path)
	require.Equal(t, []step{
		{durable.Rename, obj},
		{durable.Rename, obj + fileAttrsSuffix},
		{durable.SyncDir, filepath.Join(dir, "a", "b")},
	}, steps[4:])
	got, err := k.Get(context.Background(), "a/b/obj")
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
	entries, err := os.ReadDir(filepath.Join(dir, "a", "b"))
	require.NoError(t, err)
	require.Len(t, entries, 2, "no temp file is left")
}

// scenario: create-if-absent-survives-power-loss (ADR-0278) — a create-if-absent Put syncs its file, links it onto the
// key and syncs the directory before it returns; a second one is fault.Conflict and leaves the first.
func TestScenarioCreateIfAbsentSurvivesPowerLoss(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var steps []step
	k := fileBucketAt(t, dir, func(op durable.Op, path string) { steps = append(steps, step{op, path}) })
	require.NoError(t, k.Put(context.Background(), "gen", []byte("v1"), blob.PutOptions{IfNotExist: true}))

	require.Len(t, steps, 3, "%v", steps)
	require.Equal(t, durable.SyncFile, steps[0].op)
	require.Regexp(t, tempOf(filepath.Join(dir, "gen")), steps[0].path)
	require.Equal(t, []step{{durable.Link, filepath.Join(dir, "gen")}, {durable.SyncDir, dir}}, steps[1:])
	err := k.Put(context.Background(), "gen", []byte("v2"), blob.PutOptions{IfNotExist: true})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	got, err := k.Get(context.Background(), "gen")
	require.NoError(t, err)
	require.Equal(t, "v1", string(got))
}

// scenario: interrupted-overwrite-reads-whole (ADR-0278) — new data under the attributes of the Put before it, as a
// power loss between the two renames leaves them, reads back whole with no MD5, so no GET, HEAD or LIST has an ETag.
func TestScenarioInterruptedOverwriteReadsWhole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	k := fileBucketAt(t, dir, nil)
	require.NoError(t, k.Put(ctx, "obj", []byte("old"), blob.PutOptions{ContentType: "text/plain"}))
	attrs := filepath.Join(dir, "obj"+fileAttrsSuffix)
	old, err := os.ReadFile(attrs) //nolint:gosec // the test's own store
	require.NoError(t, err)
	require.NoError(t, k.Put(ctx, "obj", []byte("new bytes"), blob.PutOptions{ContentType: "application/json"}))
	require.NoError(t, os.WriteFile(attrs, old, 0o600))

	got, err := k.Get(ctx, "obj")
	require.NoError(t, err)
	require.Equal(t, "new bytes", string(got))
	part, err := k.GetRange(ctx, "obj", 4, 5)
	require.NoError(t, err)
	require.Equal(t, "bytes", string(part))
	a, err := k.Attributes(ctx, "obj")
	require.NoError(t, err)
	require.Empty(t, a.MD5, "HEAD and GET have no ETag")
	require.Equal(t, "text/plain", a.ContentType, "the attributes' content type")
	items, err := k.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Empty(t, items[0].MD5, "LIST has no ETag")
}

// oracle: docs/design/blob/file-put.fizz#WholeNeverRefused
// scenario: concurrent-writes-read-whole (ADR-0278) — a Put paused between its two renames while a second Put of the
// key runs whole, and reads at each step, return the bytes of one of the objects, never an error.
func TestScenarioConcurrentWritesReadWhole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	obj := filepath.Join(dir, "obj")
	k := fileBucketAt(t, dir, nil)
	require.NoError(t, k.Put(ctx, "obj", []byte("v0 object"), blob.PutOptions{}))
	paused, resume := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	k.disk = durable.Disk{Trace: func(op durable.Op, path string) {
		if op == durable.Rename && path == obj && first.CompareAndSwap(false, true) {
			close(paused)
			<-resume
		}
	}}
	whole := map[string]bool{"v0 object": true, "v1 object": true, "v2 object": true}
	read := func(stage string) {
		got, err := k.Get(ctx, "obj")
		require.NoError(t, err, stage)
		require.True(t, whole[string(got)], "%s: %q", stage, got)
		part, err := k.GetRange(ctx, "obj", 3, -1)
		require.NoError(t, err, stage)
		require.Equal(t, "object", string(part), stage)
	}
	done := make(chan error, 1)
	go func() { done <- k.Put(ctx, "obj", []byte("v1 object"), blob.PutOptions{}) }()
	<-paused
	read("v1's data under v0's attributes")
	require.NoError(t, k.Put(ctx, "obj", []byte("v2 object"), blob.PutOptions{}))
	read("v2 whole")
	close(resume)
	require.NoError(t, <-done)
	read("v2's data under v1's attributes")
}

// scenario: corrupt-object-refused (ADR-0278) — bytes that miss the MD5 of attributes naming their file, or of
// attributes written before ADR-0278, and attributes that do not decode fail Get and GetRange with blob.ErrCorrupt
// in fault.Internal; the object stays.
func TestScenarioCorruptObjectRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, setUp := range map[string]func(t *testing.T, dir string, k *bucket){
		"paired": func(t *testing.T, dir string, k *bucket) {
			require.NoError(t, k.Put(ctx, "obj", []byte("0123456789"), blob.PutOptions{}))
			damage(t, filepath.Join(dir, "obj"), []byte("01234X6789"))
		},
		"written before ADR-0278": func(t *testing.T, dir string, _ *bucket) {
			legacy, err := gcblob.OpenBucket(ctx, FileURL(dir))
			require.NoError(t, err)
			require.NoError(t, legacy.WriteAll(ctx, "obj", []byte("0123456789"), nil))
			require.NoError(t, legacy.Close())
			require.NoError(t, os.WriteFile(filepath.Join(dir, "obj"), nil, 0o600))
		},
		"attributes that do not decode": func(t *testing.T, dir string, k *bucket) {
			require.NoError(t, k.Put(ctx, "obj", []byte("0123456789"), blob.PutOptions{}))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "obj"+fileAttrsSuffix), []byte(`{"md5":`), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			k := fileBucketAt(t, dir, nil)
			setUp(t, dir, k)
			_, err := k.Get(ctx, "obj")
			require.ErrorIs(t, err, blob.ErrCorrupt)
			require.Equal(t, fault.Internal, fault.KindOf(err))
			_, err = k.GetRange(ctx, "obj", 0, 2)
			require.ErrorIs(t, err, blob.ErrCorrupt)
			require.FileExists(t, filepath.Join(dir, "obj"))
		})
	}
}

// ADR-0278 Decision 1: funcd's writer stores what gocloud's WriteAll stores — the path, the bytes, the content type,
// the metadata and the MD5 — for escaped keys and a detected content type too.
func TestFileWriteMatchesGocloud(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		key  string
		data []byte
		opts blob.PutOptions
	}{
		{"plain.txt", []byte("hello"), blob.PutOptions{ContentType: "text/plain; charset=utf-8"}},
		{"a//b", []byte("<html><body>x</body></html>"), blob.PutOptions{}},
		{"dir/f/", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, blob.PutOptions{Metadata: map[string]string{"Owner": "fn", "x-trace": "1"}}},
		{"ctl\x01key", nil, blob.PutOptions{ContentType: "Application/JSON"}},
		{"x/../y", []byte("up"), blob.PutOptions{}},
	} {
		gdir, fdir := t.TempDir(), t.TempDir()
		g, err := gcblob.OpenBucket(ctx, FileURL(gdir))
		require.NoError(t, err)
		require.NoError(t, g.WriteAll(ctx, c.key, c.data, &gcblob.WriterOptions{ContentType: c.opts.ContentType, Metadata: c.opts.Metadata}))
		require.NoError(t, g.Close())
		require.NoError(t, fileBucketAt(t, fdir, nil).Put(ctx, c.key, c.data, c.opts))
		require.Equal(t, storedTree(t, gdir), storedTree(t, fdir), "key %q", c.key)
	}
}

// storedTree is every file under dir by its relative path: a data file's bytes, an attributes file decoded without
// funcd's identity.
func storedTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		data, err := os.ReadFile(path) //nolint:gosec // the test's own store
		require.NoError(t, err)
		if filepath.Ext(path) == fileAttrsSuffix {
			var xa fileAttrs
			require.NoError(t, json.Unmarshal(data, &xa))
			xa.File = nil
			norm, err := json.Marshal(xa)
			require.NoError(t, err)
			data = norm
		}
		out[rel] = string(data)
		return nil
	}))
	return out
}

// ADR-0278 Decision 2: a ranged read checks a file whole once per run — concurrent first reads share one hash, a
// failure is not remembered — and the pass set keeps at most passSetSize files, the least recently used out first.
func TestRangeCheckOncePerFile(t *testing.T) {
	t.Parallel()
	t.Run("once per file", func(t *testing.T) {
		t.Parallel()
		p := newPassSet()
		var hashes atomic.Int32
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				errs <- p.check(passKey{id: fileIdentity{Ino: 1}}, func() error {
					hashes.Add(1)
					time.Sleep(5 * time.Millisecond)
					return nil
				})
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		require.Equal(t, int32(1), hashes.Load())
	})
	t.Run("a failure is not remembered", func(t *testing.T) {
		t.Parallel()
		p := newPassSet()
		calls := 0
		fail := func() error { calls++; return blob.ErrCorrupt }
		require.ErrorIs(t, p.check(passKey{id: fileIdentity{Ino: 2}}, fail), blob.ErrCorrupt)
		require.ErrorIs(t, p.check(passKey{id: fileIdentity{Ino: 2}}, fail), blob.ErrCorrupt)
		require.Equal(t, 2, calls)
	})
	t.Run("bounded, the least recently used out first", func(t *testing.T) {
		t.Parallel()
		p := newPassSet()
		for i := range passSetSize {
			p.pass(passKey{id: fileIdentity{Ino: uint64(i)}})
		}
		hashes := map[uint64]int{}
		check := func(ino uint64) {
			require.NoError(t, p.check(passKey{id: fileIdentity{Ino: ino}}, func() error { hashes[ino]++; return nil }))
		}
		check(0)
		p.pass(passKey{id: fileIdentity{Ino: passSetSize}})
		check(passSetSize)
		check(0)
		check(1)
		require.Equal(t, map[uint64]int{1: 1}, hashes, "file 0, used last, stays; file 1, used least recently, went")
	})
	t.Run("a ranged read hashes a file once", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		dir := t.TempDir()
		k := fileBucketAt(t, dir, nil)
		require.NoError(t, k.Put(ctx, "obj", []byte("0123456789"), blob.PutOptions{}))
		_, err := k.GetRange(ctx, "obj", 0, 2)
		require.NoError(t, err)
		damage(t, filepath.Join(dir, "obj"), []byte("X123456789"))
		part, err := k.GetRange(ctx, "obj", 0, 2)
		require.NoError(t, err, "the file passed in this run")
		require.Equal(t, "X1", string(part))
		_, err = k.Get(ctx, "obj")
		require.True(t, errors.Is(err, blob.ErrCorrupt), "a full read hashes every time")
	})
}

// ADR-0278 Decision 1: a Put whose context is cancelled before its renames publishes nothing: the old object stays,
// and no temp file is left.
func TestFileWriteCancelledPublishesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	k := fileBucketAt(t, dir, nil)
	require.NoError(t, k.Put(context.Background(), "obj", []byte("old"), blob.PutOptions{}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, opts := range []blob.PutOptions{{}, {IfNotExist: true}} {
		key := "obj"
		if opts.IfNotExist {
			key = "new"
		}
		require.ErrorIs(t, k.Put(ctx, key, []byte("new bytes"), opts), context.Canceled)
	}
	got, err := k.Get(context.Background(), "obj")
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
	_, err = k.Get(context.Background(), "new")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "obj and its attributes, no temp file")
}

// A key that is not valid UTF-8 is refused on the file backend, as gocloud refuses it: fileEscapeKey would map it
// onto the path of another key (ADR-0278 review).
func TestFileNonUTF8KeyRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	k := fileBucketAt(t, t.TempDir(), nil)
	require.NoError(t, k.Put(ctx, "y\uFFFD", []byte("replacement"), blob.PutOptions{}))
	for name, call := range map[string]func() error{
		"Put":        func() error { return k.Put(ctx, "y\xff", []byte("x"), blob.PutOptions{}) },
		"create":     func() error { return k.Put(ctx, "y\xfe", []byte("x"), blob.PutOptions{IfNotExist: true}) },
		"Get":        func() error { _, err := k.Get(ctx, "y\xff"); return err },
		"GetRange":   func() error { _, err := k.GetRange(ctx, "y\xff", 0, 1); return err },
		"Attributes": func() error { _, err := k.Attributes(ctx, "y\xff"); return err },
	} {
		require.Equal(t, fault.Invalid, fault.KindOf(call()), name)
	}
}
