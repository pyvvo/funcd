// Package backup runs an app owner's backups (ADR-0222, F111): the BackupSchedule reconciler takes each cron slot and
// keeps the schedule's status and RPO alert, the Backup reconciler runs one copy with the platform's rights and
// deletes the record at its expireTime, and the Restore reconciler loads a Backup's members into empty stores or in
// place. RBAC at admission is the whole boundary (Decision 10).
package backup

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// The reasons a record's Ready condition names (Decisions 3 to 7).
const (
	ReasonMemberNotFound      = "MemberNotFound"
	ReasonScheduleNotFound    = "ScheduleNotFound"
	ReasonTargetUnconditional = "TargetUnconditional"
	ReasonLakeUnstable        = "LakeUnstable"
	ReasonInterrupted         = "Interrupted"
	ReasonNotEmpty            = "NotEmpty"
	ReasonIntegrity           = "Integrity"
	ReasonExportFailed        = "ExportFailed"
	ReasonSameProvider        = "SameProvider"
	// ReasonEngineNotReady is a run that needs a database whose engine is not Ready (ADR-0234).
	ReasonEngineNotReady = "EngineNotReady"
	// ReasonNamespaceHeld is an in-place Restore whose namespace already carries a restore marker.
	ReasonNamespaceHeld = "NamespaceHeld"
	// reasonFailed is a run's failure no other reason names; its message carries the cause.
	reasonFailed = "Failed"
	// reasonBackupNotReady is a Restore's Backup that is missing or Failed.
	reasonBackupNotReady = "BackupNotReady"
)

const condReady = v1.ConditionType("Ready")

// The Secret keys a target credential and an identity are read from (Decision 6).
const (
	keyAccessKeyID     = "AWS_ACCESS_KEY_ID"
	keySecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	keySessionToken    = "AWS_SESSION_TOKEN"
	keyAgeIdentity     = "AGE_IDENTITY"
)

// streamWorkload is the workload backups' stream in the funcd.backup metrics (Decision 9).
const streamWorkload = "workload"

// SecretReader resolves Secrets as a developer of their namespace (secrets.Resolver).
type SecretReader interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// KVLoader is the KV driver's per-store load, teardown and listing (ADR-0222 LoadPrefix, ADR-0072 DropPrefix).
type KVLoader interface {
	LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error
	DropPrefix(prefix string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// SQLLoader loads, empties and inspects a database through its engine (ADR-0234). HasTables counts tables, views,
// indexes and triggers.
type SQLLoader interface {
	Load(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, file io.Reader) error
	Empty(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
	HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
}

// Hold is the daemon's hold (ADR-0206): the platform gate the runs wait on, the namespace gate the slots ask, and the
// namespace hold an in-place Restore takes.
type Hold interface {
	hold.Gate
	hold.NamespaceGate
	HoldNamespace(ns v1.NamespaceName, m hold.Marker) error
	ReleaseNamespaceMarker(ns v1.NamespaceName, m hold.Marker) error
}

// Deps are the reconcilers' dependencies; Store and Secrets are required.
type Deps struct {
	Store   store.Store
	Secrets SecretReader
	// Sources are what a Backup copies; KV loads and empties a KVStore; Purger empties a Bucket; SQL loads and empties
	// a SQLDatabase (nil ⇒ a Restore of one fails).
	Sources workload.Sources
	KV      KVLoader
	Purger  gc.BucketPurger
	SQL     SQLLoader
	// Hold nil is a platform never held, whose in-place Restore holds nothing.
	Hold Hold
	// StoreURL is the blob store's URL, blob.target or the file:// URL of blob.dir: a schedule on its provider is
	// Ready with reason SameProvider (Decision 6). "" compares with nothing.
	StoreURL string
	// Period re-checks a schedule at the latest after it, the namespace hold's release enqueuing nothing, and paces a
	// Backup waiting on the platform hold; 0 ⇒ controller.SupervisionPeriod.
	Period time.Duration
	Clock  clock.Clock
	Meter  metric.Meter
	Logger *slog.Logger
}

// Reconcilers are the three kinds' reconcilers, sharing one base.
type Reconcilers struct {
	Schedules *ScheduleReconciler
	Backups   *BackupReconciler
	Restores  *RestoreReconciler
}

// base is what every reconciler reads.
type base struct {
	d        Deps
	log      *slog.Logger
	runs     metric.Int64Counter
	duration metric.Float64Histogram

	mu      sync.Mutex
	active  map[v1.ObjectRef]bool // the Backups and Restores this process runs
	risk    map[v1.ObjectRef]bool // each schedule's rpoRisk as last seen
	warned  map[v1.ObjectRef]bool // the schedules whose SameProvider Warn this process logged
	running sync.WaitGroup
}

// New builds the reconcilers and registers the metrics: funcd.backup.runs and .duration_ms with stream=workload, and
// the gauge funcd.backup.workload_rpo_risk, the schedules at risk.
func New(d Deps) (*Reconcilers, error) {
	const op = "backup.New"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Secrets == nil:
		return nil, fault.Invalidf(op, "secrets are required")
	}
	d.Period = cmp.Or(d.Period, controller.SupervisionPeriod)
	if d.Clock == nil {
		d.Clock = clock.System()
	}
	if d.Meter == nil {
		d.Meter = metricnoop.NewMeterProvider().Meter("funcd.backup")
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	b := &base{d: d, log: log.With("component", "backup.workload"), active: map[v1.ObjectRef]bool{}, risk: map[v1.ObjectRef]bool{},
		warned: map[v1.ObjectRef]bool{}}
	var err error
	if b.runs, err = d.Meter.Int64Counter("funcd.backup.runs"); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.runs")
	}
	if b.duration, err = d.Meter.Float64Histogram("funcd.backup.duration_ms", metric.WithUnit("ms")); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.duration_ms")
	}
	if _, err = d.Meter.Int64ObservableGauge("funcd.backup.workload_rpo_risk",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(b.risks()))
			return nil
		})); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "register funcd.backup.workload_rpo_risk")
	}
	return &Reconcilers{Schedules: &ScheduleReconciler{b}, Backups: &BackupReconciler{b}, Restores: &RestoreReconciler{b}}, nil
}

