package workload_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

func TestMain(m *testing.M) { s3stub.Main(m) }

// s3Prefix is a Bucket's prefix in the shared blob store, s3/<ns>/<bucket>/.
func s3Prefix(ns v1.NamespaceName, b v1.ObjectName) string {
	return "s3/" + string(ns) + "/" + string(b) + "/"
}

// catalogRoot is a namespace's prefix in the catalog store, and bucketCatalogs a Bucket's in it.
func catalogRoot(ns v1.NamespaceName) string { return "lake-catalogs/" + string(ns) + "/" }

func bucketCatalogs(ns v1.NamespaceName, b v1.ObjectName) string {
	return catalogRoot(ns) + string(b) + "/"
}

func meta(name, rg string) v1.ObjectMeta {
	return v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "orders", ResourceGroup: v1.ResourceGroupName(rg)}
}

func tm(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

// TestResolveScopes (Decision 2): names, an App's kv, buckets and lakes entries (name or ref) and a ResourceGroup's
// stores resolve to members, KVStores, Buckets then Lakes, a lake with its spec.location; a named member
// or App that is missing is NotFound.
func TestResolveScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	lake := v1.LakeLocation{Bucket: "lake-files", Prefix: "lake"}
	for _, o := range []v1.Object{
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("orders-state", "app")},
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("shared-state", "other")},
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("orders-files", "app")},
		&v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta("lake", "app"), Spec: v1.LakeSpec{
			Location: lake,
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "lake-files", Prefix: "lake"}}},
		}},
		&v1.App{TypeMeta: tm(v1.KindApp), ObjectMeta: meta("orders", "app"), Spec: v1.AppSpec{
			KV: []v1.KVStoreEntry{{Name: "orders-state"}, {Ref: "shared-state"}}, Lakes: []v1.LakeEntry{{Ref: "lake"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	kv := func(n string) workload.Member { return workload.Member{Kind: v1.KindKVStore, Name: v1.ObjectName(n)} }
	bucket := workload.Member{Kind: v1.KindBucket, Name: "orders-files"}
	lakeMember := workload.Member{Kind: v1.KindLake, Name: "lake", Location: lake}
	for name, c := range map[string]struct {
		scope v1.BackupScope
		want  []workload.Member
	}{
		"names":          {v1.BackupScope{Lakes: []v1.ObjectName{"lake"}, KVStores: []v1.ObjectName{"orders-state"}}, []workload.Member{kv("orders-state"), lakeMember}},
		"app":            {v1.BackupScope{App: "orders"}, []workload.Member{kv("orders-state"), kv("shared-state"), lakeMember}},
		"resource group": {v1.BackupScope{ResourceGroup: "app"}, []workload.Member{kv("orders-state"), bucket, lakeMember}},
	} {
		got, err := workload.Resolve(ctx, st, "orders", c.scope, workload.DefaultLimits())
		require.NoError(t, err, name)
		require.Equal(t, c.want, got, name)
	}
	// ADR-0312: in each form a Bucket brings in the Lakes on its Lake prefixes, once, after the Lakes the scope names, even of
	// another ResourceGroup; a prefix of a Function, with no owner or whose owner is located elsewhere stays the Bucket's.
	gold, silver, away := v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}, v1.LakeLocation{Bucket: "lakehouse", Prefix: "silver"},
		v1.LakeLocation{Bucket: "elsewhere", Prefix: "away"}
	lakeOf := func(n string, l v1.LakeLocation, rg string) *v1.Lake {
		return &v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta(n, rg), Spec: v1.LakeSpec{Location: l,
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: l.Bucket, Prefix: l.Prefix}}}}}
	}
	for _, o := range []v1.Object{
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("lakehouse", "brings"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "gold", Owner: v1.LakeOwner("gold")}, {Name: "gold2"}, {Name: "fn", Owner: v1.FunctionOwner("gold")},
			{Name: "silver", Owner: v1.LakeOwner("silver")}, {Name: "off", Owner: v1.LakeOwner("away")}}}},
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("elsewhere", "brings"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "away", Owner: v1.LakeOwner("away")}}}},
		lakeOf("gold", gold, "brings"), lakeOf("silver", silver, "brings"), lakeOf("away", away, "another"),
		&v1.App{TypeMeta: tm(v1.KindApp), ObjectMeta: meta("shop", "brings"), Spec: v1.AppSpec{
			Buckets: []v1.BucketEntry{{Ref: "lakehouse"}}, Lakes: []v1.LakeEntry{{Ref: "silver"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	house := workload.Member{Kind: v1.KindBucket, Name: "lakehouse", LakePrefixes: []string{"gold", "silver"}}
	there := workload.Member{Kind: v1.KindBucket, Name: "elsewhere", LakePrefixes: []string{"away"}}
	onGold, onSilver, onAway := workload.Member{Kind: v1.KindLake, Name: "gold", Location: gold}, workload.Member{Kind: v1.KindLake, Name: "silver", Location: silver},
		workload.Member{Kind: v1.KindLake, Name: "away", Location: away}
	for name, c := range map[string]struct {
		scope v1.BackupScope
		want  []workload.Member
	}{
		"a Bucket by name":                                     {v1.BackupScope{Buckets: []v1.ObjectName{"lakehouse"}}, []workload.Member{house, onGold, onSilver}},
		"a Bucket and one of its Lakes":                        {v1.BackupScope{Buckets: []v1.ObjectName{"lakehouse"}, Lakes: []v1.ObjectName{"silver"}}, []workload.Member{house, onSilver, onGold}},
		"a Lake alone adds no Bucket":                          {v1.BackupScope{Lakes: []v1.ObjectName{"gold"}}, []workload.Member{onGold}},
		"an App that names one Lake":                           {v1.BackupScope{App: "shop"}, []workload.Member{house, onSilver, onGold}},
		"a ResourceGroup, and a Lake of another on its Bucket": {v1.BackupScope{ResourceGroup: "brings"}, []workload.Member{there, house, onGold, onSilver, onAway}},
	} {
		got, err := workload.Resolve(ctx, st, "orders", c.scope, workload.DefaultLimits())
		require.NoError(t, err, name)
		require.Equal(t, c.want, got, name)
	}
	// A gone owner's prefixes are the Bucket's: no Lake prefix and no member.
	_, err := st.Create(ctx, &v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("ghosts", "ghosts"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
		{Name: "p", Owner: v1.LakeOwner("ghost")}, {Name: "q", Owner: v1.LakeOwner("ghost")}}}})
	require.NoError(t, err)
	got, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{Buckets: []v1.ObjectName{"ghosts"}}, workload.DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, []workload.Member{{Kind: v1.KindBucket, Name: "ghosts"}}, got)
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{Buckets: []v1.ObjectName{"gone"}}, workload.DefaultLimits())
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.ErrorContains(t, err, "Bucket/gone")
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{App: "gone"}, workload.DefaultLimits())
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// A scope of its limit of members resolves. One member more is fault.Invalid naming the limit, in each form and whatever brings
// that member in, as a copy over it would fail its listing.
func TestResolve_RefusesAScopeOverACopysMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	names := make([]v1.ObjectName, workload.DefaultLimits().CopyMembers+1)
	entries := make([]v1.KVStoreEntry, len(names))
	for i := range names {
		names[i] = v1.ObjectName(fmt.Sprintf("kv-%d", i))
		entries[i] = v1.KVStoreEntry{Ref: names[i]}
		_, err := st.Create(ctx, &v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta(string(names[i]), "large")})
		require.NoError(t, err)
	}
	for _, o := range []v1.Object{
		&v1.App{TypeMeta: tm(v1.KindApp), ObjectMeta: meta("large", "other"), Spec: v1.AppSpec{KV: entries}},
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("lakehouse", "other"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "gold", Owner: v1.LakeOwner("gold")}}}},
		&v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta("gold", "other"), Spec: v1.LakeSpec{Location: v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"},
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}}}}},
		&v1.Function{TypeMeta: tm(v1.KindFunction), ObjectMeta: meta("fn", "other"), Spec: v1.FunctionSpec{Volumes: []v1.VolumeEntry{{Name: "data", Type: v1.VolumeLocal, Size: "1GiB"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	limit := workload.DefaultLimits().CopyMembers
	got, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{KVStores: names[:limit]}, workload.DefaultLimits())
	require.NoError(t, err)
	require.Len(t, got, limit)
	for name, scope := range map[string]v1.BackupScope{
		"names":                     {KVStores: names},
		"an App":                    {App: "large"},
		"a ResourceGroup":           {ResourceGroup: "large"},
		"a Lake a Bucket brings in": {KVStores: names[:limit-1], Buckets: []v1.ObjectName{"lakehouse"}},
		"a Function's volume":       {KVStores: names[:limit], Volumes: []string{"fn/data"}},
	} {
		_, err := workload.Resolve(ctx, st, "orders", scope, workload.DefaultLimits())
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.ErrorContains(t, err, fmt.Sprint(limit), name)
	}
	lowered := workload.Limits{CopyMembers: 2}
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{KVStores: names[:2]}, lowered)
	require.NoError(t, err)
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{KVStores: names[:3]}, lowered)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "more than the 2 one copy may hold")
	got, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{KVStores: names}, workload.Limits{CopyMembers: limit + 1})
	require.NoError(t, err, "a raised limit resolves what the default refuses")
	require.Len(t, got, limit+1)
}

