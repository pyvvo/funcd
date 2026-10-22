// Package route reconciles the Route resource (ADR-0110, F79): it validates each Route's
// backends, Sets every backend-valid Route on the edge aggregator, which applies the claim rules
// (Disabled, ReservedPath, HostRequired, HostReserved, RouteConflict) across every edge source (ADR-0176, ADR-0240,
// ADR-0241), and sets Ready/NotReady and status.host from the aggregator's verdicts.
package route

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/store"
)

const op = "route.Reconcile"

const condReady = v1.ConditionType("Ready")

// No watch enqueues a Route when its backend Function or Bucket (or its Namespace) changes, so a live
// Route is requeued: within backendRequeue while a backend is missing (ADR-0121's bounded requeue),
// within resyncPeriod otherwise, so a deleted backend is noticed.
const (
	backendRequeue = 2 * time.Second
	resyncPeriod   = 10 * time.Second
)

// routeEntrySource is this reconciler's edge-aggregator source key (ADR-0138): the user-Route set is
// one partition of the shared edge table, alongside the Lake reconciler's lake partition.
const routeEntrySource = "routes"

// Deps are the reconciler's dependencies.
type Deps struct {
	Store store.Store
	// Routes contributes the user-Route entry set (source "routes") to the shared edge aggregator
	// (ADR-0138) — replacing a direct router.Program so user Routes coexist with the Lake's
	// node-private lake route without clobbering the replace-all edge table.
	Routes router.EntrySetter
	Logger *slog.Logger
	// ReferentPollInterval and ResyncInterval are controller.referentPollInterval and controller.routeResyncInterval
	// (ADR-0163); 0 ⇒ backendRequeue and resyncPeriod.
	ReferentPollInterval time.Duration
	ResyncInterval       time.Duration
}

// Reconciler drives Route → programmed edge router.
type Reconciler struct {
	store  store.Store
	routes router.EntrySetter
	logger *slog.Logger
	// backendRequeue and resync are the requeues while a backend is missing and otherwise.
	backendRequeue time.Duration
	resync         time.Duration
}

// NewReconciler builds the Route reconciler.
func NewReconciler(d Deps) (*Reconciler, error) {
	if d.Store == nil {
		return nil, fault.Invalidf("route.NewReconciler", "store is required")
	}
	if d.Routes == nil {
		return nil, fault.Invalidf("route.NewReconciler", "routes (edge aggregator) is required")
	}
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	r := &Reconciler{store: d.Store, routes: d.Routes, logger: l.With("component", "route"), backendRequeue: d.ReferentPollInterval, resync: d.ResyncInterval}
	if r.backendRequeue <= 0 {
		r.backendRequeue = backendRequeue
	}
	if r.resync <= 0 {
		r.resync = resyncPeriod
	}
	return r, nil
}

type routeKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

type evalResult struct {
	ready          bool
	backendMissing bool
	reason         string
	message        string
	host           string // the claimed host of a Route with a verdict (ADR-0240 Decision 7)
}

// Reconcile evaluates the whole Route set, Sets the backend-valid Routes on the edge aggregator, and
// writes the status of every Route whose evaluation or verdict changed it — one Route's change can
// move another's readiness, and no event enqueues that other Route.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	routes, results, entries, err := r.evaluate(ctx)
	if err != nil {
		return controller.Result{}, err
	}
	verdicts, err := r.routes.Set(ctx, routeEntrySource, entries)
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "program edge router")
	}
	for _, v := range verdicts {
		results[routeKey{v.Owner.Namespace, v.Owner.Name}] = evalResult{ready: v.Reason == "", reason: v.Reason, message: v.Message, host: v.Host}
	}
	for _, rt := range routes {
		if !applyStatus(rt, results[routeKey{rt.Namespace, rt.Name}]) {
			continue
		}
		if _, err := r.store.Update(ctx, rt); err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "update route status %s/%s", rt.Namespace, rt.Name)
		}
	}
	res, ok := results[routeKey{req.Namespace, req.Name}]
	switch {
	case !ok:
		return controller.Result{}, nil // deleted; the table was already reprogrammed without it
	case res.backendMissing:
		return controller.Result{RequeueAfter: r.backendRequeue}, nil
	default:
		return controller.Result{RequeueAfter: r.resync}, nil
	}
}

