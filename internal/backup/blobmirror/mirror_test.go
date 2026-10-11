package blobmirror_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// scenario: mirror-incremental — with the target refusing Get, Attributes and Delete, a second run after one object
// was added and one deleted uploads only the new object, its index lists exactly the live objects, and its manifest
// is created last.
func TestScenarioMirrorIncremental(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put("s3/ns/b/a", "A")
	f.put("s3/ns/b/b", "B")
	m1 := f.run()
	require.Equal(t, uint64(1), m1.Generation)
	require.Equal(t, uint64(1), m1.Epoch)
	require.Equal(t, 2, m1.Objects)
	first := len(f.stub.PutKeys(""))

	f.clock.Advance(time.Hour)
	f.put("s3/ns/b/c", "C")
	f.del("s3/ns/b/a")
	m2 := f.run()
	require.Equal(t, uint64(2), m2.Generation)
	puts := f.stub.PutKeys("")[first:]
	var objects []string
	for _, k := range puts {
		if strings.Contains(k, "/objects/") {
			objects = append(objects, k)
		}
	}
	require.Len(t, objects, 2, "one part and its sha256- key: %v", objects)
	require.True(t, strings.HasSuffix(objects[0], "/0000000002/part-00000"))
	require.Contains(t, objects[1], "/0000000002/sha256-")
	require.Equal(t, "blob/0000000001/gen/0000000002/manifest.yaml", puts[len(puts)-1], "the manifest is created last")
	f.requireBoxOnly()

	require.Equal(t, map[string]string{"s3/ns/b/b": "B", "s3/ns/b/c": "C"}, f.restored(2, nil))
	require.Equal(t, map[string]string{"s3/ns/b/a": "A", "s3/ns/b/b": "B"}, f.restored(1, nil))
	es, err := blobmirror.List(context.Background(), f.reader())
	require.NoError(t, err)
	require.Len(t, es, 2)
	require.Equal(t, blobmirror.Entry{Generation: 1, Epoch: 1, Complete: true, At: start}, es[0])
}

// scenario: mirror-frozen-image — an object overwritten and another deleted after the freeze, before their copy,
// are in the generation as at the freeze.
func TestScenarioMirrorFrozenImage(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.put("x", "x at the freeze")
	f.put("y", "y at the freeze")
	blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{BeforeCopy: func() {
		f.put("x", "x after the freeze, longer")
		f.del("y")
	}})
	m := f.run()
	require.Equal(t, map[string]string{"x": "x at the freeze", "y": "y at the freeze"}, f.restored(m.Generation, nil))
}

const (
	lake = "s3/ns/b/lake/"
	ck   = lake + "_ducklake/catalog.db"
)

func parquet(name string) string { return lake + "data/" + name + ".parquet" }

// requireCatalogConsistent checks the generation's checkpoint names only files the generation holds.
func requireCatalogConsistent(t *testing.T, got map[string]string) {
	t.Helper()
	cp, ok := got[ck]
	require.True(t, ok, "the generation holds the checkpoint")
	for _, name := range strings.Split(cp, ",") {
		require.Contains(t, got, parquet(name), "checkpoint %q names %s", cp, name)
	}
}

// scenario: catalog-checkpoint-first — mid-freeze the engine writes f2 and a checkpoint listing both, or a
// checkpoint without f1 and deletes f1: each checkpoint's files are in the generation.
func TestScenarioCatalogCheckpointFirst(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(f *fixture){
		"f2 added": func(f *fixture) {
			f.put(parquet("f2"), "rows 2")
			f.put(ck, "f1,f2")
		},
		"f1 cleaned up": func(f *fixture) {
			f.put(parquet("f2"), "rows 2")
			f.put(ck, "f2")
			f.del(parquet("f1"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil)
			f.put(parquet("f1"), "rows 1")
			f.put(ck, "f1")
			changed := false
			blobmirror.Set(f.mirror, nil, nil, nil, &blobmirror.Hooks{AfterLink: func(key string) {
				if key == ck && !changed {
					changed = true
					change(f)
				}
			}})
			m := f.run()
			requireCatalogConsistent(t, f.restored(m.Generation, nil))
		})
	}
}

// scenario: mirror-epoch-rolls — a run that finds the epoch older than blob.backup.rebaseline writes epoch e+1 with
// every object uploaded again and changes nothing under epoch e.
func TestScenarioMirrorEpochRolls(t *testing.T) {
	t.Parallel()
	f := newFixture(t, func(c *blobmirror.Config) { c.Rebaseline, c.Retention = 2*time.Hour, 2*time.Hour })
	f.put("a", "A")
	f.put("b", "B")
	f.run()
	f.clock.Advance(time.Hour)
	require.Equal(t, uint64(1), f.run().Epoch, "an epoch an hour old stays")
	old := f.stub.Keys("blob/0000000001/")
	puts := len(f.stub.PutKeys(""))

	f.clock.Advance(90 * time.Minute)
	m := f.run()
	require.Equal(t, uint64(2), m.Epoch)
	require.Equal(t, uint64(3), m.Generation, "n is one sequence across epochs")
	require.Equal(t, old, f.stub.Keys("blob/0000000001/"), "nothing changed under epoch 1")
	for _, k := range f.stub.PutKeys("")[puts:] {
		require.True(t, strings.HasPrefix(k, "blob/0000000002/"), "%s", k)
	}
	require.Len(t, f.stub.Keys("blob/0000000002/objects/"), 4, "both objects uploaded again, each a part and a sha256- key")
	require.Equal(t, map[string]string{"a": "A", "b": "B"}, f.restored(3, nil))
}