// TestObjectFrame: EncodeObject's frame decodes to the object it framed, metadata in any order; a torn or trailing
// frame is fault.Invalid.
func TestObjectFrame(t *testing.T) {
	t.Parallel()
	md := map[string]string{"b": "2", "a": "1"}
	v := workload.EncodeObject("text/csv", md, []byte("id,total\n"))
	ct, gotMD, body, err := workload.DecodeObject(v)
	require.NoError(t, err)
	require.Equal(t, "text/csv", ct)
	require.Equal(t, md, gotMD)
	require.Equal(t, "id,total\n", string(body))
	ct, gotMD, body, err = workload.DecodeObject(workload.EncodeObject("", nil, nil))
	require.NoError(t, err)
	require.Empty(t, ct)
	require.Nil(t, gotMD)
	require.Empty(t, body)
	for _, bad := range [][]byte{v[:len(v)-1], append(append([]byte{}, v...), 0), {0xff}} {
		_, _, _, err := workload.DecodeObject(bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err))
	}
}

// A Write puts each member's parts and the manifest last, IfNotExist, sealed to two recipients; the manifest reads
// back, Integrity passes and the records open with one recipient's identity; a damaged part fails Integrity; a target
// that refuses the condition is ErrTargetUnconditional.
func TestWriteReadBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memory.New()
	require.NoError(t, kv.Put(ctx, "orders/orders-state/t/1", []byte("one")))
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, shared.Put(ctx, "s3/orders/orders-files/a.csv", []byte("a"), blob.PutOptions{ContentType: "text/csv", Metadata: map[string]string{"k": "v"}}))
	src := []workload.MemberKind{
		workload.KVStores(nil, kv.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix, nil),
		workload.Buckets(nil, shared, s3Prefix, bucketCatalogs),
	}
	owner, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recovery, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	sealer, err := envelope.Parse([]string{owner.Recipient().String(), recovery.Recipient().String()})
	require.NoError(t, err)
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	members := []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}, {Kind: v1.KindBucket, Name: "orders-files"}}
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", members, src, workload.Options{Target: target, Path: path, Seal: sealer.Seal(), Recipients: sealer.Keys().Recipients})
	require.NoError(t, err)
	require.Equal(t, []string{path + "kv/orders-state/part-00000", path + "blob/orders-files/part-00000", path + "manifest.yaml"}, s.PutKeys(path))
	require.Equal(t, "nightly-1-uid", m.Backup)

	got, err := workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
	require.Equal(t, m, got)
	require.NoError(t, workload.Integrity(ctx, target, path, got))
	unseal, err := envelope.Opener([]age.Identity{recovery})(got.Recipients)
	require.NoError(t, err)
	next := workload.Records(ctx, target, path, got.Members[1].Files[0], unseal)
	r, err := next()
	require.NoError(t, err)
	require.Equal(t, "a.csv", string(r.Key))
	ct, md, body, err := workload.DecodeObject(r.Value)
	require.NoError(t, err)
	require.Equal(t, []string{"text/csv", "v", "a"}, []string{ct, md["k"], string(body)})
	_, err = next()
	require.ErrorIs(t, err, io.EOF)

	s.Seed(path+"kv/orders-state/part-00000", []byte("damaged"))
	require.Equal(t, fault.Invalid, fault.KindOf(workload.Integrity(ctx, target, path, got)))

	refusing := s3stub.New(t, nil)
	refusing.Set(func(s *s3stub.Stub) { s.Cond = s3stub.Refused })
	rt, err := gocloud.OpenWith(ctx, refusing.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	_, err = workload.Write(ctx, "orders", members, src, workload.Options{Target: rt, Path: path, Seal: sealer.Seal()})
	require.True(t, errors.Is(err, workload.ErrTargetUnconditional), "%v", err)
}

