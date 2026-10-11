package funcd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	_ "modernc.org/sqlite"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// withBackupClock sets the clock the workload backups read (ADR-0222's scenarios run on a manual one) and the meter
// that takes their metrics.
func withBackupClock(c clock.Clock, m metric.Meter) Option {
	return func(cfg *config) error {
		cfg.backupClock, cfg.backupMeter = c, m
		return nil
	}
}

const (
	wbAdmin  = "wb-admin-token"
	wbDev    = "wb-dev-token"
	wbViewer = "wb-viewer-token"
	wbPeriod = 50 * time.Millisecond
)

// syncBuffer is a log sink the platform's goroutines share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// wbRig is a running platform for ADR-0222's scenarios: a manual backup clock at the current time, the orders
// namespace with its stores and Secrets, an S3 stub target, the owner's and the recovery identities, and a client per
// role (an admin, a developer and a viewer of orders).
type wbRig struct {
	t                  *testing.T
	ctx                context.Context
	p                  *Platform
	st                 store.Store
	clk                *clock.Manual
	stub               *s3stub.Stub
	owner, recovery    *age.X25519Identity
	admin, dev, viewer *sdk.Client
	hold               *hold.Hold
	logs               *syncBuffer
	metrics            *sdkmetric.ManualReader
	dir                string
	opts               []Option
	stop               func()
}

type wbOptions struct {
	held  bool
	setup func(r *wbRig)
	opts  []Option
}

func newWBRig(t *testing.T, o wbOptions) *wbRig {
	t.Helper()
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if o.held {
		require.NoError(t, hold.Write(dir, hold.Marker{Reason: "restore", Since: v1.NewTimestamp(time.Now())}))
	}
	h, err := hold.Open(dir)
	require.NoError(t, err)
	r := &wbRig{t: t, ctx: ctx, clk: clock.NewManual(time.Now().UTC().Truncate(time.Second)), stub: s3stub.New(t, nil),
		logs: &syncBuffer{}, metrics: sdkmetric.NewManualReader(), dir: dir, opts: o.opts}
	r.owner, err = age.GenerateX25519Identity()
	require.NoError(t, err)
	r.recovery, err = age.GenerateX25519Identity()
	require.NoError(t, err)
	r.newPlatform(h)
	r.st = r.p.cfg.store
	tables := v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}
	r.seed(&v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: ordersMeta("orders-state"), Spec: tables},
		&v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: ordersMeta("orders-state-r"), Spec: tables},
		&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("orders-files")},
		r.secret("backup-writer", map[string]string{"AWS_ACCESS_KEY_ID": "AKIDWRITER", "AWS_SECRET_ACCESS_KEY": "w"}),
		r.secret("backup-reader", map[string]string{"AWS_ACCESS_KEY_ID": "AKIDREADER", "AWS_SECRET_ACCESS_KEY": "r"}),
		r.secret("owner-key", map[string]string{"AGE_IDENTITY": r.owner.String()}))
	r.put("orders/orders-state/t/1", "one")
	require.NoError(t, r.p.cfg.blob.Put(ctx, bucketPrefix("orders", "orders-files")+"a.csv", []byte("id\n"),
		blob.PutOptions{ContentType: "text/csv", Metadata: map[string]string{"source": "export"}}))
	if o.setup != nil {
		o.setup(r)
	}
	r.run()
	return r
}

// newPlatform builds the rig's platform over the hold h, with the rig's options and extra.
func (r *wbRig) newPlatform(h *hold.Hold, extra ...Option) {
	r.t.Helper()
	opts := append([]Option{InMemory(), WithLogger(slog.New(slog.NewTextHandler(r.logs, nil))), WithHold(h),
		WithCredentials(Credential{Token: wbAdmin, Role: "admin"},
			Credential{Token: wbDev, Role: "developer", Namespaces: []string{"orders", "web"}},
			Credential{Token: wbViewer, Role: "viewer", Namespaces: []string{"orders"}}),
		WithPacing(Pacing{SupervisionPeriod: wbPeriod, ReferentPollInterval: wbPeriod}), withBackupClock(r.clk, sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.metrics)).Meter("funcd.backup")),
		WithBlobPollInterval(wbPeriod)}, r.opts...)
	p, err := New(append(opts, extra...)...)
	require.NoError(r.t, err)
	r.p, r.hold = p, h
}

