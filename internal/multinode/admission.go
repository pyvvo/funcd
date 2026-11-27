package multinode

import (
	"context"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
)

// namespaceNode checks a Namespace's spec.node (ADR-0368 Decision 2): it names a declared WorkerNode, or the local node,
// and a namespace that holds objects keeps its node, since its data stays where it ran.
type namespaceNode struct {
	st    store.Store
	local v1.ObjectName
}

func (namespaceNode) Name() string           { return "namespace-node" }
func (namespaceNode) Phase() admission.Phase { return admission.Validating }
func (namespaceNode) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk.Kind == v1.KindNamespace && (op == admission.Create || op == admission.Update)
}

func (a namespaceNode) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	const op = "admission.namespace-node"
	n, ok := req.Object.(*v1.Namespace)
	if !ok {
		return req.Object, nil
	}
	if node := n.Spec.Node; node != "" && node != a.local {
		_, err := a.st.Get(ctx, v1.KindWorkerNode.GVK(), "", node)
		if fault.KindOf(err) == fault.NotFound {
			return nil, fault.Invalidf(op, "spec.node names WorkerNode %s, which is not declared: apply a WorkerNode named %s first", node, node)
		}
		if err != nil {
			return nil, err
		}
	}
	if req.Operation != admission.Update {
		return n, nil
	}
	old, ok := req.Old.(*v1.Namespace)
	if !ok || old.Spec.Node == n.Spec.Node {
		return n, nil
	}
	holds, err := holdsObjects(ctx, a.st, v1.NamespaceName(n.Name))
	if err != nil {
		return nil, err
	}
	if holds {
		return nil, fault.Conflictf(op, "namespace %s holds objects, so its spec.node cannot change from %q to %q: its data stays on the node that runs it", n.Name, old.Spec.Node, n.Spec.Node)
	}
	return n, nil
}

func holdsObjects(ctx context.Context, st store.Store, ns v1.NamespaceName) (bool, error) {
	for _, k := range v1.AllKinds() {
		if !k.Namespaced() {
			continue
		}
		res, err := st.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return false, err
		}
		if len(res.Items) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// workerNodeInUse refuses to delete a WorkerNode a Namespace still names (ADR-0368 Decision 2): its removal is declared
// too, once its namespaces have moved.
type workerNodeInUse struct{ st store.Store }

func (workerNodeInUse) Name() string           { return "workernode-in-use" }
func (workerNodeInUse) Phase() admission.Phase { return admission.Validating }
func (workerNodeInUse) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk.Kind == v1.KindWorkerNode && op == admission.Delete
}

func (a workerNodeInUse) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	if req.Old == nil {
		return req.Object, nil
	}
	name := req.Old.GetObjectMeta().Name
	res, err := a.st.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var users []string
	for _, obj := range res.Items {
		if n, ok := obj.(*v1.Namespace); ok && n.Spec.Node == name {
			users = append(users, string(n.Name))
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		return nil, fault.Conflictf("admission.workernode-in-use", "WorkerNode %s still runs namespaces %v: move them to another node first", name, users)
	}
	return req.Object, nil
}