// A KVStore member on a KV driver that offers no per-store export fails Write with fault.Unavailable naming it, before
// anything is written, as a Restore fails on a driver that loads no store.
func TestWriteWithoutKVExport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	src := []workload.MemberKind{workload.KVStores(nil, nil, nil), workload.Buckets(nil, shared, s3Prefix, bucketCatalogs)}
	const path = "orders/nightly-1-uid/"
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}}, src,
		workload.Options{Target: target, Path: path})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "KVStore/orders-state")
	require.Empty(t, s.PutKeys(path), "nothing is written")
}

// scenario: a-lake-without-a-catalog-keeps-its-files (ADR-0312 Decision 5) — a lake that has no checkpoint yet (a new DuckLake
// catalog its engine has not checkpointed) does not fail the backup of the stores beside it, and still copies the objects
// under its prefix, since its Bucket's copy skips them, with no catalog.db; a lake with no object either is an empty member.
func TestWriteLakeWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, shared.Put(ctx, "s3/orders/orders-files/a.csv", []byte("a"), blob.PutOptions{}))
	require.NoError(t, shared.Put(ctx, "s3/orders/lakehouse/gold/orphan.parquet", []byte("p"), blob.PutOptions{}))
	src := []workload.MemberKind{workload.Buckets(nil, shared, s3Prefix, bucketCatalogs), workload.Lakes(nil, shared, s3Prefix, catalogRoot, nil)}
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	members := []workload.Member{
		{Kind: v1.KindBucket, Name: "orders-files"},
		{Kind: v1.KindLake, Name: "lake", Location: v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}},
		{Kind: v1.KindLake, Name: "empty", Location: v1.LakeLocation{Bucket: "lakehouse", Prefix: "fresh"}},
	}
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", members, src, workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	require.Equal(t, []string{path + "blob/orders-files/part-00000", path + "lake/lake/1/files/part-00000", path + "manifest.yaml"}, s.PutKeys(path))
	require.Len(t, m.Members, 3)
	require.Equal(t, 1, m.Members[1].Attempt)
	require.Len(t, m.Members[1].Files, 1, "its objects, and no catalog.db")
	r, err := workload.Records(ctx, target, path, m.Members[1].Files[0], nil)()
	require.NoError(t, err)
	require.Equal(t, "orphan.parquet", string(r.Key))
	require.Equal(t, workload.MemberFile{Kind: v1.KindLake, Name: "empty"}, m.Members[2])
	got, err := workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
	require.NoError(t, workload.Integrity(ctx, target, path, got))
}