// evaluate lists every Route in (namespace, name) order and returns the Routes, the result of each
// one whose backend is missing, and the compiled entries of the others.
func (r *Reconciler) evaluate(ctx context.Context) ([]*v1.Route, map[routeKey]evalResult, []router.Entry, error) {
	list, err := r.store.List(ctx, v1.KindRoute.GVK(), store.ListOptions{})
	if err != nil {
		return nil, nil, nil, fault.Wrapf(err, fault.KindOf(err), op, "list routes")
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

	results := map[routeKey]evalResult{}
	entries := []router.Entry{}
	for _, rt := range routes {
		reason, message, err := r.firstBackendProblem(ctx, rt)
		if err != nil {
			return nil, nil, nil, err
		}
		var sites map[v1.ObjectName]servedSite
		if reason == "" {
			if sites, reason, message, err = r.resolveSites(ctx, rt); err != nil {
				return nil, nil, nil, err
			}
		}
		if reason != "" {
			results[routeKey{rt.Namespace, rt.Name}] = evalResult{backendMissing: true, reason: reason, message: message}
			continue
		}
		entries = append(entries, compile(rt, sites))
	}
	return routes, results, entries, nil
}

// firstBackendProblem returns the NotReady reason+message for the first rule whose backend does not
// exist (or "","" if all exist). A function arm checks the Function exists (BackendNotFound); a
// static arm (ADR-0120, F82) checks the Bucket exists in the Route's namespace (BucketNotFound),
// mirroring BackendNotFound — cross-resource state the reconciler owns, not Validate. A non-NotFound
// store error is propagated.
func (r *Reconciler) firstBackendProblem(ctx context.Context, rt *v1.Route) (reason, message string, err error) {
	for i := range rt.Spec.Rules {
		b := &rt.Spec.Rules[i].Backend
		if b.Site != "" {
			continue // resolveSites
		}
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

// applyStatus sets rt's Ready condition, phase and host from res and reports whether they changed.
func applyStatus(rt *v1.Route, res evalResult) bool {
	prev, had := rt.Status.Conditions.Get(condReady)
	prevPhase, prevHost := rt.Status.Phase, rt.Status.Host
	rt.Status.Host = res.host
	if res.ready {
		rt.Status.Phase = v1.PhaseReady
		rt.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Programmed", ObservedGeneration: rt.Generation})
	} else {
		rt.Status.Phase = v1.PhasePending
		rt.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: res.reason, Message: res.message, ObservedGeneration: rt.Generation})
	}
	cur, _ := rt.Status.Conditions.Get(condReady)
	return !had || cur != prev || rt.Status.Phase != prevPhase || rt.Status.Host != prevHost
}

// The verdicts of a site rule (ADR-0242 Decision 4).
const (
	reasonSiteNotFound = "SiteNotFound"
	reasonSiteNotReady = "SiteNotReady"
)

// servedSite is what a Site's own Route serves now: its bundle backend and its config.json backend, or nil.
type servedSite struct{ bundle, config *v1.StaticBackend }

// resolveSites reads each Site a rule names and the Route named after it (ADR-0242 Decision 3): no Site is
// SiteNotFound; a Site whose own Route is missing, not controlled by it or serving no build is SiteNotReady.
func (r *Reconciler) resolveSites(ctx context.Context, rt *v1.Route) (map[v1.ObjectName]servedSite, string, string, error) {
	var out map[v1.ObjectName]servedSite
	for i := range rt.Spec.Rules {
		name := rt.Spec.Rules[i].Backend.Site
		if _, done := out[name]; name == "" || done {
			continue
		}
		obj, err := r.store.Get(ctx, v1.KindSite.GVK(), rt.Namespace, name)
		if fault.KindOf(err) == fault.NotFound {
			return nil, reasonSiteNotFound, fmt.Sprintf("backend site %q not found", name), nil
		}
		if err != nil {
			return nil, "", "", fault.Wrapf(err, fault.KindOf(err), op, "get backend site %s/%s", rt.Namespace, name)
		}
		site := obj.(*v1.Site)
		var own *v1.Route
		switch ro, gerr := r.store.Get(ctx, v1.KindRoute.GVK(), rt.Namespace, name); {
		case gerr == nil:
			own = ro.(*v1.Route)
		case fault.KindOf(gerr) != fault.NotFound:
			return nil, "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get route %s/%s of backend site", rt.Namespace, name)
		}
		bundle, config, ok := site.Served(own)
		switch {
		case ok:
		case own == nil:
			return nil, reasonSiteNotReady, fmt.Sprintf("backend site %q has no Route of its own yet", name), nil
		case !v1.ControlledBy(own.OwnerReferences, v1.KindSite, site.UID):
			return nil, reasonSiteNotReady, fmt.Sprintf("backend site %q does not control the Route of its name", name), nil
		default:
			return nil, reasonSiteNotReady, fmt.Sprintf("backend site %q serves no build yet", name), nil
		}
		if out == nil {
			out = map[v1.ObjectName]servedSite{}
		}
		out[name] = servedSite{bundle: bundle, config: config}
	}
	return out, "", "", nil
}

