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

// A settings kind's Function applies a target's settings, called from the target's pass (ADR-0341): funcd computes
// each target's list and writes each object's status, and the Function, which alone knows the engine's API, applies
// the list through its binding of the target (ADR-0338).

// SettingsCaller calls a settings kind's Function with one target's list.
type SettingsCaller interface {
	Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call SettingsCall) (SettingsReply, error)
}

// SettingsCall is a call's data (Decision 2): the kind, why funcd calls, the target and every object of the kind that
// names it.
type SettingsCall struct {
	Kind    string           `json:"kind"`
	Cause   string           `json:"cause"` // start, change, retry, resync
	Target  SettingsTarget   `json:"target"`
	Objects []SettingsObject `json:"objects"`
}

// SettingsTarget is the target, and the alias under which the Function binds it.
type SettingsTarget struct {
	Kind  string        `json:"kind"`
	Name  v1.ObjectName `json:"name"`
	Alias string        `json:"alias"`
}

// SettingsObject is one object of the list, at the uid and generation funcd sends.
type SettingsObject struct {
	Name       v1.ObjectName   `json:"name"`
	UID        v1.UID          `json:"uid"`
	Generation int64           `json:"generation"`
	Config     json.RawMessage `json:"config"`
}

// SettingsReply is the Function's answer.
type SettingsReply struct {
	Results []SettingsResult `json:"results"`
}

// SettingsResult is whether the engine holds one object now.
type SettingsResult struct {
	Name    v1.ObjectName `json:"name"`
	OK      bool          `json:"ok"`
	Reason  string        `json:"reason,omitempty"`
	Message string        `json:"message,omitempty"`
}

// The reasons of a settings object (Decision 4), and the causes of a call (Decision 1).
const (
	reasonReconcileFailed  = "ReconcileFailed"
	reasonNoResult         = "NoResult"
	reasonTargetNotFound   = "TargetNotFound"
	reasonTargetNotReady   = "TargetNotReady"
	reasonFunctionNotFound = "FunctionNotFound"
	reasonFunctionNotBound = "FunctionNotBound"

	causeStart  = "start"
	causeChange = "change"
	causeRetry  = "retry"
	causeResync = "resync"
	causeRun    = "run" // a JobRun of the namespace ended (ADR-0345 Decision 4)
)

// DefaultSettingsTimeout is addons.settingsTimeout unset (Decision 8); maxSettingsBackoff is the longest wait before a
// failed call is due again (Decision 5).
const (
	DefaultSettingsTimeout = 30 * time.Second
	maxSettingsBackoff     = 5 * time.Minute
)

// waiting reports a reason of a settings object whose calls miss something (Decision 4).
func waiting(reason string) bool {
	switch reason {
	case reasonAddonNotFound, reasonTargetNotFound, reasonTargetNotReady, reasonFunctionNotFound, reasonFunctionNotBound:
		return true
	}
	return false
}

// SetSettingsCaller gives the reconciler the caller of the settings Functions and the controller's Enqueue, which
// pkg/funcd builds after the reconciler: a call's end requeues its target. timeout is addons.settingsTimeout, 0 ⇒
// DefaultSettingsTimeout. With no caller, no call is made.
func (r *Reconciler) SetSettingsCaller(c SettingsCaller, enqueue func(controller.Request), timeout time.Duration) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	r.caller, r.enqueue, r.settingsTimeout = c, enqueue, cmp.Or(timeout, DefaultSettingsTimeout)
}

// settingsKey is one target and one settings kind: at most one call runs for it at a time.
type settingsKey struct {
	ns     v1.NamespaceName
	target v1.ObjectName
	kind   string
}

// settingsState is what this process knows of a key's calls, in memory only, so a start sends again (Decision 7). An
// engineless kind's group keeps the same state (ADR-0345 Decision 2), and run, a JobRun's end in its namespace that no
// call has answered yet (ADR-0345 Decision 4).
type settingsState struct {
	sent     string // the digest of the last call that ended
	ended    time.Time
	inflight bool
	failures int
	retryAt  time.Time
	run      bool
}

