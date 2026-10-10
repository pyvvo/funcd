package kv

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/store"
)

// PrefixManager is the narrow KV-driver view the reconciler needs (ADR-0072/0073): DropPrefix reclaims a
// store/table prefix, and List(prefix) enumerates the live keys used to discover orphaned table
// sub-prefixes on a table-removal. DropPrefix is a concrete driver method beyond the kvstore.KV port,
// type-asserted at wiring; declared here so the kv package depends on no driver. A driver without it ⇒
// reclamation is a no-op.
type PrefixManager interface {
	DropPrefix(prefix string) error
	List(ctx context.Context, prefix string) (keys []string, err error)
}

// ReconcilerDeps configures the KindKVStore reconciler (ADR-0072/0073).
type ReconcilerDeps struct {
	Store  store.Store
	KV     PrefixManager // nil ⇒ delete + table-removal reclamation is a no-op
	Logger *slog.Logger
	// Health is the storage prober whose KV result sets the Ready condition (ADR-0215 Decision 7); nil ⇒ always Ready.
	Health *health.Prober
}

// Reconciler is the controller.Reconciler for KindKVStore (ADR-0072/0073): present ⇒ Ready +
// status.tables/bindings + reclaim any table dropped from spec.tables[]; absent (deleted) ⇒
// DropPrefix(<ns>/<name>/).
type Reconciler struct {
	store  store.Store
	kv     PrefixManager
	health *health.Prober
	logger *slog.Logger
}

// NewReconciler builds the KVStore reconciler. Store is required; KV (the PrefixManager) is optional.
func NewReconciler(d ReconcilerDeps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("services.kv.NewReconciler", "store is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{store: d.Store, kv: d.KV, health: d.Health, logger: logger.With("component", "services.kv.reconciler")}, nil
}

// Reconcile converges one KVStore. A present store reports status.tables (declared sub-domains) + status.bindings
// (Function.spec.kv entries referencing it) and is Ready while the KV storage probe passes, else Degraded
// StorageUnreachable (ADR-0215 Decision 7), its status written only when it changed; any table removed from
// spec.tables[] but still holding data is reclaimed via DropPrefix(<ns>/<store>/<table>/); a deleted
// store (NotFound) reclaims its whole prefix via DropPrefix(<ns>/<store>/).
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.kv.Reconcile"
	obj, err := r.store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			// delete path: reclaim the store's prefix (ADR-0072 / ADR-0066 DropPrefix).
			if r.kv != nil {
				if derr := r.kv.DropPrefix(storePrefix(req.Namespace, req.Name)); derr != nil {
					return controller.Result{}, fault.Wrapf(derr, fault.KindOf(derr), op, "drop prefix for %s/%s", req.Namespace, req.Name)
				}
			}
			return controller.Result{}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get kvstore")
	}
	ks, ok := obj.(*v1.KVStore)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a KVStore", req.Namespace, req.Name)
	}

	if err := r.reclaimOrphanTables(ctx, ks); err != nil {
		return controller.Result{}, err
	}

	bindings, err := r.countBindings(ctx, ks.Namespace, ks.Name)
	if err != nil {
		return controller.Result{}, err
	}
	next := v1.KVStoreStatus{
		Status: health.StoreStatus(ks.Status.Status, r.health.Result(health.TargetKV), ks.Generation),
		Tables: len(ks.Spec.Tables), Bindings: bindings,
	}
	if reflect.DeepEqual(next, ks.Status) {
		return controller.Result{}, nil
	}
	ks.Status = next
	if _, uerr := r.store.Update(ctx, ks); uerr != nil {
		if fault.KindOf(uerr) == fault.Conflict {
			return controller.Result{}, nil // re-reconciled on the next watch event
		}
		return controller.Result{}, fault.Wrapf(uerr, fault.KindOf(uerr), op, "status write-back")
	}
	return controller.Result{}, nil
}