// run runs the platform until stop or the test's end, and waits for its control plane.
func (r *wbRig) run() {
	r.t.Helper()
	runCtx, cancel := context.WithCancel(r.ctx)
	done := make(chan error, 1)
	go func() { done <- r.p.Run(runCtx) }()
	r.stop = sync.OnceFunc(func() { cancel(); <-done })
	r.t.Cleanup(r.stop)
	client := func(token string) *sdk.Client {
		c, err := sdk.New("http://"+r.p.Addr(), sdk.WithToken(token))
		require.NoError(r.t, err)
		return c
	}
	r.admin, r.dev, r.viewer = client(wbAdmin), client(wbDev), client(wbViewer)
	require.Eventually(r.t, func() bool {
		_, err := r.viewer.List(r.ctx, v1.KindBackup, "orders")
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the control plane answers")
}

// restart stops the platform and runs another over the same store, KV driver, blob store and data directory. The
// blob store must survive the first platform's Shutdown.
func (r *wbRig) restart() {
	r.t.Helper()
	r.stop()
	h, err := hold.Open(r.dir)
	require.NoError(r.t, err)
	r.metrics = sdkmetric.NewManualReader()
	r.newPlatform(h, WithStore(r.st), WithKVStore(r.p.cfg.kvStore))
	r.run()
}

func ordersMeta(name string) v1.ObjectMeta {
	return v1.ObjectMeta{Namespace: "orders", ResourceGroup: "rg1", Name: v1.ObjectName(name)}
}

func (r *wbRig) seed(objs ...v1.Object) { seedObjects(r.t, r.st, objs...) }

func (r *wbRig) secret(name string, data map[string]string) *v1.Secret {
	s := &v1.Secret{TypeMeta: tmeta(v1.KindSecret), ObjectMeta: ordersMeta(name), Spec: v1.SecretSpec{Data: map[string][]byte{}}}
	for k, v := range data {
		s.Spec.Data[k] = []byte(v)
	}
	return s
}

func (r *wbRig) put(key, value string) {
	require.NoError(r.t, r.p.cfg.kvStore.Put(r.ctx, key, []byte(value)))
}

// kv is a store's keys and values, without its prefix.
func (r *wbRig) kv(store string) map[string]string {
	prefix := "orders/" + store + "/"
	keys, err := r.p.cfg.kvStore.List(r.ctx, prefix)
	require.NoError(r.t, err)
	out := map[string]string{}
	for _, k := range keys {
		v, _, err := r.p.cfg.kvStore.Get(r.ctx, k)
		require.NoError(r.t, err)
		out[strings.TrimPrefix(k, prefix)] = string(v)
	}
	return out
}

// schedule is the orders-nightly fixture: 03:00 Paris, orders-state and orders-files, onto the stub.
func (r *wbRig) schedule(name, expr string, mutate func(*v1.BackupScheduleSpec)) *v1.BackupSchedule {
	s := &v1.BackupSchedule{TypeMeta: tmeta(v1.KindBackupSchedule), ObjectMeta: ordersMeta(name), Spec: v1.BackupScheduleSpec{
		Schedule: expr, TimeZone: "Europe/Paris",
		Scope:      v1.BackupScope{KVStores: []v1.ObjectName{"orders-state"}, Buckets: []v1.ObjectName{"orders-files"}},
		Target:     v1.BackupTarget{URL: r.stub.URL(), CredentialsSecret: "backup-writer"},
		Encryption: v1.BackupEncryption{Recipients: []string{r.owner.Recipient().String(), r.recovery.Recipient().String()}},
	}}
	if mutate != nil {
		mutate(&s.Spec)
	}
	return s
}

func (r *wbRig) create(c *sdk.Client, o v1.Object) v1.Object {
	r.t.Helper()
	got, err := c.Create(r.ctx, o)
	require.NoError(r.t, err)
	return got
}

func (r *wbRig) backupObj(name string, schedule v1.ObjectName) *v1.Backup {
	return &v1.Backup{TypeMeta: tmeta(v1.KindBackup), ObjectMeta: ordersMeta(name), Spec: v1.BackupSpec{Schedule: schedule}}
}

func (r *wbRig) getBackup(name v1.ObjectName) (*v1.Backup, bool) {
	obj, err := r.st.Get(r.ctx, v1.KindBackup.GVK(), "orders", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(r.t, err)
	return obj.(*v1.Backup), true
}

// waitBackup waits for Backup name to reach phase.
func (r *wbRig) waitBackup(name v1.ObjectName, phase v1.RunPhase) *v1.Backup {
	r.t.Helper()
	var b *v1.Backup
	defer func() {
		if r.t.Failed() {
			r.t.Logf("Backup %s: %+v\n%s", name, b, r.logs.String())
		}
	}()
	require.Eventually(r.t, func() bool {
		var ok bool
		b, ok = r.getBackup(name)
		return ok && b.Status.Phase == phase
	}, 15*time.Second, 10*time.Millisecond, "Backup %s reaches %s", name, phase)
	return b
}

func (r *wbRig) backups() []string {
	l, err := r.st.List(r.ctx, v1.KindBackup.GVK(), store.ListOptions{Namespace: "orders"})
	require.NoError(r.t, err)
	var out []string
	for _, o := range l.Items {
		out = append(out, string(o.GetName()))
	}
	sort.Strings(out)
	return out
}

func (r *wbRig) getSchedule(name v1.ObjectName) *v1.BackupSchedule {
	obj, err := r.st.Get(r.ctx, v1.KindBackupSchedule.GVK(), "orders", name)
	require.NoError(r.t, err)
	return obj.(*v1.BackupSchedule)
}

func (r *wbRig) waitRestore(name v1.ObjectName, phase v1.RunPhase) *v1.Restore {
	r.t.Helper()
	var rs *v1.Restore
	require.Eventually(r.t, func() bool {
		obj, err := r.st.Get(r.ctx, v1.KindRestore.GVK(), "orders", name)
		require.NoError(r.t, err)
		rs = obj.(*v1.Restore)
		return rs.Status.Phase == phase
	}, 15*time.Second, 10*time.Millisecond, "Restore %s reaches %s\n%s", name, phase, r.logs.String())
	return rs
}

// still waits a few periods and checks cond still holds, for an act that must not happen.
func (r *wbRig) still(cond func() bool, msg string) {
	r.t.Helper()
	require.True(r.t, poll.Never(func() bool { return !cond() }, 6*wbPeriod, wbPeriod/2), msg)
}

func reason(st v1.Status) string {
	c, _ := st.Conditions.Get("Ready")
	return c.Reason
}

// nextSlot is expr's first slot in Paris after t.
func nextSlot(t *testing.T, expr string, after time.Time) time.Time {
	t.Helper()
	loc, err := cron.LoadZone("Europe/Paris")
	require.NoError(t, err)
	tt, err := cron.Parse(expr, loc)
	require.NoError(t, err)
	return tt.Next(after)
}

// advanceTo moves the backup clock to at plus a second.
func (r *wbRig) advanceTo(at time.Time) { r.clk.Advance(at.Add(time.Second).Sub(r.clk.Now())) }

// target opens the stub as the owner's reader would.
func (r *wbRig) target() blob.Bucket {
	b, err := gocloud.OpenWith(r.ctx, r.stub.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDREADER", Secret: "r"}})
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = b.Close() })
	return b
}

// records reads every record of one member's first file of the copy under path, opened with id.
func (r *wbRig) records(path string, kind v1.Kind, name v1.ObjectName, id age.Identity) map[string]string {
	r.t.Helper()
	src := r.target()
	m, err := workload.ReadManifest(r.ctx, src, path)
	require.NoError(r.t, err)
	require.NoError(r.t, workload.Integrity(r.ctx, src, path, m))
	unseal, err := envelope.Opener([]age.Identity{id})(m.Recipients)
	require.NoError(r.t, err)
	for _, mf := range m.Members {
		if mf.Kind != kind || mf.Name != name {
			continue
		}
		out := map[string]string{}
		next := workload.Records(r.ctx, src, path, mf.Files[0], unseal)
		for {
			rec, err := next()
			if errors.Is(err, io.EOF) {
				return out
			}
			require.NoError(r.t, err)
			out[string(rec.Key)] = string(rec.Value)
		}
	}
	r.t.Fatalf("the copy under %s holds no %s/%s", path, kind, name)
	return nil
}

// scenario: schedule-runs-at-slot — orders-nightly (0 3 * * *, Europe/Paris, a KVStore and a Bucket): when 03:00 Paris
// passes, Backup orders-nightly-<slot minutes> is Succeeded, its parts then manifest.yaml are on the target, each
// opening with either recipient's identity, and lastSuccessTime is set.
func TestScenarioScheduleRunsAtSlot(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	s := r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil)).(*v1.BackupSchedule)
	slot := nextSlot(t, "0 3 * * *", time.Time(s.CreationTime))
	r.still(func() bool { return len(r.backups()) == 0 }, "no Backup before the slot")
	r.advanceTo(slot)
	name := v1.BackupSlotName("orders-nightly", slot)
	b := r.waitBackup(name, v1.RunSucceeded)
	require.Equal(t, []v1.BackupMember{{Kind: v1.KindKVStore, Name: "orders-state", Bytes: b.Status.Members[0].Bytes},
		{Kind: v1.KindBucket, Name: "orders-files", Bytes: b.Status.Members[1].Bytes}}, b.Status.Members)
	require.Equal(t, "orders/"+string(name)+"-"+string(b.UID)+"/", b.Status.Path)
	puts := r.stub.PutKeys(b.Status.Path)
	require.Equal(t, []string{b.Status.Path + "kv/orders-state/part-00000", b.Status.Path + "blob/orders-files/part-00000",
		b.Status.Path + "manifest.yaml"}, puts, "the parts, then the manifest")
	for _, id := range []age.Identity{r.owner, r.recovery} {
		require.Equal(t, map[string]string{"t/1": "one"}, r.records(b.Status.Path, v1.KindKVStore, "orders-state", id))
		objs := r.records(b.Status.Path, v1.KindBucket, "orders-files", id)
		ct, md, body, err := workload.DecodeObject([]byte(objs["a.csv"]))
		require.NoError(t, err)
		require.Equal(t, []string{"text/csv", "export", "id\n"}, []string{ct, md["source"], string(body)})
	}
	require.Eventually(t, func() bool { return r.getSchedule("orders-nightly").Status.LastSuccessTime != nil }, 10*time.Second, 10*time.Millisecond)
	st := r.getSchedule("orders-nightly").Status
	require.Equal(t, v1.NewTimestamp(slot), *st.LastScheduleTime)
	require.Equal(t, b.Status.StartTime, st.EarliestRestorePoint)
}

