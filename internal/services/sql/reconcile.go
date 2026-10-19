package sql

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// The engine's fixed shape (ADR-0232 Decisions 2 and 3).
const (
	Runtime       = "rqlite"
	enginePort    = 4001
	engineUID     = 65532
	dataPath      = "/data"
	defaultMemory = "256Mi"
	enginePoll    = 2 * time.Second
	condReady     = v1.ConditionType("Ready")
	// condDataMissing is a database that was Ready before and came back with no engine directory, as after a
	// platform restore on a new host (ADR-0234 Decision 6).
	condDataMissing = v1.ConditionType("DataMissing")
)

// The Ready reasons a SQLDatabase reports when False, and the reasons of DataMissing.
const (
	ReasonEngineNotReady       = "EngineNotReady"
	ReasonEngineConvergeFailed = "EngineConvergeFailed"
	ReasonNoDirectory          = "NoDirectory"
	ReasonRestored             = "Restored"
)

// ReconcilerDeps configures the SQLDatabase reconciler (ADR-0232).
type ReconcilerDeps struct {
	Store             store.Store
	Provider          provider.Runtime // OwnerKind SQLDatabase; must implement provider.Volumes
	Engines           *Engines
	Master            []byte
	ImageFor          func(runtime string) string
	Hold              hold.Gate // nil ⇒ never held
	Logger            *slog.Logger
	SupervisionPeriod time.Duration
	// ChangeFeedURL is the endpoint each engine posts its changes to (ADR-0235 Decision 1); nil ⇒ no change feed.
	ChangeFeedURL func(ns v1.NamespaceName, name v1.ObjectName) string
	// Watermarks are the change feed's, dropped with a deleted database; nil ⇒ none.
	Watermarks Watermarks
	// DurableStorage is engine storage that outlives a daemon start: only then does a missing directory mean lost
	// data (DataMissing). False with no storage root or with storage.mode memory.
	DurableStorage bool
}

// Reconciler is the controller.Reconciler for KindSQLDatabase.
type Reconciler struct {
	d       ReconcilerDeps
	volumes provider.Volumes
	log     *slog.Logger

	// One client each, so engine calls reuse their connections.
	backupClient, probeClient *http.Client

	mu      sync.Mutex
	seen    map[string]bool // the databases this process has passed over once
	missing map[string]bool // DataMissing found but not yet written into the database's status
}

// NewReconciler builds the reconciler. Store, Provider, Engines and Master are required, and Provider must implement
// provider.Volumes, else Invalid.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	const op = "sql.NewReconciler"
	switch {
	case d.Store == nil:
		return nil, fault.Invalidf(op, "store is required")
	case d.Provider == nil:
		return nil, fault.Invalidf(op, "provider runtime is required")
	case d.Engines == nil:
		return nil, fault.Invalidf(op, "engine registry is required")
	case len(d.Master) == 0:
		return nil, fault.Invalidf(op, "node master is required")
	}
	vols, ok := d.Provider.(provider.Volumes)
	if !ok {
		return nil, fault.Invalidf(op, "the provider runtime offers no durable storage")
	}
	if d.ImageFor == nil {
		d.ImageFor = func(rt string) string { return "funcd/runtime-" + rt }
	}
	if d.SupervisionPeriod <= 0 {
		d.SupervisionPeriod = controller.SupervisionPeriod
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Reconciler{d: d, volumes: vols, log: d.Logger.With("component", "sql.reconciler"),
		backupClient: httpx.NodeClient(0), probeClient: httpx.NodeClient(5 * time.Second),
		seen: map[string]bool{}, missing: map[string]bool{}}, nil
}

// Engines is the registry of Ready engines the reconciler fills.
func (r *Reconciler) Engines() *Engines { return r.d.Engines }

// Backups copies and loads databases over this reconciler's engines, for workload backups (ADR-0234).
func (r *Reconciler) Backups() Backups {
	return Backups{Engines: r.d.Engines, Master: r.d.Master, Client: r.backupClient}
}

// Prober answers the deletion protection over this reconciler's engines and storage.
func (r *Reconciler) Prober() Prober {
	return Prober{Engines: r.d.Engines, Master: r.d.Master, Volumes: r.volumes, Client: r.probeClient}
}

func (r *Reconciler) held() bool { return r.d.Hold != nil && r.d.Hold.Held() }