// siteRules are the rows a site rule compiles into (ADR-0242 Decision 2): the bundle at the rule's path and, when the
// Site has one, its config.json at <path>/config.json, Exact, for the rule's methods within GET and HEAD.
func siteRules(rule *v1.RouteRule, served servedSite, auth v1.AuthMode) []router.CompiledRule {
	out := []router.CompiledRule{{Path: rule.Path, Methods: methodSet(rule.Methods), Auth: auth, Static: served.bundle}}
	if served.config == nil {
		return out
	}
	methods := map[string]bool{}
	for _, m := range []v1.HTTPMethod{v1.MethodGet, v1.MethodHead} {
		if len(rule.Methods) == 0 || slices.Contains(rule.Methods, m) {
			methods[string(m)] = true
		}
	}
	if len(methods) == 0 {
		return out
	}
	cfg := strings.TrimSuffix(rule.Path, "/") + "/" + v1.SiteRuntimeConfigFile
	return append(out, router.CompiledRule{Path: cfg, Exact: true, Methods: methods, Auth: auth, Static: served.config})
}

func methodSet(ms []v1.HTTPMethod) map[string]bool {
	if len(ms) == 0 {
		return nil
	}
	set := make(map[string]bool, len(ms))
	for _, m := range ms {
		set[string(m)] = true
	}
	return set
}

// compile turns a backend-valid Route into a router.Entry; the rows of its site rules follow its written rules, so a
// written rule on the same path wins (ADR-0242 Decision 2).
func compile(rt *v1.Route, sites map[v1.ObjectName]servedSite) router.Entry {
	rules := make([]router.CompiledRule, 0, len(rt.Spec.Rules))
	var siteRows []router.CompiledRule
	for i := range rt.Spec.Rules {
		rule := &rt.Spec.Rules[i]
		if rule.Backend.Site != "" {
			var auth v1.AuthMode
			if rule.Auth != nil {
				auth = rule.Auth.Mode
			}
			siteRows = append(siteRows, siteRules(rule, sites[rule.Backend.Site], auth)...)
			continue
		}
		mset := methodSet(rule.Methods)
		var ruleAuth v1.AuthMode
		if rule.Auth != nil {
			ruleAuth = rule.Auth.Mode
		}
		rules = append(rules, router.CompiledRule{
			Path:     rule.Path,
			Exact:    rule.PathType == v1.PathTypeExact,
			Methods:  mset,
			Function: rule.Backend.Function,
			Auth:     ruleAuth,
			Static:   rule.Backend.Static,
		})
	}
	var authMode v1.AuthMode
	if rt.Spec.Auth != nil {
		authMode = rt.Spec.Auth.Mode
	}
	return router.Entry{
		Namespace: rt.Namespace,
		Host:      rt.Spec.Host,
		Auth:      authMode,
		Rules:     append(rules, siteRows...),
		Owner:     router.Owner{Kind: v1.KindRoute, Namespace: rt.Namespace, Name: rt.Name},
		Owned:     owned.IsOwned(rt),
		Enabled:   rt.Spec.Enabled,
	}
}