// scenario: schedule-applies-live — orders-nightly updated to 0 4 * * *: the next Backup starts at 04:00, with no
// restart.
func TestScenarioScheduleAppliesLive(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	s := r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil)).(*v1.BackupSchedule)
	s.Spec.Schedule, s.ResourceVersion = "0 4 * * *", ""
	_, err := r.dev.Apply(r.ctx, s)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got := r.getSchedule("orders-nightly")
		c, ok := got.Status.Conditions.Get("Ready")
		return ok && got.Generation == 2 && c.ObservedGeneration == 2
	}, 10*time.Second, 10*time.Millisecond, "Ready follows the new generation")
	three := nextSlot(t, "0 3 * * *", time.Time(s.CreationTime))
	four := nextSlot(t, "0 4 * * *", three)
	r.advanceTo(three)
	r.still(func() bool { return len(r.backups()) == 0 }, "03:00 is no slot any more")
	r.advanceTo(four)
	b := r.waitBackup(v1.BackupSlotName("orders-nightly", four), v1.RunSucceeded)
	require.False(t, time.Time(*b.Status.StartTime).Before(four), "the run starts at 04:00")
}

// scenario: catch-up-once-no-overlap — funcd down across three slots creates one Backup when it starts; a Backup still
// Running at the next slot makes that slot create none.
func TestScenarioCatchUpOnceNoOverlap(t *testing.T) {
	t.Parallel()
	var created time.Time
	release := make(chan struct{})
	var blocking, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		s := r.schedule("hourly", "0 * * * *", nil)
		r.seed(s)
		got, err := r.st.Get(r.ctx, v1.KindBackupSchedule.GVK(), "orders", "hourly")
		require.NoError(t, err)
		created = time.Time(got.GetObjectMeta().CreationTime)
		r.advanceTo(nextSlot(t, "0 * * * *", created).Add(2 * time.Hour))
	}})
	first := nextSlot(t, "0 * * * *", created).Add(2 * time.Hour)
	b := r.waitBackup(v1.BackupSlotName("hourly", first), v1.RunSucceeded)
	require.Equal(t, []string{string(b.Name)}, r.backups(), "three missed slots, one Backup")

	r.stub.Set(func(s *s3stub.Stub) {
		s.FailPut = func(string) bool { blocking.Do(func() { <-release }); return false }
	})
	second := first.Add(time.Hour)
	r.advanceTo(second)
	r.waitBackup(v1.BackupSlotName("hourly", second), v1.RunRunning)
	third := second.Add(time.Hour)
	r.advanceTo(third)
	r.still(func() bool { return len(r.backups()) == 2 }, "a slot while a Backup runs creates none")
	unblock()
	r.waitBackup(v1.BackupSlotName("hourly", second), v1.RunSucceeded)
	r.still(func() bool { return len(r.backups()) == 2 }, "the skipped slot is not caught up")
	r.advanceTo(third.Add(time.Hour))
	r.waitBackup(v1.BackupSlotName("hourly", third.Add(time.Hour)), v1.RunSucceeded)
}