// Reconcile converges one SQLDatabase: deleted ⇒ tear the engine down and reclaim its directory unless held; else
// converge its engine, record a Ready engine's address, and write the status only when it changed.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "sql.Reconcile"
	ref := provider.ProviderRef{Namespace: req.Namespace, Name: req.Name}
	obj, err := r.d.Store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, r.deleted(ctx, ref)
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get sqldatabase")
	}
	db, ok := obj.(*v1.SQLDatabase)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a SQLDatabase", req.Namespace, req.Name)
	}
	missing, merr := r.dataMissing(ctx, db)
	if merr != nil {
		return controller.Result{}, merr
	}
	// A changed spec (its bounds) reaches the engine only through a new container; the same pass records the
	// generation, so one change makes exactly one teardown (ADR-0232 Decision 2).
	if db.Status.ObservedGeneration != 0 && db.Status.ObservedGeneration != db.Generation {
		r.d.Engines.Delete(ref.Namespace, ref.Name)
		if terr := r.d.Provider.Teardown(ctx, ref); terr != nil {
			return controller.Result{}, fault.Wrapf(terr, fault.KindOf(terr), op, "restart engine %s/%s for its new spec", ref.Namespace, ref.Name)
		}
	}
	st, cerr := r.d.Provider.Converge(ctx, r.engineSpec(db))
	if cerr != nil {
		r.log.WarnContext(ctx, "sql engine converge failed", "database", string(req.Namespace)+"/"+string(req.Name), "err", cerr)
		st = provider.ProviderStatus{Reason: ReasonEngineConvergeFailed}
	}
	bindings, berr := r.bindings(ctx, db)
	if berr != nil {
		return controller.Result{}, berr
	}
	next := db.Status
	next.Conditions = append(v1.Conditions(nil), db.Status.Conditions...)
	next.ObservedGeneration = db.Generation
	next.Bindings = bindings
	if missing != nil {
		next.Conditions.Set(*missing)
	}
	if st.Ready {
		r.d.Engines.Set(ref.Namespace, ref.Name, st.Address)
		next.Phase = v1.PhaseReady
		next.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, ObservedGeneration: db.Generation})
	} else {
		r.d.Engines.Delete(ref.Namespace, ref.Name)
		reason := st.Reason
		if reason == "" {
			reason = ReasonEngineNotReady
		}
		next.Phase = v1.PhasePending
		next.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, ObservedGeneration: db.Generation})
	}
	switch {
	case reflect.DeepEqual(next, db.Status):
		r.recorded(engineKey(db.Namespace, db.Name), next)
	default:
		db.Status = next
		_, uerr := r.d.Store.Update(ctx, db)
		switch {
		case uerr == nil:
			r.recorded(engineKey(db.Namespace, db.Name), next)
		case fault.KindOf(uerr) == fault.Conflict:
			// Another write won: run again on the fresh object at once, so the computed change is not held a whole period.
			return controller.Result{Requeue: true}, nil
		default:
			return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), op, "write sqldatabase status")
		}
	}
	if !st.Ready {
		return controller.Result{RequeueAfter: enginePoll}, nil
	}
	return controller.Result{RequeueAfter: r.d.SupervisionPeriod}, nil
}

// dataMissing is the DataMissing condition db's pass sets, nil for none. On this process's first pass over db, with
// durable storage, a database whose stored Ready is True and whose directory is gone is missing its data; a
// Succeeded Restore into db since then clears it. A migration's new tables do not.
func (r *Reconciler) dataMissing(ctx context.Context, db *v1.SQLDatabase) (*v1.Condition, error) {
	const op = "sql.Reconcile"
	key := engineKey(db.Namespace, db.Name)
	lost := &v1.Condition{Type: condDataMissing, Status: v1.ConditionTrue, Reason: ReasonNoDirectory, ObservedGeneration: db.Generation,
		Message: "the database was Ready before and its engine directory is gone, so it starts empty: run a workload Restore into it"}
	r.mu.Lock()
	first, pending := !r.seen[key], r.missing[key]
	r.mu.Unlock()
	if pending {
		return lost, nil
	}
	if cur, ok := db.Status.Conditions.Get(condDataMissing); ok && cur.Status == v1.ConditionTrue {
		restored, err := r.restoredSince(ctx, db, time.Time(cur.LastTransitionTime))
		if err != nil || !restored {
			return nil, err
		}
		return &v1.Condition{Type: condDataMissing, Status: v1.ConditionFalse, Reason: ReasonRestored,
			ObservedGeneration: db.Generation}, nil
	}
	if !first {
		return nil, nil
	}
	ready, ok := db.Status.Conditions.Get(condReady)
	if !r.d.DurableStorage || !ok || ready.Status != v1.ConditionTrue {
		r.markSeen(key, false)
		return nil, nil
	}
	refs, err := r.volumes.List(ctx)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the engine directories")
	}
	for _, ref := range refs {
		if ref.Namespace == db.Namespace && ref.Name == db.Name {
			r.markSeen(key, false)
			return nil, nil
		}
	}
	// The pass's Converge recreates the directory, so the loss stays pending until a status write records it.
	r.markSeen(key, true)
	r.log.WarnContext(ctx, "sql database lost its data", "database", key)
	return lost, nil
}

