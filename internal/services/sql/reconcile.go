package sql

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
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
)

// The Ready reasons a SQLDatabase reports when False.
const (
	ReasonEngineNotReady       = "EngineNotReady"
	ReasonEngineConvergeFailed = "EngineConvergeFailed"
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
}

// Reconciler is the controller.Reconciler for KindSQLDatabase.
type Reconciler struct {
	d       ReconcilerDeps
	volumes provider.Volumes
	log     *slog.Logger
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
	return &Reconciler{d: d, volumes: vols, log: d.Logger.With("component", "sql.reconciler")}, nil
}

// Engines is the registry of Ready engines the reconciler fills.
func (r *Reconciler) Engines() *Engines { return r.d.Engines }

// Prober answers the deletion protection over this reconciler's engines and storage.
func (r *Reconciler) Prober() Prober {
	return Prober{Engines: r.d.Engines, Master: r.d.Master, Volumes: r.volumes}
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
	next := db.Status
	next.Conditions = append(v1.Conditions(nil), db.Status.Conditions...)
	next.ObservedGeneration = db.Generation
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
	if !reflect.DeepEqual(next, db.Status) {
		db.Status = next
		if _, uerr := r.d.Store.Update(ctx, db); uerr != nil && fault.KindOf(uerr) != fault.Conflict {
			return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), op, "write sqldatabase status")
		}
	}
	if !st.Ready {
		return controller.Result{RequeueAfter: enginePoll}, nil
	}
	return controller.Result{RequeueAfter: r.d.SupervisionPeriod}, nil
}

// engineSpec is the engine of db (ADR-0232 Decisions 2 to 5).
func (r *Reconciler) engineSpec(db *v1.SQLDatabase) provider.ProviderSpec {
	res := provider.ResourceSpec{CPU: db.Spec.Resources.CPU, Memory: db.Spec.Resources.Memory}
	if res.Memory == "" {
		res.Memory = defaultMemory
	}
	return provider.ProviderSpec{
		Ref:   provider.ProviderRef{Namespace: db.Namespace, Name: db.Name},
		Image: r.d.ImageFor(Runtime),
		Port:  enginePort,
		Env: map[string]string{
			"FUNCD_SQL_NODE_ID":  string(db.Name),
			"FUNCD_SQL_PASSWORD": EnginePassword(r.d.Master, db.Namespace, db.Name),
		},
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
	if err := r.d.Provider.Teardown(ctx, ref); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "tear down engine %s/%s", ref.Namespace, ref.Name)
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
