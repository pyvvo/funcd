package addon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// An engineless kind's objects run no engine (ADR-0345): a Function of their namespace reconciles them, called as a
// settings kind's Function is (ADR-0341), with the list of each group, the objects of one namespace and one kind that
// one Function reconciles. An object with deletion: delete is held at its deletion until its Function answers ok to a
// call that carried it as deleting.

// ObjectsCaller calls an engineless kind's Function with one group's list (Decision 2).
type ObjectsCaller interface {
	Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call ObjectsCall) (SettingsReply, error)
}

// ObjectsCall is a call's data: the kind, why funcd calls, and every object of the group, sorted by name, deleting
// ones included.
type ObjectsCall struct {
	Kind    string       `json:"kind"`
	Cause   string       `json:"cause"` // start, change, retry, resync, run
	Objects []CallObject `json:"objects"`
}

// CallObject is one object of a group's list, at the uid and generation funcd sends.
type CallObject struct {
	Name       v1.ObjectName   `json:"name"`
	UID        v1.UID          `json:"uid"`
	Generation int64           `json:"generation"`
	Deleting   bool            `json:"deleting,omitempty"`
	Config     json.RawMessage `json:"config"`
}

// reasonDeleting is a held object's reason until its Function answers a call that carried it as deleting (Decision 6).
const reasonDeleting = "Deleting"

// groupKey is one group (Decision 2): a namespace, an engineless kind and the Function that reconciles its objects.
type groupKey struct {
	ns   v1.NamespaceName
	kind string
	fn   v1.ObjectName
}

// SetObjectsCaller gives the reconciler the caller of the engineless kinds' Functions. A call's end requeues its
// objects through the Enqueue that SetSettingsCaller gave, and addons.settingsTimeout bounds it (Decision 2). With no
// caller, no call is made.
func (r *Reconciler) SetObjectsCaller(c ObjectsCaller) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	r.objectsCaller = c
}

// enginelessFunction names the Function that reconciles o, an object of the engineless kind at's: its own in
// spec.functions, else the kind's function.ref (Decision 2).
func enginelessFunction(at v1.AddonAttributes, o *v1.AddonObject) v1.ObjectName {
	for _, f := range o.Spec.Functions {
		if v1.SameAddonKind(f.Kind, at.Kind) {
			return f.Ref
		}
	}
	if at.Function == nil {
		return ""
	}
	return at.Function.Ref
}

// enginelessObject is the pass of an object of an engineless kind, which runs no engine (Decisions 1 and 5 to 7). A
// deleting object set to retain goes with no call. Otherwise the object pins its revision as an engine's object does,
// and its first pass stores status.workload before any call carries it. It waits while its revision or its Function is
// missing, a deleting one too; a new or edited object is Progressing until the answer for its generation, and a
// deleting one Deleting until the first answer to a call that carried it so. Its group's call is due as a settings
// call is, and runs on a goroutine of its own.
func (r *Reconciler) enginelessObject(ctx context.Context, current *v1.Addon, o *v1.AddonObject) (controller.Result, error) {
	r.seePart(o)
	if o.DeletionTime != nil && o.Spec.Deletion != v1.DeletionDelete {
		return controller.Result{}, r.removeHeld(ctx, o.Namespace, o.Name, o.UID)
	}
	if current == nil {
		return r.objectStatus(ctx, o, reasonAddonNotFound, fmt.Sprintf("no Addon registers the kind %q", o.Spec.Kind), false)
	}
	a, ph, err := r.pinned(ctx, current, o)
	if err != nil {
		return controller.Result{}, err
	}
	if ph != nil {
		return r.objectStatus(ctx, o, ph.reason, ph.message, false)
	}
	if o.Status.Workload != v1.WorkloadEngineless {
		o.Status.Workload = v1.WorkloadEngineless
		res, err := r.objectStatus(ctx, o, reasonProgressing, progressing(o), true)
		if err == nil {
			res.Requeue = true // the next pass may call: the mark is stored
		}
		return res, err
	}
	at := a.Spec.KindAttributes()
	fnName := enginelessFunction(at, o)
	fn, err := r.function(ctx, o.Namespace, fnName)
	switch {
	case err != nil:
		return controller.Result{}, err
	case fn == nil:
		return r.objectStatus(ctx, o, reasonFunctionNotFound,
			fmt.Sprintf("the namespace holds no Function %q, which reconciles the %s objects", fnName, at.Kind), false)
	}
	switch c, ok := o.Status.Conditions.Get(condReady); {
	case o.DeletionTime != nil && !r.answeredDeleting(o.UID):
		if _, err := r.objectStatus(ctx, o, reasonDeleting,
			"held until its Function answers ok to a call that carries it as deleting", false); err != nil {
			return controller.Result{}, err
		}
	case o.DeletionTime == nil && (!ok || c.ObservedGeneration != o.Generation || waiting(c.Reason) || c.Reason == reasonDeleting):
		if _, err := r.objectStatus(ctx, o, reasonProgressing, progressing(o), false); err != nil {
			return controller.Result{}, err
		}
	}
	next, err := r.syncGroup(ctx, at, o, fn)
	if err != nil {
		return controller.Result{}, err
	}
	if next > 0 && next < r.period {
		return controller.Result{RequeueAfter: next}, nil
	}
	return controller.Result{RequeueAfter: r.period}, nil
}