// markSeen records the first pass over a database, and whether it found the database's data missing.
func (r *Reconciler) markSeen(key string, missing bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen[key] = true
	if missing {
		r.missing[key] = true
	}
}

// recorded drops a pending DataMissing once the stored status carries it.
func (r *Reconciler) recorded(key string, st v1.SQLDatabaseStatus) {
	if c, ok := st.Conditions.Get(condDataMissing); ok && c.Status == v1.ConditionTrue {
		r.mu.Lock()
		delete(r.missing, key)
		r.mu.Unlock()
	}
}

// restoredSince reports a Succeeded Restore whose destination is db, created at or after since: its creation time is
// the store's, the clock DataMissing's transition reads too, both in milliseconds. A Restore of the transition's
// millisecond counts, since its load waits for the engine that the transition's pass registers.
func (r *Reconciler) restoredSince(ctx context.Context, db *v1.SQLDatabase, since time.Time) (bool, error) {
	const op = "sql.Reconcile"
	l, err := r.d.Store.List(ctx, v1.KindRestore.GVK(), store.ListOptions{Namespace: db.Namespace})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), op, "list restores in %q", db.Namespace)
	}
	for _, o := range l.Items {
		rs, ok := o.(*v1.Restore)
		if !ok || rs.Status.Phase != v1.RunSucceeded || time.Time(rs.CreationTime).Before(since) {
			continue
		}
		for _, m := range rs.Spec.Members {
			if m.Kind == v1.KindSQLDatabase && cmp.Or(m.As, m.Name) == db.Name {
				return true, nil
			}
		}
	}
	return false, nil
}

// MapRestore maps a Restore to the databases it loads, whose DataMissing it may clear.
func (r *Reconciler) MapRestore(_ context.Context, obj v1.Object) []controller.Request {
	rs, ok := obj.(*v1.Restore)
	if !ok {
		return nil
	}
	var out []controller.Request
	for _, m := range rs.Spec.Members {
		if m.Kind == v1.KindSQLDatabase {
			out = append(out, controller.Request{GVK: v1.KindSQLDatabase.GVK(), Namespace: rs.Namespace, Name: cmp.Or(m.As, m.Name)})
		}
	}
	return out
}

// bindings counts the Functions whose spec.bindings.sql names db (ADR-0233 Decision 1).
func (r *Reconciler) bindings(ctx context.Context, db *v1.SQLDatabase) (int, error) {
	const op = "sql.Reconcile"
	fns, err := r.d.Store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: db.Namespace})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "list functions in %q", db.Namespace)
	}
	n := 0
	for _, o := range fns.Items {
		if fn, ok := o.(*v1.Function); ok && slices.ContainsFunc(fn.Spec.Bindings.SQL, func(b v1.FunctionSQL) bool { return b.Database == db.Name }) {
			n++
		}
	}
	return n, nil
}

// MapFunction maps a Function change to the databases of its namespace, whose binding counts it may change.
func (r *Reconciler) MapFunction(ctx context.Context, obj v1.Object) []controller.Request {
	ns := obj.GetObjectMeta().Namespace
	list, err := r.d.Store.List(ctx, v1.KindSQLDatabase.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		r.log.WarnContext(ctx, "list sqldatabases to recount bindings", "namespace", string(ns), "err", err)
		return nil
	}
	reqs := make([]controller.Request, 0, len(list.Items))
	for _, o := range list.Items {
		meta := o.GetObjectMeta()
		reqs = append(reqs, controller.Request{GVK: v1.KindSQLDatabase.GVK(), Namespace: meta.Namespace, Name: meta.Name})
	}
	return reqs
}

// engineSpec is the engine of db (ADR-0232 Decisions 2 to 5).
func (r *Reconciler) engineSpec(db *v1.SQLDatabase) provider.ProviderSpec {
	res := provider.ResourceSpec{CPU: db.Spec.Resources.CPU, Memory: db.Spec.Resources.Memory}
	if res.Memory == "" {
		res.Memory = defaultMemory
	}
	env := map[string]string{
		"FUNCD_SQL_NODE_ID":  string(db.Name),
		"FUNCD_SQL_PASSWORD": EnginePassword(r.d.Master, db.Namespace, db.Name),
	}
	if r.d.ChangeFeedURL != nil {
		env["FUNCD_SQL_CDC_ENDPOINT"] = r.d.ChangeFeedURL(db.Namespace, db.Name)
	}
	return provider.ProviderSpec{
		Ref:       provider.ProviderRef{Namespace: db.Namespace, Name: db.Name},
		Image:     r.d.ImageFor(Runtime),
		Port:      enginePort,
		Env:       env,
		Readiness: provider.ReadinessProbe{Path: "/readyz", ExpectStatus: http.StatusOK},
		Replicas:  1,
		Resources: res,
		Storage:   &provider.StorageSpec{Path: dataPath, UID: engineUID, GID: engineUID},
	}
}

