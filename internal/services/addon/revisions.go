package addon

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// An Addon's versions are revisions, and each object pins the one its engine runs (ADR-0339).

const (
	// reasonProgressing holds an object with no revision to run yet: its Addon has none, or an App's part waits for
	// its App's revision to be applied (ADR-0339 Decisions 2 and 3).
	reasonProgressing = "Progressing"
	// reasonAddonRevisionNotFound holds an object whose pinned revision is gone (ADR-0339 Decision 2).
	reasonAddonRevisionNotFound = "AddonRevisionNotFound"
	// condApplied is the AppRevision condition a part waits for before it moves (ADR-0200 Decision 5).
	condApplied = v1.ConditionType("Applied")
)

// Revisions is the controller.Reconciler of Addon (ADR-0339 Decisions 1 and 6): it stamps the revision of the Addon's
// generation, names it current, and removes every other revision that no object pins and no AppRevision records.
func (r *Reconciler) Revisions() controller.Reconciler { return addonRevisions{r} }

type addonRevisions struct{ *Reconciler }

// Reconcile stamps the revision of the Addon's generation when it has none, so numbers may skip, then sets
// status.currentRevision and removes the revisions nothing uses. While a revision that is not current remains, it
// requeues after the supervision period: an object's pin or an App's history can let it go with no event of the Addon.
// Each pass is one step of the controller's one worker, so an object's pass that read the current revision pinned it
// before this one lists the pins (ADR-0339 Decision 6).
func (r addonRevisions) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "services.addon.Revisions"
	obj, err := r.store.Get(ctx, v1.KindAddon.GVK(), "", req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil // the GC collects its revisions with it
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get Addon %s", req.Name)
	}
	a, ok := obj.(*v1.Addon)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s is not an Addon", req.Name)
	}
	name := v1.AddonRevisionName(a.Name, a.Generation)
	cur, err := r.store.Get(ctx, v1.KindAddonRevision.GVK(), "", name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		if err := r.stampRevision(ctx, a, name); err != nil {
			return controller.Result{}, err
		}
	case err != nil:
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get AddonRevision %s", name)
	case !v1.ControlledBy(cur.GetObjectMeta().OwnerReferences, v1.KindAddon, a.UID):
		// A deleted Addon's of the same name: the GC collects it, and a later pass stamps this one's.
		return controller.Result{RequeueAfter: r.period}, nil
	}
	if a.Status.CurrentRevision != name {
		a.Status.CurrentRevision = name
		if _, err := r.store.Update(ctx, a); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				return controller.Result{}, nil // the Addon changed: its event brings the next pass
			}
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "set the current revision of Addon %s", a.Name)
		}
	}
	left, err := r.retire(ctx, a, name)
	if err != nil || !left {
		return controller.Result{}, err
	}
	return controller.Result{RequeueAfter: r.period}, nil
}

// stampRevision creates the revision name of a: its generation and a frozen copy of its spec, controlled by a.
func (r addonRevisions) stampRevision(ctx context.Context, a *v1.Addon, name v1.ObjectName) error {
	obj, _ := v1.NewObject(v1.KindAddonRevision)
	rev := obj.(*v1.AddonRevision)
	rev.ObjectMeta = v1.ObjectMeta{Name: name, OwnerReferences: []v1.OwnerReference{{
		ObjectRef: v1.ObjectRef{Kind: v1.KindAddon, Name: a.Name}, UID: a.UID, Controller: true, BlockOwnerDeletion: true,
	}}}
	rev.Spec = v1.AddonRevisionSpec{Addon: a.Name, Number: a.Generation, Spec: a.Spec}
	if _, err := r.store.Create(ctx, rev); err != nil && fault.KindOf(err) != fault.Conflict {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.Revisions", "stamp AddonRevision %s", name)
	}
	r.logger.InfoContext(ctx, "stamped", "addon", string(a.Name), "revision", string(name))
	return nil
}