// progressing is a new or edited object's message until its Function answers for its generation.
func progressing(o *v1.AddonObject) string {
	return fmt.Sprintf("waiting for the answer of the Function for generation %d", o.Generation)
}

// objectStatus writes o Pending with reason and message at its generation, unless it already reads so and force,
// which a status change the condition does not show needs, is false.
func (r *Reconciler) objectStatus(ctx context.Context, o *v1.AddonObject, reason, message string, force bool) (controller.Result, error) {
	c, ok := o.Status.Conditions.Get(condReady)
	if !force && ok && o.Status.Phase == v1.PhasePending && c.Status == v1.ConditionFalse && c.Reason == reason &&
		c.Message == message && c.ObservedGeneration == o.Generation && o.Status.ObservedGeneration == o.Generation {
		return controller.Result{}, nil
	}
	o.Status.Phase, o.Status.ObservedGeneration = v1.PhasePending, o.Generation
	o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message,
		ObservedGeneration: o.Generation})
	updated, err := r.store.Update(ctx, o)
	if err != nil {
		return controller.Result{}, retryOnConflict(err, "services.addon.enginelessObject")
	}
	if u, ok := updated.(*v1.AddonObject); ok {
		*o = *u
	}
	return controller.Result{}, nil
}

// syncGroup starts the call of o's group when one is due and none runs (Decision 2), and returns how soon the next is
// due, 0 for none before the supervision period. The group is every object of o's namespace and kind that fn
// reconciles and whose first pass stored its mark, sorted by name.
func (r *Reconciler) syncGroup(ctx context.Context, at v1.AddonAttributes, o *v1.AddonObject, fn *v1.Function) (time.Duration, error) {
	objs, err := r.groupObjects(ctx, at.Kind, o.Namespace, fn.Name)
	if err != nil {
		return 0, err
	}
	key := groupKey{o.Namespace, at.Kind, fn.Name}
	digest := groupDigest(fn, objs)
	cause, wait := r.groupDue(key, digest, at.EffectiveResync())
	if cause == "" {
		return wait, nil
	}
	call := ObjectsCall{Kind: at.Kind, Cause: cause, Objects: make([]CallObject, len(objs))}
	for i, so := range objs {
		call.Objects[i] = CallObject{Name: so.Name, UID: so.UID, Generation: so.Generation, Deleting: so.DeletionTime != nil,
			Config: configOf(so)}
	}
	r.startObjectsCall(ctx, key, call, objs, digest, at.EffectiveResync())
	return 0, nil
}