// deleted tears a deleted database's engine down and reclaims its directory unless the platform is held.
func (r *Reconciler) deleted(ctx context.Context, ref provider.ProviderRef) error {
	const op = "sql.Reconcile"
	r.d.Engines.Delete(ref.Namespace, ref.Name)
	// A database created again under this name is a new one: it has lost nothing.
	r.mu.Lock()
	delete(r.seen, engineKey(ref.Namespace, ref.Name))
	delete(r.missing, engineKey(ref.Namespace, ref.Name))
	r.mu.Unlock()
	if err := r.d.Provider.Teardown(ctx, ref); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "tear down engine %s/%s", ref.Namespace, ref.Name)
	}
	if r.d.Watermarks != nil {
		if err := r.d.Watermarks.Delete(ctx, ref.Namespace, ref.Name); err != nil {
			return err
		}
	}
	if r.held() {
		return nil
	}
	if err := r.volumes.Reclaim(ctx, ref); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "reclaim the storage of %s/%s", ref.Namespace, ref.Name)
	}
	return nil
}

// Orphans returns the engines whose directory has no SQLDatabase, for hold status (ADR-0232 Decision 7).
func (r *Reconciler) Orphans(ctx context.Context) ([]v1.ObjectRef, error) {
	const op = "sql.Orphans"
	refs, err := r.volumes.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []v1.ObjectRef
	for _, ref := range refs {
		_, gerr := r.d.Store.Get(ctx, v1.KindSQLDatabase.GVK(), ref.Namespace, ref.Name)
		switch {
		case fault.KindOf(gerr) == fault.NotFound:
			out = append(out, v1.ObjectRef{Kind: v1.KindSQLDatabase, Namespace: ref.Namespace, Name: ref.Name})
		case gerr != nil:
			return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get sqldatabase %s/%s", ref.Namespace, ref.Name)
		}
	}
	return out, nil
}

// ReclaimOrphans removes, before the controller runs, the directory of every engine with no SQLDatabase left; a
// no-op while the platform is held, whose hold status lists them instead.
func (r *Reconciler) ReclaimOrphans(ctx context.Context) error {
	if r.held() {
		return nil
	}
	orphans, err := r.Orphans(ctx)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if derr := r.deleted(ctx, provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}); derr != nil {
			return derr
		}
	}
	return nil
}

// Prober answers the deletion protection's question (ADR-0232 Decision 7): does a database's engine hold a user table?
type Prober struct {
	Engines *Engines
	Master  []byte
	Volumes provider.Volumes
	Client  *http.Client // nil ⇒ a node client with a 5 s timeout
}

const userTablesQuery = "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"

// HasTables reports whether the engine holds a user table. With no Ready engine it answers false when the database
// has no directory (nothing was ever written) and fails Unavailable when it has one (fail closed).
func (p Prober) HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	const op = "sql.HasTables"
	addr, ok := p.Engines.Address(ns, name)
	if !ok {
		refs, err := p.Volumes.List(ctx)
		if err != nil {
			return false, err
		}
		for _, ref := range refs {
			if ref.Namespace == ns && ref.Name == name {
				return false, fault.Unavailablef(op, "the engine of %s/%s is not Ready, so its data cannot be checked", ns, name)
			}
		}
		return false, nil
	}
	client := p.Client
	if client == nil {
		client = httpx.NodeClient(5 * time.Second)
	}
	stmt, _ := json.Marshal(userTablesQuery)
	res, err := call(ctx, client, addr, EnginePassword(p.Master, ns, name), "/db/query", url.Values{}, []json.RawMessage{stmt})
	if err != nil {
		return false, err
	}
	if len(res) != 1 || res[0].Error != "" || len(res[0].Values) != 1 || len(res[0].Values[0]) != 1 {
		return false, fault.Internalf(op, "unexpected answer counting the tables of %s/%s", ns, name)
	}
	var n int64
	if err := json.Unmarshal(res[0].Values[0][0], &n); err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "decode the table count of %s/%s", ns, name)
	}
	return n > 0, nil
}
