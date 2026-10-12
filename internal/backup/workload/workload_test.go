package workload_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func meta(name, rg string) v1.ObjectMeta {
	return v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "orders", ResourceGroup: v1.ResourceGroupName(rg)}
}

func tm(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }

// TestResolveScopes (Decision 2): names, an App's kv, buckets and catalogs entries (name or ref) and a ResourceGroup's
// stores resolve to members, KVStores, Buckets then CatalogServices, a catalog with its spec.catalog; a named member
// or App that is missing is NotFound.
func TestResolveScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	lake := v1.CatalogRef{Bucket: "lake-files", Prefix: "lake"}
	for _, o := range []v1.Object{
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("orders-state", "app")},
		&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta("shared-state", "other")},
		&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta("orders-files", "app")},
		&v1.CatalogService{TypeMeta: tm(v1.KindCatalogService), ObjectMeta: meta("lake", "app"), Spec: v1.CatalogServiceSpec{
			Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: "lake-files", Prefix: "lake"}}, Catalog: lake}},
		&v1.App{TypeMeta: tm(v1.KindApp), ObjectMeta: meta("orders", "app"), Spec: v1.AppSpec{
			KV: []v1.AppKVStore{{Name: "orders-state"}, {Ref: "shared-state"}}, Catalogs: []v1.AppCatalog{{Ref: "lake"}}}},
	} {
		_, err := st.Create(ctx, o)
		require.NoError(t, err)
	}
	kv := func(n string) workload.Member { return workload.Member{Kind: v1.KindKVStore, Name: v1.ObjectName(n)} }
	bucket := workload.Member{Kind: v1.KindBucket, Name: "orders-files"}
	catalog := workload.Member{Kind: v1.KindCatalogService, Name: "lake", Catalog: lake}
	for name, c := range map[string]struct {
		scope v1.BackupScope
		want  []workload.Member
	}{
		"names":          {v1.BackupScope{Catalogs: []v1.ObjectName{"lake"}, KVStores: []v1.ObjectName{"orders-state"}}, []workload.Member{kv("orders-state"), catalog}},
		"app":            {v1.BackupScope{App: "orders"}, []workload.Member{kv("orders-state"), kv("shared-state"), catalog}},
		"resource group": {v1.BackupScope{ResourceGroup: "app"}, []workload.Member{kv("orders-state"), bucket, catalog}},
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

// lakeDB builds a DuckLake checkpoint: data path dataPath, schema main/ and table t/ relative, files as given.
func lakeDB(t *testing.T, dataPath string, files ...[2]string) []byte {
	t.Helper()
	file := filepath.Join(t.TempDir(), "catalog.db")
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	stmts := []string{
		`CREATE TABLE ducklake_metadata (key VARCHAR, value VARCHAR, scope VARCHAR, scope_id BIGINT)`,
		`CREATE TABLE ducklake_schema (schema_id BIGINT, schema_name VARCHAR, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_table (table_id BIGINT, schema_id BIGINT, table_name VARCHAR, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_data_file (data_file_id BIGINT, table_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_delete_file (delete_file_id BIGINT, table_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`INSERT INTO ducklake_metadata VALUES ('data_path', '` + dataPath + `', NULL, NULL)`,
		`INSERT INTO ducklake_schema VALUES (0, 'main', 'main/', true)`,
		`INSERT INTO ducklake_table VALUES (1, 0, 't', 't/', true)`,
		`INSERT INTO ducklake_table VALUES (1, 0, 't', 't/', true)`,
	}
	for i, f := range files {
		table := "ducklake_data_file"
		if strings.HasSuffix(f[0], ".delete") {
			table = "ducklake_delete_file"
		}
		stmts = append(stmts, `INSERT INTO `+table+` VALUES (`+string(rune('0'+i))+`, 1, '`+f[0]+`', `+f[1]+`)`)
	}
	for _, s := range stmts {
		_, err := db.Exec(s)
		require.NoError(t, err, s)
	}
	require.NoError(t, db.Close())
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	return data
}

// TestCatalogFilesRelative (Decision 5): a relative file resolves through its table, schema and the data path, an
// absolute one under the data path is kept, both as keys relative to the data path, once each though the table has
// two versions; an absolute path outside the data path is fault.Invalid.
func TestCatalogFilesRelative(t *testing.T) {
	t.Parallel()
	const dataPath = "s3://lake-files/lake/"
	db := lakeDB(t, dataPath, [2]string{"f1.parquet", "true"}, [2]string{dataPath + "main/t/f2.parquet", "false"},
		[2]string{"f1.delete", "true"})
	got, err := workload.CatalogFiles(db)
	require.NoError(t, err)
	require.Equal(t, []string{"main/t/f1.delete", "main/t/f1.parquet", "main/t/f2.parquet"}, got)
	_, err = workload.CatalogFiles(lakeDB(t, dataPath, [2]string{"s3://elsewhere/f.parquet", "false"}))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	_, err = workload.CatalogFiles([]byte("not a database"))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
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
	src := workload.Sources{
		KV:   kv.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix,
		Blob: shared,
		BucketPrefix: func(ns v1.NamespaceName, b v1.ObjectName) string {
			return "s3/" + string(ns) + "/" + string(b) + "/"
		},
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
	src := workload.Sources{Blob: shared, BucketPrefix: func(ns v1.NamespaceName, b v1.ObjectName) string {
		return string(ns) + "/" + string(b) + "/"
	}}
	const path = "orders/nightly-1-uid/"
	_, err = workload.Write(ctx, "orders", []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}}, src,
		workload.Options{Target: target, Path: path})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "KVStore/orders-state")
	require.Empty(t, s.PutKeys(path), "nothing is written")
}

// A catalog that has no checkpoint yet (a new DuckLake catalog its engine has not checkpointed) is an empty member, so
// it does not fail the backup of the stores beside it; an orphan object under its prefix, which no checkpoint names,
// is not copied. Found by the app-todo lane, where every Backup of an App with a new catalog failed.
func TestWriteCatalogWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	require.NoError(t, shared.Put(ctx, "s3/orders/orders-files/a.csv", []byte("a"), blob.PutOptions{}))
	require.NoError(t, shared.Put(ctx, "s3/orders/lakehouse/gold/orphan.parquet", []byte("p"), blob.PutOptions{}))
	src := workload.Sources{Blob: shared, BucketPrefix: func(ns v1.NamespaceName, b v1.ObjectName) string {
		return "s3/" + string(ns) + "/" + string(b) + "/"
	}}
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	members := []workload.Member{
		{Kind: v1.KindBucket, Name: "orders-files"},
		{Kind: v1.KindCatalogService, Name: "lake", Catalog: v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}},
	}
	const path = "orders/nightly-1-uid/"
	m, err := workload.Write(ctx, "orders", members, src, workload.Options{Target: target, Path: path})
	require.NoError(t, err)
	require.Equal(t, []string{path + "blob/orders-files/part-00000", path + "manifest.yaml"}, s.PutKeys(path))
	require.Len(t, m.Members, 2)
	require.Equal(t, workload.MemberFile{Kind: v1.KindCatalogService, Name: "lake"}, m.Members[1])
	got, err := workload.ReadManifest(ctx, target, path)
	require.NoError(t, err)
	require.NoError(t, workload.Integrity(ctx, target, path, got))
}
