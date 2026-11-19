package function

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/store"
)

// condIngressReady reports the Route a Function owns for an address of its own (ADR-0241 Decision 5), as a
// Lake reports its edge entry.
const condIngressReady v1.ConditionType = "IngressReady"

// syncRoute writes the Route of a Function whose ingress names a host or a path, deletes it when that address goes,
// and sets IngressReady from it, or to ProviderNotFound while its oidc stance names no OIDCProvider (ADR-0335 Decision
// 10). It reports whether the condition changed, so the pass writes the status.
func (r *Reconciler) syncRoute(ctx context.Context, fn *v1.Function) (bool, error) {
	name := owned.FunctionRouteName(fn.Name)
	prev, had := fn.Status.Conditions.Get(condIngressReady)
	ing := fn.Spec.Ingress
	missing, err := r.missingProvider(ctx, fn)
	if err != nil {
		return false, err
	}
	if ing == nil || ing.Host == "" && ing.Path == "" {
		if err := owned.Remove(ctx, r.store, fn, name); err != nil {
			return false, err
		}
		fn.Status.Conditions = slices.DeleteFunc(fn.Status.Conditions, func(c v1.Condition) bool { return c.Type == condIngressReady })
		if missing != "" {
			fn.Status.Conditions.Set(providerNotFound(fn, missing))
			cur, _ := fn.Status.Conditions.Get(condIngressReady)
			return !had || prev != cur, nil
		}
		return had, nil
	}
	spec := v1.RouteSpec{Host: ing.Host, Enabled: ing.Enabled, Rules: []v1.RouteRule{{
		Path:    v1.IngressPath(ing, v1.KindFunction, fn.Name),
		Backend: v1.RouteBackend{Function: fn.Name},
	}}}
	if ing.Auth != nil {
		auth := *ing.Auth
		spec.Auth = &auth
	}
	cond := v1.Condition{Type: condIngressReady, Status: v1.ConditionTrue, Reason: "Programmed"}
	rt, err := owned.Ensure(ctx, r.store, fn, name, spec)
	switch {
	case errors.Is(err, owned.ErrNotOwned):
		cond = v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: "RouteNotOwned",
			Message: fmt.Sprintf("route %q exists and is not controlled by this function", name)}
	case err != nil:
		return false, err
	default:
		switch c, ok := rt.Status.Conditions.Get(condReady); {
		case !ok || c.ObservedGeneration < rt.Generation:
			cond = v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: "RouteNotReady",
				Message: fmt.Sprintf("route %q is pending", name)}
		case c.Reason == "Disabled":
			cond.Reason = "Disabled" // off by choice (ADR-0241 Decision 6)
		case c.Status != v1.ConditionTrue:
			cond = v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: "RouteNotReady",
				Message: fmt.Sprintf("route %q: %s: %s", name, c.Reason, c.Message)}
		}
	}
	cond.ObservedGeneration = fn.Generation
	if missing != "" && cond.Reason != "RouteNotOwned" {
		cond = providerNotFound(fn, missing)
	}
	fn.Status.Conditions.Set(cond)
	cur, _ := fn.Status.Conditions.Get(condIngressReady)
	return !had || prev != cur, nil
}

// reasonProviderNotFound is an oidc stance that names no OIDCProvider of the Function's namespace (ADR-0335 Decision 10).
const reasonProviderNotFound = "ProviderNotFound"

func providerNotFound(fn *v1.Function, provider v1.ObjectName) v1.Condition {
	return v1.Condition{Type: condIngressReady, Status: v1.ConditionFalse, Reason: reasonProviderNotFound, ObservedGeneration: fn.Generation,
		Message: fmt.Sprintf("spec.ingress.auth.provider %q names no OIDCProvider of namespace %s", provider, fn.Namespace)}
}

// missingProvider is the OIDCProvider fn's own oidc stance names when its namespace does not hold it, else "".
func (r *Reconciler) missingProvider(ctx context.Context, fn *v1.Function) (v1.ObjectName, error) {
	provider, _, ok := fn.Spec.Ingress.Stance().OIDC()
	if !ok {
		return "", nil
	}
	_, err := r.store.Get(ctx, v1.KindOIDCProvider.GVK(), fn.Namespace, provider)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return provider, nil
	case err != nil:
		return "", fault.Wrapf(err, fault.KindOf(err), "function.syncRoute", "get OIDCProvider %s/%s", fn.Namespace, provider)
	}
	return "", nil
}

// MapOIDCProvider maps an OIDCProvider event to the Functions of its namespace whose ingress.auth names it, so their
// IngressReady follows it.
func (r *Reconciler) MapOIDCProvider(ctx context.Context, obj v1.Object) []controller.Request {
	meta := obj.GetObjectMeta()
	res, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: meta.Namespace})
	if err != nil {
		r.logger.Warn("could not list the Functions an OIDCProvider change affects", "namespace", meta.Namespace, "provider", meta.Name, "err", err)
		return nil
	}
	var out []controller.Request
	for _, o := range res.Items {
		if fn, ok := o.(*v1.Function); ok {
			if provider, _, oidc := fn.Spec.Ingress.Stance().OIDC(); oidc && provider == meta.Name {
				out = append(out, controller.Request{GVK: v1.KindFunction.GVK(), Namespace: fn.Namespace, Name: fn.Name})
			}
		}
	}
	return out
}
