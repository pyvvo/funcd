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
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// A settings kind's Function applies a target's settings, called from the target's pass (ADR-0341): funcd computes
// each target's list and writes each object's status, and the Function, which alone knows the engine's API, applies
// the list through its binding of the target (ADR-0338). The kind is read whole from the revision the target pins, and
// its Function may come from its image (ADR-0358).

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
	case reasonAddonNotFound, reasonTargetNotFound, reasonTargetNotReady, reasonFunctionNotFound, reasonFunctionNotBound,
		reasonArtifactNotReady, reasonFunctionNotOwned:
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
	gone     bool // its object went while its call ran
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

// forget drops each state that match names, under settingsMu. One whose call runs stays until that call ends, so that an
// object created again under the name waits for it: one call at most runs for a key.
func forget[K comparable](states map[K]*settingsState, match func(K) bool) {
	for k, s := range states {
		switch {
		case !match(k):
		case s.inflight:
			s.gone = true
		default:
			delete(states, k)
		}
	}
}

// endLocked records the end of key's call, under settingsMu, or drops the state that forget left for it.
func endLocked[K comparable](states map[K]*settingsState, key K, now time.Time, digest string, resync time.Duration, err error) {
	s, ok := states[key]
	switch {
	case !ok:
	case s.gone:
		delete(states, key)
	default:
		s.done(now, digest, resync, err)
	}
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

// forgetTarget drops what this process knows of a deleted object's calls, those of the Function that funcd created for
// an engineless one included.
func (r *Reconciler) forgetTarget(ns v1.NamespaceName, name v1.ObjectName) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	delete(r.targets, provider.ProviderRef{Namespace: ns, Name: name})
	forget(r.settings, func(k settingsKey) bool { return k.ns == ns && k.target == name })
	forget(r.groups, func(k groupKey) bool { return k.ns == ns && k.fn == v1.AddonFunctionName(name, k.kind) })
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

// settled reports whether the last call that ended for key sent digest and ended well, with none in flight.
func (r *Reconciler) settled(key settingsKey, digest string) bool {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	s, ok := r.settings[key]
	return ok && !s.inflight && s.failures == 0 && s.sent == digest
}

// forgetCalls drops what this process knows of key's calls.
func (r *Reconciler) forgetCalls(key settingsKey) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	delete(r.settings, key)
}

// settingsFunction is the Function that gets o's objects of kind st, the alias under which it binds o, and whether funcd
// creates it. It is nil while there is none to call: the ref's is missing or does not bind o, or the image's is not read
// yet, is another's, or is not needed, since no object names o (ADR-0358 Decision 2).
func (r *Reconciler) settingsFunction(ctx context.Context, rev *v1.AddonRevision, st *v1.AddonSettings, o *v1.AddonObject,
	needed bool) (*v1.Function, string, bool, error) {
	name, created := functionOf(st.Function, st.Kind, o)
	if !created {
		fn, err := r.function(ctx, o.Namespace, name)
		if err != nil || fn == nil {
			return nil, "", false, err
		}
		alias, bound := aliasOf(fn, o.Name)
		if !bound {
			return nil, "", false, nil
		}
		return fn, alias, false, nil
	}
	if needed {
		if reason, _, err := r.ensureFunction(ctx, rev, o, st.Kind, st.Function.Image); err != nil || reason != "" {
			return nil, "", true, err
		}
	}
	fn, err := r.ownedFunction(ctx, o, name)
	return fn, selfAlias, true, err
}

