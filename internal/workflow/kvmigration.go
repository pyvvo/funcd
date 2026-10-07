package workflow

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

const migrationOp = "workflow.kvstore-migration"

// The completion record of the KVStore marker migration (ADR-0180 Decision 3): written after the last mark and
// the binding strip, so a crash before either finishes re-runs it. The control plane refuses API writes to it.
const (
	KVMigrationNamespace v1.NamespaceName = "funcd-system"
	KVMigrationRecord    v1.ObjectName    = "kvstore-marker-migration"
)

// MarkKVStoresOnce marks, once, each unmarked KVStore the previous materializer made for a Workflow, then
// strips every Workflow-controlled Function's binding to a store it left unmarked and records completion.
// funcd calls it at boot before the controllers, the collector and the control plane start (ADR-0178). It logs
// through log (nil means slog.Default()).
func MarkKVStoresOnce(ctx context.Context, s store.Store, log *slog.Logger) error {
	_, err := s.Get(ctx, v1.KindConfigMap.GVK(), KVMigrationNamespace, KVMigrationRecord)
	if err == nil {
		return nil
	}
	if fault.KindOf(err) != fault.NotFound {
		return fault.Wrapf(err, fault.KindOf(err), migrationOp, "read the completion record")
	}
	log = cmp.Or(log, slog.Default()).With("component", "workflow.kvstore-migration")
	wfs, err := s.List(ctx, v1.KindWorkflow.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), migrationOp, "list workflows")
	}
	stores, err := s.List(ctx, v1.KindKVStore.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), migrationOp, "list kvstores")
	}
	markedStores := map[v1.ObjectRef]bool{}
	for _, obj := range stores.Items {
		st := obj.(*v1.KVStore)
		key := v1.ObjectRef{Namespace: st.Namespace, Name: st.Name}
		if hasMarker(st.OwnerReferences) {
			markedStores[key] = true
			continue
		}
		wf := upgradeOwner(st, wfs.Items)
		if wf == nil {
			continue
		}
		st.OwnerReferences = append(st.OwnerReferences, kvMarker(wf))
		if _, err := s.Update(ctx, st); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), migrationOp, "mark kvstore %q", st.Name)
		}
		markedStores[key] = true
		log.InfoContext(ctx, "kvstore marked for its workflow", "namespace", st.Namespace, "store", st.Name, "workflow", wf.Name, "uid", wf.UID)
	}
	if err := stripUnmarkedBindings(ctx, s, markedStores); err != nil {
		return err
	}
	rec := &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: KVMigrationRecord, Namespace: KVMigrationNamespace, ResourceGroup: "funcd-system"},
		Spec:       v1.ConfigMapSpec{Data: map[string]string{"COMPLETED_AT": time.Now().UTC().Format(time.RFC3339)}},
	}
	if _, err := s.Create(ctx, rec); err != nil && fault.KindOf(err) != fault.Conflict {
		return fault.Wrapf(err, fault.KindOf(err), migrationOp, "record completion")
	}
	return nil
}

// hasMarker reports whether refs hold a non-controller Workflow ref.
func hasMarker(refs []v1.OwnerReference) bool {
	return slices.ContainsFunc(refs, IsKVMarker)
}

// upgradeOwner returns the one Workflow of st's namespace that may adopt st at upgrade and declares it in
// spec.kv (Decision 3 (d)), or nil when none or more than one does.
func upgradeOwner(st *v1.KVStore, wfs []v1.Object) *v1.Workflow {
	var found *v1.Workflow
	for _, obj := range wfs {
		wf := obj.(*v1.Workflow)
		if wf.Namespace != st.Namespace || !declares(wf, st.Name) || !adoptableAtUpgrade(st, wf) {
			continue
		}
		if found != nil {
			return nil
		}
		found = wf
	}
	return found
}

func declares(wf *v1.Workflow, name v1.ObjectName) bool {
	for i := range wf.Spec.KV {
		if wf.Spec.KV[i].Name == name {
			return true
		}
	}
	return false
}

// adoptableAtUpgrade is Decision 3 (a)-(c): wf is not younger than cur, every controller ref on cur names
// wf's kind, name and UID, and every table owner is empty or exactly one of wf's materialized step names.
func adoptableAtUpgrade(cur *v1.KVStore, wf *v1.Workflow) bool {
	if wf.CreationTime.After(cur.CreationTime) {
		return false
	}
	for _, r := range cur.OwnerReferences {
		if r.Controller && (r.Kind != v1.KindWorkflow || r.Name != wf.Name || r.UID != wf.UID) {
			return false
		}
	}
	steps := make(map[v1.ObjectName]bool, len(wf.Spec.Steps))
	for i := range wf.Spec.Steps {
		steps[materializedName(wf, wf.Spec.Steps[i].Name)] = true
	}
	for _, t := range cur.Spec.Tables {
		if t.Owner != "" && !steps[t.Owner] {
			return false
		}
	}
	return true
}

// stripUnmarkedBindings removes every Workflow-controlled Function's kv binding to a store without a marker.
func stripUnmarkedBindings(ctx context.Context, s store.Store, markedStores map[v1.ObjectRef]bool) error {
	fns, err := s.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), migrationOp, "list functions")
	}
	for _, obj := range fns.Items {
		fn := obj.(*v1.Function)
		if r, ok := v1.ControllerOf(fn.OwnerReferences); !ok || r.Kind != v1.KindWorkflow || len(fn.Spec.KV) == 0 {
			continue
		}
		var keep []v1.FunctionKV
		for _, b := range fn.Spec.KV {
			if markedStores[v1.ObjectRef{Namespace: fn.Namespace, Name: b.Store}] {
				keep = append(keep, b)
			}
		}
		if len(keep) == len(fn.Spec.KV) {
			continue
		}
		fn.Spec.KV = keep
		if _, err := s.Update(ctx, fn); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), migrationOp, "strip kv bindings of function %q", fn.Name)
		}
	}
	return nil
}