// scenario: backup-outlives-app — an App with a backupSchedules entry and two Succeeded Backups: deleting the App
// collects the schedule; both Backups stay until expireTime, then are deleted.
func TestScenarioBackupOutlivesApp(t *testing.T) {
	t.Parallel()
	// A pass of the App reconciler that read the App before its delete can write the schedule again after the
	// collector's watch; the sweep collects it then.
	r := newWBRig(t, wbOptions{opts: []Option{WithGCSweepInterval(200 * time.Millisecond)}})
	spec := r.schedule("x", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Scope, s.TTL = v1.BackupScope{}, v1.Duration(2*time.Hour)
	}).Spec
	r.create(r.dev, &v1.App{TypeMeta: tmeta(v1.KindApp), ObjectMeta: ordersMeta("shop"), Spec: v1.AppSpec{
		KV: []v1.AppKVStore{{Ref: "orders-state"}}, BackupSchedules: []v1.AppBackupSchedule{{Name: "shop-nightly", BackupScheduleSpec: spec}}}})
	require.Eventually(t, func() bool {
		_, err := r.st.Get(r.ctx, v1.KindBackupSchedule.GVK(), "orders", "shop-nightly")
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the App writes its schedule")
	require.Equal(t, v1.ObjectName("shop"), r.getSchedule("shop-nightly").Spec.Scope.App, "an empty scope backs up the App")
	for _, n := range []string{"b1", "b2"} {
		r.create(r.dev, r.backupObj(n, "shop-nightly"))
		b := r.waitBackup(v1.ObjectName(n), v1.RunSucceeded)
		require.Equal(t, []v1.BackupMember{{Kind: v1.KindKVStore, Name: "orders-state", Bytes: b.Status.Members[0].Bytes}}, b.Status.Members)
		require.Empty(t, b.OwnerReferences, "a Backup has no owner")
	}
	require.NoError(t, r.dev.Delete(r.ctx, v1.KindApp, "orders", "shop"))
	require.Eventually(t, func() bool {
		_, err := r.st.Get(r.ctx, v1.KindBackupSchedule.GVK(), "orders", "shop-nightly")
		return fault.KindOf(err) == fault.NotFound
	}, 15*time.Second, 10*time.Millisecond, "the owner GC collects the schedule\n%s", r.logs.String())
	b1, _ := r.getBackup("b1")
	require.Equal(t, []string{"b1", "b2"}, r.backups())
	r.advanceTo(time.Time(*b1.Status.ExpireTime).Add(-time.Minute))
	r.still(func() bool { return len(r.backups()) == 2 }, "both stay until expireTime")
	r.advanceTo(time.Time(*b1.Status.ExpireTime).Add(time.Minute))
	require.Eventually(t, func() bool { return len(r.backups()) == 0 }, 10*time.Second, 10*time.Millisecond, "expired Backups are deleted")
}

// scenario: api-creates-backup — a developer of orders POSTs a Backup naming the schedule and it runs; a viewer gets
// 403.
func TestScenarioAPICreatesBackup(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	_, err := r.viewer.Create(r.ctx, r.backupObj("by-viewer", "orders-nightly"))
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	r.create(r.dev, r.backupObj("before-migration", "orders-nightly"))
	b := r.waitBackup("before-migration", v1.RunSucceeded)
	require.NotEmpty(t, b.Status.Recipients)
	_, err = r.viewer.Get(r.ctx, v1.KindBackup, "orders", "before-migration")
	require.NoError(t, err, "a viewer reads Backups")
}

// scenario: target-in-store-refused — with blob.target s3://store, a schedule naming s3://store?prefix=b/ is refused
// with 400 naming spec.target.url; another bucket on that endpoint is stored, Ready with reason SameProvider, and one
// Warn names it.
func TestScenarioTargetInStoreRefused(t *testing.T) {
	t.Parallel()
	const endpoint = "&region=us-east-1&endpoint=https://s3.example.com"
	r := newWBRig(t, wbOptions{opts: []Option{WithBlobStoreURL("s3://store?" + endpoint[1:])}})
	_, err := r.dev.Create(r.ctx, r.schedule("inside", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
		s.Target.URL = "s3://store?prefix=b/" + endpoint
	}))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.target.url")
	r.create(r.dev, r.schedule("beside", "0 3 * * *", func(s *v1.BackupScheduleSpec) { s.Target.URL = "s3://backups?" + endpoint[1:] }))
	require.Eventually(t, func() bool { return reason(r.getSchedule("beside").Status.Status) == "SameProvider" },
		10*time.Second, 10*time.Millisecond)
	const warn = "workload backup target shares the blob store's provider"
	warns := func() int { return strings.Count(r.logs.String(), warn) }
	require.Eventually(t, func() bool { return warns() == 1 }, 10*time.Second, 10*time.Millisecond, "%s", r.logs.String())
	r.still(func() bool { return warns() == 1 }, "one Warn")
	require.Regexp(t, `level=WARN msg="`+warn+`[^\n]*schedule=beside`, r.logs.String())
}

