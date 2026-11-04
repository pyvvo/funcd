package funcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/volume"
)

// scenario: each-kind-round-trips-as-today, a Bucket's destinations (ADR-0311, ADR-0312) — restored as the empty Bucket
// lake-files-r, a Bucket's copy brings its objects, with their type and metadata, and none of its Lake's, the source
// keeping what it got since; restored in place, the Bucket holds the copy's objects again and none of what it got since,
// and its Lake stays as it is.
func TestScenarioRestoreBucketAsAndInPlace(t *testing.T) {
	t.Parallel()
	k := newKindsRig(t, nil, nil, nil, func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("lake-files-r")})
	})
	k.create(k.dev, k.backupObj("b1", "kinds-nightly"))
	k.waitBackup("b1", v1.RunSucceeded)
	files, catalogs := bucketPrefix("orders", "lake-files"), lakeCatalogPrefix("orders")
	require.NoError(t, k.p.cfg.blob.Put(k.ctx, files+"later.txt", []byte("later"), blob.PutOptions{}))
	require.NoError(t, k.p.cfg.blob.Put(k.ctx, catalogs+"lake-files/lake/_ducklake/catalog.db", []byte("C2"), blob.PutOptions{}))

	k.create(k.dev, k.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "lake-files", As: "lake-files-r"}))
	k.waitRestore("copy", v1.RunSucceeded)
	copied := bucketPrefix("orders", "lake-files-r")
	require.Empty(t, k.blobAt(copied+"lake/main/t/f1.parquet"), "the Lake's file is its member's")
	attrs, err := k.p.cfg.blob.Attributes(k.ctx, copied+"notes.txt")
	require.NoError(t, err)
	require.Equal(t, []string{"n", "text/plain", "export"}, []string{k.blobAt(copied + "notes.txt"), attrs.ContentType, attrs.Metadata["source"]})
	require.Empty(t, k.blobAt(copied+"later.txt"))
	require.Empty(t, k.blobAt(catalogs+"lake-files-r/lake/_ducklake/catalog.db"), "and so is its catalog")
	require.Empty(t, k.blobAt(copied+"lake/_ducklake/catalog.db"), "no catalog among the objects")
	require.Equal(t, "later", k.blobAt(files+"later.txt"), "the source is unchanged")
	require.Equal(t, "C2", k.blobAt(catalogs+"lake-files/lake/_ducklake/catalog.db"))

	k.create(k.admin, k.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "lake-files", InPlace: true}))
	k.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, []string{"f1", "n", ""}, []string{k.blobAt(files + "lake/main/t/f1.parquet"), k.blobAt(files + "notes.txt"),
		k.blobAt(files + "later.txt")}, "the in-place load empties the Bucket first")
	require.Equal(t, "C2", k.blobAt(catalogs+"lake-files/lake/_ducklake/catalog.db"), "the Lake keeps what it holds")
}

