package funcd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// lakeOn seeds Bucket bucket, whose prefix lake Lake lake owns, and puts data, keyed by blob key, in the shared store.
func (r *wbRig) lakeOn(bucket v1.ObjectName, data map[string]string) {
	r.t.Helper()
	r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta(string(bucket)), Spec: v1.BucketSpec{
		Prefixes: []v1.BucketPrefix{{Name: "lake", Owner: v1.LakeOwner("lake")}}}},
		&v1.Lake{TypeMeta: tmeta(v1.KindLake), ObjectMeta: ordersMeta("lake"), Spec: v1.LakeSpec{
			Location: v1.LakeLocation{Bucket: bucket, Prefix: "lake"},
			Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: bucket, Prefix: "lake"}}},
		}})
	for key, v := range data {
		require.NoError(r.t, r.p.cfg.blob.Put(r.ctx, key, []byte(v), blob.PutOptions{}))
	}
}

// blobAt is the object at key in the shared store, empty when there is none.
func (r *wbRig) blobAt(key string) string {
	r.t.Helper()
	data, err := r.p.cfg.blob.Get(r.ctx, key)
	if fault.KindOf(err) == fault.NotFound {
		return ""
	}
	require.NoError(r.t, err)
	return string(data)
}

// Issue #1383: an in-place Restore of a Bucket empties it with the purger, which also deletes the catalogs of the Lakes
// on its prefixes (ADR-0293 Decision 5), while the Bucket's copy holds only its objects: the Lake on lake-files gets
// its files back and loses its catalog.
func TestIssue1383_InPlaceBucketRestoreKeepsItsLakesCatalog(t *testing.T) {
	t.Parallel()
	catalog, file := lakeCatalogPrefix("orders")+"lake-files/lake/_ducklake/catalog.db", bucketPrefix("orders", "lake-files")+"lake/main/t/f1.parquet"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) { r.lakeOn("lake-files", map[string]string{catalog: "C1", file: "f1"}) }})
	r.create(r.dev, r.schedule("files-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Buckets: []v1.ObjectName{"lake-files"}}
	}))
	r.create(r.dev, r.backupObj("b1", "files-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)

	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "lake-files", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "f1", r.blobAt(file))
	require.Equal(t, "C1", r.blobAt(catalog), "the Lake on the Bucket keeps its catalog")
}

// Issue #1383: a failed Restore empties every Bucket it began to load with the purger: the Lake on new-files, whose rows
// live in its catalog alone and whose Bucket therefore passed as empty, loses a catalog the Restore never wrote.
func TestIssue1383_FailedRestoreKeepsACatalogItNeverWrote(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/b.csv", fail: true}
	catalog := lakeCatalogPrefix("orders") + "new-files/lake/_ducklake/catalog.db"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		r.lakeOn("new-files", map[string]string{catalog: "C1"})
		require.NoError(r.t, shared.Put(r.ctx, bucketPrefix("orders", "orders-files")+"b.csv", []byte("id\n"), blob.PutOptions{}))
	}})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)

	shared.armed.Store(true)
	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "orders-files", As: "new-files"}))
	r.waitRestore("copy", v1.RunFailed)
	require.Empty(t, r.blobAt(bucketPrefix("orders", "new-files")+"a.csv"), "the failure empties what the Restore wrote")
	require.Equal(t, "C1", r.blobAt(catalog), "and nothing else")
}

// Issue #1383: an in-place Restore loads its members in spec order: a Lake listed after its Bucket keeps its catalog,
// and one listed before it loads its catalog, which the Bucket's purge then deletes.
func TestIssue1383_InPlaceRestoreOfALakeAndItsBucketKeepsTheCatalog(t *testing.T) {
	t.Parallel()
	catalog, file := lakeCatalogPrefix("orders")+"lake-files/lake/_ducklake/catalog.db", bucketPrefix("orders", "lake-files")+"lake/main/t/f1.parquet"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) { r.lakeOn("lake-files", map[string]string{catalog: "C1", file: "f1"}) }})
	r.create(r.dev, r.schedule("lake-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Buckets: []v1.ObjectName{"lake-files"}, Lakes: []v1.ObjectName{"lake"}}
	}))
	r.create(r.dev, r.backupObj("b1", "lake-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)

	bucket := v1.RestoreMember{Kind: v1.KindBucket, Name: "lake-files", InPlace: true}
	lake := v1.RestoreMember{Kind: v1.KindLake, Name: "lake", InPlace: true}
	for _, c := range []struct {
		name    string
		members []v1.RestoreMember
	}{{"bucket-first", []v1.RestoreMember{bucket, lake}}, {"lake-first", []v1.RestoreMember{lake, bucket}}} {
		rs := r.restoreObj(c.name, "b1", bucket)
		rs.Spec.Members = c.members
		r.create(r.admin, rs)
		r.waitRestore(v1.ObjectName(c.name), v1.RunSucceeded)
		require.Equal(t, "f1", r.blobAt(file), c.name)
		require.Equal(t, "C1", r.blobAt(catalog), "%s: the Lake keeps its catalog", c.name)
	}
}