// ReclaimDeleted drops the data of every <ns>/<store>/ prefix whose KVStore no longer exists. Reconcile reclaims
// a store only when it sees the store NotFound, which a delete committed before a crash or a stop never reaches
// (issue #708, ADR-0170 "also across a crash"). Run it before the controller and the control plane start, so no
// store of the same name is created meanwhile. A key whose first two segments are not DNS labels belongs to
// another user of the KV substrate (the eventing watermarks) and is left alone.
func (r *Reconciler) ReclaimDeleted(ctx context.Context) error {
	const op = "services.kv.ReclaimDeleted"
	if r.kv == nil {
		return nil
	}
	keys, err := r.kv.List(ctx, "")
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list keys")
	}
	checked := map[string]bool{}
	var errs []error
	for _, k := range keys {
		parts := strings.SplitN(k, "/", 3)
		if len(parts) < 3 {
			continue
		}
		ns, name := v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
		sp := storePrefix(ns, name)
		if checked[sp] || ns.Validate() != nil || name.Validate() != nil {
			continue
		}
		checked[sp] = true
		_, gerr := r.store.Get(ctx, v1.KindKVStore.GVK(), ns, name)
		if fault.KindOf(gerr) != fault.NotFound {
			if gerr != nil {
				errs = append(errs, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get kvstore %s/%s", ns, name))
			}
			continue
		}
		if derr := r.kv.DropPrefix(sp); derr != nil {
			errs = append(errs, fault.Wrapf(derr, fault.KindOf(derr), op, "drop prefix for %s/%s", ns, name))
			continue
		}
		r.logger.InfoContext(ctx, "reclaimed the data of a deleted KV store", "namespace", ns, "store", name)
	}
	return errors.Join(errs...)
}

// reclaimOrphanTables drops the data of any table prefix present on disk under <ns>/<store>/ but no
// longer declared in spec.tables[] (ADR-0073 table-removal reclaim).
func (r *Reconciler) reclaimOrphanTables(ctx context.Context, ks *v1.KVStore) error {
	const op = "services.kv.reclaimOrphanTables"
	if r.kv == nil {
		return nil
	}
	sp := storePrefix(ks.Namespace, ks.Name)
	keys, err := r.kv.List(ctx, sp)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list keys under %q", sp)
	}
	desired := make(map[string]bool, len(ks.Spec.Tables))
	for _, tb := range ks.Spec.Tables {
		desired[tb.Name] = true
	}
	live := map[string]bool{} // table names observed in the live data
	for _, k := range keys {
		rest := strings.TrimPrefix(k, sp)
		if i := strings.IndexByte(rest, '/'); i > 0 {
			live[rest[:i]] = true
		}
	}
	for tableName := range live {
		if desired[tableName] {
			continue
		}
		if derr := r.kv.DropPrefix(sp + tableName + "/"); derr != nil {
			return fault.Wrapf(derr, fault.KindOf(derr), op, "drop prefix for removed table %q", tableName)
		}
		r.logger.Info("reclaimed removed KV table", "store", ks.Name, "namespace", ks.Namespace, "table", tableName)
	}
	return nil
}

// countBindings counts the Function.spec.kv entries in ns whose store is storeName.
func (r *Reconciler) countBindings(ctx context.Context, ns v1.NamespaceName, storeName v1.ObjectName) (int, error) {
	const op = "services.kv.countBindings"
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "list functions in %q", ns)
	}
	n := 0
	for _, o := range list.Items {
		f, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		for _, b := range f.Spec.KV {
			if b.Store == storeName {
				n++
			}
		}
	}
	return n, nil
}

// MapFunction is the controller.MapFunc that re-runs the reconcile of every KVStore in a changed
// Function's namespace: the count is of Function.spec.kv entries, and an unbind or a delete no longer
// names the store it referenced.
func (r *Reconciler) MapFunction(ctx context.Context, obj v1.Object) []controller.Request {
	ns := obj.GetObjectMeta().Namespace
	list, err := r.store.List(ctx, v1.KindKVStore.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		r.logger.WarnContext(ctx, "list kvstores to recount bindings", "namespace", string(ns), "error", err)
		return nil
	}
	reqs := make([]controller.Request, 0, len(list.Items))
	for _, o := range list.Items {
		meta := o.GetObjectMeta()
		reqs = append(reqs, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: meta.Namespace, Name: meta.Name})
	}
	return reqs
}

// storePrefix is "<ns>/<store>/" (ADR-0072/0073).
func storePrefix(ns v1.NamespaceName, store v1.ObjectName) string {
	return string(ns) + "/" + string(store) + "/"
}
