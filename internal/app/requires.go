package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/store"
)

// reasonRequirementNotMet stops the pass of a revision that waits for a required App (ADR-0219 Decision 3), on the
// App's Ready and the AppRevision's Applied.
const reasonRequirementNotMet = "RequirementNotMet"

const requiresOp = "admission.app-requires"

// requires is what spec.requires means for one pass (ADR-0219 Decisions 2 and 5): the status lines, the Apps that
// require this one, and the message of the first unmet requirement, empty when every one is met.
type requires struct {
	states     []v1.AppRequirementState
	requiredBy []v1.ObjectName
	unmet      string
}

// requirements lists the namespace's Apps once and judges each requires entry of a in order (Decision 2).
func (r *Reconciler) requirements(ctx context.Context, a *v1.App) (requires, error) {
	res, err := r.store.List(ctx, v1.KindApp.GVK(), store.ListOptions{Namespace: a.Namespace})
	if err != nil {
		return requires{}, fault.Wrapf(err, fault.KindOf(err), op, "list apps in %q", a.Namespace)
	}
	apps := make(map[v1.ObjectName]*v1.App, len(res.Items))
	var rq requires
	for _, o := range res.Items {
		other, ok := o.(*v1.App)
		if !ok {
			continue
		}
		apps[other.Name] = other
		if requirement(other, a.Name) != nil {
			rq.requiredBy = append(rq.requiredBy, other.Name)
		}
	}
	slices.Sort(rq.requiredBy)
	for _, e := range a.Spec.Requires {
		dep := apps[e.App]
		s := v1.AppRequirementState{App: e.App}
		if dep != nil {
			s.Version = dep.Spec.Version
		}
		what := unmet(e, dep)
		s.Met = what == ""
		if !s.Met && rq.unmet == "" {
			rq.unmet = fmt.Sprintf("App/%s %s; %s needs %s", e.App, what, a.Name, cmp.Or(e.Version, "any version"))
		}
		rq.states = append(rq.states, s)
	}
	return rq, nil
}

// unmet is what keeps e from being met by the required App dep, checked in Decision 2's order, "" when it is met: dep
// exists, matches e at its spec.version and serves that version Ready.
func unmet(e v1.AppRequirement, dep *v1.App) string {
	switch {
	case dep == nil:
		return "does not exist"
	case e.Version != "" && !semverVersion(dep.Spec.Version):
		return "has no SemVer version"
	case !e.Matches(dep.Spec.Version):
		return "is " + dep.Spec.Version
	case dep.Status.Phase != v1.PhaseReady:
		return fmt.Sprintf("is not Ready (%s)", cmp.Or(string(dep.Status.Phase), "none"))
	case dep.Status.CurrentRevision != dep.Status.LatestRevision || dep.Status.Version != dep.Spec.Version:
		return "has not rolled out its spec yet"
	}
	return ""
}

func semverVersion(v string) bool {
	_, err := semver.StrictNewVersion(v)
	return err == nil
}

// requirement is a's requires entry for the App name, nil when it has none.
func requirement(a *v1.App, name v1.ObjectName) *v1.AppRequirement {
	i := slices.IndexFunc(a.Spec.Requires, func(e v1.AppRequirement) bool { return e.App == name })
	if i < 0 {
		return nil
	}
	return &a.Spec.Requires[i]
}

// wait is Decision 3's step for a latest revision that is Deploying, not current and without startedAt: when every
// requirement is met, it stores startedAt from the clock with a status update before any part is written and puts
// the returned revision in revs; otherwise it returns the RequirementNotMet stop, which names no part. A Conflict on
// that update asks for a requeue, with no part written.
func (r *Reconciler) wait(ctx context.Context, a *v1.App, revs []*v1.AppRevision, rq requires) (*stop, bool, error) {
	if len(revs) == 0 {
		return nil, false, nil
	}
	latest := revs[len(revs)-1]
	if latest.Status.Phase != v1.PhaseDeploying || latest.Name == a.Status.CurrentRevision || latest.Status.StartedAt != nil {
		return nil, false, nil
	}
	if rq.unmet != "" {
		return &stop{reason: reasonRequirementNotMet, detail: rq.unmet}, false, nil
	}
	t := v1.NewTimestamp(r.clock.Now())
	latest.Status.StartedAt = &t
	updated, err := r.store.Update(ctx, latest)
	switch {
	case fault.KindOf(err) == fault.Conflict:
		return nil, true, nil
	case err != nil:
		return nil, false, fault.Wrapf(err, fault.KindOf(err), op, "store startedAt of app revision %s/%s", latest.Namespace, latest.Name)
	}
	revs[len(revs)-1] = updated.(*v1.AppRevision)
	return nil, false, nil
}