// groupObjects are the objects of kind in ns that fn reconciles and whose first pass stored their mark, sorted by
// name, deleting ones included. An object's Function is its own in spec.functions, else its pinned revision's
// function.ref: one whose revision is gone is in no group.
func (r *Reconciler) groupObjects(ctx context.Context, kind string, ns v1.NamespaceName, fn v1.ObjectName) ([]*v1.AddonObject, error) {
	const op = "services.addon.groupObjects"
	list, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects of %s", ns)
	}
	revs := map[v1.ObjectName]*v1.AddonRevision{}
	var out []*v1.AddonObject
	for _, item := range list.Items {
		o, ok := item.(*v1.AddonObject)
		if !ok || o.Status.Workload != v1.WorkloadEngineless || !v1.SameAddonKind(o.Spec.Kind, kind) {
			continue
		}
		rev, seen := revs[o.Status.AddonRevision]
		if !seen {
			obj, gerr := r.store.Get(ctx, v1.KindAddonRevision.GVK(), "", o.Status.AddonRevision)
			switch {
			case fault.KindOf(gerr) == fault.NotFound:
			case gerr != nil:
				return nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "get AddonRevision %s", o.Status.AddonRevision)
			default:
				rev, _ = obj.(*v1.AddonRevision)
			}
			revs[o.Status.AddonRevision] = rev
		}
		if rev != nil && enginelessFunction(rev.Spec.Spec.KindAttributes(), o) == fn {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(x, y *v1.AddonObject) int { return strings.Compare(string(x.Name), string(y.Name)) })
	return out, nil
}

// groupDigest names what a group's call sends (Decision 2): the Function, its uid and its current revision, and each
// object's name, uid, generation and deleting state.
func groupDigest(fn *v1.Function, objs []*v1.AddonObject) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", fn.Name, fn.UID, fn.Status.CurrentRevision)
	for _, o := range objs {
		fmt.Fprintf(&b, "|%s:%s:%d:%t", o.Name, o.UID, o.Generation, o.DeletionTime != nil)
	}
	return b.String()
}

// groupDue is settingsDue for a group: the cause of a call due for key now, marked in flight, else how long until one
// is due.
func (r *Reconciler) groupDue(key groupKey, digest string, resync time.Duration) (string, time.Duration) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	return dueLocked(r.groups, r.objectsCaller != nil, key, r.now(), digest, resync)
}

// startObjectsCall calls key's Function with call on a goroutine bounded by addons.settingsTimeout. Once it ends it
// writes each sent object's status from the answer, removes each held object it answered ok while it carried it as
// deleting, records the end, and requeues every sent object. funcd's stop cuts the call, and nothing is written from it.
func (r *Reconciler) startObjectsCall(ctx context.Context, key groupKey, call ObjectsCall, objs []*v1.AddonObject, digest string,
	resync time.Duration) {
	const op = "services.addon.objectsCall"
	r.settingsMu.Lock()
	caller, enqueue, timeout := r.objectsCaller, r.enqueue, r.settingsTimeout
	r.settingsMu.Unlock()
	log := r.logger.With("namespace", key.ns, "kind", key.kind, "function", key.fn, "cause", call.Cause, "objects", len(objs))
	log.InfoContext(ctx, "objects call started")
	go func() {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		reply, err := caller.Call(cctx, key.ns, key.fn, call)
		if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = fault.Unavailablef(op, "the Function %s did not answer within addons.settingsTimeout %s", key.fn, timeout)
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.WarnContext(ctx, "objects call failed", "error", err)
		} else {
			log.InfoContext(ctx, "objects call ended")
		}
		r.writeObjectResults(ctx, key.ns, objs, reply, err)
		r.groupDone(key, digest, resync, err)
		if enqueue == nil {
			return
		}
		for _, so := range objs {
			enqueue(controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: key.ns, Name: so.Name})
		}
	}()
}

// groupDone records the end of key's call.
func (r *Reconciler) groupDone(key groupKey, digest string, resync time.Duration, err error) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if s, ok := r.groups[key]; ok {
		s.done(r.now(), digest, resync, err)
	}
}