// scenario: run-interrupted — a Backup Running when funcd is killed is Failed (Interrupted) at the start.
func TestScenarioRunInterrupted(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		r.seed(r.schedule("orders-nightly", "0 3 * * *", nil))
		b := r.backupObj("killed", "orders-nightly")
		b.Status.Phase = v1.RunRunning
		r.seed(b)
	}})
	b := r.waitBackup("killed", v1.RunFailed)
	require.Equal(t, "Interrupted", reason(b.Status.Status))
}

// TestRestoreInterruptedHoldRelease: an in-place Restore Running when funcd is killed is Failed (Interrupted) at the
// start and keeps its namespace held; hold status names it, and hold release --namespace lifts its marker with
// restore run's data marker.
func TestRestoreInterruptedHoldRelease(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		rs := r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", InPlace: true})
		rs.Status.Phase = v1.RunRunning
		r.seed(rs)
		since := v1.NewTimestamp(r.clk.Now())
		require.NoError(r.t, r.hold.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonData, Since: since}))
		require.NoError(r.t, r.hold.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonRestore, Since: since, Restore: "rewind"}))
	}})
	rs := r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "Interrupted", reason(rs.Status.Status))
	require.True(t, r.hold.HeldIn("orders"), "an interrupted load keeps its namespace held")
	ev, err := r.admin.HoldStatus(r.ctx)
	require.NoError(t, err)
	require.Len(t, ev.Namespaces, 2)
	require.Equal(t, [2]string{"orders", hold.ReasonData}, [2]string{string(ev.Namespaces[0].Namespace), ev.Namespaces[0].Reason})
	got := ev.Namespaces[1]
	require.Equal(t, []string{"orders", hold.ReasonRestore, "rewind", string(v1.RunFailed)},
		[]string{string(got.Namespace), got.Reason, string(got.Restore), string(got.Phase)}, "hold status names the Restore and its phase")

	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	require.False(t, r.hold.HeldIn("orders"), "the release lifts the restore marker with the data marker")
	require.Equal(t, fault.Conflict, fault.KindOf(r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"})))
}

// stallBucket is the shared blob store whose Put of a key ending in stall, once armed, waits for its context to end
// (a load that a stop interrupts), or with fail set fails at once. Its Close keeps it open for the platform a restart
// builds.
type stallBucket struct {
	blob.Bucket
	stall   string
	fail    bool
	armed   atomic.Bool
	stalled chan struct{}
	once    sync.Once
}

func (b *stallBucket) Put(ctx context.Context, key string, data []byte, o blob.PutOptions) error {
	if b.armed.Load() && strings.HasSuffix(key, b.stall) {
		if b.fail {
			return fault.Unavailablef("test", "the blob store refused %s", key)
		}
		b.once.Do(func() { close(b.stalled) })
		<-ctx.Done()
		return ctx.Err()
	}
	return b.Bucket.Put(ctx, key, data, o)
}

func (*stallBucket) Close() error { return nil }

// TestRestoreStopKeepsNamespaceHeld: a graceful stop in the middle of an in-place load of orders-files, a.csv
// written and b.csv stalled, keeps the namespace held under a marker naming the Restore, which is not Succeeded; after
// the restart it is Interrupted, still held, and hold release --namespace lifts it.
func TestRestoreStopKeepsNamespaceHeld(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/b.csv", stalled: make(chan struct{})}
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		require.NoError(r.t, shared.Put(r.ctx, bucketPrefix("orders", "orders-files")+"b.csv", []byte("id\n"), blob.PutOptions{}))
	}})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	shared.armed.Store(true)
	r.create(r.admin, r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "orders-files", InPlace: true}))
	require.Eventually(t, func() bool {
		select {
		case <-shared.stalled:
			return true
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "the load stalls on b.csv\n%s", r.logs.String())
	marker := func() v1.ObjectName { return r.hold.NamespaceMarkers()["orders"][hold.ReasonRestore].Restore }
	require.Equal(t, v1.ObjectName("rewind"), marker())

	r.stop()
	require.True(t, r.hold.HeldIn("orders"), "a stop in the middle of the load keeps the namespace held")
	require.Equal(t, v1.ObjectName("rewind"), marker())
	obj, err := r.st.Get(r.ctx, v1.KindRestore.GVK(), "orders", "rewind")
	require.NoError(t, err)
	require.NotEqual(t, v1.RunSucceeded, obj.(*v1.Restore).Status.Phase)

	r.restart()
	rs := r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "Interrupted", reason(rs.Status.Status))
	require.True(t, r.hold.HeldIn("orders"))
	require.Equal(t, v1.ObjectName("rewind"), marker())
	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	require.False(t, r.hold.HeldIn("orders"))
}

