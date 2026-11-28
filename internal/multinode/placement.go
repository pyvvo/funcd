package multinode

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// Placement answers where a namespace runs, from its Namespace object's spec.node (ADR-0368 Decision 1): a namespace
// stays whole on one node, and an empty spec.node is the local node of an all-in-one. It reads the store at each call,
// so a namespace placed a moment ago never runs on the wrong node.
type Placement struct {
	st        store.Store
	local     v1.ObjectName
	localRuns bool
}

// NewPlacement builds the placement of a control plane whose own node is local; localRuns is false on a control plane
// alone (funcd server), which runs no namespace itself.
func NewPlacement(st store.Store, local v1.ObjectName, localRuns bool) *Placement {
	return &Placement{st: st, local: local, localRuns: localRuns}
}

// NodeOf is the node namespace ns is placed on; "" ⇒ none, the local node of an all-in-one. A namespace with no
// Namespace object is not placed.
func (p *Placement) NodeOf(ctx context.Context, ns v1.NamespaceName) (v1.ObjectName, error) {
	obj, err := p.st.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return "", nil
	case err != nil:
		return "", err
	}
	n, ok := obj.(*v1.Namespace)
	if !ok {
		return "", fault.Internalf("multinode.NodeOf", "Namespace %q decoded as %T", ns, obj)
	}
	return n.Spec.Node, nil
}

// Local reports whether node is this process's own node: unplaced, or placed by name on it.
func (p *Placement) Local(node v1.ObjectName) bool { return node == "" || node == p.local }

// Skip reports whether this process leaves namespace ns to another node: placed on a node other than the local one,
// or on the local one, unplaced or by name, on a control plane that runs no namespace. A cluster-scoped object (ns "") is never skipped. A failed
// read skips the namespace on a control plane alone, which runs none, and nothing on an all-in-one, whose own work goes
// on (ADR-0368 Decision 3).
func (p *Placement) Skip(ns v1.NamespaceName) bool {
	if p == nil || ns == "" {
		return false
	}
	node, err := p.NodeOf(context.Background(), ns)
	if err != nil {
		return !p.localRuns
	}
	if p.Local(node) {
		return !p.localRuns
	}
	return true
}

// Namespaces are the namespaces placed on node.
func (p *Placement) Namespaces(ctx context.Context, node v1.ObjectName) ([]v1.NamespaceName, error) {
	res, err := p.st.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []v1.NamespaceName
	for _, obj := range res.Items {
		if n, ok := obj.(*v1.Namespace); ok && n.Spec.Node == node && node != "" {
			out = append(out, v1.NamespaceName(n.Name))
		}
	}
	return out, nil
}
