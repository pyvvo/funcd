// Package route reconciles the Route resource (ADR-0110, F79): it validates each Route's
// backends, applies the multi-tenancy rules (host required in explicit-mode namespaces;
// deterministic cross-namespace (host,path,method) collision resolution), sets Ready/NotReady,
// and programs the edge router replace-all from the full Ready-route set — mirroring the
// Function reconciler's programAllRoutes discipline.
package route

import (
	"context"
	"log/slog"
	"sort"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/edge/router"
	"github.com/green-0-rabbit/funcd/internal/store"
)

const op = "route.Reconcile"

const condReady = v1.ConditionType("Ready")

// Deps are the reconciler's dependencies.
type Deps struct {
	Store  store.Store
	Router router.Router
	Logger *slog.Logger
}

// Reconciler drives Route → programmed edge router.
type Reconciler struct {
	store  store.Store
	rtr    router.Router
	logger *slog.Logger
}

// NewReconciler builds the Route reconciler.
func NewReconciler(d Deps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("route.NewReconciler", "store is required")
	}
	if d.Router == nil {
		return nil, fault.Invalidf("route.NewReconciler", "router is required")
	}
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Reconciler{store: d.Store, rtr: d.Router, logger: l.With("component", "route")}, nil
}

type routeKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

type evalResult struct {
	ready   bool
	reason  string
	message string
}

// Reconcile evaluates the whole Route set (deterministic collision resolution), programs the
// router with the winners, and writes the reconciled Route's status.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	results, entries, err := r.evaluate(ctx)
	if err != nil {
		return controller.Result{}, err
	}
	if err := r.rtr.Program(ctx, entries); err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "program edge router")
	}
	obj, err := r.store.Get(ctx, v1.KindRoute.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return controller.Result{}, nil // deleted; the table was already reprogrammed without it
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get route %s/%s", req.Namespace, req.Name)
	}
	rt := obj.(*v1.Route)
	res := results[routeKey{req.Namespace, req.Name}]
	applyStatus(rt, res)
	if _, err := r.store.Update(ctx, rt); err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "update route status %q", rt.Name)
	}
	return controller.Result{}, nil
}

// evaluate lists every Route, resolves them in (namespace, name) order, and returns each Route's
// readiness plus the compiled entries for the Ready ones.
func (r *Reconciler) evaluate(ctx context.Context) (map[routeKey]evalResult, []router.Entry, error) {
	list, err := r.store.List(ctx, v1.KindRoute.GVK(), store.ListOptions{})
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), op, "list routes")
	}
	routes := make([]*v1.Route, 0, len(list.Items))
	for _, o := range list.Items {
		if rt, ok := o.(*v1.Route); ok {
			routes = append(routes, rt)
		}
	}
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Namespace != routes[j].Namespace {
			return routes[i].Namespace < routes[j].Namespace
		}
		return routes[i].Name < routes[j].Name
	})

	modes := map[v1.NamespaceName]v1.ExposureMode{}
	results := map[routeKey]evalResult{}
	entries := []router.Entry{}
	claimed := map[string]routeKey{} // (host \x00 path \x00 method) → first claimant

	for _, rt := range routes {
		key := routeKey{rt.Namespace, rt.Name}
		if reason, message, err := r.firstBackendProblem(ctx, rt); err != nil {
			return nil, nil, err
		} else if reason != "" {
			results[key] = evalResult{reason: reason, message: message}
			continue
		}
		mode, ok := modes[rt.Namespace]
		if !ok {
			mode = r.modeOf(ctx, rt.Namespace)
			modes[rt.Namespace] = mode
		}
		if mode == v1.ExposureExplicit && rt.Spec.Host == "" {
			results[key] = evalResult{reason: "HostRequired", message: "an explicit-mode namespace requires spec.host"}
			continue
		}
		claims := claimsFor(rt)
		conflict := false
		for _, ck := range claims {
			if owner, ok := claimed[ck]; ok && owner != key {
				conflict = true
				break
			}
		}
		if conflict {
			results[key] = evalResult{reason: "RouteConflict", message: "conflicts with an earlier Route on (host, path, method)"}
			continue
		}
		for _, ck := range claims {
			claimed[ck] = key
		}
		results[key] = evalResult{ready: true}
		entries = append(entries, compile(rt))
	}
	return results, entries, nil
}