// writeObjectResults applies a call's answer to each object it sent (Decisions 5 and 6). An object sent as deleting
// goes on an ok result, at its uid; any other outcome writes its status: the result's reason, else ReconcileFailed,
// NoResult when the answer leaves it out, and ReconcileFailed for a failed call. An object sent live gets its status at
// the uid and generation sent, Ready on ok, unless it has turned deleting since: that call removes nothing, and the
// next one carries it as deleting.
func (r *Reconciler) writeObjectResults(ctx context.Context, ns v1.NamespaceName, objs []*v1.AddonObject, reply SettingsReply, callErr error) {
	results := map[v1.ObjectName]SettingsResult{}
	for _, res := range reply.Results {
		results[res.Name] = res
	}
	for _, sent := range objs {
		res, found := results[sent.Name]
		deleting := sent.DeletionTime != nil
		if deleting && callErr == nil && found && res.OK {
			if err := r.removeHeld(ctx, ns, sent.Name, sent.UID); err != nil {
				r.logger.WarnContext(ctx, "could not remove a held add-on object", "namespace", ns, "name", sent.Name, "error", err)
			}
			continue
		}
		cond := v1.Condition{Type: condReady, Status: v1.ConditionFalse, ObservedGeneration: sent.Generation}
		phase := v1.PhasePending
		switch {
		case callErr != nil:
			cond.Reason, cond.Message = reasonReconcileFailed, callErr.Error()
		case !found:
			cond.Reason, cond.Message = reasonNoResult, "the Function's answer holds no result for the object"
		case res.OK:
			cond.Status, phase = v1.ConditionTrue, v1.PhaseReady
		default:
			cond.Reason, cond.Message = cmp.Or(res.Reason, reasonReconcileFailed), res.Message
		}
		r.writeObjectResult(ctx, ns, sent, deleting, phase, cond)
	}
}

// writeObjectResult writes one sent object's status, again after a conflicting write: a deleting one at its uid while
// it is still deleting, which ends its Deleting; a live one at its uid and generation while it is not deleting.
func (r *Reconciler) writeObjectResult(ctx context.Context, ns v1.NamespaceName, sent *v1.AddonObject, deleting bool, phase v1.Phase,
	cond v1.Condition) {
	for range 3 {
		obj, err := r.store.Get(ctx, v1.KindAddonObject.GVK(), ns, sent.Name)
		so, ok := obj.(*v1.AddonObject)
		if err != nil || !ok || so.UID != sent.UID || (so.DeletionTime != nil) != deleting || (!deleting && so.Generation != sent.Generation) {
			return
		}
		cond.ObservedGeneration = so.Generation
		so.Status.Phase, so.Status.ObservedGeneration = phase, so.Generation
		so.Status.Conditions.Set(cond)
		_, err = r.store.Update(ctx, so)
		if fault.KindOf(err) == fault.Conflict {
			continue
		}
		if err != nil {
			r.logger.WarnContext(ctx, "could not write an engineless object's status", "namespace", ns, "name", sent.Name, "error", err)
			return
		}
		if deleting {
			r.markAnswered(so.UID)
		}
		return
	}
}

// removeHeld removes the held object ns/name of uid, at its resourceVersion, again after a conflicting write: an ok to
// a call that carried it as deleting, or retain set on it, lets it go (Decisions 6 and 7). One gone, of another uid, or
// no longer deleting stays.
func (r *Reconciler) removeHeld(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, uid v1.UID) error {
	const op = "services.addon.removeHeld"
	for range 3 {
		obj, err := r.store.Get(ctx, v1.KindAddonObject.GVK(), ns, name)
		if fault.KindOf(err) == fault.NotFound {
			return nil
		}
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "get %s/%s", ns, name)
		}
		o, ok := obj.(*v1.AddonObject)
		if !ok || o.UID != uid || o.DeletionTime == nil {
			return nil
		}
		err = r.store.Delete(ctx, v1.KindAddonObject.GVK(), ns, name, o.ResourceVersion)
		switch fault.KindOf(err) {
		case "":
			r.logger.InfoContext(ctx, "removed a held add-on object", "namespace", ns, "name", name, "deletion", o.Spec.Deletion)
			r.forgetAnswered(uid)
			return nil
		case fault.NotFound:
			return nil
		case fault.Conflict:
			continue
		}
		return fault.Wrapf(err, fault.KindOf(err), op, "delete %s/%s", ns, name)
	}
	return nil
}

