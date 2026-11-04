package funcd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// The setup of ADR-0312's scenarios: Bucket data holds notes.txt and gold2/x; its prefix gold is owned by Lake sales,
// located there, with file f1 and catalog C1.
const (
	dataNotes    = "s3/orders/data/notes.txt"
	dataGold2    = "s3/orders/data/gold2/x"
	salesFile    = "s3/orders/data/gold/f1"
	salesFile2   = "s3/orders/data/gold/f2"
	salesCatalog = "lake-catalogs/orders/data/gold/_ducklake/catalog.db"
)

// lakeNamed is Lake name, located on bucket/prefix.
func lakeNamed(name string, bucket v1.ObjectName, prefix string) *v1.Lake {
	return &v1.Lake{TypeMeta: tmeta(v1.KindLake), ObjectMeta: ordersMeta(name), Spec: v1.LakeSpec{
		Location: v1.LakeLocation{Bucket: bucket, Prefix: prefix},
		Bindings: v1.LakeBindings{Blob: []v1.FunctionBlob{{Alias: "lake", Bucket: bucket, Prefix: prefix}}},
	}}
}

func (r *wbRig) putBlob(key, value string) {
	r.t.Helper()
	require.NoError(r.t, r.p.cfg.blob.Put(r.ctx, key, []byte(value), blob.PutOptions{}))
}

// seedData seeds the setup. Its Lake is of another ResourceGroup, so a form that takes it takes it through the Bucket.
func (r *wbRig) seedData() {
	r.t.Helper()
	sales := lakeNamed("sales", "data", "gold")
	sales.ResourceGroup = "another"
	r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{
		Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: v1.LakeOwner("sales")}, {Name: "gold2"}}}}, sales)
	for key, value := range map[string]string{dataNotes: "n", dataGold2: "x", salesFile: "f1", salesCatalog: "C1"} {
		r.putBlob(key, value)
	}
}

// dataSchedule creates the schedule named name whose scope names only Bucket data.
func (r *wbRig) dataSchedule(name string) {
	r.t.Helper()
	r.create(r.dev, r.schedule(name, "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Buckets: []v1.ObjectName{"data"}}
	}))
}

// memberFiles reads each file of a member of the copy under path, as its records' keys and bodies.
func (r *wbRig) memberFiles(path string, kind v1.Kind, name v1.ObjectName) []map[string]string {
	r.t.Helper()
	src := r.target()
	m, err := workload.ReadManifest(r.ctx, src, path)
	require.NoError(r.t, err)
	unseal, err := envelope.Opener([]age.Identity{r.owner})(m.Recipients)
	require.NoError(r.t, err)
	i := slices.IndexFunc(m.Members, func(mf workload.MemberFile) bool { return mf.Kind == kind && mf.Name == name })
	require.GreaterOrEqual(r.t, i, 0, "the copy holds %s/%s", kind, name)
	var out []map[string]string
	for _, f := range m.Members[i].Files {
		records := map[string]string{}
		next := workload.Records(r.ctx, src, path, f, unseal)
		for {
			rec, err := next()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(r.t, err)
			_, _, body, err := workload.DecodeObject(rec.Value)
			require.NoError(r.t, err)
			records[string(rec.Key)] = string(body)
		}
		out = append(out, records)
	}
	return out
}

func memberNames(b *v1.Backup) []string {
	var out []string
	for _, m := range b.Status.Members {
		out = append(out, string(m.Kind)+"/"+string(m.Name))
	}
	return out
}

