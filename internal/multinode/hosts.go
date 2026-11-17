package multinode

import (
	"context"
	"net"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
)

// hostKinds are the kinds that claim a host at the edge.
var hostKinds = []v1.Kind{v1.KindRoute, v1.KindFunction, v1.KindSite, v1.KindLake} //nolint:gochecknoglobals // a constant list

// hostClaim is the host an object claims at the edge: a Route's spec.host, or the spec.ingress.host of a Function, a Site
// or a Lake, in the spelling the edge matches (lower case, no port, no trailing dot). Empty claims the namespace's own
// host, which only its node answers.
func hostClaim(obj v1.Object) string {
	var h string
	switch o := obj.(type) {
	case *v1.Route:
		h = o.Spec.Host
	case *v1.Function:
		if o.Spec.Ingress != nil {
			h = o.Spec.Ingress.Host
		}
	case *v1.Site:
		h = o.Spec.Ingress.Host
	case *v1.Lake:
		if o.Spec.Ingress != nil {
			h = o.Spec.Ingress.Host
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
}

// hostOnOneNode refuses a namespace's claim on a host that a namespace on another node holds (FEAT-0002 decision 6): a
// host belongs to one node, where the namespaces share it under the edge's own rules, so two nodes never answer it.
// Sharing a host across nodes is a gateway's job, in front of them.
type hostOnOneNode struct {
	st        store.Store
	placement *Placement
}

func (hostOnOneNode) Name() string           { return "host-on-one-node" }
func (hostOnOneNode) Phase() admission.Phase { return admission.Validating }
func (hostOnOneNode) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	if op != admission.Create && op != admission.Update {
		return false
	}
	for _, k := range hostKinds {
		if gvk.Kind == k {
			return true
		}
	}
	return false
}

func (a hostOnOneNode) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	host := hostClaim(req.Object)
	if host == "" {
		return req.Object, nil
	}
	ns := req.Object.GetObjectMeta().Namespace
	mine, err := a.node(ctx, ns)
	if err != nil {
		return nil, err
	}
	for _, k := range hostKinds {
		res, err := a.st.List(ctx, k.GVK(), store.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, obj := range res.Items {
			other := obj.GetObjectMeta().Namespace
			if other == ns || hostClaim(obj) != host {
				continue
			}
			at, err := a.node(ctx, other)
			if err != nil {
				return nil, err
			}
			if at != mine {
				return nil, fault.Conflictf("admission.host-on-one-node",
					"host %s belongs to %s, which runs namespace %s that claims it: a host belongs to one node; to share it "+
						"across nodes, put a gateway in front of them", host, describeNode(at), other)
			}
		}
	}
	return req.Object, nil
}

// node is the node that runs ns, with the local node spelled "".
func (a hostOnOneNode) node(ctx context.Context, ns v1.NamespaceName) (v1.ObjectName, error) {
	n, err := a.placement.NodeOf(ctx, ns)
	if err != nil || a.placement.Local(n) {
		return "", err
	}
	return n, nil
}

func describeNode(n v1.ObjectName) string {
	if n == "" {
		return "the control plane's own node"
	}
	return "worker node " + string(n)
}
