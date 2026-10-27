package funcd

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmem "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const qAdmin = "quarantine-admin"

// qWorld is what a quarantine scenario keeps across starts: the metastore, the KV substrate, how to open the blob
// store again (a start closes it at its stop), the quarantine's clock and the log.
type qWorld struct {
	st   store.Store
	kv   kvstore.KV
	open func(t *testing.T) blob.Bucket
	clk  *clock.Manual
	log  *syncBuffer
}

func newQWorld(t *testing.T, open func(t *testing.T) blob.Bucket) *qWorld {
	t.Helper()
	return &qWorld{st: store.New(storemem.New()), kv: kvmem.New(), open: open,
		clk: clock.NewManual(time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)), log: &syncBuffer{}}
}

func fileStore(t *testing.T) func(t *testing.T) blob.Bucket {
	t.Helper()
	url := gocloud.FileURL(t.TempDir())
	return func(t *testing.T) blob.Bucket {
		b, err := gocloud.Open(context.Background(), url)
		require.NoError(t, err)
		return b
	}
}

// qRun is one start of the platform on w.
type qRun struct {
	ctx    context.Context
	p      *Platform
	shared blob.Bucket
	admin  *sdk.Client
	stop   func()
}

// start runs a platform on w, and returns once its control plane serves: after the start's reclaims.
func (w *qWorld) start(t *testing.T, opts ...Option) *qRun {
	t.Helper()
	shared := w.open(t)
	base := []Option{InMemory(), WithStore(w.st), WithKVStore(w.kv), WithBlob(shared),
		WithCredentials(Credential{Token: qAdmin, Role: "admin"}),
		WithLogger(slog.New(slog.NewTextHandler(w.log, &slog.HandlerOptions{Level: slog.LevelInfo}))),
		func(c *config) error { c.quarantineClock = w.clk; return nil }}
	p, err := New(append(base, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("platform Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	admin, err := sdk.New("http://"+p.Addr(), sdk.WithToken(qAdmin))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, herr := admin.HoldStatus(ctx)
		return herr == nil
	}, 10*time.Second, 10*time.Millisecond)
	return &qRun{ctx: ctx, p: p, shared: shared, admin: admin, stop: stop}
}

// orphans seeds a Bucket prefix and a KV store prefix that no object names.
func (w *qWorld) orphans(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	shared := w.open(t)
	for _, k := range []string{"s3/default/gone/a", "s3/default/gone/b/c"} {
		require.NoError(t, shared.Put(ctx, k, []byte(k), blob.PutOptions{ContentType: "text/plain"}))
	}
	require.NoError(t, shared.Close())
	require.NoError(t, w.kv.Put(ctx, "default/old/t/k", []byte("old")))
}

func (r *qRun) quarantine(t *testing.T) []hold.QuarantineEntry {
	t.Helper()
	ev, err := r.admin.HoldStatus(r.ctx)
	require.NoError(t, err)
	return ev.Quarantine
}

func (r *qRun) objects(t *testing.T, prefix string) []string {
	t.Helper()
	items, err := r.shared.List(r.ctx, prefix)
	require.NoError(t, err)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, strings.TrimPrefix(it.Key, prefix))
	}
	return out
}

func (w *qWorld) kvKeys(t *testing.T, prefix string) []string {
	t.Helper()
	keys, err := w.kv.List(context.Background(), prefix)
	require.NoError(t, err)
	return keys
}

func (r *qRun) apply(t *testing.T, objs ...v1.Object) {
	t.Helper()
	for _, o := range objs {
		_, err := r.admin.Apply(r.ctx, o)
		require.NoError(t, err)
	}
}

func goneBucket() *v1.Bucket {
	return &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "gone", Namespace: "default", ResourceGroup: "rg1"}}
}

func oldStore() *v1.KVStore {
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: "old", Namespace: "default", ResourceGroup: "rg1"}}
	ks.Spec.Tables = []v1.KVTable{{Name: "t"}}
	return ks
}

// scenario: orphan-quarantined — a Bucket prefix with objects and a KV store prefix with keys, neither named by an
// object: an unheld start empties both prefixes, hold status lists both with their since and until, and one Warn
// line names each; on a file store the Bucket's objects keep their bytes and content type.
func TestScenarioOrphanQuarantined(t *testing.T) {
	t.Parallel()
	w := newQWorld(t, fileStore(t))
	w.orphans(t)
	r := w.start(t)

	since := v1.NewTimestamp(w.clk.Now())
	until := v1.NewTimestamp(w.clk.Now().Add(168 * time.Hour))
	require.Equal(t, []hold.QuarantineEntry{
		{Kind: v1.KindBucket, Namespace: "default", Name: "gone", Since: since, Until: until, State: "kept"},
		{Kind: v1.KindKVStore, Namespace: "default", Name: "old", Since: since, Until: until, State: "kept"},
	}, r.quarantine(t))
	require.Empty(t, r.objects(t, "s3/default/gone/"))
	require.Empty(t, w.kvKeys(t, "default/old/"))
	data := "quarantine/bucket/default/gone/" + quarantine.Stamp(w.clk.Now()) + "/data/"
	require.ElementsMatch(t, []string{"a", "b/c"}, r.objects(t, data))
	a, err := r.shared.Attributes(r.ctx, data+"a")
	require.NoError(t, err)
	require.Equal(t, "text/plain", a.ContentType)
	for _, line := range []string{"moved the data of a Bucket with no object into the quarantine",
		"moved the data of a KVStore with no object into the quarantine"} {
		require.Equal(t, 1, strings.Count(w.log.String(), line), line)
	}
}

