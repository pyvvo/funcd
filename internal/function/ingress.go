package function

import (
	"context"
	"errors"
	"fmt"
	"slices"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/route/owned"
)

// condIngressReady reports the Route a Function owns for an address of its own (ADR-0241 Decision 5), as a
// Lake reports its edge entry.
const condIngressReady v1.ConditionType = "IngressReady"

// syncRoute writes the Route of a Function whose ingress names a host or a path, deletes it when that address goes,
// and sets IngressReady from it. It reports whether the condition changed, so the pass writes the status.
func (r *Reconciler) syncRoute(ctx context.Context, fn *v1.Function) (bool, error) {
	name := owned.FunctionRouteName(fn.Name)
	prev, had := fn.Status.Conditions.Get(condIngressReady)
	ing := fn.Spec.Ingress
	if ing == nil || ing.Host == "" && ing.Path == "" {
		if err := owned.Remove(ctx, r.store, fn, name); err != nil {
			return false, err
		}
		fn.Status.Conditions = slices.DeleteFunc(fn.Status.Conditions, func(c v1.Condition) bool { return c.Type == condIngressReady })
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
	fn.Status.Conditions.Set(cond)
	cur, _ := fn.Status.Conditions.Get(condIngressReady)
	return !had || prev != cur, nil
}