// scenario: each-kind-round-trips-as-today, a volume's destination — cart's volume state restored as cart2's empty
// volume of that name gets the copy's files, and cart's keeps what it got since.
func TestScenarioVolumeRestoreAsAnotherFunction(t *testing.T) {
	t.Parallel()
	k := newKindsRig(t, nil, []v1.ObjectName{"cart2"}, nil, nil)
	k.create(k.dev, k.backupObj("b1", "kinds-nightly"))
	k.waitBackup("b1", v1.RunSucceeded)
	require.NoError(t, os.WriteFile(filepath.Join(k.leaves["cart/state"], "newer.txt"), []byte("after"), 0o600))

	k.create(k.dev, k.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", As: "cart2"}))
	k.waitRestore("copy", v1.RunSucceeded)
	stores := k.stores()
	require.Equal(t, "rows", stores["volume:cart2/state/orders.sqlite"])
	require.NotContains(t, stores, "volume:cart2/state/newer.txt")
	require.Equal(t, "after", stores["volume:cart/state/newer.txt"], "the source is unchanged")
}

// Decision 5's reasons (ADR-0311): a member the Backup does not hold and a destination that does not exist each fail a
// Restore MemberNotFound, writing nothing.
func TestRestoreReason_MemberNotFound(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	for name, m := range map[string]v1.RestoreMember{
		"not-held":  {Kind: v1.KindKVStore, Name: "orders-state-r", As: "orders-state"},
		"no-store":  {Kind: v1.KindKVStore, Name: "orders-state", As: "gone-state"},
		"no-bucket": {Kind: v1.KindBucket, Name: "orders-files", As: "gone-files"},
	} {
		r.create(r.dev, r.restoreObj(name, "b1", m))
		rs := r.waitRestore(v1.ObjectName(name), v1.RunFailed)
		require.Equal(t, "MemberNotFound", reason(rs.Status.Status), name)
	}
	require.Equal(t, map[string]string{"t/1": "one"}, r.kv("orders-state"), "nothing is written")
}

// tearObject cuts the last byte off the frame of object key in Bucket bucket's file of the copy under path, then seals
// the file again to the copy's recipients and records its new size and sha256: the file is whole, the frame is torn.
func (r *wbRig) tearObject(path string, bucket v1.ObjectName, key string) {
	r.t.Helper()
	src := r.target()
	m, err := workload.ReadManifest(r.ctx, src, path)
	require.NoError(r.t, err)
	unseal, err := envelope.Opener([]age.Identity{r.owner})(m.Recipients)
	require.NoError(r.t, err)
	i := slices.IndexFunc(m.Members, func(mf workload.MemberFile) bool { return mf.Kind == v1.KindBucket && mf.Name == bucket })
	require.GreaterOrEqual(r.t, i, 0, "the copy holds Bucket %s", bucket)
	f := &m.Members[i].Files[0]
	sealer, err := envelope.Parse([]string{r.owner.Recipient().String(), r.recovery.Recipient().String()})
	require.NoError(r.t, err)
	var sealed bytes.Buffer
	wc, err := sealer.Seal()(&sealed)
	require.NoError(r.t, err)
	next := workload.Records(r.ctx, src, path, *f, unseal)
	for {
		rec, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(r.t, err)
		if string(rec.Key) == key {
			rec.Value = rec.Value[:len(rec.Value)-1]
		}
		require.NoError(r.t, backup.WriteRecord(wc, rec))
	}
	require.NoError(r.t, wc.Close())
	sum := sha256.Sum256(sealed.Bytes())
	f.Parts, f.Bytes, f.SHA256 = 1, int64(sealed.Len()), hex.EncodeToString(sum[:])
	r.stub.Seed(path+f.Name+"/part-00000", sealed.Bytes())
	data, err := yaml.Marshal(m)
	require.NoError(r.t, err)
	r.stub.Seed(path+"manifest.yaml", data)
}

// Decision 5's reasons: an object record whose frame is torn, in a copy whose files are whole (re-sealed, its sha256
// updated), fails a Restore Integrity.
func TestRestoreReason_TornObjectIsIntegrity(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("orders-files-r")})
	}})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	b := r.waitBackup("b1", v1.RunSucceeded)
	r.tearObject(b.Status.Path, "orders-files", "a.csv")

	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "orders-files", As: "orders-files-r"}))
	rs := r.waitRestore("copy", v1.RunFailed)
	c, _ := rs.Status.Conditions.Get("Ready")
	require.Equal(t, []string{"Integrity", "workload.DecodeObject: torn or trailing body"}, []string{c.Reason, c.Message},
		"the frame, not the file")
}

// Decision 5's reasons: a database whose engine is not Ready fails a Restore EngineNotReady, at the plan's emptiness
// check of an as target and at the load in place.
func TestRestoreReason_EngineNotReady(t *testing.T) {
	t.Parallel()
	engines := newFileEngines(t)
	engines.notReady["stopped-db"] = true
	r := sqlRig(t, engines, "todo-db", "stopped-db")
	engines.engine(t, "todo-db").set(func(e *fileEngine) { e.data = sqlite("milk") })
	r.create(r.dev, r.dbSchedule("todo-db"))
	r.create(r.dev, r.backupObj("b1", "orders-db"))
	r.waitBackup("b1", v1.RunSucceeded)
	r.create(r.dev, r.restoreObj("into-stopped", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", As: "stopped-db"}))
	rs := r.waitRestore("into-stopped", v1.RunFailed)
	require.Equal(t, "EngineNotReady", reason(rs.Status.Status), "at the plan")

	// the in-place Restore's export copies orders-state only, so its load is the first to need todo-db's engine
	s := r.getSchedule("orders-db")
	s.Spec.Scope, s.ResourceVersion = v1.BackupScope{KVStores: []v1.ObjectName{"orders-state"}}, ""
	_, err := r.dev.Apply(r.ctx, s)
	require.NoError(t, err)
	engines.mu.Lock()
	engines.notReady["todo-db"] = true
	engines.mu.Unlock()
	require.Eventually(t, func() bool {
		obj, err := r.st.Get(r.ctx, v1.KindSQLDatabase.GVK(), "orders", "todo-db")
		return err == nil && obj.(*v1.SQLDatabase).Status.Phase != v1.PhaseReady
	}, 10*time.Second, 10*time.Millisecond, "todo-db's engine stops being Ready")
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindSQLDatabase, Name: "todo-db", InPlace: true}))
	rs = r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "EngineNotReady", reason(rs.Status.Status), "at the load")
}