// retire removes each revision of a but current that no add-on object of its kind pins and no AppRevision records,
// and reports whether a revision that is not current remains.
func (r addonRevisions) retire(ctx context.Context, a *v1.Addon, current v1.ObjectName) (bool, error) {
	const op = "services.addon.retire"
	revs, err := r.store.List(ctx, v1.KindAddonRevision.GVK(), store.ListOptions{})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), op, "list AddonRevisions")
	}
	var old []v1.Object
	for _, o := range revs.Items {
		if m := o.GetObjectMeta(); m.Name != current && v1.ControlledBy(m.OwnerReferences, v1.KindAddon, a.UID) {
			old = append(old, o)
		}
	}
	if len(old) == 0 {
		return false, nil
	}
	used, err := r.usedRevisions(ctx, a.Spec.Kind)
	if err != nil {
		return false, err
	}
	left := false
	for _, o := range old {
		m := o.GetObjectMeta()
		if used[m.Name] {
			left = true
			continue
		}
		if err := r.store.Delete(ctx, v1.KindAddonRevision.GVK(), "", m.Name, m.ResourceVersion); err != nil {
			switch fault.KindOf(err) {
			case fault.NotFound:
			case fault.Conflict:
				left = true
			default:
				return false, fault.Wrapf(err, fault.KindOf(err), op, "remove AddonRevision %s", m.Name)
			}
			continue
		}
		r.logger.InfoContext(ctx, "removed an unused revision", "addon", string(a.Name), "revision", string(m.Name))
	}
	return left, nil
}

// usedRevisions are the revisions of kind that an add-on object pins or an AppRevision records, in any namespace.
func (r addonRevisions) usedRevisions(ctx context.Context, kind string) (map[v1.ObjectName]bool, error) {
	const op = "services.addon.retire"
	used := map[v1.ObjectName]bool{}
	objs, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list add-on objects")
	}
	for _, o := range objs.Items {
		if ao, ok := o.(*v1.AddonObject); ok && v1.SameAddonKind(ao.Spec.Kind, kind) && ao.Status.AddonRevision != "" {
			used[ao.Status.AddonRevision] = true
		}
	}
	apps, err := r.store.List(ctx, v1.KindAppRevision.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list AppRevisions")
	}
	for _, o := range apps.Items {
		if ar, ok := o.(*v1.AppRevision); ok {
			for _, rec := range ar.Spec.AddonRevisions {
				if v1.SameAddonKind(rec.Kind, kind) {
					used[rec.Revision] = true
				}
			}
		}
	}
	return used, nil
}

// pinHold is why an object runs no engine for want of a revision.
type pinHold struct{ reason, message string }

// pinned is a with the spec of the revision o's engine runs, after this pass moved o's pin (ADR-0339 Decisions 2, 3
// and 7): under dev every object follows the current revision; an App's part follows its App's revisions forward; a
// standalone object pins the current revision at its first pass and at each new generation. It holds o while it has
// no revision to run, and while the one it pins is gone.
func (r *Reconciler) pinned(ctx context.Context, a *v1.Addon, o *v1.AddonObject) (*v1.Addon, *pinHold, error) {
	app, part := partOf(o)
	if part {
		if err := r.followApp(ctx, a, o, app); err != nil {
			return nil, nil, err
		}
	}
	switch {
	case r.followCurrent:
		o.Status.AddonRevision = a.Status.CurrentRevision
	case !part && (o.Status.AddonRevision == "" || o.Status.ObservedGeneration != o.Generation):
		o.Status.AddonRevision = a.Status.CurrentRevision
	}
	if o.Status.AddonRevision == "" {
		msg := fmt.Sprintf("Addon %s has no revision yet", a.Name)
		if part && !r.followCurrent {
			msg = fmt.Sprintf("waits for a revision of App %s to be applied", app.Name)
		}
		return nil, &pinHold{reasonProgressing, msg}, nil
	}
	obj, err := r.store.Get(ctx, v1.KindAddonRevision.GVK(), "", o.Status.AddonRevision)
	if fault.KindOf(err) == fault.NotFound {
		return nil, &pinHold{reasonAddonRevisionNotFound, fmt.Sprintf("the AddonRevision %s it pins is gone", o.Status.AddonRevision)}, nil
	}
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), "services.addon.pinned", "get AddonRevision %s", o.Status.AddonRevision)
	}
	rev, ok := obj.(*v1.AddonRevision)
	if !ok {
		return nil, nil, fault.Internalf("services.addon.pinned", "object %s is not an AddonRevision", o.Status.AddonRevision)
	}
	pinnedAddon := *a
	pinnedAddon.Spec = rev.Spec.Spec
	return &pinnedAddon, nil, nil
}