// The object id is SHA-256 over key, size, ModTime, MD5 and recipients, as ADR-0208 Decision 4 fixes it: stable,
// and changed by any of them.
func TestObjectIDStable(t *testing.T) {
	t.Parallel()
	mod := time.Date(2026, 10, 5, 10, 0, 0, 123, time.UTC)
	md5 := bytes.Repeat([]byte{0xab}, 16)
	rs := []string{"0123456789abcdef", "fedcba9876543210"}
	id := blobmirror.ObjectID("s3/ns/b/k", 42, mod, md5, rs)
	require.Equal(t, id, blobmirror.ObjectID("s3/ns/b/k", 42, mod, md5, rs))
	require.Equal(t, "7f32448056a052290ffd1407c0e573677516c9eb826861779fd55f752955bcf8", id)
	for name, other := range map[string]string{
		"key":        blobmirror.ObjectID("s3/ns/b/l", 42, mod, md5, rs),
		"size":       blobmirror.ObjectID("s3/ns/b/k", 43, mod, md5, rs),
		"time":       blobmirror.ObjectID("s3/ns/b/k", 42, mod.Add(1), md5, rs),
		"md5":        blobmirror.ObjectID("s3/ns/b/k", 42, mod, nil, rs),
		"recipients": blobmirror.ObjectID("s3/ns/b/k", 42, mod, md5, rs[:1]),
		"none":       blobmirror.ObjectID("s3/ns/b/k", 42, mod, md5, nil),
	} {
		require.NotEqual(t, id, other, name)
	}
}

// A 20 MiB object goes up in three parts, none over 8 MiB, one put each; a run that failed past part 0 leaves no
// sha256- key, so the next run uploads it again under its own n.
func TestLargeObjectPartedBoundedMemory(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	big := bytes.Repeat([]byte("0123456789abcdef"), 20<<20/16)
	require.NoError(t, f.store.Put(context.Background(), "big", big, blob.PutOptions{}))
	f.stub.Set(func(s *s3stub.Stub) {
		s.FailPut = func(key string) bool { return strings.HasSuffix(key, "/0000000001/part-00001") }
	})
	_, err := f.mirror.Run(context.Background())
	require.Error(t, err)
	require.Empty(t, f.stub.Keys("blob/0000000001/gen/"))
	f.stub.Set(func(s *s3stub.Stub) { s.FailPut = nil })

	m := f.run()
	require.Equal(t, uint64(2), m.Generation)
	var parts []string
	for _, k := range f.stub.Keys("blob/") {
		data, _, _, _ := f.stub.Object(k)
		require.LessOrEqual(t, len(data), backup.PartBytes, "%s", k)
		if strings.Contains(k, "/0000000002/part-") && strings.Contains(k, "/objects/") {
			parts = append(parts, k)
		}
	}
	require.Len(t, parts, 3)
	require.Equal(t, map[string]string{"big": string(big)}, f.restored(2, nil))
}

func TestLifecycleRule(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		rebaseline, retention time.Duration
		days                  int
	}{
		{720 * time.Hour, 720 * time.Hour, 60},
		{time.Hour, time.Hour, 1},
		{24 * time.Hour, time.Hour, 2},
		{24 * time.Hour, 24 * time.Hour, 2},
	} {
		prefix, days := blobmirror.LifecycleRule(c.rebaseline, c.retention)
		require.Equal(t, "blob/", prefix)
		require.Equal(t, c.days, days, "%s + %s", c.rebaseline, c.retention)
	}
}

// New refuses an empty Dir, no target and times that are not positive or below the interval.
func TestNewChecksConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	for name, mod := range map[string]func(c *blobmirror.Config){
		"no dir":             func(c *blobmirror.Config) { c.Dir = "" },
		"no target":          func(c *blobmirror.Config) { c.Target = nil },
		"no retry":           func(c *blobmirror.Config) { c.RetryInterval = 0 },
		"retention too low":  func(c *blobmirror.Config) { c.Retention = time.Minute },
		"rebaseline too low": func(c *blobmirror.Config) { c.Rebaseline = time.Minute },
	} {
		cfg := f.cfg
		mod(&cfg)
		_, err := blobmirror.New(cfg)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
	}
}

// A Ready refusal fails the run before anything is listed or put.
func TestRunWaitsForReady(t *testing.T) {
	t.Parallel()
	refused := fault.Invalidf("test", "backup.singleWriter")
	f := newFixture(t, func(c *blobmirror.Config) {
		c.Ready = func(context.Context) (bool, error) { return false, refused }
	})
	f.put("a", "A")
	_, err := f.mirror.Run(context.Background())
	require.ErrorIs(t, err, refused)
	requests, _ := f.stub.Seen()
	require.Empty(t, requests)
}
