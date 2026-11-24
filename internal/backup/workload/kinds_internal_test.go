package workload

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// sqlFile is a SQLLoader whose Source is a database's file; the test calls nothing else.
type sqlFile struct{ SQLLoader }

func (sqlFile) Source(context.Context, v1.NamespaceName, v1.ObjectName) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("SQLite format 3\x00")), nil
}

// volumeTar is a VolumeLoader whose Source is a volume's tar; the test calls nothing else.
type volumeTar struct{ VolumeLoader }

func (volumeTar) Source(context.Context, v1.NamespaceName, v1.ObjectName, v1.ObjectName) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("a tar")), nil
}

// snapshotFile is an AddonFiles whose Newest is an engine's snapshot; the test calls nothing else.
type snapshotFile struct{ AddonFiles }

func (snapshotFile) Newest(context.Context, v1.NamespaceName, v1.ObjectName, string, time.Duration) (string, io.ReadCloser, error) {
	return "backup_1.sqlite", io.NopCloser(strings.NewReader("SQLite format 3\x00")), nil
}

// addonStore holds rauthy's Addon and the IdentityProvider login of orders.
func addonStore(t *testing.T) store.Store {
	t.Helper()
	st := store.New(storemem.New())
	for _, o := range []v1.Object{
		&v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddon}, ObjectMeta: v1.ObjectMeta{Name: "rauthy"},
			Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "identityprovider", Image: "ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809", Workloads: []v1.Workload{v1.WorkloadEngine},
				Attributes: &v1.AddonAttributes{Kind: "IdentityProvider", Port: 8080, Readiness: v1.AddonReadiness{Path: "/ping"}, Storage: &v1.AddonStorage{Path: "/app/data"}, Snapshot: &v1.AddonSnapshot{Path: "/app/data/backups"}}}}}},
		&v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindAddonObject},
			ObjectMeta: v1.ObjectMeta{Name: "login", Namespace: "orders", ResourceGroup: "app"}, Spec: v1.AddonObjectSpec{Kind: "IdentityProvider"}},
	} {
		_, err := st.Create(context.Background(), o)
		require.NoError(t, err)
	}
	return st
}

// TestKinds_ParseReadsWhatWriteWrote (ADR-0311 Decision 1): each kind's Parse names the member, and a Lake's attempt,
// of every key its Write put under a copy's directory, and no other kind's Parse accepts one of them.
func TestKinds_ParseReadsWhatWriteWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := memory.New()
	require.NoError(t, kv.Put(ctx, "orders/orders-state/t/1", []byte("one")))
	shared, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	for key, data := range map[string]string{
		"s3/orders/orders-files/a.csv":                              "a",
		"s3/orders/lake-files/lake/main/t/f1.parquet":               "f1",
		"lake-catalogs/orders/lake-files/lake/_ducklake/catalog.db": "C1",
	} {
		require.NoError(t, shared.Put(ctx, key, []byte(data), blob.PutOptions{}))
	}
	prefix := func(ns v1.NamespaceName, b v1.ObjectName) string { return "s3/" + string(ns) + "/" + string(b) + "/" }
	catalogs := func(ns v1.NamespaceName) string { return "lake-catalogs/" + string(ns) + "/" }
	kinds := []MemberKind{
		KVStores(nil, kv.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix, nil),
		Buckets(nil, shared, prefix, func(ns v1.NamespaceName, b v1.ObjectName) string { return catalogs(ns) + string(b) + "/" }),
		Lakes(nil, shared, prefix, catalogs, nil),
		Databases(nil, sqlFile{}),
		Volumes(nil, volumeTar{}),
		Addons(addonStore(t), snapshotFile{}),
	}
	members := []Member{
		{Kind: v1.KindKVStore, Name: "orders-state"},
		{Kind: v1.KindBucket, Name: "lake-files"},
		{Kind: v1.KindLake, Name: "lake", Location: v1.LakeLocation{Bucket: "lake-files", Prefix: "lake"}},
		{Kind: v1.KindSQLDatabase, Name: "todo-db"},
		{Kind: v1.KindFunction, Name: "cart", Volume: "state"},
		{Kind: "IdentityProvider", Name: "login"},
	}
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	for i, k := range kinds {
		require.True(t, Serves(k, members[i].Kind))
		path := fmt.Sprintf("orders/b%d-uid/", i)
		mf, err := k.Write(ctx, &writer{o: Options{Target: target, Path: path}, buf: make([]byte, 0, backup.PartBytes)}, "orders", members[i])
		require.NoError(t, err, "%s", k.Kind())
		keys := s.PutKeys(path)
		require.NotEmpty(t, keys, "%s", k.Kind())
		for _, key := range keys {
			seg := strings.Split(strings.TrimPrefix(key, path), "/")
			for j, other := range kinds {
				bm, attempt, ok := other.Parse(seg)
				if j != i {
					require.False(t, ok, "the %s kind parses %s's key %s", other.Kind(), k.Kind(), key)
					continue
				}
				require.True(t, ok, "%s does not parse its key %s", k.Kind(), key)
				require.Equal(t, v1.BackupMember{Kind: mf.Kind, Name: mf.Name, Volume: mf.Volume}, bm, key)
				require.Equal(t, mf.Attempt, attempt, key)
			}
		}
	}
}