// scenario: bucket-copy-skips-its-lakes (ADR-0312) — a Backup whose scope names only Bucket data, an App that holds data but not
// sales, or data's ResourceGroup: blob/data/ holds notes.txt and gold2/x, and Lake sales, a member once, holds f1 and C1. A
// scope that names only sales adds no Bucket.
// oracle: docs/design/backup/bucket-lake-members.fizz#EachObjectInOneMember
func TestScenarioBucketCopySkipsItsLakes(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seedData()
		r.seed(&v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: ordersMeta("shop"), Spec: v1.AppSpec{Buckets: []v1.BucketEntry{{Ref: "data"}}}})
	}})
	for name, scope := range map[string]v1.BackupScope{
		"names":          {Buckets: []v1.ObjectName{"data"}},
		"named-too":      {Buckets: []v1.ObjectName{"data"}, Lakes: []v1.ObjectName{"sales"}},
		"app":            {App: "shop"},
		"resource-group": {ResourceGroup: "rg1"},
	} {
		r.create(r.dev, r.schedule(name, "0 3 * * *", func(s *v1.BackupScheduleSpec) { s.Scope = scope }))
		r.create(r.dev, r.backupObj(name+"-b", v1.ObjectName(name)))
		b := r.waitBackup(v1.ObjectName(name+"-b"), v1.RunSucceeded)
		members := memberNames(b)
		require.Contains(t, members, "Bucket/data", name)
		require.Equal(t, 1, countOf(members, "Lake/sales"), "%s: %v", name, members)
		require.Equal(t, []map[string]string{{"notes.txt": "n", "gold2/x": "x"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"), name)
		require.Equal(t, []map[string]string{{"f1": "f1"}, {workload.CatalogRecord: "C1"}}, r.memberFiles(b.Status.Path, v1.KindLake, "sales"), name)
	}
	r.create(r.dev, r.schedule("lake-only", "0 3 * * *", func(s *v1.BackupScheduleSpec) { s.Scope = v1.BackupScope{Lakes: []v1.ObjectName{"sales"}} }))
	r.create(r.dev, r.backupObj("lake-only-b", "lake-only"))
	require.Equal(t, []string{"Lake/sales"}, memberNames(r.waitBackup("lake-only-b", v1.RunSucceeded)), "a Lake adds no Bucket")
}

func countOf(names []string, name string) int {
	n := 0
	for _, x := range names {
		if x == name {
			n++
		}
	}
	return n
}

// scenario: lake-off-its-own-prefix-is-held (ADR-0312), the Bucket's side — a Lake applied on a prefix a Function owns does not
// hide that prefix: it stays in the Bucket's copy and comes back with its rewind, and the Lake is no member through the Bucket.
func TestScenarioLakeOffItsOwnPrefixStaysInTheBucketsCopy(t *testing.T) {
	t.Parallel()
	const upload = "s3/orders/media/uploads/a.jpg"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("media"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "uploads", Owner: v1.FunctionOwner("uploader")}}}}, lakeNamed("squatter", "media", "uploads"))
		r.putBlob(upload, "jpg")
	}})
	r.create(r.dev, r.schedule("media-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) { s.Scope = v1.BackupScope{Buckets: []v1.ObjectName{"media"}} }))
	r.create(r.dev, r.backupObj("b1", "media-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []string{"Bucket/media"}, memberNames(b))
	require.Equal(t, []map[string]string{{"uploads/a.jpg": "jpg"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "media"))

	r.putBlob(upload, "changed")
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "media", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "jpg", r.blobAt(upload))
}

// scenario: bucket-restore-leaves-its-lakes (ADR-0312) — a Restore that names Bucket data in place rewinds its own objects, and
// the same engine holds f1, f2 and C2.
// oracle: docs/design/backup/bucket-lake-members.fizz#BucketRestoreLeavesItsLake
func TestScenarioBucketRestoreLeavesItsLakes(t *testing.T) {
	t.Parallel()
	engine := &countedEngine{}
	r := newWBRig(t, wbOptions{opts: []Option{WithLakeProviderRuntime(engine)}, setup: func(r *wbRig) { r.seedData() }})
	require.Eventually(t, func() bool { _, starts := engine.state(); return starts == 1 }, 10*time.Second, 10*time.Millisecond)
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	const later = "s3/orders/data/later.txt"
	r.putBlob(later, "later")
	r.putBlob(dataNotes, "changed")
	r.putBlob(salesFile2, "f2")
	r.putBlob(salesCatalog, "C2")

	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, []string{"n", "x", ""}, []string{r.blobAt(dataNotes), r.blobAt(dataGold2), r.blobAt(later)}, "the Bucket's own objects rewind")
	require.Equal(t, []string{"f1", "f2", "C2"}, []string{r.blobAt(salesFile), r.blobAt(salesFile2), r.blobAt(salesCatalog)}, "the Lake stays")
	running, starts := engine.state()
	require.True(t, running)
	require.Equal(t, 1, starts, "the same engine: a Bucket's Restore needs no stop")
}

// scenario: naming-the-lake-rolls-it-back (ADR-0312) — a Restore that names both in place, in either order, leaves the Bucket
// as above and the Lake with f1 and C1, no f2.
// oracle: docs/design/backup/bucket-lake-members.fizz#OrderDoesNotMatter
func TestScenarioNamingTheLakeRollsItBack(t *testing.T) {
	t.Parallel()
	engine := &countedEngine{}
	r := newWBRig(t, wbOptions{opts: []Option{WithLakeProviderRuntime(engine)}, setup: func(r *wbRig) { r.seedData() }})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	const later = "s3/orders/data/later.txt"
	bucket := v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}
	lake := v1.RestoreMember{Kind: v1.KindLake, Name: "sales", InPlace: true}
	for name, ms := range map[string][]v1.RestoreMember{"bucket-first": {bucket, lake}, "lake-first": {lake, bucket}} {
		r.putBlob(later, "later")
		r.putBlob(dataNotes, "changed")
		r.putBlob(salesFile2, "f2")
		r.putBlob(salesCatalog, "C2")
		rs := r.restoreObj(name, "b1", bucket)
		rs.Spec.Members = ms
		r.create(r.admin, rs)
		r.waitRestore(v1.ObjectName(name), v1.RunSucceeded)
		require.Equal(t, []string{"n", ""}, []string{r.blobAt(dataNotes), r.blobAt(later)}, name)
		require.Equal(t, []string{"f1", "", "C1"}, []string{r.blobAt(salesFile), r.blobAt(salesFile2), r.blobAt(salesCatalog)}, name)
	}
}

// scenario: bucket-restored-as-carries-no-lake (ADR-0312) — data restored as data-r, which holds only a Lake of its own, gets
// notes.txt and gold2/x, its Lake stays, and a second Restore is NotEmpty; naming sales asLocation too brings f1 and C1 to
// a free prefix. One Restore that names the Bucket as a new name and its Lake on that name brings the whole copy there, in
// either order.
func TestScenarioBucketRestoredAsCarriesNoLake(t *testing.T) {
	t.Parallel()
	const ownFile, ownCatalog = "s3/orders/data-r/own/o1", "lake-catalogs/orders/data-r/own/_ducklake/catalog.db"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seedData()
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data-r"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "own", Owner: v1.LakeOwner("own-lake")}, {Name: "free"}}}}, lakeNamed("own-lake", "data-r", "own"))
		r.putBlob(ownFile, "o1")
		r.putBlob(ownCatalog, "O1")
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("whole-a")}, &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("whole-b")})
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)

	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "data-r"}))
	r.waitRestore("copy", v1.RunSucceeded)
	require.Equal(t, []string{"n", "x"}, []string{r.blobAt("s3/orders/data-r/notes.txt"), r.blobAt("s3/orders/data-r/gold2/x")})
	require.Empty(t, r.blobAt("s3/orders/data-r/gold/f1"), "it carries no Lake")
	require.Empty(t, r.blobAt("lake-catalogs/orders/data-r/gold/_ducklake/catalog.db"))
	require.Equal(t, []string{"o1", "O1"}, []string{r.blobAt(ownFile), r.blobAt(ownCatalog)}, "its Lake stays")

	r.create(r.dev, r.restoreObj("again", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "data-r"}))
	require.Equal(t, "NotEmpty", reason(r.waitRestore("again", v1.RunFailed).Status.Status))

	r.create(r.dev, r.restoreObj("lake-copy", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales",
		AsLocation: &v1.LakeLocation{Bucket: "data-r", Prefix: "free"}}))
	r.waitRestore("lake-copy", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt("s3/orders/data-r/free/f1"), r.blobAt("lake-catalogs/orders/data-r/free/_ducklake/catalog.db")})

	for dest, bucketFirst := range map[v1.ObjectName]bool{"whole-a": true, "whole-b": false} {
		bucket := v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: dest}
		lake := v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: dest, Prefix: "gold"}}
		rs := r.restoreObj("copy-"+string(dest), "b1", bucket)
		rs.Spec.Members = []v1.RestoreMember{lake, bucket}
		if bucketFirst {
			rs.Spec.Members = []v1.RestoreMember{bucket, lake}
		}
		r.create(r.dev, rs)
		r.waitRestore(v1.ObjectName("copy-"+string(dest)), v1.RunSucceeded)
		root, catalogs := "s3/orders/"+string(dest)+"/", "lake-catalogs/orders/"+string(dest)+"/"
		require.Equal(t, []string{"n", "x", "f1", "C1"}, []string{r.blobAt(root + "notes.txt"), r.blobAt(root + "gold2/x"), r.blobAt(root + "gold/f1"),
			r.blobAt(catalogs + "gold/_ducklake/catalog.db")}, "%s holds the whole copy", dest)
	}
}