// due names the cause of a call that is due now for s, which this process knew (known), the first of start, change,
// run, retry and resync that holds, and marks it in flight; else how long until one is due, 0 while a call runs, whose
// end requeues the call's objects. A JobRun's end is due at once, before a failed call's backoff ends.
func (s *settingsState) due(known bool, now time.Time, digest string, resync time.Duration) (string, time.Duration) {
	var cause string
	switch {
	case s.inflight:
		return "", 0
	case !known:
		cause = causeStart
	case digest != s.sent:
		cause = causeChange
	case s.run:
		cause = causeRun
	case s.failures > 0 && now.Before(s.retryAt):
		return "", s.retryAt.Sub(now)
	case s.failures > 0:
		cause = causeRetry
	case now.Before(s.ended.Add(resync)):
		return "", s.ended.Add(resync).Sub(now)
	default:
		cause = causeResync
	}
	s.inflight, s.run = true, false
	return cause, 0
}

// done records a call's end at now: the digest it sent and, after a failure, when it is due again: 1 s, then twice as
// long after each failure, at most 5 min or the resync (Decision 5).
func (s *settingsState) done(now time.Time, digest string, resync time.Duration, err error) {
	s.inflight, s.sent, s.ended = false, digest, now
	if err == nil {
		s.failures = 0
		return
	}
	s.failures++
	s.retryAt = s.ended.Add(min(time.Second<<min(s.failures-1, 20), maxSettingsBackoff, resync))
}

// targetSeen is whether a target was Ready at its last pass, and how many times it turned Ready.
type targetSeen struct {
	ready bool
	epoch int
}

// seeTarget records whether a target's engine is Ready in this pass, and returns how many times it turned Ready, which
// each digest holds, so an engine that restarted gets its list again.
func (r *Reconciler) seeTarget(ns v1.NamespaceName, name v1.ObjectName, ready bool) int {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	ref := provider.ProviderRef{Namespace: ns, Name: name}
	s := r.targets[ref]
	if ready && !s.ready {
		s.epoch++
	}
	s.ready = ready
	r.targets[ref] = s
	return s.epoch
}

// forgetTarget drops what this process knows of a deleted object's calls.
func (r *Reconciler) forgetTarget(ns v1.NamespaceName, name v1.ObjectName) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	delete(r.targets, provider.ProviderRef{Namespace: ns, Name: name})
	for k := range r.settings {
		if k.ns == ns && k.target == name {
			delete(r.settings, k)
		}
	}
}

// functionFor names the Function that applies target's objects of kind st: the target's own in spec.functions, else
// the kind's function.ref (Decision 3).
func functionFor(st *v1.AddonSettings, target *v1.AddonObject) v1.ObjectName {
	for _, f := range target.Spec.Functions {
		if v1.SameAddonKind(f.Kind, st.Kind) {
			return f.Ref
		}
	}
	return st.Function.Ref
}

// aliasOf is the alias under which fn binds the add-on object target, the call's grant (Decision 3).
func aliasOf(fn *v1.Function, target v1.ObjectName) (string, bool) {
	for _, b := range fn.Spec.Bindings.Addon {
		if b.Object == target {
			return b.Alias, true
		}
	}
	return "", false
}

// function is the Function name of ns, nil when the namespace holds none.
func (r *Reconciler) function(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Function, error) {
	const op = "services.addon.function"
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get Function %s/%s", ns, name)
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return nil, fault.Internalf(op, "object %s/%s is not a Function", ns, name)
	}
	return fn, nil
}

// syncSettings is the settings step of a Ready target's pass (Decision 1). For each settings kind of the target's Addon
// as it is now, it starts a call with the target's list when one is due and none runs; a missing or unbound Function
// calls nothing, and the objects wait. The call runs on a goroutine of its own, so no pass waits on a Function
// (Decision 2). It returns how soon the next call is due, 0 for none due before the supervision period.
func (r *Reconciler) syncSettings(ctx context.Context, a *v1.Addon, o *v1.AddonObject) (time.Duration, error) {
	const op = "services.addon.syncSettings"
	epoch := r.seeTarget(o.Namespace, o.Name, true)
	if a == nil || len(a.Spec.KindAttributes().Settings) == 0 {
		return 0, nil
	}
	list, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: o.Namespace})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects of %s", o.Namespace)
	}
	var next time.Duration
	for i := range a.Spec.KindAttributes().Settings {
		st := &a.Spec.KindAttributes().Settings[i]
		fn, err := r.function(ctx, o.Namespace, functionFor(st, o))
		if err != nil {
			return 0, err
		}
		if fn == nil {
			continue
		}
		alias, bound := aliasOf(fn, o.Name)
		if !bound {
			continue
		}
		objs := targetObjects(list.Items, st, o.Name)
		key := settingsKey{o.Namespace, o.Name, st.Kind}
		digest := settingsDigest(epoch, o.Status.Address, fn, alias, objs)
		cause, wait := r.settingsDue(key, digest, st.EffectiveResync())
		if cause != "" {
			call := SettingsCall{Kind: st.Kind, Cause: cause, Target: SettingsTarget{Kind: a.Spec.Kind(), Name: o.Name, Alias: alias},
				Objects: make([]SettingsObject, len(objs))}
			for j, so := range objs {
				call.Objects[j] = SettingsObject{Name: so.Name, UID: so.UID, Generation: so.Generation, Config: configOf(so)}
			}
			r.startCall(ctx, key, fn.Name, call, objs, digest, st.EffectiveResync())
		}
		if wait > 0 && (next == 0 || wait < next) {
			next = wait
		}
	}
	return next, nil
}