// syncSettings is the settings step of a Ready target's pass (Decision 1). For each settings kind that both the Addon
// and rev, the revision the target pins, declare, read from rev (ADR-0358 Decision 5), it starts a call with the target's
// list when one is due and none runs; with no Function to call, nothing is called and the objects wait. The call runs on
// a goroutine of its own, so no pass waits on a Function (Decision 2). The Function of an image is there while an object
// of its kind names the target, then until the empty list that the last one left is applied (ADR-0358 Decision 2). It
// returns how soon the next call is due, 0 for none due before the supervision period.
func (r *Reconciler) syncSettings(ctx context.Context, a *v1.Addon, rev *v1.AddonRevision, o *v1.AddonObject) (time.Duration, error) {
	const op = "services.addon.syncSettings"
	epoch := r.seeTarget(o.Namespace, o.Name, true)
	if a == nil {
		return 0, nil
	}
	kinds, keep := rev.Spec.Spec.KindAttributes().Settings, map[v1.ObjectName]bool{}
	var items []v1.Object
	if len(kinds) > 0 {
		list, err := r.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: o.Namespace})
		if err != nil {
			return 0, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects of %s", o.Namespace)
		}
		items = list.Items
	}
	var next time.Duration
	for i := range kinds {
		st := &kinds[i]
		if a.Spec.SettingsKind(st.Kind) == nil {
			continue
		}
		objs := targetObjects(items, st, o.Name)
		fn, alias, created, err := r.settingsFunction(ctx, rev, st, o, len(objs) > 0)
		if err != nil {
			return 0, err
		}
		if created {
			keep[v1.AddonFunctionName(o.Name, st.Kind)] = true
		}
		if fn == nil {
			continue
		}
		key := settingsKey{o.Namespace, o.Name, st.Kind}
		digest := settingsDigest(epoch, o.Status.Address, fn, alias, objs)
		if created && len(objs) == 0 && r.settled(key, digest) {
			r.forgetCalls(key)
			if err := r.deleteFunction(ctx, fn); err != nil {
				return 0, err
			}
			continue
		}
		if created && !checked(fn) {
			continue
		}
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
	return next, r.pruneFunctions(ctx, o, keep)
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
// Function, the revision that answers and the alias that binds the target, and each object's name, uid and generation.
// The Function's uid tells a Function created again from the one it replaced.
func settingsDigest(epoch int, address string, fn *v1.Function, alias string, objs []*v1.AddonObject) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%s|%s|%s", epoch, address, fn.Name, fn.UID, answering(fn), alias)
	for _, o := range objs {
		fmt.Fprintf(&b, "|%s:%s:%d", o.Name, o.UID, o.Generation)
	}
	return b.String()
}

// answering is the revision of fn that a call reaches: the one whose workers take the calls, or while it sleeps the latest
// one, which a call wakes. A new revision's workers come up beside the serving ones (ADR-0143), so a list sent when it is
// stamped would be answered by the code it replaces, and nothing would send it again before the resync.
func answering(fn *v1.Function) string {
	return cmp.Or(fn.Status.ServingRevision, fn.Status.CurrentRevision)
}

// checked reports whether a call to fn, which funcd creates, meets the contract of the code that answers it (ADR-0358
// Decision 5): a revision of it serves, or the one that a call wakes is stamped from its spec. Before that stamp, whose
// event brings the next pass, a call is checked against no contract, or against that of the image it replaces.
func checked(fn *v1.Function) bool {
	return fn.Status.ServingRevision != "" || fn.Status.CurrentRevision == string(v1.FunctionRevisionName(fn.Name, fn.Generation))
}

// settingsDue names the cause of a call that is due for key now, the first of start, change, retry and resync that
// holds, and marks it in flight; else how long until one is due, 0 while a call runs, whose end requeues the target.
func (r *Reconciler) settingsDue(key settingsKey, digest string, resync time.Duration) (string, time.Duration) {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	return dueLocked(r.settings, r.caller != nil, key, r.now(), digest, resync)
}

// dueLocked is due for key's state in states, under settingsMu, a new key's state made unknown: nothing is due without
// a caller (calls false).
func dueLocked[K comparable](states map[K]*settingsState, calls bool, key K, now time.Time, digest string,
	resync time.Duration) (string, time.Duration) {
	if !calls {
		return "", 0
	}
	s, known := states[key]
	if !known {
		s = &settingsState{}
		states[key] = s
	}
	return s.due(known, now, digest, resync)
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
	endLocked(r.settings, key, r.now(), digest, resync, err)
}

// settingsObject is the pass of a settings object, which runs no engine (Decision 4). While its calls miss something it
// is Pending with the first missing piece, even after an answer. Once nothing is missing, an object with no answer for
// its generation is Pending with Progressing, and the answers of its target's calls write the rest. An App's part records
// the App's revision it follows, as a part that runs an engine does (ADR-0339 Decision 3): its App waits for that.
func (r *Reconciler) settingsObject(ctx context.Context, a *v1.Addon, st *v1.AddonSettings, o *v1.AddonObject) (controller.Result, error) {
	const op = "services.addon.settingsObject"
	moved := false
	if app, part := partOf(o); part {
		latest, err := r.latestApplied(ctx, o, app)
		if err != nil {
			return controller.Result{}, err
		}
		if latest != nil && latest.Spec.Number > revisionNumber(app.Name, o.Status.AppRevision) {
			o.Status.AppRevision, moved = latest.Name, true
		}
	}
	reason, msg, err := r.settingsWait(ctx, a, st, o)
	if err != nil {
		return controller.Result{}, err
	}
	c, ok := o.Status.Conditions.Get(condReady)
	if reason == "" {
		if ok && c.ObservedGeneration == o.Generation && !waiting(c.Reason) {
			if !moved {
				return controller.Result{}, nil
			}
			if _, err := r.store.Update(ctx, o); err != nil {
				return controller.Result{}, retryOnConflict(err, op)
			}
			return controller.Result{}, nil
		}
		reason, msg = reasonProgressing, fmt.Sprintf("waiting for the answer of the Function for generation %d", o.Generation)
	}
	if !moved && ok && o.Status.Phase == v1.PhasePending && c.Status == v1.ConditionFalse && c.Reason == reason && c.Message == msg &&
		c.ObservedGeneration == o.Generation && o.Status.ObservedGeneration == o.Generation {
		return controller.Result{}, nil
	}
	o.Status.Phase, o.Status.ObservedGeneration = v1.PhasePending, o.Generation
	o.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg,
		ObservedGeneration: o.Generation})
	if _, err := r.store.Update(ctx, o); err != nil {
		return controller.Result{}, retryOnConflict(err, op)
	}
	return controller.Result{}, nil
}