// ADR-0312 Decision 10: a Lake restored asLocation where a Lake is located is NotEmpty, that prefix empty or not: no hold
// covers this path, so no engine may stop there, and a running one would overwrite the load. A Restore first and the Lake
// applied after passes.
func TestScenarioAsLocationOnALakeIsNotEmpty(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seedData()
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data-r"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "own", Owner: v1.LakeOwner("own-lake")}, {Name: "free", Owner: v1.LakeOwner("later-lake")}}}},
			lakeNamed("own-lake", "data-r", "own"))
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	r.create(r.dev, r.restoreObj("on-lake", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: "data-r", Prefix: "own"}}))
	require.Equal(t, "NotEmpty", reason(r.waitRestore("on-lake", v1.RunFailed).Status.Status))
	require.Empty(t, r.blobAt("s3/orders/data-r/own/f1"), "nothing is written")
	require.Empty(t, r.blobAt("lake-catalogs/orders/data-r/own/_ducklake/catalog.db"))

	r.create(r.dev, r.restoreObj("before-lake", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: "data-r", Prefix: "free"}}))
	r.waitRestore("before-lake", v1.RunSucceeded)
	require.Equal(t, "C1", r.blobAt("lake-catalogs/orders/data-r/free/_ducklake/catalog.db"))
}