// targetObjects are the objects of kind st that name target, sorted by name.
func targetObjects(items []v1.Object, st *v1.AddonSettings, target v1.ObjectName) []*v1.AddonObject {
	var out []*v1.AddonObject
	for _, item := range items {
		so, ok := item.(*v1.AddonObject)
		if !ok || !v1.SameAddonKind(so.Spec.Kind, st.Kind) {
			continue
		}
		if t, ok := TargetOf(st, so); ok && t == target {
			out = append(out, so)
		}
	}
	slices.SortFunc(out, func(x, y *v1.AddonObject) int { return strings.Compare(string(x.Name), string(y.Name)) })
	return out
}

// settingsDigest names what a call sends (Decision 1): the target's address and how many times it turned Ready, the
// Function, its current revision and the alias that binds the target, and each object's name, uid and generation. The
// Function's uid tells a Function created again from the one it replaced.
func settingsDigest(epoch int, address string, fn *v1.Function, alias string, objs []*v1.AddonObject) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%s|%s|%s", epoch, address, fn.Name, fn.UID, fn.Status.CurrentRevision, alias)
	for _, o := range objs {
		fmt.Fprintf(&b, "|%s:%s:%d", o.Name, o.UID, o.Generation)
	}
	return b.String()
}

// settingsDue names the cause of a call that is due for key now, the first of start, change, retry and resync that
// holds, and marks it in flight; else how long until one is due, 0 while a call runs, whose end requeues the target.
func (r *Reconciler) settingsDue(key settingsKey, digest string, resync time.Duration) (string, time.Duration) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if r.caller == nil {
		return "", 0
	}
	s, known := r.settings[key]
	if !known {
		s = &settingsState{}
		r.settings[key] = s
	}
	return s.due(known, r.now(), digest, resync)
}

// startCall calls fn with call on a goroutine bounded by addons.settingsTimeout. Once it ends it writes each sent
// object's status from the answer, records the end, and requeues the target and the objects it wrote, whose passes
// check again what their calls need. funcd's stop cuts the call, and nothing is written from it (Decision 7).
func (r *Reconciler) startCall(ctx context.Context, key settingsKey, fn v1.ObjectName, call SettingsCall, objs []*v1.AddonObject,
	digest string, resync time.Duration) {
	const op = "services.addon.settingsCall"
	r.settingsMu.Lock()
	caller, enqueue, timeout := r.caller, r.enqueue, r.settingsTimeout
	r.settingsMu.Unlock()
	log := r.logger.With("namespace", key.ns, "target", key.target, "kind", key.kind, "function", fn, "cause", call.Cause,
		"objects", len(objs))
	log.InfoContext(ctx, "settings call started")
	go func() {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		reply, err := caller.Call(cctx, key.ns, fn, call)
		if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = fault.Unavailablef(op, "the Function %s did not answer within addons.settingsTimeout %s", fn, timeout)
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.WarnContext(ctx, "settings call failed", "error", err)
		} else {
			log.InfoContext(ctx, "settings call ended")
		}
		r.writeResults(ctx, key.ns, objs, reply, err)
		r.settingsDone(key, digest, resync, err)
		if enqueue == nil {
			return
		}
		enqueue(controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: key.ns, Name: key.target})
		for _, so := range objs {
			enqueue(controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: key.ns, Name: so.Name})
		}
	}()
}

