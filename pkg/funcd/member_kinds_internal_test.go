package funcd

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/lifecycle"
	"github.com/pyvvo/funcd/internal/volume"
)

// kindsRig is the backup rig of ADR-0311's guards, with SQL databases on fileEngines and Function volumes: a member of
// every kind the providers serve, each holding data (KVStore orders-state, Bucket lake-files with Lake lake on its
// prefix lake, SQLDatabase todo-db and Function cart's volume state), and a catalog on orders-files that no member
// names. fns are more Functions with an empty volume state, and dbs more empty databases.
type kindsRig struct {
	*wbRig
	engines *fileEngines
	leaves  map[string]string // each volume's directory, by <function>/<volume>
}

// kindsMembers are kindsRig's members, as a Restore names them.
func kindsMembers() []v1.RestoreMember {
	return []v1.RestoreMember{
		{Kind: v1.KindKVStore, Name: "orders-state"},
		{Kind: v1.KindBucket, Name: "lake-files"},
		{Kind: v1.KindLake, Name: "lake"},
		{Kind: v1.KindSQLDatabase, Name: "todo-db"},
		{Kind: v1.KindFunction, Name: "cart", Volume: "state"},
	}
}

func newKindsRig(t *testing.T, shared blob.Bucket, fns, dbs []v1.ObjectName, setup func(r *wbRig)) *kindsRig {
	t.Helper()
	engines := newFileEngines(t)
	opts := []Option{WithSQLProviderRuntime(engines), WithFunctionVolumes(volume.Options{Root: t.TempDir()}, false)}
	if shared != nil {
		opts = append(opts, WithBlob(shared))
	}
	fns, dbs = append([]v1.ObjectName{"cart"}, fns...), append([]v1.ObjectName{"todo-db"}, dbs...)
	r := newWBRig(t, wbOptions{opts: opts, setup: func(r *wbRig) {
		r.lakeOn("lake-files", map[string]string{
			lakeCatalogPrefix("orders") + "lake-files/lake/_ducklake/catalog.db": "C1",
			bucketPrefix("orders", "lake-files") + "lake/main/t/f1.parquet":      "f1",
		})
		require.NoError(r.t, r.p.cfg.blob.Put(r.ctx, bucketPrefix("orders", "lake-files")+"notes.txt", []byte("n"),
			blob.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"source": "export"}}))
		require.NoError(r.t, r.p.cfg.blob.Put(r.ctx, lakeCatalogPrefix("orders")+"orders-files/other/_ducklake/catalog.db",
			[]byte("C9"), blob.PutOptions{}))
		for _, name := range dbs {
			r.seed(&v1.SQLDatabase{TypeMeta: tmeta(v1.KindSQLDatabase), ObjectMeta: ordersMeta(string(name))})
		}
		for _, name := range fns {
			r.seed(&v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta(string(name)), Spec: v1.FunctionSpec{
				Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
				Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 1 << 20}},
			}})
		}
		if setup != nil {
			setup(r)
		}
	}})
	for _, name := range dbs {
		r.waitSQLReady(name)
	}
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = []byte("SQLite format 3\x00milk") })
	k := &kindsRig{wbRig: r, engines: engines, leaves: map[string]string{}}
	for _, name := range fns {
		dir, err := r.p.exec.volumes.Reserve(r.ctx, volume.ID{Namespace: "orders", Function: name, Name: "state"},
			r.getFunctionUID(name), 1<<20, v1.DeletionRetain)
		require.NoError(t, err)
		k.leaves[string(name)+"/state"] = dir
	}
	require.NoError(t, os.WriteFile(filepath.Join(k.leaves["cart/state"], "orders.sqlite"), []byte("rows"), 0o600))
	k.create(k.dev, k.schedule("kinds-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{KVStores: []v1.ObjectName{"orders-state"}, Buckets: []v1.ObjectName{"lake-files"},
			Lakes: []v1.ObjectName{"lake"}, Databases: []v1.ObjectName{"todo-db"}, Volumes: []string{"cart/state"}}
	}))
	return k
}

// stores is what orders' stores hold, by place: each object under s3/orders/ and lake-catalogs/orders/ with its type
// and metadata, each KV key under orders/, each database's file ("" for one its engine counts empty, as Empty leaves
// it) and each file of a volume.
func (k *kindsRig) stores() map[string]string {
	k.t.Helper()
	out := map[string]string{}
	for _, prefix := range []string{s3Root + "orders/", lakeCatalogPrefix("orders")} {
		items, err := k.p.cfg.blob.List(k.ctx, prefix)
		require.NoError(k.t, err)
		for _, it := range items {
			data, err := k.p.cfg.blob.Get(k.ctx, it.Key)
			require.NoError(k.t, err)
			attrs, err := k.p.cfg.blob.Attributes(k.ctx, it.Key)
			require.NoError(k.t, err)
			out[it.Key] = fmt.Sprintf("%q %s %v", data, attrs.ContentType, attrs.Metadata)
		}
	}
	keys, err := k.p.cfg.kvStore.List(k.ctx, "orders/")
	require.NoError(k.t, err)
	for _, key := range keys {
		v, _, err := k.p.cfg.kvStore.Get(k.ctx, key)
		require.NoError(k.t, err)
		out["kv:"+key] = string(v)
	}
	k.engines.mu.Lock()
	engines := maps.Clone(k.engines.engines)
	k.engines.mu.Unlock()
	for ref, e := range engines {
		f := e.file()
		if len(f) == 0 || len(f) == 4096 {
			f = nil
		}
		out["sql:"+string(ref.Namespace)+"/"+string(ref.Name)] = string(f)
	}
	for vol, dir := range k.leaves {
		require.NoError(k.t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			rel, _ := filepath.Rel(dir, path)
			out["volume:"+vol+"/"+filepath.ToSlash(rel)] = string(data)
			return err
		}))
	}
	return out
}