// scenario: same-name-starts-empty — once the start quarantined them, a Bucket and a KVStore applied under the
// orphans' names start empty, and the quarantine keeps their old data.
// oracle: docs/design/storage/quarantine.fizz#NoReturnUnasked
func TestScenarioSameNameStartsEmpty(t *testing.T) {
	t.Parallel()
	w := newQWorld(t, fileStore(t))
	w.orphans(t)
	r := w.start(t)
	r.apply(t, goneBucket(), oldStore())
	require.Eventually(t, func() bool {
		obj, err := r.admin.Get(r.ctx, v1.KindKVStore, "default", "old")
		return err == nil && obj.(*v1.KVStore).Status.Phase == v1.PhaseReady
	}, 5*time.Second, 10*time.Millisecond)
	require.Empty(t, r.objects(t, "s3/default/gone/"))
	require.Empty(t, w.kvKeys(t, "default/old/"))
	require.Len(t, r.quarantine(t), 2, "the quarantine keeps the old data")
}

// scenario: quarantine-returned — the return refuses an object that is missing or holds data, moving nothing; into
// the re-applied empty object it brings the data back and hold status no longer lists it; run again it is NotFound.
// oracle: docs/design/storage/quarantine.fizz#ReturnsIntoEmpty
func TestScenarioQuarantineReturned(t *testing.T) {
	t.Parallel()
	w := newQWorld(t, fileStore(t))
	w.orphans(t)
	r := w.start(t)
	bucket := hold.QuarantineReturn{Kind: v1.KindBucket, Namespace: "default", Name: "gone"}
	kv := hold.QuarantineReturn{Kind: v1.KindKVStore, Namespace: "default", Name: "old"}
	for _, q := range []hold.QuarantineReturn{bucket, kv} {
		require.Equal(t, fault.Conflict, fault.KindOf(r.admin.ReturnQuarantined(r.ctx, q)), "%s: no object", q.Kind)
	}

	r.apply(t, goneBucket(), oldStore())
	require.NoError(t, r.shared.Put(r.ctx, "s3/default/gone/new", []byte("n"), blob.PutOptions{}))
	require.NoError(t, w.kv.Put(r.ctx, "default/old/t/new", []byte("n")))
	for _, q := range []hold.QuarantineReturn{bucket, kv} {
		require.Equal(t, fault.Conflict, fault.KindOf(r.admin.ReturnQuarantined(r.ctx, q)), "%s: data in it", q.Kind)
	}
	for _, e := range r.quarantine(t) {
		require.Equal(t, "kept", e.State, "a refused return moves nothing and starts no return: %s", e.Kind)
	}
	require.NoError(t, r.shared.Delete(r.ctx, "s3/default/gone/new"))
	require.NoError(t, w.kv.Delete(r.ctx, "default/old/t/new"))

	for _, q := range []hold.QuarantineReturn{bucket, kv} {
		require.NoError(t, r.admin.ReturnQuarantined(r.ctx, q), q.Kind)
	}
	require.ElementsMatch(t, []string{"a", "b/c"}, r.objects(t, "s3/default/gone/"))
	got, err := r.shared.Get(r.ctx, "s3/default/gone/b/c")
	require.NoError(t, err)
	require.Equal(t, "s3/default/gone/b/c", string(got))
	v, found, err := w.kv.Get(r.ctx, "default/old/t/k")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "old", string(v))
	require.Empty(t, r.quarantine(t))
	for _, q := range []hold.QuarantineReturn{bucket, kv} {
		require.Equal(t, fault.NotFound, fault.KindOf(r.admin.ReturnQuarantined(r.ctx, q)), q.Kind)
	}
}