// firstBackendProblem returns the NotReady reason+message for the first rule whose backend does not
// exist (or "","" if all exist). A function arm checks the Function exists (BackendNotFound); a
// static arm (ADR-0120, F82) checks the Bucket exists in the Route's namespace (BucketNotFound),
// mirroring BackendNotFound — cross-resource state the reconciler owns, not Validate. A non-NotFound
// store error is propagated.
func (r *Reconciler) firstBackendProblem(ctx context.Context, rt *v1.Route) (reason, message string, err error) {
	for i := range rt.Spec.Rules {
		b := &rt.Spec.Rules[i].Backend
		if b.Static != nil {
			bucket := b.Static.Bucket
			if _, gerr := r.store.Get(ctx, v1.KindBucket.GVK(), rt.Namespace, bucket); gerr != nil {
				if fault.KindOf(gerr) == fault.NotFound {
					return "BucketNotFound", "backend bucket \"" + string(bucket) + "\" not found", nil
				}
				return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get backend bucket %s/%s", rt.Namespace, bucket)
			}
			continue
		}
		fn := b.Function
		if _, gerr := r.store.Get(ctx, v1.KindFunction.GVK(), rt.Namespace, fn); gerr != nil {
			if fault.KindOf(gerr) == fault.NotFound {
				return "BackendNotFound", "backend function \"" + string(fn) + "\" not found", nil
			}
			return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get backend function %s/%s", rt.Namespace, fn)
		}
	}
	return "", "", nil
}

// modeOf reads a namespace's normalized exposure mode; an absent Namespace is implicit.
func (r *Reconciler) modeOf(ctx context.Context, ns v1.NamespaceName) v1.ExposureMode {
	obj, err := r.store.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	if err != nil {
		return v1.ExposureImplicit
	}
	if n, ok := obj.(*v1.Namespace); ok {
		return n.Spec.DefaultExposure.Normalized()
	}
	return v1.ExposureImplicit
}

func applyStatus(rt *v1.Route, res evalResult) {
	if res.ready {
		rt.Status.Phase = v1.PhaseReady
		rt.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Programmed", ObservedGeneration: rt.Generation})
		return
	}
	rt.Status.Phase = v1.PhasePending
	rt.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: res.reason, Message: res.message, ObservedGeneration: rt.Generation})
}

// claimsFor expands a Route into its (host, path, method) claim keys (methods empty ⇒ all).
func claimsFor(rt *v1.Route) []string {
	var keys []string
	for i := range rt.Spec.Rules {
		rule := &rt.Spec.Rules[i]
		methods := rule.Methods
		if len(methods) == 0 {
			methods = allHTTPMethods()
		}
		for _, m := range methods {
			keys = append(keys, rt.Spec.Host+"\x00"+rule.Path+"\x00"+string(m))
		}
	}
	return keys
}

// compile turns a Ready Route into a router.Entry.
func compile(rt *v1.Route) router.Entry {
	rules := make([]router.CompiledRule, 0, len(rt.Spec.Rules))
	for i := range rt.Spec.Rules {
		rule := &rt.Spec.Rules[i]
		var mset map[string]bool
		if len(rule.Methods) > 0 {
			mset = make(map[string]bool, len(rule.Methods))
			for _, m := range rule.Methods {
				mset[string(m)] = true
			}
		}
		rules = append(rules, router.CompiledRule{
			Path:     rule.Path,
			Exact:    rule.PathType == v1.PathTypeExact,
			Methods:  mset,
			Function: rule.Backend.Function,
			Static:   rule.Backend.Static,
		})
	}
	var authMode v1.AuthMode
	if rt.Spec.Auth != nil {
		authMode = rt.Spec.Auth.Mode
	}
	return router.Entry{Namespace: rt.Namespace, Host: rt.Spec.Host, Auth: authMode, Rules: rules}
}

func allHTTPMethods() []v1.HTTPMethod {
	return []v1.HTTPMethod{v1.MethodGet, v1.MethodHead, v1.MethodPost, v1.MethodPut, v1.MethodPatch, v1.MethodDelete, v1.MethodOptions}
}