// scenario: load-skips-a-lakes-prefix (ADR-0312) — a Bucket's file that holds gold/f0 and a catalog of gold, from before Lake
// sales took gold: restored in place, neither is written and the Lake stays.
func TestScenarioLoadSkipsALakesPrefix(t *testing.T) {
	t.Parallel()
	const f0, oldCatalog = "s3/orders/data/gold/f0", "old"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "gold"}}}})
		r.putBlob(f0, "f0")
		r.putBlob(salesCatalog, oldCatalog)
		r.putBlob(dataNotes, "n")
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []map[string]string{{"gold/f0": "f0", "notes.txt": "n"}, {"gold/_ducklake/catalog.db": oldCatalog}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))

	require.NoError(t, r.p.cfg.blob.Delete(r.ctx, f0))
	obj, err := r.st.Get(r.ctx, v1.KindBucket.GVK(), "orders", "data")
	require.NoError(t, err)
	data := obj.(*v1.Bucket)
	data.Spec.Prefixes[0].Owner = v1.LakeOwner("sales")
	_, err = r.st.Update(r.ctx, data)
	require.NoError(t, err)
	r.seed(lakeNamed("sales", "data", "gold"))
	r.putBlob(salesFile, "f1")
	r.putBlob(salesCatalog, "C1")
	r.putBlob(dataNotes, "changed")

	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
	rs := r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "n", r.blobAt(dataNotes))
	require.Empty(t, r.blobAt(f0), "the copy holds it, the load skips it")
	require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt(salesFile), r.blobAt(salesCatalog)}, "the Lake stays")
	c, _ := rs.Status.Conditions.Get("Ready")
	require.Empty(t, c.Message, "a skip leaves no trace in the status")
}

// scenario: catalog-with-no-lake-is-the-buckets (ADR-0312) — a catalog on a prefix no Lake owns: blob/data/catalogs/ holds it, it
// comes back under the destination, and an as target that holds only such a catalog is NotEmpty.
func TestScenarioCatalogWithNoLakeIsTheBuckets(t *testing.T) {
	t.Parallel()
	const stray = "lake-catalogs/orders/data/old/_ducklake/catalog.db"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seedData()
		r.putBlob(stray, "C9")
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data-r")}, &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("taken")})
		r.putBlob("lake-catalogs/orders/taken/old/_ducklake/catalog.db", "C0")
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Contains(t, r.stub.PutKeys(b.Status.Path), b.Status.Path+"blob/data/catalogs/part-00000")
	require.Equal(t, []map[string]string{{"notes.txt": "n", "gold2/x": "x"}, {"old/_ducklake/catalog.db": "C9"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))

	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "data-r"}))
	r.waitRestore("copy", v1.RunSucceeded)
	require.Equal(t, "C9", r.blobAt("lake-catalogs/orders/data-r/old/_ducklake/catalog.db"))
	require.Empty(t, r.blobAt("s3/orders/data-r/old/_ducklake/catalog.db"), "no catalog among the objects")

	r.create(r.dev, r.restoreObj("onto-catalog", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "taken"}))
	require.Equal(t, "NotEmpty", reason(r.waitRestore("onto-catalog", v1.RunFailed).Status.Status))
	require.Equal(t, "C0", r.blobAt("lake-catalogs/orders/taken/old/_ducklake/catalog.db"))

	r.putBlob(stray, "C8")
	r.putBlob("lake-catalogs/orders/data/newer/_ducklake/catalog.db", "C7")
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "C9", r.blobAt(stray), "the rewind brings its catalog back")
	require.Empty(t, r.blobAt("lake-catalogs/orders/data/newer/_ducklake/catalog.db"), "and empties what it got since")
	require.Equal(t, "C1", r.blobAt(salesCatalog), "and leaves its Lake's")
}