// MapRequires is the controller.MapFunc of App (ADR-0219 Decision 6): for a changed or deleted App it requeues each
// App of its namespace whose spec.requires names it, each App its own spec.requires names, and each App whose
// status.requiredBy names it, so a removed or renamed requirement reaches the formerly required App.
func (r *Reconciler) MapRequires(ctx context.Context, obj v1.Object) []controller.Request {
	a, ok := obj.(*v1.App)
	if !ok {
		return nil
	}
	var reqs []controller.Request
	add := func(name v1.ObjectName) {
		req := controller.Request{GVK: v1.KindApp.GVK(), Namespace: a.Namespace, Name: name}
		if name != a.Name && !slices.Contains(reqs, req) {
			reqs = append(reqs, req)
		}
	}
	for _, e := range a.Spec.Requires {
		add(e.App)
	}
	res, err := r.store.List(ctx, v1.KindApp.GVK(), store.ListOptions{Namespace: a.Namespace})
	if err != nil {
		r.log.WarnContext(ctx, "list apps of a changed app", "namespace", string(a.Namespace), "name", string(a.Name), "error", err)
		return reqs
	}
	for _, o := range res.Items {
		if other, ok := o.(*v1.App); ok && (requirement(other, a.Name) != nil || slices.Contains(other.Status.RequiredBy, a.Name)) {
			add(other.Name)
		}
	}
	return reqs
}

// requiresAdmission is app-requires (ADR-0219 Decision 7).
type requiresAdmission struct{ r admission.StoreReader }

// NewRequiresAdmission returns app-requires, the Validating admission of App Create, Update and Delete that refuses
// an upgrade or a rollback out of a started dependent's range, the delete of a required App, and a requirement
// cycle, each refusal naming the dependents sorted by name. A missing required App is never refused: it waits.
func NewRequiresAdmission(r admission.StoreReader) admission.Admission {
	return requiresAdmission{r: r}
}

func (requiresAdmission) Name() string           { return "app-requires" }
func (requiresAdmission) Phase() admission.Phase { return admission.Validating }

// ReadsNamespace: every check reads the namespace's Apps, so the namespace lock covers it (ADR-0147).
func (requiresAdmission) ReadsNamespace() bool { return true }

func (requiresAdmission) Handles(gvk v1.GroupVersionKind, op admission.Operation) bool {
	return gvk == v1.KindApp.GVK() && (op == admission.Create || op == admission.Update || op == admission.Delete)
}

func (a requiresAdmission) Admit(ctx context.Context, req admission.Request) (v1.Object, error) {
	if req.Operation == admission.Delete {
		old, ok := req.Old.(*v1.App)
		if !ok {
			return req.Old, nil
		}
		apps, err := a.apps(ctx, old.Namespace)
		if err != nil {
			return nil, err
		}
		var deps []string
		for _, d := range apps {
			if d.Name != old.Name && requirement(d, old.Name) != nil {
				deps = append(deps, string(d.Name))
			}
		}
		if len(deps) > 0 {
			slices.Sort(deps)
			return nil, fault.Conflictf(requiresOp, "app %q is required by %s; remove the requirement first", old.Name, strings.Join(deps, ", "))
		}
		return req.Old, nil
	}
	app, ok := req.Object.(*v1.App)
	if !ok {
		return req.Object, nil
	}
	old, upgrade := req.Old.(*v1.App)
	if len(app.Spec.Requires) == 0 && !upgrade {
		return req.Object, nil
	}
	apps, err := a.apps(ctx, app.Namespace)
	if err != nil {
		return nil, err
	}
	if cyc := cycle(app, apps); cyc != "" {
		return nil, fault.Invalidf(requiresOp, "spec.requires would create a requirement cycle (%s)", cyc)
	}
	if !upgrade {
		return req.Object, nil
	}
	var deps []string
	for _, d := range apps {
		e := requirement(d, app.Name)
		if d.Name == app.Name || e == nil || waiting(d) || !e.Matches(old.Spec.Version) || e.Matches(app.Spec.Version) {
			continue
		}
		deps = append(deps, fmt.Sprintf("%s (%s)", d.Name, e.Version))
	}
	if len(deps) > 0 {
		slices.Sort(deps)
		return nil, fault.Conflictf(requiresOp, "app %q version %q is outside the range its dependents require: %s",
			app.Name, app.Spec.Version, strings.Join(deps, ", "))
	}
	return req.Object, nil
}

func (a requiresAdmission) apps(ctx context.Context, ns v1.NamespaceName) ([]*v1.App, error) {
	items, err := a.r.List(ctx, v1.KindApp.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, requiresOp, "list apps in %q", ns)
	}
	out := make([]*v1.App, 0, len(items))
	for _, o := range items {
		if d, ok := o.(*v1.App); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// cycle is the requirement cycle through app, "" when none: the stored Apps' edges with app's own from the request,
// as link-validity builds the link graph; a self-requirement is a cycle.
func cycle(app *v1.App, apps []*v1.App) string {
	if len(app.Spec.Requires) == 0 {
		return ""
	}
	adj := make(map[string][]string)
	for _, d := range apps {
		if d.Name == app.Name {
			continue
		}
		for _, e := range d.Spec.Requires {
			adj[string(d.Name)] = append(adj[string(d.Name)], string(e.App))
		}
	}
	for _, e := range app.Spec.Requires {
		adj[string(app.Name)] = append(adj[string(app.Name)], string(e.App))
	}
	return admission.FindCycle(string(app.Name), adj)
}

// waiting reports whether the dependent d has not started, as the design note decides: no currentRevision, and its
// Ready reason RequirementNotMet or no phase yet (created paused, or not reconciled yet).
func waiting(d *v1.App) bool {
	if d.Status.CurrentRevision != "" {
		return false
	}
	c, _ := d.Status.Conditions.Get(condReady)
	return c.Reason == reasonRequirementNotMet || d.Status.Phase == ""
}