// scenario: refused-restores (ADR-0312 Decision 8) — a funcd reads one format: a copy of format 1 or 3 fails naming both, and
// Format is 2.
// A manifest's part count bounds what Integrity reads, so a negative one reads no part.
func TestIntegrity_ANegativePartCountReadsNoPart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	target, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, target.Put(ctx, "c/kv/x/part-00000", []byte("x"), blob.PutOptions{}))
	err = workload.Integrity(ctx, target, "c/", workload.Manifest{Members: []workload.MemberFile{{Kind: v1.KindKVStore, Name: "x",
		Files: []backup.StoreFile{{Name: "kv/x", Parts: -1, Bytes: 1}}}}})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "holds 0 bytes")
}

// countedReads is a target that counts the reads of an object's content.
type countedReads struct {
	blob.Bucket
	reads atomic.Int32
}

func (b *countedReads) Get(ctx context.Context, key string) ([]byte, error) {
	b.reads.Add(1)
	return b.Bucket.Get(ctx, key)
}

// A manifest over MaxManifestBytes is refused, naming it, with nothing read; one of that size is read.
func TestReadManifest_RefusesAManifestLargerThanFuncdWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	target := &countedReads{Bucket: mem}
	const path = "orders/nightly-1-uid/"
	sized := func(n int) []byte {
		return append([]byte("format: 2\n#"), bytes.Repeat([]byte("x"), n-len("format: 2\n#"))...)
	}
	require.NoError(t, mem.Put(ctx, path+"manifest.yaml", sized(workload.MaxManifestBytes+1), blob.PutOptions{}))
	_, err = workload.ReadManifest(ctx, target, path)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, path+"manifest.yaml")
	require.Zero(t, target.reads.Load())

	require.NoError(t, mem.Put(ctx, path+"manifest.yaml", sized(workload.MaxManifestBytes), blob.PutOptions{}))
	m, err := workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
	require.Equal(t, workload.Format, m.Format)
}