// TestRestoreInPlaceRefusedWhileMarkerStands: an in-place load of orders-files that fails on b.csv keeps the
// namespace held; a second in-place Restore, of orders-state only, is refused naming the first and the release; after
// hold release --namespace it is admitted and succeeds.
func TestRestoreInPlaceRefusedWhileMarkerStands(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	shared := &stallBucket{Bucket: mem, stall: "/b.csv", fail: true}
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		require.NoError(r.t, shared.Put(r.ctx, bucketPrefix("orders", "orders-files")+"b.csv", []byte("id\n"), blob.PutOptions{}))
	}})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	shared.armed.Store(true)
	r.create(r.admin, r.restoreObj("first", "b1", v1.RestoreMember{Kind: v1.KindBucket, Name: "orders-files", InPlace: true}))
	r.waitRestore("first", v1.RunFailed)
	require.True(t, r.hold.HeldIn("orders"), "a failed load keeps the namespace held")

	second := r.restoreObj("second", "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", InPlace: true})
	_, err = r.admin.Create(r.ctx, second)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "first")
	require.ErrorContains(t, err, "hold release --namespace orders")

	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	r.create(r.admin, second)
	r.waitRestore("second", v1.RunSucceeded)
	require.False(t, r.hold.HeldIn("orders"))
}

// TestRestoreLoadRefusedWhileMarkerStands: an in-place Restore that reaches the load past admission while another
// Restore's marker stands fails NamespaceHeld before any write, and the standing marker is unchanged.
func TestRestoreLoadRefusedWhileMarkerStands(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	r.put("orders/orders-state/t/1", "changed")
	standing := hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(r.clk.Now()), Restore: "other"}
	require.NoError(t, r.hold.HoldNamespace("orders", standing))
	r.seed(r.restoreObj("rewind", "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", InPlace: true}))

	rs := r.waitRestore("rewind", v1.RunFailed)
	require.Equal(t, "NamespaceHeld", reason(rs.Status.Status))
	require.Equal(t, map[string]string{"t/1": "changed"}, r.kv("orders-state"), "no member was written")
	require.Equal(t, standing, r.hold.NamespaceMarkers()["orders"][hold.ReasonRestore], "the standing marker is unchanged")
}

// scenario: rpo-risk — a daily schedule whose 03:00 Backup fails turns rpoRisk, the gauge and one Warn on; a success
// clears them.
func TestScenarioRPORisk(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	s := r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil)).(*v1.BackupSchedule)
	r.stub.Set(func(s *s3stub.Stub) { s.FailPut = func(string) bool { return true } })
	slot := nextSlot(t, "0 3 * * *", time.Time(s.CreationTime))
	r.advanceTo(slot)
	r.waitBackup(v1.BackupSlotName("orders-nightly", slot), v1.RunFailed)
	require.Eventually(t, func() bool { return r.getSchedule("orders-nightly").Status.RPORisk }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return r.gauge() == 1 }, 10*time.Second, 10*time.Millisecond, "the gauge counts the schedule")
	warns := func() int { return strings.Count(r.logs.String(), "workload backup rpo at risk") }
	require.Eventually(t, func() bool { return warns() == 1 }, 10*time.Second, 10*time.Millisecond)
	r.still(func() bool { return warns() == 1 }, "one Warn")

	r.stub.Set(func(s *s3stub.Stub) { s.FailPut = nil })
	r.create(r.dev, r.backupObj("fix", "orders-nightly"))
	r.waitBackup("fix", v1.RunSucceeded)
	require.Eventually(t, func() bool { return !r.getSchedule("orders-nightly").Status.RPORisk }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return r.gauge() == 0 }, 10*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return strings.Contains(r.logs.String(), "workload backup rpo no longer at risk") },
		10*time.Second, 10*time.Millisecond)
}

// gauge reads funcd.backup.workload_rpo_risk.
func (r *wbRig) gauge() int64 {
	r.t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(r.t, r.metrics.Collect(r.ctx, &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == "funcd.backup.workload_rpo_risk" && len(g.DataPoints) == 1 {
				return g.DataPoints[0].Value
			}
		}
	}
	r.t.Fatal("no funcd.backup.workload_rpo_risk gauge")
	return 0
}