// Decision 5's reasons: a volume as target whose Function declares it but has no leaf for it, its pass having found no
// capacity, fails a Restore Failed: only a missing member or destination is MemberNotFound.
func TestRestoreReason_VolumeWithoutLeafIsFailed(t *testing.T) {
	t.Parallel()
	fn := func(name string, size int64) *v1.Function {
		return &v1.Function{TypeMeta: tmeta(v1.KindFunction), ObjectMeta: ordersMeta(name), Spec: v1.FunctionSpec{
			Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs",
			Volumes: []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: size}},
		}}
	}
	r := newWBRig(t, wbOptions{
		opts:  []Option{WithFunctionVolumes(volume.Options{Root: t.TempDir(), CapacityBytes: 2 << 20}, false)},
		setup: func(r *wbRig) { r.seed(fn("cart", 1<<20), fn("big", 4<<20)) },
	})
	dir, err := r.p.exec.volumes.Reserve(r.ctx, volume.ID{Namespace: "orders", Function: "cart", Name: "state"}, r.getFunctionUID("cart"),
		1<<20, v1.DeletionRetain)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders.sqlite"), []byte("rows"), 0o600))
	r.create(r.dev, r.schedule("orders-vol", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Volumes: []string{"cart/state"}}
	}))
	r.create(r.dev, r.backupObj("b1", "orders-vol"))
	r.waitBackup("b1", v1.RunSucceeded)

	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindFunction, Name: "cart", Volume: "state", As: "big"}))
	rs := r.waitRestore("copy", v1.RunFailed)
	c, _ := rs.Status.Conditions.Get("Ready")
	require.Equal(t, "Failed", c.Reason)
	require.Contains(t, c.Message, "open volume", "the emptiness check finds no leaf")
}

// putLog is the shared blob store, noting the key of each Put.
type putLog struct {
	blob.Bucket
	mu   sync.Mutex
	keys []string
}

func (b *putLog) Put(ctx context.Context, key string, data []byte, o blob.PutOptions) error {
	b.mu.Lock()
	b.keys = append(b.keys, key)
	b.mu.Unlock()
	return b.Bucket.Put(ctx, key, data, o)
}

// under returns the keys put under prefix or equal to key, in order, and forgets every key.
func (b *putLog) under(prefix, key string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, k := range b.keys {
		if strings.HasPrefix(k, prefix) || k == key {
			out = append(out, k)
		}
	}
	b.keys = nil
	return out
}

// Decision 4's Lake row (ADR-0311): a Restore puts a lake's files before its catalog, so the catalog never names a file
// that is not there yet.
func TestRestoreLake_PutsFilesBeforeCatalog(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &putLog{Bucket: mem}
	files, catalogs := bucketPrefix("orders", "lake-files"), lakeCatalogPrefix("orders")+"lake-files/"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		r.lakeOn("lake-files", map[string]string{catalogs + "lake/_ducklake/catalog.db": "C1",
			files + "lake/main/t/f1.parquet": "f1", files + "lake/main/t/f2.parquet": "f2"})
	}})
	r.create(r.dev, r.schedule("lake-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope = v1.BackupScope{Lakes: []v1.ObjectName{"lake"}}
	}))
	r.create(r.dev, r.backupObj("b1", "lake-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	shared.under("", "")

	r.create(r.dev, r.restoreObj("copy", "b1", v1.RestoreMember{Kind: v1.KindLake, Name: "lake",
		AsLocation: &v1.LakeLocation{Bucket: "lake-files", Prefix: "copy"}}))
	r.waitRestore("copy", v1.RunSucceeded)
	require.Equal(t, []string{files + "copy/main/t/f1.parquet", files + "copy/main/t/f2.parquet", catalogs + "copy/_ducklake/catalog.db"},
		shared.under(files+"copy/", catalogs+"copy/_ducklake/catalog.db"))
}