// writeResults writes each sent object's status from the answer (Decision 4): a failed call makes each one
// ReconcileFailed, and an object the answer leaves out is NoResult. A result for an object not sent is ignored, and the
// last result of a name counts.
func (r *Reconciler) writeResults(ctx context.Context, ns v1.NamespaceName, objs []*v1.AddonObject, reply SettingsReply, callErr error) {
	results := map[v1.ObjectName]SettingsResult{}
	for _, res := range reply.Results {
		results[res.Name] = res
	}
	for _, sent := range objs {
		cond := v1.Condition{Type: condReady, Status: v1.ConditionFalse, ObservedGeneration: sent.Generation}
		phase := v1.PhasePending
		res, found := results[sent.Name]
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
		r.writeResult(ctx, ns, sent, phase, cond)
	}
}

// writeResult writes one object's status at the uid and generation the call sent, again after a conflicting write; an
// object whose uid or spec changed since is left to the next call.
func (r *Reconciler) writeResult(ctx context.Context, ns v1.NamespaceName, sent *v1.AddonObject, phase v1.Phase, cond v1.Condition) {
	for range 3 {
		obj, err := r.store.Get(ctx, v1.KindAddonObject.GVK(), ns, sent.Name)
		so, ok := obj.(*v1.AddonObject)
		if err != nil || !ok || so.UID != sent.UID || so.Generation != sent.Generation {
			return
		}
		so.Status.Phase, so.Status.ObservedGeneration = phase, sent.Generation
		so.Status.Conditions.Set(cond)
		_, err = r.store.Update(ctx, so)
		if fault.KindOf(err) == fault.Conflict {
			continue
		}
		if err != nil {
			r.logger.WarnContext(ctx, "could not write a settings object's status", "namespace", ns, "name", sent.Name, "error", err)
		}
		return
	}
}

// settingsDone records a call's end: the digest it sent and, after a failure, when it is due again: 1 s, then twice as
// long after each failure, at most 5 min or the resync (Decision 5).
func (r *Reconciler) settingsDone(key settingsKey, digest string, resync time.Duration, err error) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if s, ok := r.settings[key]; ok {
		s.done(r.now(), digest, resync, err)
	}
}

// settingsObject is the pass of a settings object, which runs no engine (Decision 4). While its calls miss something it
// is Pending with the first missing piece, even after an answer. Once nothing is missing, an object with no answer for
// its generation is Pending with Progressing, and the answers of its target's calls write the rest.
func (r *Reconciler) settingsObject(ctx context.Context, a *v1.Addon, st *v1.AddonSettings, o *v1.AddonObject) (controller.Result, error) {
	reason, msg, err := r.settingsWait(ctx, a, st, o)
	if err != nil {
		return controller.Result{}, err
	}
	if reason == "" {
		if c, ok := o.Status.Conditions.Get(condReady); ok && c.ObservedGeneration == o.Generation && !waiting(c.Reason) {
			return controller.Result{}, nil
		}
		reason, msg = reasonProgressing, fmt.Sprintf("waiting for the answer of the Function for generation %d", o.Generation)
	}
	c, ok := o.Status.Conditions.Get(condReady)
	if ok && o.Status.Phase == v1.PhasePending && c.Status == v1.ConditionFalse && c.Reason == reason && c.Message == msg &&
		c.ObservedGeneration == o.Generation && o.Status.ObservedGeneration == o.Generation {
		return controller.Result{}, nil
	}
	o.Status.Phase, o.Status.ObservedGeneration = v1.PhasePending, o.Generation
	o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg,
		ObservedGeneration: o.Generation})
	if _, err := r.store.Update(ctx, o); err != nil {
		return controller.Result{}, retryOnConflict(err, "services.addon.settingsObject")
	}
	return controller.Result{}, nil
}

// settingsWait names the first piece a settings object's calls miss, in Decision 4's order after its Addon, and why;
// "" when none is missing: its target, a Ready target, the Function that applies it, and that Function's binding of the
// target.
func (r *Reconciler) settingsWait(ctx context.Context, a *v1.Addon, st *v1.AddonSettings, o *v1.AddonObject) (string, string, error) {
	name, ok := TargetOf(st, o)
	if !ok {
		return reasonTargetNotFound, st.Target + " names no target", nil
	}
	obj, err := r.store.Get(ctx, v1.KindAddonObject.GVK(), o.Namespace, name)
	t, isObj := obj.(*v1.AddonObject)
	switch {
	case fault.KindOf(err) == fault.NotFound || (err == nil && (!isObj || !v1.SameAddonKind(t.Spec.Kind, a.Spec.Kind()))):
		return reasonTargetNotFound, fmt.Sprintf("the namespace holds no %s %q", a.Spec.Kind(), name), nil
	case err != nil:
		return "", "", fault.Wrapf(err, fault.KindOf(err), "services.addon.settingsWait", "get the target %s/%s", o.Namespace, name)
	case t.Status.Phase != v1.PhaseReady:
		return reasonTargetNotReady, fmt.Sprintf("%s %q is not Ready", a.Spec.Kind(), name), nil
	}
	fnName := functionFor(st, t)
	fn, err := r.function(ctx, o.Namespace, fnName)
	switch {
	case err != nil:
		return "", "", err
	case fn == nil:
		return reasonFunctionNotFound, fmt.Sprintf("the namespace holds no Function %q, which applies the %s objects of %s %q",
			fnName, st.Kind, a.Spec.Kind(), name), nil
	}
	if _, bound := aliasOf(fn, name); !bound {
		return reasonFunctionNotBound, fmt.Sprintf("the Function %q does not bind %s %q in spec.bindings.addon", fnName,
			a.Spec.Kind(), name), nil
	}
	return "", "", nil
}