// sealedCopy writes a Backup's copy of orders-state onto the stub with workload.Write and records it Succeeded, as a
// run would, for a platform that is held from the start.
func (r *wbRig) sealedCopy(name v1.ObjectName, schedule v1.ObjectName) *v1.Backup {
	r.t.Helper()
	sealer, err := envelope.Parse([]string{r.owner.Recipient().String(), r.recovery.Recipient().String()})
	require.NoError(r.t, err)
	b := r.backupObj(string(name), schedule)
	created, err := r.st.Create(r.ctx, b)
	require.NoError(r.t, err)
	b = created.(*v1.Backup)
	path := "orders/" + string(name) + "-" + string(b.UID) + "/"
	tg, err := gocloud.OpenWith(r.ctx, r.stub.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDWRITER", Secret: "w"}})
	require.NoError(r.t, err)
	defer func() { _ = tg.Close() }()
	src := workload.Sources{KV: r.p.cfg.kvStore.(interface {
		SnapshotPrefix(string) snapshot.Source
	}).SnapshotPrefix, Blob: r.p.cfg.blob, BucketPrefix: bucketPrefix}
	_, err = workload.Write(r.ctx, "orders", []workload.Member{{Kind: v1.KindKVStore, Name: "orders-state"}}, src,
		workload.Options{Target: tg, Path: path, Seal: sealer.Seal(), Recipients: sealer.Keys().Recipients})
	require.NoError(r.t, err)
	now := v1.NewTimestamp(r.clk.Now())
	b.Status = v1.BackupStatus{Phase: v1.RunSucceeded, StartTime: &now, CompletionTime: &now, Target: r.stub.URL(), Path: path,
		Recipients: sealer.Keys().Recipients}
	_, err = r.st.Update(r.ctx, b)
	require.NoError(r.t, err)
	return b
}

func (r *wbRig) restoreObj(name, backup string, m v1.RestoreMember) *v1.Restore {
	return &v1.Restore{TypeMeta: tmeta(v1.KindRestore), ObjectMeta: ordersMeta(name), Spec: v1.RestoreSpec{
		Backup: v1.ObjectName(backup), Source: v1.RestoreSource{CredentialsSecret: "backup-reader", IdentitySecret: "owner-key"},
		Members: []v1.RestoreMember{m}}}
}

// scenario: restore-new-name — a Succeeded Backup restored by a developer into the empty KVStore orders-state-r: its
// keys equal the backup's and the source is unchanged; into a store that is not empty it fails NotEmpty.
func TestScenarioRestoreNewName(t *testing.T) {
	t.Parallel()
	r := newWBRig(t, wbOptions{})
	r.create(r.dev, r.schedule("orders-nightly", "0 3 * * *", nil))
	r.create(r.dev, r.backupObj("b1", "orders-nightly"))
	r.waitBackup("b1", v1.RunSucceeded)
	r.put("orders/orders-state/t/2", "after")
	r.create(r.dev, r.restoreObj("back", "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", As: "orders-state-r"}))
	r.waitRestore("back", v1.RunSucceeded)
	require.Equal(t, map[string]string{"t/1": "one"}, r.kv("orders-state-r"))
	require.Equal(t, map[string]string{"t/1": "one", "t/2": "after"}, r.kv("orders-state"), "the source is unchanged")

	r.create(r.dev, r.restoreObj("again", "b1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", As: "orders-state-r"}))
	rs := r.waitRestore("again", v1.RunFailed)
	require.Equal(t, "NotEmpty", reason(rs.Status.Status))
	require.Equal(t, map[string]string{"t/1": "one"}, r.kv("orders-state-r"), "a refused restore writes nothing")
}

// scenario: restore-in-place — a developer's in-place Restore is 403; an admin's, on a held platform, first completes
// Backup <restore>-before, holds the namespace during the load and brings the Backup's keys back; a failed export
// fails ExportFailed and writes nothing.
func TestScenarioRestoreInPlace(t *testing.T) {
	t.Parallel()
	var b1 *v1.Backup
	r := newWBRig(t, wbOptions{held: true, setup: func(r *wbRig) {
		r.seed(r.schedule("orders-nightly", "0 3 * * *", nil))
		b1 = r.sealedCopy("b1", "orders-nightly")
	}})
	require.True(t, r.hold.Held())
	r.put("orders/orders-state/t/1", "changed")
	r.put("orders/orders-state/t/2", "new")
	inPlace := v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", InPlace: true}
	_, err := r.dev.Create(r.ctx, r.restoreObj("rewind", "b1", inPlace))
	require.Equal(t, fault.Forbidden, fault.KindOf(err))

	r.create(r.admin, r.restoreObj("rewind", "b1", inPlace))
	rs := r.waitRestore("rewind", v1.RunSucceeded)
	require.Equal(t, v1.ObjectName("rewind-before"), rs.Status.Export)
	export := r.waitBackup("rewind-before", v1.RunSucceeded)
	require.False(t, time.Time(*export.Status.CompletionTime).After(time.Time(*rs.Status.StartTime)), "the export completes first")
	require.Equal(t, map[string]string{"t/1": "changed", "t/2": "new"}, r.records(export.Status.Path, v1.KindKVStore, "orders-state", r.owner))
	require.Equal(t, map[string]string{"t/1": "one"}, r.kv("orders-state"))
	require.False(t, r.hold.ReleasedAtIn("orders").IsZero(), "the namespace was held during the load, then released")
	_ = b1

	r.put("orders/orders-state/t/3", "kept")
	r.stub.Set(func(s *s3stub.Stub) { s.FailPut = func(k string) bool { return strings.Contains(k, "/again-before-") } })
	r.create(r.admin, r.restoreObj("again", "b1", inPlace))
	rs = r.waitRestore("again", v1.RunFailed)
	require.Equal(t, "ExportFailed", reason(rs.Status.Status))
	require.Equal(t, map[string]string{"t/1": "one", "t/3": "kept"}, r.kv("orders-state"), "no write after a failed export")
}