// scenario: failed-restore-leaves-the-lakes (ADR-0312) — a Restore as Bucket new, with Lake fresh on new/gold, that fails on a
// put after fresh saved: what the load wrote is gone, and fresh's data stays.
func TestScenarioFailedRestoreLeavesTheLakes(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/b.csv", fail: true}
	const freshFile, freshCatalog = "s3/orders/new/gold/g1", "lake-catalogs/orders/new/gold/_ducklake/catalog.db"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("new"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: v1.LakeOwner("fresh")}}}}, lakeNamed("fresh", "new", "gold"))
		r.putBlob(freshFile, "g1")
		r.putBlob(freshCatalog, "G1")
		r.putBlob("s3/orders/orders-files/b.csv", "id\n")
	}})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	shared.armed.Store(true)
	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "orders-files", As: "new"}))
	r.waitRestore("copy", v1.RunFailed)
	require.Empty(t, r.blobAt("s3/orders/new/a.csv"), "what the load wrote is gone")
	require.Equal(t, []string{"g1", "G1"}, []string{r.blobAt(freshFile), r.blobAt(freshCatalog)}, "the Lake's data stays")
}

// scenario: gone-lake-keeps-its-prefix (ADR-0312) — with gold's owner, Lake sales, gone: a Backup of Bucket data holds f1 and C1 in
// the Bucket's copy, with no member of the Lake's, and the Bucket as a new name brings them back. A Bucket's in-place Restore
// leaves gold/, from a copy made with the Lake or without it, so a Lake applied again finds its newest data.
func TestScenarioGoneLakeKeepsItsPrefix(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seedData()
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data-r"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "free"}}}})
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("with-lake", "data-nightly"))
	r.waitBackup("with-lake", v1.RunSucceeded)
	require.NoError(t, r.st.Delete(r.ctx, v1.KindLake.GVK(), "orders", "sales", ""))

	r.create(r.dev, r.backupObj("gone-lake", "data-nightly"))
	b := r.waitBackup("gone-lake", v1.RunSucceeded)
	require.Equal(t, []string{"Bucket/data"}, memberNames(b))
	require.Equal(t, []map[string]string{{"notes.txt": "n", "gold2/x": "x", "gold/f1": "f1"}, {"gold/_ducklake/catalog.db": "C1"}},
		r.memberFiles(b.Status.Path, v1.KindBucket, "data"), "the Bucket's copy holds them, objects and catalog")
	lake := v1.RestoreMember{Kind: v1.KindLake, Name: "sales", InPlace: true}
	r.create(r.admin, r.restoreObj("names-it", "gone-lake", lake))
	require.Equal(t, "MemberNotFound", reason(r.waitRestore("names-it", v1.RunFailed).Status.Status))

	r.create(r.dev, r.restoreObj("elsewhere", "gone-lake", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "data-r"}))
	r.waitRestore("elsewhere", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt("s3/orders/data-r/gold/f1"), r.blobAt("lake-catalogs/orders/data-r/gold/_ducklake/catalog.db")})
	r.create(r.dev, r.restoreObj("its-member", "with-lake", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: "data-r", Prefix: "free"}}))
	r.waitRestore("its-member", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt("s3/orders/data-r/free/f1"), r.blobAt("lake-catalogs/orders/data-r/free/_ducklake/catalog.db")}, "a copy made with the Lake keeps its member")

	r.putBlob(salesFile2, "f2")
	r.putBlob(salesCatalog, "C2")
	for _, backup := range []string{"with-lake", "gone-lake"} {
		r.putBlob(dataNotes, "changed")
		r.putBlob("s3/orders/data/later.txt", "later")
		r.create(r.admin, r.restoreObj("rewind-"+backup, backup, v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
		r.waitRestore(v1.ObjectName("rewind-"+backup), v1.RunSucceeded)
		require.Equal(t, []string{"n", ""}, []string{r.blobAt(dataNotes), r.blobAt("s3/orders/data/later.txt")}, backup)
		require.Equal(t, []string{"f1", "f2", "C2"}, []string{r.blobAt(salesFile), r.blobAt(salesFile2), r.blobAt(salesCatalog)}, "%s: the rewind leaves gold/", backup)
	}

	r.seed(lakeNamed("sales", "data", "gold"))
	r.create(r.admin, r.restoreObj("applied-again", "with-lake", lake))
	r.waitRestore("applied-again", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "", "C1"}, []string{r.blobAt(salesFile), r.blobAt(salesFile2), r.blobAt(salesCatalog)})

	require.NoError(t, r.st.Delete(r.ctx, v1.KindLake.GVK(), "orders", "sales", ""))
	obj, err := r.st.Get(r.ctx, v1.KindBucket.GVK(), "orders", "data")
	require.NoError(t, err)
	data := obj.(*v1.Bucket)
	data.Spec.Prefixes[0].Owner = v1.PrefixOwner{}
	_, err = r.st.Update(r.ctx, data)
	require.NoError(t, err)
	r.putBlob(salesFile2, "f2")
	r.putBlob(salesCatalog, "C2")
	r.create(r.admin, r.restoreObj("the-buckets-now", "gone-lake", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
	r.waitRestore("the-buckets-now", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "", "C1"}, []string{r.blobAt(salesFile), r.blobAt(salesFile2), r.blobAt(salesCatalog)}, "in place once the owner field makes the prefix the Bucket's")
}

// scenario: gone-lake-keeps-its-prefix, a second prefix (ADR-0312 Decision 1) — every prefix of a gone owner is in the Bucket's copy,
// objects and catalog, and a Bucket's in-place Restore leaves them all.
func TestScenarioGoneOwnerOfTwoPrefixes(t *testing.T) {
	t.Parallel()
	objectOf := func(p string) string { return "s3/orders/data/" + p + "/f" }
	catalogOf := func(p string) string { return "lake-catalogs/orders/data/" + p + "/_ducklake/catalog.db" }
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
			{Name: "gold", Owner: v1.LakeOwner("sales")}, {Name: "gold3", Owner: v1.LakeOwner("sales")}}}})
		for _, p := range []string{"gold", "gold3"} {
			r.putBlob(objectOf(p), "f-"+p)
			r.putBlob(catalogOf(p), "C-"+p)
		}
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []string{"Bucket/data"}, memberNames(b))
	require.Equal(t, []map[string]string{{"gold/f": "f-gold", "gold3/f": "f-gold3"},
		{"gold/_ducklake/catalog.db": "C-gold", "gold3/_ducklake/catalog.db": "C-gold3"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))

	for _, p := range []string{"gold", "gold3"} {
		r.putBlob(objectOf(p), "kept")
		r.putBlob(catalogOf(p), "kept")
	}
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	for _, p := range []string{"gold", "gold3"} {
		require.Equal(t, []string{"kept", "kept"}, []string{r.blobAt(objectOf(p)), r.blobAt(catalogOf(p))}, p)
	}
}