// Wait blocks until every run this process started has ended; funcd's shutdown calls it after the controller stops.
func (r *Reconcilers) Wait() { r.Schedules.running.Wait() }

func (b *base) risks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, at := range b.risk {
		if at {
			n++
		}
	}
	return n
}

// claim marks k as run by this process; false when it already is.
func (b *base) claim(k v1.ObjectRef) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[k] {
		return false
	}
	b.active[k] = true
	return true
}

func (b *base) release(k v1.ObjectRef) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.active, k)
}

func (b *base) isActive(k v1.ObjectRef) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active[k]
}

// spawn starts f in the background, counted by Wait.
func (b *base) spawn(f func()) {
	b.running.Add(1)
	go func() {
		defer b.running.Done()
		f()
	}()
}

func (b *base) count(ctx context.Context, took time.Duration, err error) {
	result := "ok"
	if err != nil {
		result = "failed"
	}
	attrs := metric.WithAttributes(attribute.String("stream", streamWorkload), attribute.String("result", result))
	b.runs.Add(ctx, 1, attrs)
	b.duration.Record(ctx, float64(took)/float64(time.Millisecond), attrs)
}

func (b *base) heldIn(ns v1.NamespaceName) bool { return b.d.Hold != nil && b.d.Hold.HeldIn(ns) }

func (b *base) held() bool { return b.d.Hold != nil && b.d.Hold.Held() }

// developerFor is the namespace-scoped identity the Secret reads use, as the lake's injector: a developer of ns
// reads its own namespace's Secrets only (Decision 6).
func developerFor(ns v1.NamespaceName) auth.Identity {
	return auth.Identity{Subject: "system:backup:" + string(ns), Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{ns}}
}

// staticKey reads a target credential from the Secret name of ns.
func (b *base) staticKey(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (gocloud.StaticKey, error) {
	const op = "backup.credential"
	env, err := b.d.Secrets.ResolveEnv(ctx, developerFor(ns), ns, []string{string(name)})
	if err != nil {
		return gocloud.StaticKey{}, err
	}
	k := gocloud.StaticKey{ID: env[keyAccessKeyID], Secret: env[keySecretAccessKey], Session: env[keySessionToken]}
	if k.ID == "" || k.Secret == "" {
		return gocloud.StaticKey{}, fault.Invalidf(op, "Secret %s/%s lacks %s or %s", ns, name, keyAccessKeyID, keySecretAccessKey)
	}
	return k, nil
}

// openTarget opens a Backup's target URL with key, the only credential source.
func openTarget(ctx context.Context, url string, key gocloud.StaticKey) (blob.Bucket, error) {
	return gocloud.OpenWith(ctx, url, gocloud.OpenOptions{Static: &key})
}

// setReady writes the Ready condition at generation gen.
func setReady(st *v1.Status, gen int64, ok bool, reason, message string) {
	status := v1.ConditionFalse
	if ok {
		status = v1.ConditionTrue
	}
	st.Conditions.Set(v1.Condition{Type: condReady, Status: status, ObservedGeneration: gen, Reason: reason, Message: message})
	st.ObservedGeneration = gen
}

// readyReason is st's Ready reason, "" without one.
func readyReason(st v1.Status) string {
	c, _ := st.Conditions.Get(condReady)
	return c.Reason
}

func ts(t time.Time) *v1.Timestamp {
	out := v1.NewTimestamp(t)
	return &out
}

func refOf(o v1.Object) v1.ObjectRef {
	return v1.ObjectRef{Kind: o.GroupVersionKind().Kind, Namespace: o.GetNamespace(), Name: o.GetName()}
}

// writeStatus copies a run's status onto the stored object kind ns/name of uid and writes it, retrying a write lost to
// another, so a run's end is not lost to a concurrent write of the record's metadata.
func writeStatus(ctx context.Context, st store.Store, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, uid v1.UID,
	copyTo func(cur v1.Object)) error {
	const op = "backup.writeStatus"
	for range 5 {
		cur, err := st.Get(ctx, kind.GVK(), ns, name)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "get %s %s/%s", kind, ns, name)
		}
		if cur.GetObjectMeta().UID != uid {
			return fault.Conflictf(op, "%s %s/%s was recreated", kind, ns, name)
		}
		copyTo(cur)
		if _, err = st.Update(ctx, cur); fault.KindOf(err) != fault.Conflict {
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "write the status of %s %s/%s", kind, ns, name)
			}
			return nil
		}
	}
	return fault.Conflictf(op, "write the status of %s %s/%s: kept conflicting", kind, ns, name)
}

// terminal reports a finished run phase.
func terminal(p v1.RunPhase) bool { return p == v1.RunSucceeded || p == v1.RunFailed }