// A part over backup.PartBytes fails Integrity, naming it, with nothing read; one of that size passes, and an empty one is not
// read at all, since a range of an empty object is an error on S3.
func TestIntegrity_RefusesAPartLargerThanFuncdWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	target := &countedReads{Bucket: mem}
	manifest := func(data []byte) workload.Manifest {
		sum := sha256.Sum256(data)
		return workload.Manifest{Members: []workload.MemberFile{{Kind: v1.KindKVStore, Name: "x",
			Files: []backup.StoreFile{{Name: "kv/x", Parts: 1, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}}}
	}
	over := bytes.Repeat([]byte("x"), backup.PartBytes+1)
	require.NoError(t, mem.Put(ctx, "c/kv/x/part-00000", over, blob.PutOptions{}))
	err = workload.Integrity(ctx, target, "c/", manifest(over))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "c/kv/x/part-00000")
	require.Zero(t, target.reads.Load())

	require.NoError(t, mem.Put(ctx, "c/kv/x/part-00000", nil, blob.PutOptions{}))
	require.NoError(t, workload.Integrity(ctx, target, "c/", manifest(nil)))
	require.Zero(t, target.reads.Load())

	require.NoError(t, mem.Put(ctx, "c/kv/x/part-00000", over[:backup.PartBytes], blob.PutOptions{}))
	require.NoError(t, workload.Integrity(ctx, target, "c/", manifest(over[:backup.PartBytes])))
	require.EqualValues(t, 1, target.reads.Load())
}

// shortReport is a target that reports every object 4 bytes long and records the length of a range read.
type shortReport struct {
	blob.Bucket
	asked atomic.Int64
}

func (b *shortReport) Attributes(ctx context.Context, key string) (blob.Attributes, error) {
	a, err := b.Bucket.Attributes(ctx, key)
	a.Size = 4
	return a, err
}

func (b *shortReport) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	b.asked.Store(length)
	return b.Bucket.(blob.RangeReader).GetRange(ctx, key, offset, length)
}