// answeredDeleting reports whether a call that carried the object of uid as deleting has written its status since this
// process started, which ends its Deleting.
func (r *Reconciler) answeredDeleting(uid v1.UID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.answered[uid]
}

func (r *Reconciler) markAnswered(uid v1.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answered[uid] = true
}

func (r *Reconciler) forgetAnswered(uid v1.UID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.answered, uid)
}

// seePart records the App that o is a part of, so its removal asks the collector to pass over its namespace.
func (r *Reconciler) seePart(o *v1.AddonObject) {
	ref := provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}
	app, part := partOf(o)
	r.mu.Lock()
	defer r.mu.Unlock()
	if part {
		r.parts[ref] = app.UID
	} else {
		delete(r.parts, ref)
	}
}

// partGone runs once the object ns/name is gone (Decision 8): when it was an App's engineless part and the App has no
// other one left, the collector passes over ns, so the App's other parts go now, not at the next sweep.
func (r *Reconciler) partGone(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	r.mu.Lock()
	app, was := r.parts[ref]
	delete(r.parts, ref)
	r.mu.Unlock()
	if !was || r.collect == nil {
		return nil
	}
	left := r.objects(ctx, ns, func(o *v1.AddonObject) bool {
		return o.Status.Workload == v1.WorkloadEngineless && v1.ControlledBy(o.OwnerReferences, v1.KindApp, app)
	})
	if len(left) > 0 {
		return nil
	}
	if err := r.collect(ctx, ns); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.partGone", "collect the parts of the App of %s/%s", ns, name)
	}
	return nil
}

// MapJobRun maps a JobRun that ended to the engineless objects of its namespace, and makes each group there due with
// cause run, at once or when its call in flight ends (Decision 4). It reads no binding: the Function reads from its own
// state which run ended.
func (r *Reconciler) MapJobRun(ctx context.Context, obj v1.Object) []controller.Request {
	run, ok := obj.(*v1.JobRun)
	if !ok || !run.Status.Phase.Ended() {
		return nil
	}
	r.settingsMu.Lock()
	for k, s := range r.groups {
		if k.ns == run.Namespace {
			s.run = true
		}
	}
	r.settingsMu.Unlock()
	return r.objects(ctx, run.Namespace, func(o *v1.AddonObject) bool { return o.Status.Workload == v1.WorkloadEngineless })
}

// MapEnginelessFunction maps a Function's change to the engineless objects of its namespace that it reconciles: its
// creation or deletion ends or starts a wait, and a new revision changes the next call's digest (Decision 2). An
// object's Function is its own in spec.functions, else the function.ref of the revision it pins (Decision 1), or of its
// kind as its Addon is now, which its next pass may pin: either maps it.
func (r *Reconciler) MapEnginelessFunction(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	addons, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the Addons a Function's change affects", "error", err)
		return nil
	}
	kinds := map[string]v1.AddonAttributes{}
	for _, item := range addons.Items {
		if a, ok := item.(*v1.Addon); ok && a.Spec.Engineless() {
			kinds[v1.AddonPlural(a.Spec.Kind())] = a.Spec.KindAttributes()
		}
	}
	if len(kinds) == 0 {
		return nil
	}
	revs := map[v1.ObjectName]*v1.AddonRevision{}
	return r.objects(ctx, m.Namespace, func(o *v1.AddonObject) bool {
		at, ok := kinds[v1.AddonPlural(o.Spec.Kind)]
		if ok && enginelessFunction(at, o) == m.Name {
			return true
		}
		if o.Status.Workload != v1.WorkloadEngineless || o.Status.AddonRevision == "" {
			return false
		}
		rev, seen := revs[o.Status.AddonRevision]
		if !seen {
			if got, err := r.store.Get(ctx, v1.KindAddonRevision.GVK(), "", o.Status.AddonRevision); err == nil {
				rev, _ = got.(*v1.AddonRevision)
			}
			revs[o.Status.AddonRevision] = rev
		}
		return rev != nil && enginelessFunction(rev.Spec.Spec.KindAttributes(), o) == m.Name
	})
}