// scenario: every-kind-round-trips-the-whole-store (ADR-0311, #1383's class) — each member restored in place alone, then
// all in one Restore that lists the Lake before its Bucket, with only the volume emptied first, leaves every store as
// it was after the backup: no Clear deletes what its member's copy lacks or what no member names. Its members are of
// every kind the providers serve, so a new kind joins them.
func TestKinds_RoundTripLeavesTheStoresAsTheyWere(t *testing.T) {
	t.Parallel()
	k := newKindsRig(t, nil, nil, nil, func(r *wbRig) { r.put("orders/orders-state-r/t/9", "no member names it") })
	var served, members []v1.Kind
	for _, h := range lifecycle.Each[backupHook](k.p.phases) {
		for _, kind := range h.BackupKinds() {
			served = append(served, kind.Kind())
		}
	}
	for _, m := range kindsMembers() {
		members = append(members, m.Kind)
	}
	require.ElementsMatch(t, served, members, "a member of every kind the providers serve")
	k.create(k.dev, k.backupObj("b1", "kinds-nightly"))
	k.waitBackup("b1", v1.RunSucceeded)
	want := k.stores()

	restore := func(name string, ms ...v1.RestoreMember) {
		t.Helper()
		rs := k.restoreObj(name, "b1", ms[0])
		rs.Spec.Members = nil
		for _, m := range ms {
			m.InPlace = true
			rs.Spec.Members = append(rs.Spec.Members, m)
		}
		if slices.ContainsFunc(ms, func(m v1.RestoreMember) bool { return m.Kind == v1.KindFunction }) {
			require.NoError(t, k.p.exec.volumes.Clear(k.ctx, volume.ID{Namespace: "orders", Function: "cart", Name: "state"}))
		}
		k.create(k.admin, rs)
		k.waitRestore(v1.ObjectName(name), v1.RunSucceeded)
		require.Equal(t, want, k.stores(), "after Restore %s", name)
	}
	ms := kindsMembers()
	for _, m := range ms {
		restore("alone-"+strings.ToLower(string(m.Kind)), m)
	}
	restore("all", ms[0], ms[2], ms[1], ms[3], ms[4])
}

// scenario: every-kind-round-trips-the-whole-store, its failures — a Restore of every kind into as targets is refused
// NotEmpty by a Bucket that holds only a catalog, writing nothing, and one that fails on a put mid-load leaves every
// target whose load began empty and every other key as it was.
func TestKinds_AFailedRestoreDeletesOnlyWhatItWrote(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/copy/main/t/f1.parquet", fail: true}
	k := newKindsRig(t, shared, []v1.ObjectName{"cart2"}, []v1.ObjectName{"todo-db-restored"}, func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("new-files")},
			&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("copy-files")})
		require.NoError(r.t, r.p.cfg.blob.Put(r.ctx, lakeCatalogPrefix("orders")+"new-files/lake/_ducklake/catalog.db",
			[]byte("C0"), blob.PutOptions{}))
	})
	k.create(k.dev, k.backupObj("b1", "kinds-nightly"))
	k.waitBackup("b1", v1.RunSucceeded)
	shared.armed.Store(true)
	before := k.stores()
	restore := func(name string, bucket v1.ObjectName) *v1.Restore {
		t.Helper()
		rs := k.restoreObj(name, "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", As: "orders-state-r"})
		rs.Spec.Members = append(rs.Spec.Members,
			v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", As: "todo-db-restored"},
			v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", As: "cart2"},
			v1.RestoreMember{Kind: v1.KindBucket, Name: "lake-files", As: bucket},
			v1.RestoreMember{Kind: v1.KindLake, Name: "lake", AsLocation: &v1.LakeLocation{Bucket: "orders-files", Prefix: "copy"}})
		k.create(k.dev, rs)
		return k.waitRestore(v1.ObjectName(name), v1.RunFailed)
	}

	rs := restore("onto-catalog", "new-files")
	require.Equal(t, "NotEmpty", reason(rs.Status.Status), "a Bucket that holds only a catalog holds its Lake's rows")
	require.Equal(t, before, k.stores(), "a refused Restore writes nothing")

	restore("mid-load", "copy-files")
	require.Equal(t, before, k.stores(), "the failure empties every target whose load began, and nothing else")
}
