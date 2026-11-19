package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/store"
)

// CondEdgeDefaultsReady is a Namespace's condition while its edgeDefaults.auth names no OIDCProvider of the namespace:
// False, ProviderNotFound (ADR-0335 Decision 10). It is absent otherwise.
const CondEdgeDefaultsReady = v1.ConditionType("EdgeDefaultsReady")

// NamespaceCheck is the Namespace kind's reconciler: it reports an edgeDefaults.auth that names a missing OIDCProvider.
type NamespaceCheck struct {
	store store.Store
	log   *slog.Logger
}

// NewNamespaceCheck builds the Namespace check.
func NewNamespaceCheck(st store.Store, log *slog.Logger) *NamespaceCheck {
	if log == nil {
		log = slog.Default()
	}
	return &NamespaceCheck{store: st, log: log.With("component", "edge.oidc.namespace")}
}

// Reconcile sets or clears the Namespace's CondEdgeDefaultsReady.
func (c *NamespaceCheck) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "oidc.NamespaceCheck"
	obj, err := c.store.Get(ctx, v1.KindNamespace.GVK(), "", req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get Namespace %s", req.Name)
	}
	n, ok := obj.(*v1.Namespace)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s is not a Namespace", req.Name)
	}
	before, _ := json.Marshal(n.Status)
	var stance *v1.RouteAuth
	if d := n.Spec.EdgeDefaults; d != nil {
		stance = d.Auth.RouteAuth()
	}
	missing := ""
	if provider, _, ok := stance.OIDC(); ok {
		_, err := c.store.Get(ctx, v1.KindOIDCProvider.GVK(), v1.NamespaceName(n.Name), provider)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			missing = string(provider)
		case err != nil:
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get OIDCProvider %s/%s", n.Name, provider)
		}
	}
	if missing != "" {
		n.Status.Conditions.Set(v1.Condition{Type: CondEdgeDefaultsReady, Status: v1.ConditionFalse, Reason: router.ReasonProviderNotFound,
			Message: fmt.Sprintf("spec.edgeDefaults.auth.provider %q names no OIDCProvider of the namespace", missing), ObservedGeneration: n.Generation})
	} else {
		n.Status.Conditions = slices.DeleteFunc(n.Status.Conditions, func(c v1.Condition) bool { return c.Type == CondEdgeDefaultsReady })
	}
	if after, _ := json.Marshal(n.Status); string(after) == string(before) {
		return controller.Result{}, nil
	}
	if _, err := c.store.Update(ctx, n); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			return controller.Result{Requeue: true}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "update Namespace %s status", n.Name)
	}
	return controller.Result{}, nil
}

// MapProviderNamespace maps an OIDCProvider event to its Namespace's check.
func MapProviderNamespace(_ context.Context, obj v1.Object) []controller.Request {
	return []controller.Request{{GVK: v1.KindNamespace.GVK(), Name: v1.ObjectName(obj.GetObjectMeta().Namespace)}}
}

// MapProviders maps an event of an object whose stance or Secret a provider may read, a Route, a workload, a Secret or
// a Namespace, to every OIDCProvider of its namespace: a watch event carries no prior state, so an object that no
// longer names a provider still re-runs it.
func MapProviders(st store.Store) controller.MapFunc {
	return func(ctx context.Context, obj v1.Object) []controller.Request {
		meta := obj.GetObjectMeta()
		ns := meta.Namespace
		if obj.GroupVersionKind().Kind == v1.KindNamespace {
			ns = v1.NamespaceName(meta.Name)
		}
		list, err := st.List(ctx, v1.KindOIDCProvider.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return nil
		}
		reqs := make([]controller.Request, 0, len(list.Items))
		for _, o := range list.Items {
			reqs = append(reqs, controller.Request{GVK: v1.KindOIDCProvider.GVK(), Namespace: ns, Name: o.GetObjectMeta().Name})
		}
		return reqs
	}
}