// partOf is the App that controls o, when one does.
func partOf(o *v1.AddonObject) (v1.OwnerReference, bool) {
	c, ok := v1.ControllerOf(o.OwnerReferences)
	return c, ok && c.Kind == v1.KindApp
}

// followApp moves an App's part forward only (ADR-0339 Decision 3): to the record for its kind of the App's latest
// AppRevision, once that revision is Applied and its number is higher than the one the part follows. A superseded
// revision is never a target, and a revision's number comes from its name, so a pruned one still compares. A latest
// revision with no record for the kind, stamped while its Addon had none, gives the current revision.
func (r *Reconciler) followApp(ctx context.Context, a *v1.Addon, o *v1.AddonObject, app v1.OwnerReference) error {
	res, err := r.store.List(ctx, v1.KindAppRevision.GVK(), store.ListOptions{Namespace: o.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.followApp", "list the AppRevisions of %s", o.Namespace)
	}
	var latest *v1.AppRevision
	for _, obj := range res.Items {
		if ar, ok := obj.(*v1.AppRevision); ok && v1.ControlledBy(ar.OwnerReferences, v1.KindApp, app.UID) &&
			(latest == nil || ar.Spec.Number > latest.Spec.Number) {
			latest = ar
		}
	}
	if latest == nil {
		return nil
	}
	if c, ok := latest.Status.Conditions.Get(condApplied); !ok || c.Status != v1.ConditionTrue {
		return nil
	}
	if o.Status.AppRevision != "" && latest.Spec.Number <= revisionNumber(app.Name, o.Status.AppRevision) {
		return nil
	}
	o.Status.AppRevision = latest.Name
	if !r.followCurrent {
		o.Status.AddonRevision = cmp.Or(recordOf(latest, a.Spec.Kind), a.Status.CurrentRevision)
	}
	return nil
}

// recordOf is rev's revision for the add-on kind, "" when it records none.
func recordOf(rev *v1.AppRevision, kind string) v1.ObjectName {
	i := slices.IndexFunc(rev.Spec.AddonRevisions, func(x v1.AppAddonRevision) bool { return v1.SameAddonKind(x.Kind, kind) })
	if i < 0 {
		return ""
	}
	return rev.Spec.AddonRevisions[i].Revision
}

// revisionNumber is the number of the App revision name, <app>-<number> (ADR-0200 Decision 1), 0 when it is not one.
func revisionNumber(app v1.ObjectName, name v1.ObjectName) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(string(name), string(app)+"-"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// MapAppRevision maps an AppRevision's change to its App's add-on parts, so a part moves once its App's revision is
// Applied (ADR-0339 Decision 3).
func (r *Reconciler) MapAppRevision(ctx context.Context, obj v1.Object) []controller.Request {
	rev, ok := obj.(*v1.AppRevision)
	if !ok {
		return nil
	}
	return r.objects(ctx, rev.Namespace, func(o *v1.AddonObject) bool {
		app, part := partOf(o)
		return part && app.Name == rev.Spec.App.Name
	})
}