// A read asks for no more bytes than the target reports.
func TestIntegrity_AsksForNoMoreThanATargetReports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, mem.Put(ctx, "c/kv/x/part-00000", []byte("12345678"), blob.PutOptions{}))
	target := &shortReport{Bucket: mem}
	err = workload.Integrity(ctx, target, "c/", workload.Manifest{Members: []workload.MemberFile{{Kind: v1.KindKVStore, Name: "x",
		Files: []backup.StoreFile{{Name: "kv/x", Parts: 1, Bytes: 8}}}}})
	require.ErrorContains(t, err, "holds 4 bytes")
	require.EqualValues(t, 4, target.asked.Load())
}

func TestReadManifest_RefusesAnotherFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	require.Equal(t, 2, workload.Format)
	target, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	for _, format := range []int{1, 3} {
		path := fmt.Sprintf("orders/f%d-uid/", format)
		require.NoError(t, target.Put(ctx, path+"manifest.yaml", fmt.Appendf(nil, "format: %d\nnamespace: orders\n", format), blob.PutOptions{}))
		_, err := workload.ReadManifest(ctx, target, path)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, fmt.Sprintf("has format %d; this funcd reads format 2", format))
	}
	path := "orders/f2-uid/"
	require.NoError(t, target.Put(ctx, path+"manifest.yaml", []byte("format: 2\nnamespace: orders\n"), blob.PutOptions{}))
	_, err = workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
}

// ADR-0311 Decision 6: a member of a kind that no MemberKind serves fails Write fault.Invalid naming its kind, before
// the first put, though a member a kind serves comes before it.
func TestWrite_AnUnservedKindFailsByName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memory.New()
	require.NoError(t, kv.Put(ctx, "orders/orders-state/t/1", []byte("one")))
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	const path = "orders/nightly-1-uid/"
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"},
		{Kind: v1.KindFunction, Name: "cart", Volume: "state"}},
		[]workload.MemberKind{workload.KVStores(nil, kv.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix, nil)},
		workload.Options{Target: target, Path: path})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "no member kind serves Function")
	require.Empty(t, s.PutKeys(path), "nothing is written")
}

// ADR-0312 Decision 9: a Lake's Prepare stops its engine until release when it loads in place, and only then: an as
// location takes no hold, and no engine runs there. A nil stop does nothing.
func TestLake_PrepareStopsItsEngineInPlaceOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	for _, o := range []v1.Object{
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("lake-files", "app")},
		&v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta("lake", "app"), Spec: v1.LakeSpec{
			Location: v1.LakeLocation{Bucket: "lake-files", Prefix: "lake"},
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "lake-files", Prefix: "lake"}}},
		}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	var stopped []v1.ObjectName
	released := 0
	stop := func(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (func(), error) {
		require.Equal(t, v1.NamespaceName("orders"), ns)
		stopped = append(stopped, name)
		return func() { released++ }, nil
	}
	inPlace := v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}
	d, err := workload.Lakes(st, shared, s3Prefix, catalogRoot, stop).Destination(ctx, "orders", inPlace)
	require.NoError(t, err)
	release, err := d.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, []v1.ObjectName{"lake"}, stopped)
	release()
	require.Equal(t, 1, released)

	stopped = nil
	d, err = workload.Lakes(st, shared, s3Prefix, catalogRoot, stop).Destination(ctx, "orders", v1.RestoreMember{Kind: v1.KindLake, Name: "lake",
		AsLocation: &v1.LakeLocation{Bucket: "lake-files", Prefix: "copy"}})
	require.NoError(t, err)
	release, err = d.Prepare(ctx)
	require.NoError(t, err)
	release()
	require.Empty(t, stopped, "an as location stops no engine")

	d, err = workload.Lakes(st, shared, s3Prefix, catalogRoot, nil).Destination(ctx, "orders", inPlace)
	require.NoError(t, err)
	release, err = d.Prepare(ctx)
	require.NoError(t, err)
	release()
}