// MapSettings maps a settings object's change, its deletion included, to its target, whose pass sends the new list, and
// a target's change to its settings objects, whose passes follow it (Decision 4). The target field is immutable, so an
// object names the same target before and after a change.
func (r *Reconciler) MapSettings(ctx context.Context, obj v1.Object) []controller.Request {
	o, ok := obj.(*v1.AddonObject)
	if !ok {
		return nil
	}
	addons, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the Addons a settings change affects", "error", err)
		return nil
	}
	var owner *v1.Addon
	for _, item := range addons.Items {
		a, ok := item.(*v1.Addon)
		if !ok {
			continue
		}
		if st := a.Spec.SettingsKind(o.Spec.Kind); st != nil {
			if t, ok := TargetOf(st, o); ok {
				return []controller.Request{{GVK: v1.KindAddonObject.GVK(), Namespace: o.Namespace, Name: t}}
			}
			return nil
		}
		if v1.SameAddonKind(a.Spec.Kind(), o.Spec.Kind) {
			owner = a
		}
	}
	if owner == nil || len(owner.Spec.KindAttributes().Settings) == 0 {
		return nil
	}
	return r.objects(ctx, o.Namespace, func(so *v1.AddonObject) bool {
		st := owner.Spec.SettingsKind(so.Spec.Kind)
		if st == nil {
			return false
		}
		t, ok := TargetOf(st, so)
		return ok && t == o.Name
	})
}

// MapFunction maps a Function's change to the settings objects it applies and to their targets, and to the targets
// that name it in spec.functions: a Function created or deleted ends or starts a wait, and a new revision or binding
// changes the next call's digest (Decision 4).
func (r *Reconciler) MapFunction(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	addons, err := r.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the Addons a Function's change affects", "error", err)
		return nil
	}
	var withSettings []*v1.Addon
	for _, item := range addons.Items {
		if a, ok := item.(*v1.Addon); ok && len(a.Spec.KindAttributes().Settings) > 0 {
			withSettings = append(withSettings, a)
		}
	}
	if len(withSettings) == 0 {
		return nil
	}
	list, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: m.Namespace})
	if err != nil {
		r.logger.WarnContext(ctx, "could not list the add-on objects a Function's change affects", "error", err)
		return nil
	}
	targets := map[v1.ObjectName]*v1.AddonObject{}
	for _, item := range list.Items {
		if o, ok := item.(*v1.AddonObject); ok {
			targets[o.Name] = o
		}
	}
	seen := map[v1.ObjectName]bool{}
	var reqs []controller.Request
	add := func(name v1.ObjectName) {
		if !seen[name] {
			seen[name] = true
			reqs = append(reqs, controller.Request{GVK: v1.KindAddonObject.GVK(), Namespace: m.Namespace, Name: name})
		}
	}
	for _, o := range targets {
		for _, a := range withSettings {
			if v1.SameAddonKind(a.Spec.Kind(), o.Spec.Kind) {
				for i := range a.Spec.KindAttributes().Settings {
					if functionFor(&a.Spec.KindAttributes().Settings[i], o) == m.Name {
						add(o.Name)
					}
				}
			}
			st := a.Spec.SettingsKind(o.Spec.Kind)
			if st == nil {
				continue
			}
			name, ok := TargetOf(st, o)
			fn := st.Function.Ref
			if t := targets[name]; ok && t != nil {
				fn = functionFor(st, t)
			}
			if fn == m.Name {
				add(o.Name)
				if ok {
					add(name)
				}
			}
		}
	}
	return reqs
}
