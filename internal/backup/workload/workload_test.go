package workload_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
		got, err := workload.Resolve(ctx, st, "orders", c.scope)
		require.NoError(t, err, name)
		require.Equal(t, c.want, got, name)
	}
	_, err := workload.Resolve(ctx, st, "orders", v1.BackupScope{Buckets: []v1.ObjectName{"gone"}})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.ErrorContains(t, err, "Bucket/gone")
	_, err = workload.Resolve(ctx, st, "orders", v1.BackupScope{App: "gone"})
	require.Equal(t, fault.NotFound, fault.KindOf(err))
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
		workload.Buckets(nil, shared, s3Prefix, nil, nil),
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
	src := []workload.MemberKind{workload.KVStores(nil, nil, nil), workload.Buckets(nil, shared, s3Prefix, nil, nil)}
	const path = "orders/nightly-1-uid/"
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}}, src,
		workload.Options{Target: target, Path: path})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "KVStore/orders-state")
	require.Empty(t, s.PutKeys(path), "nothing is written")
}

// A lake that has no checkpoint yet (a new DuckLake catalog its engine has not checkpointed) is an empty member, so
// it does not fail the backup of the stores beside it; an orphan object under its prefix, which no checkpoint names,
// is not copied.
func TestWriteLakeWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, shared.Put(ctx, "s3/orders/orders-files/a.csv", []byte("a"), blob.PutOptions{}))
	require.NoError(t, shared.Put(ctx, "s3/orders/lakehouse/gold/orphan.parquet", []byte("p"), blob.PutOptions{}))
	src := []workload.MemberKind{workload.Buckets(nil, shared, s3Prefix, nil, nil), workload.Lakes(nil, shared, s3Prefix, nil, nil)}
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	members := []workload.Member{
		{Kind: v1.KindBucket, Name: "orders-files"},
		{Kind: v1.KindLake, Name: "lake", Location: v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}},
	}
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", members, src, workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	require.Equal(t, []string{path + "blob/orders-files/part-00000", path + "manifest.yaml"}, s.PutKeys(path))
	require.Len(t, m.Members, 2)
	require.Equal(t, workload.MemberFile{Kind: v1.KindLake, Name: "lake"}, m.Members[1])
	got, err := workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
	require.NoError(t, workload.Integrity(ctx, target, path, got))
}

// catalogSaver is a blob store whose first read of key saves catalog anew: a Lake's commit while a copy walks.
type catalogSaver struct {
	blob.Bucket
	key, catalog string
	once         sync.Once
}

func (b *catalogSaver) Get(ctx context.Context, key string) ([]byte, error) {
	if key == b.key {
		var err error
		b.once.Do(func() { err = b.Put(ctx, b.catalog, []byte("C2"), blob.PutOptions{}) })
		if err != nil {
			return nil, err
		}
	}
	return b.Bucket.Get(ctx, key)
}

// ADR-0311 Decision 4 (#1383): a Bucket's copy holds its objects, then the catalog of the Lake on its prefix lake keyed
// lake/_ducklake/catalog.db, read before the walk as a lake member's checkpoint is: a catalog the Lake saves during the
// walk, which can name a file the walk passed, is not the one copied.
func TestWriteBucket_ReadsCatalogsBeforeItsObjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	const catalog = "lake-catalogs/orders/lake-files/lake/_ducklake/catalog.db"
	shared := &catalogSaver{Bucket: mem, key: "s3/orders/lake-files/lake/main/t/f1.parquet", catalog: catalog}
	require.NoError(t, mem.Put(ctx, catalog, []byte("C1"), blob.PutOptions{}))
	require.NoError(t, mem.Put(ctx, shared.key, []byte("f1"), blob.PutOptions{}))
	src := []workload.MemberKind{workload.Buckets(nil, shared, s3Prefix, func(ns v1.NamespaceName, b v1.ObjectName) string {
		return "lake-catalogs/" + string(ns) + "/" + string(b) + "/"
	}, nil)}
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindBucket, Name: "lake-files"}}, src,
		workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	next := workload.Records(ctx, target, path, m.Members[0].Files[0], nil)
	var got []string
	for {
		rec, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		_, _, body, err := workload.DecodeObject(rec.Value)
		require.NoError(t, err)
		got = append(got, string(rec.Key)+"="+string(body))
	}
	require.Equal(t, []string{"lake/main/t/f1.parquet=f1", "lake/_ducklake/catalog.db=C1"}, got)
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
	catalogs := func(ns v1.NamespaceName) string { return "lake-catalogs/" + string(ns) + "/" }
	var stopped []v1.ObjectName
	released := 0
	stop := func(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (func(), error) {
		require.Equal(t, v1.NamespaceName("orders"), ns)
		stopped = append(stopped, name)
		return func() { released++ }, nil
	}
	inPlace := v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}
	d, err := workload.Lakes(st, shared, s3Prefix, catalogs, stop).Destination(ctx, "orders", inPlace)
	require.NoError(t, err)
	release, err := d.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, []v1.ObjectName{"lake"}, stopped)
	release()
	require.Equal(t, 1, released)

	stopped = nil
	d, err = workload.Lakes(st, shared, s3Prefix, catalogs, stop).Destination(ctx, "orders", v1.RestoreMember{Kind: v1.KindLake, Name: "lake",
		AsLocation: &v1.LakeLocation{Bucket: "lake-files", Prefix: "copy"}})
	require.NoError(t, err)
	release, err = d.Prepare(ctx)
	require.NoError(t, err)
	release()
	require.Empty(t, stopped, "an as location stops no engine")

	d, err = workload.Lakes(st, shared, s3Prefix, catalogs, nil).Destination(ctx, "orders", inPlace)
	require.NoError(t, err)
	release, err = d.Prepare(ctx)
	require.NoError(t, err)
	release()
}