// ADR-0312 Decision 4, at the kind: IsEmpty counts and Clear deletes only outside its Lake prefixes, read when each runs,
// past a page boundary on both sides of a prefix, and a sibling <prefix>2/ or an object named like the prefix is the Bucket's.
func TestBucket_IsEmptyAndClearActOutsideItsLakePrefixes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	const root, lakeFile, catalog = "s3/orders/data/", "s3/orders/data/gold/f1", "lake-catalogs/orders/data/gold/_ducklake/catalog.db"
	put := func(key string) { require.NoError(t, shared.Put(ctx, key, []byte("x"), blob.PutOptions{})) }
	st := store.New(storemem.New())
	bucket := &v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("data", "app"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold"}}}}
	_, err = st.Create(ctx, bucket)
	require.NoError(t, err)
	put(lakeFile)
	put(catalog)
	d, err := workload.Buckets(st, shared, s3Prefix, bucketCatalogs).Destination(ctx, "orders", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true})
	require.NoError(t, err)
	empty, err := d.IsEmpty(ctx)
	require.NoError(t, err)
	require.False(t, empty, "with no Lake on it, the prefix is the Bucket's")

	obj, err := st.Get(ctx, v1.KindBucket.GVK(), "orders", "data")
	require.NoError(t, err)
	bucket = obj.(*v1.Bucket)
	bucket.Spec.Prefixes[0].Owner = v1.LakeOwner("sales")
	_, err = st.Update(ctx, bucket)
	require.NoError(t, err)
	_, err = st.Create(ctx, &v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta("sales", "app"), Spec: v1.LakeSpec{Location: v1.LakeLocation{Bucket: "data", Prefix: "gold"},
		Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "data", Prefix: "gold"}}}}})
	require.NoError(t, err)
	empty, err = d.IsEmpty(ctx)
	require.NoError(t, err)
	require.True(t, empty, "a Lake's data is not the Bucket's, and each step reads the Lake prefixes when it runs")
	put(root + "gold2/x")
	empty, err = d.IsEmpty(ctx)
	require.NoError(t, err)
	require.False(t, empty, "a sibling of the prefix is the Bucket's")

	for i := range 1100 {
		put(fmt.Sprintf("%sa%04d", root, i))
		put(fmt.Sprintf("%sz%04d", root, i))
	}
	put(root + "gold")
	put(root + "gold-two/x")
	put("lake-catalogs/orders/data/other/_ducklake/catalog.db")
	put(root + "gold/" + string(utf8.MaxRune) + "x")
	require.NoError(t, d.Clear(ctx))
	left, err := shared.List(ctx, root)
	require.NoError(t, err)
	require.Len(t, left, 2, "the walk ends past a Lake prefix, whatever its keys")
	require.Equal(t, lakeFile, left[0].Key)
	catalogs, err := shared.List(ctx, "lake-catalogs/")
	require.NoError(t, err)
	require.Len(t, catalogs, 1, "its other catalog goes, its Lake's stays")
	require.Equal(t, catalog, catalogs[0].Key)

	require.NoError(t, st.Delete(ctx, v1.KindLake.GVK(), "orders", "sales", ""))
	empty, err = d.IsEmpty(ctx)
	require.NoError(t, err)
	require.True(t, empty, "a gone owner's prefix stays out of the count")
	require.NoError(t, d.Clear(ctx))
	for _, key := range []string{lakeFile, catalog} {
		_, err := shared.Get(ctx, key)
		require.NoError(t, err, "and a Restore leaves it")
	}
}

// movingStore is a store that moves Lake sales to silver right after its first read.
type movingStore struct {
	store.Store
	moved atomic.Bool
}

func (s *movingStore) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	obj, err := s.Store.Get(ctx, gvk, ns, name)
	if err == nil && gvk == v1.KindLake.GVK() && s.moved.CompareAndSwap(false, true) {
		again, err := s.Store.Get(ctx, gvk, ns, name)
		if err != nil {
			return nil, err
		}
		lake := again.(*v1.Lake)
		lake.Spec.Location.Prefix, lake.Spec.Bindings.Blob[0].Prefix = "silver", "silver"
		if _, err := s.Update(ctx, lake); err != nil {
			return nil, err
		}
	}
	return obj, err
}