// scenario: gone-lake-keeps-its-prefix, a Lake's other prefix (ADR-0312 Decision 1) — a Lake's Lake prefix is where it is located.
// Another prefix it owns stays in the Bucket's copy, objects and catalog, and comes back with the Bucket's Restore.
// oracle: docs/design/backup/bucket-lake-members.fizz#BucketsOwnComesBack
func TestScenarioOneOwnerOfTwoPrefixes(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ lake, other string }{
		"located on the first":  {"gold", "gold3"},
		"located on the second": {"gold3", "gold"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			objectOf := func(p string) string { return "s3/orders/data/" + p + "/f" }
			catalogOf := func(p string) string { return "lake-catalogs/orders/data/" + p + "/_ducklake/catalog.db" }
			r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
				r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{
					{Name: "gold", Owner: v1.LakeOwner("sales")}, {Name: "gold3", Owner: v1.LakeOwner("sales")}}}}, lakeNamed("sales", "data", c.lake))
				for _, p := range []string{"gold", "gold3"} {
					r.putBlob(objectOf(p), "f-"+p)
					r.putBlob(catalogOf(p), "C-"+p)
				}
			}})
			r.dataSchedule("data-nightly")
			r.create(r.dev, r.backupObj("b1", "data-nightly"))
			b := r.waitBackup("b1", v1.RunSucceeded)
			require.Equal(t, []string{"Bucket/data", "Lake/sales"}, memberNames(b))
			require.Equal(t, []map[string]string{{c.other + "/f": "f-" + c.other}, {c.other + "/_ducklake/catalog.db": "C-" + c.other}},
				r.memberFiles(b.Status.Path, v1.KindBucket, "data"), "the other prefix is the Bucket's, objects and catalog")
			require.Equal(t, []map[string]string{{"f": "f-" + c.lake}, {workload.CatalogRecord: "C-" + c.lake}},
				r.memberFiles(b.Status.Path, v1.KindLake, "sales"), "its Lake prefix is the Lake's")

			r.putBlob(objectOf(c.other), "changed")
			r.putBlob(catalogOf(c.other), "changed")
			r.putBlob(objectOf(c.lake), "kept")
			r.putBlob(catalogOf(c.lake), "kept")
			r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
			r.waitRestore("rewind", v1.RunSucceeded)
			require.Equal(t, []string{"f-" + c.other, "C-" + c.other}, []string{r.blobAt(objectOf(c.other)), r.blobAt(catalogOf(c.other))}, "the Bucket's own comes back")
			require.Equal(t, []string{"kept", "kept"}, []string{r.blobAt(objectOf(c.lake)), r.blobAt(catalogOf(c.lake))}, "and its Lake prefix stays")
		})
	}
}