// cleanupBucket is the shared blob store with a DuckLake cleanup that runs once, the first time f1 is read after
// the listing: it deletes f1 and checkpoints a catalog naming f2 instead.
type cleanupBucket struct {
	blob.Bucket
	f1, checkpoint string
	next           []byte
	once           sync.Once
}

func (b *cleanupBucket) Get(ctx context.Context, key string) ([]byte, error) {
	if key == b.f1 {
		var err error
		b.once.Do(func() {
			err = errors.Join(b.Delete(ctx, b.f1), b.Put(ctx, b.checkpoint, b.next, blob.PutOptions{}))
		})
		if err != nil {
			return nil, err
		}
	}
	return b.Bucket.Get(ctx, key)
}

// lakeCheckpoint is a DuckLake catalog.db over data path s3://lake-files/<prefix>/ naming files under main/t/.
func lakeCheckpoint(t *testing.T, prefix string, files ...string) []byte {
	t.Helper()
	file := t.TempDir() + "/catalog.db"
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	stmts := []string{
		`CREATE TABLE ducklake_metadata (key VARCHAR, value VARCHAR)`,
		`CREATE TABLE ducklake_schema (schema_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_table (table_id BIGINT, schema_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_data_file (table_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`CREATE TABLE ducklake_delete_file (table_id BIGINT, path VARCHAR, path_is_relative BOOLEAN)`,
		`INSERT INTO ducklake_metadata VALUES ('data_path', 's3://lake-files/` + prefix + `/')`,
		`INSERT INTO ducklake_schema VALUES (0, 'main/', true)`,
		`INSERT INTO ducklake_table VALUES (1, 0, 't/', true)`,
	}
	for _, f := range files {
		stmts = append(stmts, `INSERT INTO ducklake_data_file VALUES (1, '`+f+`', true)`)
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

// scenario: catalog-checkpoint-verified — catalog lake whose checkpoint C1 names f1: a cleanup deleting f1 mid-copy
// makes attempt 2 run, whose catalog.db is written after its files; a checkpoint naming a file no attempt finds
// fails the Backup CatalogUnstable after three attempts.
func TestScenarioCatalogCheckpointVerified(t *testing.T) {
	t.Parallel()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	root := bucketPrefix("orders", "lake-files")
	shared := &cleanupBucket{Bucket: mem, f1: root + "lake/main/t/f1.parquet", checkpoint: root + "lake/_ducklake/catalog.db",
		next: lakeCheckpoint(t, "lake", "f2.parquet")}
	r := newWBRig(t, wbOptions{opts: []Option{WithBlob(shared)}, setup: func(r *wbRig) {
		r.seed(&v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: ordersMeta("lake-files")})
		for _, c := range []struct{ name, prefix string }{{"lake", "lake"}, {"lake-two", "lake-two"}} {
			r.seed(&v1.CatalogService{TypeMeta: tmeta(v1.KindCatalogService), ObjectMeta: ordersMeta(c.name), Spec: v1.CatalogServiceSpec{
				Blob:    []v1.FunctionBlob{{Alias: "lake", Bucket: "lake-files", Prefix: c.prefix}},
				Catalog: v1.CatalogRef{Bucket: "lake-files", Prefix: c.prefix}}})
		}
		for key, data := range map[string][]byte{
			root + "lake/_ducklake/catalog.db":     lakeCheckpoint(t, "lake", "f1.parquet"),
			root + "lake/main/t/f1.parquet":        []byte("f1"),
			root + "lake/main/t/f2.parquet":        []byte("f2"),
			root + "lake-two/_ducklake/catalog.db": lakeCheckpoint(t, "lake-two", "missing.parquet"),
		} {
			require.NoError(t, mem.Put(r.ctx, key, data, blob.PutOptions{}))
		}
	}})
	for _, c := range []string{"lake", "lake-two"} {
		r.create(r.dev, r.schedule(c+"-nightly", "0 3 * * *", func(s *v1.BackupScheduleSpec) {
			s.Scope = v1.BackupScope{Catalogs: []v1.ObjectName{v1.ObjectName(c)}}
		}))
		r.create(r.dev, r.backupObj(c+"-now", v1.ObjectName(c+"-nightly")))
	}
	b := r.waitBackup("lake-now", v1.RunSucceeded)
	p := b.Status.Path
	require.Equal(t, []string{p + "catalog/lake/1/files/part-00000", p + "catalog/lake/2/files/part-00000",
		p + "catalog/lake/2/catalog.db/part-00000", p + "manifest.yaml"}, r.stub.PutKeys(p), "attempt 2: its files, then catalog.db")
	files := r.records(p, v1.KindCatalogService, "lake", r.owner)
	require.Contains(t, files, "main/t/f2.parquet")
	require.NotContains(t, files, "main/t/f1.parquet")

	b = r.waitBackup("lake-two-now", v1.RunFailed)
	require.Equal(t, "CatalogUnstable", reason(b.Status.Status))
	require.Len(t, r.stub.PutKeys(b.Status.Path), 3, "three attempts' files, no catalog.db and no manifest")
}