// settingsWait names the first piece a settings object's calls miss, in Decision 4's order after its Addon, and why;
// "" when none is missing: the Addon's first revision, its target, a Ready target, and the Function that applies it,
// as the revision the target pins names it (ADR-0358 Decisions 2 and 5): its kind, its image read and a Function of the
// image's that is its own, or a Function that is there and binds the target.
func (r *Reconciler) settingsWait(ctx context.Context, a *v1.Addon, st *v1.AddonSettings, o *v1.AddonObject) (string, string, error) {
	if a.Status.CurrentRevision == "" {
		return reasonProgressing, fmt.Sprintf("Addon %s has no revision yet", a.Name), nil
	}
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
	rev, err := r.revision(ctx, cmp.Or(t.Status.AddonRevision, a.Status.CurrentRevision))
	if err != nil {
		return "", "", err
	}
	var pinned *v1.AddonSettings
	if rev != nil {
		pinned = rev.Spec.Spec.SettingsKind(st.Kind)
	}
	if pinned == nil {
		return reasonFunctionNotFound, fmt.Sprintf("the revision %s that %s %q pins declares no kind %s: an edit of it moves it to the current one",
			t.Status.AddonRevision, a.Spec.Kind(), name, st.Kind), nil
	}
	fnName, created := functionOf(pinned.Function, pinned.Kind, t)
	if created && rev.ContractOf(pinned.Kind) == nil {
		return reasonArtifactNotReady, imageUnread(rev, pinned.Kind, pinned.Function.Image), nil
	}
	fn, err := r.function(ctx, o.Namespace, fnName)
	switch {
	case err != nil:
		return "", "", err
	case created && fn != nil && !ownedstore.Marked(fn.OwnerReferences, t):
		return reasonFunctionNotOwned, fmt.Sprintf("the Function %q is not the one that funcd creates for %s %q", fnName, a.Spec.Kind(), name), nil
	case created:
		return "", "", nil // the target's pass creates it
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
// that name it in spec.functions or whose image it comes from: a Function created or deleted ends or starts a wait, and a
// new revision or binding changes the next call's digest (Decision 4). A kind is read as its Addon is now and as the
// target's revision holds it, which its next pass may move to.
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
	revs := map[v1.ObjectName]*v1.AddonRevision{}
	// applies reports whether m is the Function of target's objects of kind, or of any settings kind when kind is "".
	applies := func(a *v1.Addon, target *v1.AddonObject, kind string) bool {
		kinds := slices.Clone(a.Spec.KindAttributes().Settings)
		if rev := r.cachedRevision(ctx, revs, target.Status.AddonRevision); rev != nil {
			kinds = append(kinds, rev.Spec.Spec.KindAttributes().Settings...)
		}
		return slices.ContainsFunc(kinds, func(st v1.AddonSettings) bool {
			name, _ := functionOf(st.Function, st.Kind, target)
			return name == m.Name && (kind == "" || v1.SameAddonKind(st.Kind, kind))
		})
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
			if v1.SameAddonKind(a.Spec.Kind(), o.Spec.Kind) && applies(a, o, "") {
				add(o.Name)
			}
			st := a.Spec.SettingsKind(o.Spec.Kind)
			if st == nil {
				continue
			}
			name, ok := TargetOf(st, o)
			match := st.Function.Ref == m.Name
			if t := targets[name]; ok && t != nil {
				match = applies(a, t, st.Kind)
			}
			if match {
				add(o.Name)
				if ok {
					add(name)
				}
			}
		}
	}
	return reqs
}

// cachedRevision is the AddonRevision name, read once into revs; nil when it is not there or could not be read.
func (r *Reconciler) cachedRevision(ctx context.Context, revs map[v1.ObjectName]*v1.AddonRevision, name v1.ObjectName) *v1.AddonRevision {
	rev, seen := revs[name]
	if !seen {
		rev, _ = r.revision(ctx, name)
		revs[name] = rev
	}
	return rev
}