// ADR-0312's first Risk: a prefix that stops being a Lake prefix after the copy, the Lake moved or the owner edited, is the
// Bucket's by its in-place Restore, whose file lacks it, so a Succeeded Restore deletes it. The copy's Lake member and the
// <restore>-before export hold it.
func TestScenarioLakeMovedThenBucketRestored(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*wbRig){
		"the Lake moved": func(r *wbRig) {
			obj, err := r.st.Get(r.ctx, v1.KindLake.GVK(), "orders", "sales")
			require.NoError(r.t, err)
			lake := obj.(*v1.Lake)
			lake.Spec.Location.Prefix, lake.Spec.Bindings.Blob[0].Prefix = "silver", "silver"
			_, err = r.st.Update(r.ctx, lake)
			require.NoError(r.t, err)
		},
		"the owner edited": func(r *wbRig) {
			obj, err := r.st.Get(r.ctx, v1.KindBucket.GVK(), "orders", "data")
			require.NoError(r.t, err)
			data := obj.(*v1.Bucket)
			data.Spec.Prefixes[0].Owner = v1.FunctionOwner("uploader")
			_, err = r.st.Update(r.ctx, data)
			require.NoError(r.t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
				r.seedData()
				r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data-r"), Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "free"}}}})
			}})
			r.dataSchedule("data-nightly")
			r.create(r.dev, r.backupObj("b1", "data-nightly"))
			b := r.waitBackup("b1", v1.RunSucceeded)
			require.Equal(t, []map[string]string{{"notes.txt": "n", "gold2/x": "x"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))
			change(r)

			r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}))
			r.waitRestore("rewind", v1.RunSucceeded)
			require.Equal(t, []string{"", ""}, []string{r.blobAt(salesFile), r.blobAt(salesCatalog)}, "the Restore deletes it, and its file cannot bring it back")

			r.create(r.dev, r.restoreObj("from-export", "rewind-before", v1.RestoreMember{Kind: v1.KindBucket, Name: "data", As: "data-r"}))
			r.waitRestore("from-export", v1.RunSucceeded)
			require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt("s3/orders/data-r/gold/f1"), r.blobAt("lake-catalogs/orders/data-r/gold/_ducklake/catalog.db")}, "the export holds it")
			r.create(r.dev, r.restoreObj("from-member", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: "data-r", Prefix: "free"}}))
			r.waitRestore("from-member", v1.RunSucceeded)
			require.Equal(t, []string{"f1", "C1"}, []string{r.blobAt("s3/orders/data-r/free/f1"), r.blobAt("lake-catalogs/orders/data-r/free/_ducklake/catalog.db")}, "and so does the copy's Lake member")
		})
	}
}

