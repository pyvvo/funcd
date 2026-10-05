package controlplane

import (
	"context"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/workflow"
)

// HandoverKVStore makes workflow the owner of a kept store: its refs become the marker of the Workflow at the
// UID read here, and its spec and data are untouched (ADR-0178 Decision 6). The authorizer has no subresource
// mapping, so each pair the handover needs is asked explicitly.
func (h *storeHandlers) HandoverKVStore(ctx context.Context, ns v1.NamespaceName, name, workflowName v1.ObjectName) (v1.KVStore, error) {
	const op = "controlplane.HandoverKVStore"
	for _, need := range []struct {
		verb auth.Verb
		kind v1.Kind
	}{{auth.VerbUpdate, v1.KindKVStore}, {auth.VerbDelete, v1.KindKVStore}, {auth.VerbGet, v1.KindWorkflow}} {
		if err := h.authorize(ctx, need.verb, need.kind, ns); err != nil {
			return v1.KVStore{}, err
		}
	}
	unlock, err := h.lockFor(ctx, v1.KindKVStore, admission.Update, ns)
	if err != nil {
		return v1.KVStore{}, err
	}
	defer unlock()
	cur, err := h.store.Get(ctx, v1.KindKVStore.GVK(), ns, name)
	if err != nil {
		return v1.KVStore{}, err
	}
	obj, err := h.store.Get(ctx, v1.KindWorkflow.GVK(), ns, workflowName)
	if err != nil {
		return v1.KVStore{}, err
	}
	wf := obj.(*v1.Workflow)
	if !slices.ContainsFunc(wf.Spec.KV, func(kv v1.WorkflowKVStore) bool { return kv.Name == name }) {
		return v1.KVStore{}, fault.Conflictf(op, "workflow %q does not declare kvstore %q in spec.kv", workflowName, name)
	}
	live, err := h.liveMarker(ctx, ns, cur.GetObjectMeta().OwnerReferences)
	if err != nil {
		return v1.KVStore{}, err
	}
	if live {
		return v1.KVStore{}, fault.Conflictf(op, "kvstore %q was made by a workflow that still exists; delete that workflow first", name)
	}
	next, err := withStatus(cur, cur)
	if err != nil {
		return v1.KVStore{}, err
	}
	next.GetObjectMeta().OwnerReferences = []v1.OwnerReference{workflow.KVMarker(wf)}
	id, _ := middleware.IdentityFrom(ctx)
	admitted, err := h.admit.Admit(ctx, admission.Request{
		Operation: admission.Update, GVK: v1.KindKVStore.GVK(), Object: next, Old: cur, Identity: id,
	})
	if err != nil {
		return v1.KVStore{}, err
	}
	m := admitted.GetObjectMeta()
	m.OwnerReferences = next.GetObjectMeta().OwnerReferences
	m.ResourceVersion = cur.GetObjectMeta().ResourceVersion
	out, err := h.store.Update(ctx, admitted)
	if err != nil {
		return v1.KVStore{}, err
	}
	return *out.(*v1.KVStore), nil
}

// refuseLiveMarked refuses an API write to a store a live Workflow made, so a user's apply never lands keys
// under that Workflow's lifecycle; a marker naming a deleted Workflow stays editable (ADR-0178 Decision 6).
func (h *storeHandlers) refuseLiveMarked(ctx context.Context, cur v1.Object) error {
	meta := cur.GetObjectMeta()
	live, err := h.liveMarker(ctx, meta.Namespace, meta.OwnerReferences)
	if err != nil {
		return err
	}
	if live {
		return fault.Conflictf("controlplane.ReplaceKVStore", "kvstore %q is managed by the workflow that made it", meta.Name)
	}
	return nil
}

// liveMarker reports whether refs hold a Workflow marker whose UID is that of an existing Workflow.
func (h *storeHandlers) liveMarker(ctx context.Context, ns v1.NamespaceName, refs []v1.OwnerReference) (bool, error) {
	for _, r := range refs {
		if !workflow.IsKVMarker(r) {
			continue
		}
		wf, err := h.store.Get(ctx, v1.KindWorkflow.GVK(), ns, r.Name)
		switch {
		case fault.KindOf(err) == fault.NotFound:
		case err != nil:
			return false, err
		case wf.GetObjectMeta().UID == r.UID:
			return true, nil
		}
	}
	return false, nil
}

// refuseMigrationRecord keeps the KVStore marker migration's completion record out of the API, so no principal
// can pre-create it to skip the migration or delete it to re-run it (ADR-0180 Decision 4). createObj, replaceObjIf
// and deleteObjIf run it right after authorization, so a denied caller gets 403 (ADR-0018 C3); deleteObjIf's
// call covers a forced ResourceGroup delete's members too.
func refuseMigrationRecord(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) error {
	if kind == v1.KindConfigMap && ns == workflow.KVMigrationNamespace && name == workflow.KVMigrationRecord {
		return fault.Conflictf("controlplane.ConfigMap", "configmap %s/%s is reserved for the platform", ns, name)
	}
	return nil
}