// scenario: crash-during-move — a start whose move of a Bucket's objects is cut (the S3 store refuses the second
// copy) leaves the entry moving and goes on; the next start resumes that entry, and it ends kept with every object.
// oracle: docs/design/storage/quarantine.fizz#NothingLost
// oracle: docs/design/storage/quarantine.fizz#KeptIsWhole
// oracle: docs/design/storage/quarantine.fizz#MoveEnds
func TestScenarioCrashDuringMove(t *testing.T) {
	t.Parallel()
	s := s3stub.New(t, nil)
	creds := s3stub.CredentialsFile(t, "AKIDQUARANTINE")
	w := newQWorld(t, func(t *testing.T) blob.Bucket {
		b, err := gocloud.OpenWith(context.Background(), s.URL(), gocloud.OpenOptions{CredentialsFile: creds})
		require.NoError(t, err)
		return b
	})
	w.orphans(t)
	data := "quarantine/bucket/default/gone/" + quarantine.Stamp(w.clk.Now()) + "/data/"
	s.Set(func(s *s3stub.Stub) { s.FailPut = func(key string) bool { return key == data+"b/c" } })

	first := w.start(t)
	entries := first.quarantine(t)
	require.Len(t, entries, 2)
	require.Equal(t, "moving", entries[0].State, "the cut move's entry")
	require.Equal(t, []string{"a"}, first.objects(t, data))
	require.Equal(t, []string{"b/c"}, first.objects(t, "s3/default/gone/"), "nothing is lost: the rest stays")
	first.stop()

	s.Set(func(s *s3stub.Stub) { s.FailPut = nil })
	w.clk.Advance(time.Minute)
	second := w.start(t)
	entries = second.quarantine(t)
	require.Len(t, entries, 2)
	require.Equal(t, v1.NewTimestamp(w.clk.Now().Add(-time.Minute)), entries[0].Since, "resumed, not a new entry")
	require.Equal(t, "kept", entries[0].State)
	require.ElementsMatch(t, []string{"a", "b/c"}, second.objects(t, data))
	require.Empty(t, second.objects(t, "s3/default/gone/"))
}

// scenario: quarantine-expires — the sweep deletes an entry once it is one period old, and logs it; while held, and
// during a start, nothing is deleted.
func TestScenarioQuarantineExpires(t *testing.T) {
	t.Parallel()
	w := newQWorld(t, fileStore(t))
	w.orphans(t)
	first := w.start(t)
	first.p.sweepQuarantine(first.ctx)
	require.Len(t, first.quarantine(t), 2, "younger than the period")
	first.stop()

	w.clk.Advance(168*time.Hour + time.Millisecond)
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(w.clk.Now())}))
	h, err := hold.Open(dir)
	require.NoError(t, err)
	held := w.start(t, WithHold(h))
	require.Len(t, held.quarantine(t), 2, "a start deletes nothing")
	held.p.sweepQuarantine(held.ctx)
	require.Len(t, held.quarantine(t), 2, "nothing is deleted while held")
	held.stop()

	r := w.start(t)
	require.Len(t, r.quarantine(t), 2, "a start deletes nothing")
	r.p.sweepQuarantine(r.ctx)
	require.Empty(t, r.quarantine(t))
	require.Empty(t, r.objects(t, "quarantine/"))
	require.Empty(t, w.kvKeys(t, ".quarantine/"))
	require.Equal(t, 2, strings.Count(w.log.String(), "deleted quarantined data"))
}

// scenario: catalog-quarantined-with-its-bucket (ADR-0293) — an orphan Bucket's Lake catalogs go into its entry with
// its objects and come back with them; a Bucket whose only data is a catalog is an orphan too, and its entry expires
// with the catalog.
func TestScenarioCatalogQuarantinedWithItsBucket(t *testing.T) {
	t.Parallel()
	w := newQWorld(t, fileStore(t))
	w.orphans(t)
	const catalog = "gold/_ducklake/catalog.db"
	shared := w.open(t)
	for _, k := range []string{"lake-catalogs/default/gone/" + catalog, "lake-catalogs/default/inlined/" + catalog} {
		require.NoError(t, shared.Put(context.Background(), k, []byte(k), blob.PutOptions{}))
	}
	require.NoError(t, shared.Close())

	r := w.start(t)
	var buckets []v1.ObjectName
	for _, e := range r.quarantine(t) {
		if e.Kind == v1.KindBucket {
			buckets = append(buckets, e.Name)
		}
	}
	require.ElementsMatch(t, []v1.ObjectName{"gone", "inlined"}, buckets)
	require.Empty(t, r.objects(t, "lake-catalogs/"))
	entry := "quarantine/bucket/default/gone/" + quarantine.Stamp(w.clk.Now()) + "/"
	require.Equal(t, []string{catalog}, r.objects(t, entry+"catalogs/"))

	r.apply(t, goneBucket())
	require.NoError(t, r.admin.ReturnQuarantined(r.ctx, hold.QuarantineReturn{Kind: v1.KindBucket, Namespace: "default", Name: "gone"}))
	require.Equal(t, []string{catalog}, r.objects(t, "lake-catalogs/default/gone/"), "the catalog comes back with the objects")
	require.ElementsMatch(t, []string{"a", "b/c"}, r.objects(t, "s3/default/gone/"))
	r.stop()

	w.clk.Advance(168*time.Hour + time.Millisecond)
	r = w.start(t)
	r.p.sweepQuarantine(r.ctx)
	require.Empty(t, r.objects(t, "quarantine/bucket/"), "the expired entry goes with its catalog")
}