// scenario: a-lake-without-a-catalog-keeps-its-files (ADR-0312), its Restore — with its Bucket skipping the prefix, the Lake's
// member alone holds an object from before its first commit, with no catalog, and a Restore brings it back.
func TestScenarioLakeWithoutCatalogComesBack(t *testing.T) {
	t.Parallel()
	const object = "s3/orders/data/gold/x.parquet"
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: v1.LakeOwner("sales")}, {Name: "free"}}}}, lakeNamed("sales", "data", "gold"))
		r.putBlob(object, "x")
	}})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.Equal(t, []map[string]string{{}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))
	require.Equal(t, []map[string]string{{"x.parquet": "x"}}, r.memberFiles(b.Status.Path, v1.KindLake, "sales"))

	r.create(r.dev, r.restoreObj("elsewhere", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", AsLocation: &v1.LakeLocation{Bucket: "data", Prefix: "free"}}))
	r.waitRestore("elsewhere", v1.RunSucceeded)
	require.Equal(t, "x", r.blobAt("s3/orders/data/free/x.parquet"))
	r.putBlob(object, "changed")
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "sales", InPlace: true}))
	r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, "x", r.blobAt(object))
}

// onFirstList is the shared blob store that runs fn at its first list under the prefix on, once armed.
type onFirstList struct {
	blob.Bucket
	on    string
	armed atomic.Bool
	fn    func()
}

func (b *onFirstList) ListAfter(ctx context.Context, prefix, after string, n int) ([]blob.Attributes, bool, error) {
	if strings.HasPrefix(prefix, b.on) && b.armed.CompareAndSwap(true, false) {
		b.fn()
	}
	return b.Bucket.ListAfter(ctx, prefix, after, n)
}

// ADR-0312 Decision 3: Write reads no store, so a Lake created during a run, on a prefix with objects, leaves each byte in one
// member: the Bucket's, by the Lake prefixes Resolve read.
func TestScenarioLakeCreatedDuringARunIsInOneMember(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	hook := &onFirstList{Bucket: mem, on: "s3/orders/data/"}
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(hook)}, setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("data"), Spec: v1.BucketSpec{
			Prefixes: []v1.BucketPrefix{{Name: "late", Owner: v1.LakeOwner("late-lake")}}}})
		r.putBlob("s3/orders/data/late/o", "o")
		r.putBlob("lake-catalogs/orders/data/late/_ducklake/catalog.db", "CL")
	}})
	created := make(chan error, 1)
	hook.fn = func() {
		_, err := r.st.Create(r.ctx, lakeNamed("late-lake", "data", "late"))
		created <- err
	}
	r.dataSchedule("data-nightly")
	hook.armed.Store(true)
	r.create(r.dev, r.backupObj("b1", "data-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	require.NoError(t, <-created)
	require.Equal(t, []string{"Bucket/data"}, memberNames(b))
	require.Equal(t, []map[string]string{{"late/o": "o"}, {"late/_ducklake/catalog.db": "CL"}}, r.memberFiles(b.Status.Path, v1.KindBucket, "data"))
}

// ADR-0312's Consequences: a Bucket-only schedule copies its Lakes, so a Lake that changes under every copy fails it
// LakeUnstable and fails the Bucket's in-place Restore ExportFailed, until the next try.
func TestABusyLakeFailsABucketSchedule(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	const vanishing = "s3/orders/data/gold/gone"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(&cleanupBucket{Bucket: mem, gone: vanishing})}, setup: func(r *wbRig) { r.seedData() }})
	r.dataSchedule("data-nightly")
	r.create(r.dev, r.backupObj("good", "data-nightly"))
	r.waitBackup("good", v1.RunSucceeded)

	r.putBlob(vanishing, "listed, and gone at every read")
	r.create(r.dev, r.backupObj("busy", "data-nightly"))
	require.Equal(t, "LakeUnstable", reason(r.waitBackup("busy", v1.RunFailed).Status.Status))
	bucket := v1.RestoreMember{Kind: v1.KindBucket, Name: "data", InPlace: true}
	r.create(r.admin, r.restoreObj("rewind", "good", bucket))
	require.Equal(t, "ExportFailed", reason(r.waitRestore("rewind", v1.RunFailed).Status.Status))

	require.NoError(t, r.p.cfg.blob.Delete(r.ctx, vanishing))
	r.create(r.dev, r.backupObj("next", "data-nightly"))
	r.waitBackup("next", v1.RunSucceeded)
	r.create(r.admin, r.restoreObj("rewind-again", "good", bucket))
	r.waitRestore("rewind-again", v1.RunSucceeded)
}