// ADR-0312 Decision 3: one Resolve reads each Lake once, so what the Bucket's copy skips and what the Lake's member copies
// agree, however the Lake moves during it.
func TestResolve_ReadsEachLakeOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := &movingStore{Store: store.New(storemem.New())}
	for _, o := range []v1.Object{
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("data", "app"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "gold", Owner: v1.LakeOwner("sales")}, {Name: "silver", Owner: v1.LakeOwner("sales")}}}},
		&v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta("sales", "app"), Spec: v1.LakeSpec{Location: v1.LakeLocation{Bucket: "data", Prefix: "gold"},
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "data", Prefix: "gold"}}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	got, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{Buckets: []v1.ObjectName{"data"}, Lakes: []v1.ObjectName{"sales"}}, workload.DefaultLimits())
	require.NoError(t, err)
	require.True(t, st.moved.Load())
	require.Equal(t, []workload.Member{{Kind: v1.KindBucket, Name: "data", LakePrefixes: []string{"gold"}},
		{Kind: v1.KindLake, Name: "sales", Location: v1.LakeLocation{Bucket: "data", Prefix: "gold"}}}, got)
}

// ADR-0312 Decision 3: Write reads no store, so a Lake applied or gone during a run leaves each byte in one member: the Bucket's
// copy goes by the Lake prefixes Resolve read, and the store is not read for them.
func TestWrite_KeepsTheLakePrefixesResolveRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	for _, k := range []string{"s3/orders/data/late/a", "s3/orders/data/gone/b", "lake-catalogs/orders/data/gone/_ducklake/catalog.db"} {
		require.NoError(t, shared.Put(ctx, k, []byte(k), blob.PutOptions{}))
	}
	st := store.New(storemem.New())
	lake := func(name, prefix string) *v1.Lake {
		return &v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta(name, "app"), Spec: v1.LakeSpec{Location: v1.LakeLocation{Bucket: "data", Prefix: prefix},
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "data", Prefix: prefix}}}}}
	}
	for _, o := range []v1.Object{
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("data", "app"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "late"}, {Name: "gone", Owner: v1.LakeOwner("gone-lake")}}}},
		lake("gone-lake", "gone"),
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	ms, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{Buckets: []v1.ObjectName{"data"}}, workload.DefaultLimits())
	require.NoError(t, err)
	require.Len(t, ms, 2)

	require.NoError(t, st.Delete(ctx, v1.KindLake.GVK(), "orders", "gone-lake", ""))
	_, err = st.Create(ctx, lake("late-lake", "late"))
	require.NoError(t, err)
	obj, err := st.Get(ctx, v1.KindBucket.GVK(), "orders", "data")
	require.NoError(t, err)
	edited := obj.(*v1.Bucket)
	edited.Spec.Prefixes = []v1.BucketPrefix{{Name: "late", Owner: v1.LakeOwner("late-lake")}, {Name: "gone"}}
	_, err = st.Update(ctx, edited)
	require.NoError(t, err)
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", ms, []workload.MemberKind{workload.Buckets(st, shared, s3Prefix, bucketCatalogs),
		workload.Lakes(st, shared, s3Prefix, catalogRoot, nil)}, workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	next := workload.Records(ctx, target, path, m.Members[0].Files[0], nil)
	r, err := next()
	require.NoError(t, err)
	require.Equal(t, "late/a", string(r.Key), "a Lake and an owner applied after Resolve leave its prefix in the Bucket's copy")
	_, err = next()
	require.ErrorIs(t, err, io.EOF)
	require.Len(t, m.Members[0].Files, 1, "and a Lake gone and an owner cleared after Resolve keep its catalog in its own member")
	require.Equal(t, 1, m.Members[1].Attempt)
	require.Len(t, m.Members[1].Files, 2)
}
