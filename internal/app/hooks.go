package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/eventing"
)

// The hook points, the events of the hook input and the reason of a failed hook (ADR-0214).
const (
	pointPreApply  = "preApply"
	pointPostApply = "postApply"

	eventInstall  = "install"
	eventUpgrade  = "upgrade"
	eventRollback = "rollback"

	reasonHookFailed = "HookFailed"
)

// Invoker calls a Function with a CloudEvent, waking a cold one (ADR-0033); *sensor.HTTPInvoker satisfies it.
type Invoker interface {
	Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error
}

func hooksOf(s v1.AppSpec, point string) []v1.AppHook {
	switch {
	case s.Hooks == nil:
		return nil
	case point == pointPreApply:
		return s.Hooks.PreApply
	}
	return s.Hooks.PostApply
}

func hasHooks(s v1.AppSpec) bool {
	return len(hooksOf(s, pointPreApply))+len(hooksOf(s, pointPostApply)) > 0
}

// hookInput is the input of a new revision's hook calls (Decision 2), nil when its spec has no hook: from is the App's
// current revision and its version, empty on an install; the event is install without from, rollback when the spec
// (want, as the stamp marshals it) equals that of a retained Ready revision, else upgrade.
func hookInput(a *v1.App, to v1.ObjectName, declared v1.AppSpec, want []byte, revs []*v1.AppRevision) (*v1.AppHookInput, error) {
	if !hasHooks(declared) {
		return nil, nil
	}
	in := &v1.AppHookInput{Event: eventUpgrade, App: a.Name, To: to, ToVersion: declared.Version}
	if a.Status.CurrentRevision == "" {
		in.Event = eventInstall
		return in, nil
	}
	in.From, in.FromVersion = a.Status.CurrentRevision, a.Status.Version
	for _, rev := range revs {
		if rev.Status.Phase != v1.PhaseReady {
			continue
		}
		have, err := json.Marshal(rev.Spec.Spec)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "marshal the spec of app revision %s", rev.Name)
		}
		if bytes.Equal(want, have) {
			in.Event = eventRollback
			break
		}
	}
	return in, nil
}

// pendingHook is the first entry of a point that is not done (Decision 3): failed when its last call is Failed, else
// due. last is its last call, nil when it has none.
type pendingHook struct {
	fn     v1.ObjectName
	failed bool
	last   *v1.AppHookCall
}

// firstPending is rev's first entry of point that has no Ready call, nil when every entry has one.
func firstPending(rev *v1.AppRevision, point string) *pendingHook {
	for _, h := range hooksOf(rev.Spec.Spec, point) {
		p := &pendingHook{fn: h.Function}
		done := false
		for i := range rev.Status.Hooks {
			if c := &rev.Status.Hooks[i]; c.Point == point && c.Function == h.Function {
				p.last, done = c, done || c.Phase == v1.PhaseReady
			}
		}
		if !done {
			p.failed = p.last != nil && p.last.Phase == v1.PhaseFailed
			return p
		}
	}
	return nil
}

func preHookMessage(p *pendingHook) string { return "pre-hook Function/" + string(p.fn) }

// hookFailedOn reports whether rev's Applied condition is HookFailed: a failed pre-hook, which retry reopens.
func hookFailedOn(rev *v1.AppRevision) bool {
	c, ok := rev.Status.Conditions.Get(condApplied)
	return ok && c.Reason == reasonHookFailed
}

// lastPreApply is the endTime of rev's last recorded preApply call, a start term of the deadline (Decision 4), the
// zero time when there is none.
func lastPreApply(rev *v1.AppRevision) time.Time {
	for i := len(rev.Status.Hooks) - 1; i >= 0; i-- {
		if c := rev.Status.Hooks[i]; c.Point == pointPreApply {
			return time.Time(c.EndTime)
		}
	}
	return time.Time{}
}

// preHookParts are the entries a pass writes while the latest revision's pre-hooks are not done (Decision 4): in
// section order, the configMaps a pre-hook Function's spec.config names and the kv, buckets and catalogs entries it
// binds, with the configMaps and buckets of such a catalog, then the pre-hook Functions. A ref entry is never written.
func preHookParts(a *v1.App, ents []entry) []entry {
	parts := make(map[v1.ObjectRef]v1.Object)
	for _, p := range a.Parts() {
		parts[keyOf(p)] = p
	}
	need := make(map[v1.ObjectRef]bool)
	add := func(kind v1.Kind, name v1.ObjectName) {
		need[v1.ObjectRef{Kind: kind, Namespace: a.Namespace, Name: name}] = true
	}
	hook := make(map[v1.ObjectName]bool)
	for _, h := range hooksOf(a.Spec, pointPreApply) {
		hook[h.Function] = true
		fn, ok := parts[v1.ObjectRef{Kind: v1.KindFunction, Namespace: a.Namespace, Name: h.Function}].(*v1.Function)
		if !ok {
			continue
		}
		for _, c := range fn.Spec.Config {
			add(v1.KindConfigMap, c)
		}
		for _, b := range fn.Spec.KV {
			add(v1.KindKVStore, b.Store)
		}
		for _, b := range fn.Spec.Blob {
			add(v1.KindBucket, b.Bucket)
		}
		for _, b := range fn.Spec.Catalogs {
			add(v1.KindCatalogService, b.Catalog)
			cs, ok := parts[v1.ObjectRef{Kind: v1.KindCatalogService, Namespace: a.Namespace, Name: b.Catalog}].(*v1.CatalogService)
			if !ok {
				continue
			}
			for _, c := range cs.Spec.Config {
				add(v1.KindConfigMap, c)
			}
			for _, cb := range cs.Spec.Blob {
				add(v1.KindBucket, cb.Bucket)
			}
			add(v1.KindBucket, cs.Spec.Catalog.Bucket)
		}
	}
	var out, fns []entry
	for _, e := range ents {
		switch {
		case e.ref:
		case e.kind == v1.KindFunction:
			if hook[e.name] {
				fns = append(fns, e)
			}
		case need[e.key(a.Namespace)]:
			out = append(out, e)
		}
	}
	return append(out, fns...)
}

// hookPass is what the hooks mean for one pass (Decision 4).
type hookPass struct {
	busy    bool         // the runner held a call of this App when the pass began
	started bool         // the pass started a call
	pre     *pendingHook // the latest revision's first pre-hook not done, while it is not current
	ready   bool         // pre's Function is Ready or NotStarted
	post    *pendingHook // the first post-hook not done of the latest revision, current when the pass began
	failure string       // the HookFailed message of a failed pre or post entry
}

// hooks reads the latest revision's hook state and starts its first due call when Decision 4 allows: a pre-hook of a
// Deploying latest that is not current, in a pass not stopped that wrote no part and is not busy, whose Function is
// Ready or NotStarted; a post-hook once the stored App status names the latest current (current), in a pass that is
// not busy.
func (r *Reconciler) hooks(ctx context.Context, a *v1.App, latest *v1.AppRevision, current bool, objs map[v1.ObjectRef]v1.Object,
	halt *stop, wrote *v1.ObjectRef, busy bool) (hookPass, error) {
	hp := hookPass{busy: busy}
	if latest == nil {
		return hp, nil
	}
	point := pointPreApply
	if current {
		point = pointPostApply
	}
	p := firstPending(latest, point)
	if current {
		hp.post = p
	} else {
		hp.pre = p
	}
	switch {
	case p == nil:
		return hp, nil
	case p.failed:
		var err error
		hp.failure, err = r.hookFailure(ctx, a.Namespace, p)
		return hp, err
	case current:
		hp.started = !busy && r.startCall(ctx, a, latest, point, p.fn)
		return hp, nil
	}
	if fn := objs[v1.ObjectRef{Kind: v1.KindFunction, Namespace: a.Namespace, Name: p.fn}]; fn != nil {
		state := judge(fn).child.State
		hp.ready = state == v1.AppChildReady || state == v1.AppChildNotStarted
	}
	if latest.Status.Phase == v1.PhaseDeploying && halt == nil && wrote == nil && !busy && hp.ready {
		hp.started = r.startCall(ctx, a, latest, point, p.fn)
	}
	return hp, nil
}

// hookFailure is the HookFailed message of a failed entry: Function/<fn>: Invocation/<inv>: <error>, the error read
// from the Invocation and left out when it has none or is gone.
func (r *Reconciler) hookFailure(ctx context.Context, ns v1.NamespaceName, p *pendingHook) (string, error) {
	msg := fmt.Sprintf("Function/%s: Invocation/%s", p.fn, p.last.Invocation)
	obj, err := r.get(ctx, v1.KindInvocation, ns, p.last.Invocation)
	if err != nil {
		return "", err
	}
	if inv, ok := obj.(*v1.Invocation); ok && inv.Status.Error != "" {
		msg += ": " + inv.Status.Error
	}
	return msg, nil
}

// failHook makes rev Failed with Applied and Current False HookFailed (Decision 4).
func (r *Reconciler) failHook(rev *v1.AppRevision, msg string) {
	rev.Status.Phase = v1.PhaseFailed
	for _, ct := range []v1.ConditionType{condApplied, condCurrent} {
		r.setCondition(&rev.Status.Conditions, v1.Condition{Type: ct, Status: v1.ConditionFalse, Reason: reasonHookFailed, Message: msg})
	}
}

// calls is the hook runner's ledger (Decision 6): a pass or a Retry reserves an App before its call starts, and the
// call releases it once recorded or dropped, so at most one call per App runs. base is the context of the controller's
// passes, which funcd's shutdown ends; every call runs under it, a Retry's too.
type calls struct {
	mu   sync.Mutex
	held map[v1.ObjectRef]bool
	base context.Context
}

func (c *calls) bind(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.base = ctx
}

func (c *calls) context() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base
}

func (c *calls) busy(k v1.ObjectRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.held[k]
}

// reserve holds k and reports true, or reports false when k is held already.
func (c *calls) reserve(k v1.ObjectRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[k] {
		return false
	}
	c.held[k] = true
	return true
}

func (c *calls) release(k v1.ObjectRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.held, k)
}

func appKey(ns v1.NamespaceName, name v1.ObjectName) v1.ObjectRef {
	return v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: name}
}

// startCall reserves the App and starts rev's call of fn at point under base; false when a call of the App holds the
// runner already.
func (r *Reconciler) startCall(base context.Context, a *v1.App, rev *v1.AppRevision, point string, fn v1.ObjectName) bool {
	if !r.calls.reserve(appKey(a.Namespace, a.Name)) {
		return false
	}
	r.run(base, a.Namespace, a.Name, rev, point, fn)
	return true
}

// call is one hook call the runner records.
type call struct {
	ns         v1.NamespaceName
	app, rev   v1.ObjectName
	point      string
	fn, inv    v1.ObjectName
	start, end time.Time
	err        error
}

// run calls fn in a goroutine (Decision 5), bounded by its spec.timeout or InvokeTimeout, records the call unless
// funcd's shutdown ended it (Decision 6), then releases the App, which must be reserved, and requeues it.
func (r *Reconciler) run(base context.Context, ns v1.NamespaceName, app v1.ObjectName, rev *v1.AppRevision, point string, fn v1.ObjectName) {
	c := call{ns: ns, app: app, rev: rev.Name, point: point, fn: fn, inv: v1.ObjectName("inv-" + randHex(10))}
	data, _ := json.Marshal(rev.Spec.HookInput) // a struct of strings always marshals
	ev := eventing.CloudEvent{
		SpecVersion:     "1.0",
		ID:              string(c.inv),
		Source:          fmt.Sprintf("funcd://%s/app/%s", ns, app),
		Type:            point,
		Time:            v1.NewTimestamp(r.clock.Now()),
		DataContentType: "application/json",
		Data:            data,
	}
	timeout := r.invokeTimeout
	for _, f := range rev.Spec.Spec.Functions {
		if f.Name == fn && f.Timeout > 0 {
			timeout = time.Duration(f.Timeout)
		}
	}
	go func() {
		defer func() {
			r.calls.release(appKey(ns, app))
			if r.enqueue != nil {
				r.enqueue(controller.Request{GVK: v1.KindApp.GVK(), Namespace: ns, Name: app})
			}
		}()
		c.start = r.clock.Now()
		ctx, cancel := context.WithTimeout(base, timeout)
		c.err = r.invoke(ctx, ns, fn, ev)
		cancel()
		c.end = r.clock.Now()
		if base.Err() == nil {
			r.recordCall(base, c)
		}
	}()
}

func (r *Reconciler) invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error {
	if r.invoker == nil {
		return fault.Unavailablef(op, "no invoker")
	}
	return r.invoker.Invoke(ctx, ns, fn, ev)
}

// recordCall records an ended call (Decision 6): the Invocation, with the AppRevision's controller reference, then
// the AppHookCall appended to the revision's status at its resourceVersion, read again on a Conflict. A Ready pre-hook
// call reopens a revision Failed with HookFailed (Decision 4). Nothing is recorded while funcd is held or the App
// paused (Decision 8), when the revision is gone or no longer the App's latest, or when a store write fails.
func (r *Reconciler) recordCall(ctx context.Context, c call) {
	if r.held() {
		return
	}
	obj, err := r.get(ctx, v1.KindApp, c.ns, c.app)
	a, ok := obj.(*v1.App)
	if err != nil || !ok || a.Spec.Paused {
		return
	}
	revs, err := r.revisions(ctx, a)
	if err != nil || len(revs) == 0 || revs[len(revs)-1].Name != c.rev {
		return
	}
	rev := revs[len(revs)-1]
	hc := v1.AppHookCall{Point: c.point, Function: c.fn, Invocation: c.inv, Phase: v1.PhaseReady, EndTime: v1.NewTimestamp(c.end)}
	if c.err != nil {
		hc.Phase = v1.PhaseFailed
	}
	if err := r.createInvocation(ctx, rev, c, hc.Phase); err != nil {
		r.log.WarnContext(ctx, "record hook call", "namespace", string(c.ns), "app", string(c.app), "invocation", string(c.inv), "error", err)
		return
	}
	for {
		rev.Status.Hooks = append(rev.Status.Hooks, hc)
		if hc.Point == pointPreApply && hc.Phase == v1.PhaseReady && rev.Status.Phase == v1.PhaseFailed && hookFailedOn(rev) {
			rev.Status.Phase = v1.PhaseDeploying
		}
		_, err := r.store.Update(ctx, rev)
		if fault.KindOf(err) != fault.Conflict {
			if err != nil {
				r.log.WarnContext(ctx, "record hook call", "namespace", string(c.ns), "revision", string(c.rev), "error", err)
			}
			return
		}
		obj, err := r.get(ctx, v1.KindAppRevision, c.ns, c.rev)
		if rev, ok = obj.(*v1.AppRevision); err != nil || !ok || !v1.ControlledBy(rev.OwnerReferences, v1.KindApp, a.UID) {
			return
		}
	}
}

// createInvocation stores the call's Invocation, as the Sensor records an action, owned by rev (ADR-0170).
func (r *Reconciler) createInvocation(ctx context.Context, rev *v1.AppRevision, c call, phase v1.Phase) error {
	obj, _ := v1.NewObject(v1.KindInvocation)
	inv := obj.(*v1.Invocation)
	inv.ObjectMeta = v1.ObjectMeta{Name: c.inv, Namespace: c.ns, ResourceGroup: rev.ResourceGroup, OwnerReferences: []v1.OwnerReference{{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindAppRevision, Namespace: rev.Namespace, Name: rev.Name},
		UID:                rev.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}}}
	inv.Status.Phase, inv.Status.StartTime, inv.Status.EndTime = phase, v1.NewTimestamp(c.start), v1.NewTimestamp(c.end)
	if c.err != nil {
		inv.Status.Error = c.err.Error()
	}
	_, err := r.store.Create(ctx, inv)
	return err
}

func (r *Reconciler) held() bool { return r.hold != nil && r.hold.Held() }

// Retry starts again, without waiting, the first pre-hook not done of the App's latest revision when it is Failed
// with HookFailed, or the first failed post-hook of its current revision when that is the latest (Decision 7). It
// reserves the App before it reads the revisions, as a pass notes busy. Conflict when no such hook exists, a call
// of the App runs or the App is paused; Unavailable while funcd is held; NotFound without the App.
func (r *Reconciler) Retry(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	const op = "app.Retry"
	if r.held() {
		return fault.Unavailablef(op, "funcd is held: app %s calls no hook", name)
	}
	k := appKey(ns, name)
	if !r.calls.reserve(k) {
		return fault.Conflictf(op, "a hook call of app %s is running", name)
	}
	started := false
	defer func() {
		if !started {
			r.calls.release(k)
		}
	}()
	obj, err := r.store.Get(ctx, v1.KindApp.GVK(), ns, name)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "get app %s/%s", ns, name)
	}
	a := obj.(*v1.App)
	if a.Spec.Paused {
		return fault.Conflictf(op, "app %s is paused", name)
	}
	revs, err := r.revisions(ctx, a)
	if err != nil {
		return err
	}
	var p *pendingHook
	point := pointPreApply
	if n := len(revs); n > 0 {
		switch latest := revs[n-1]; {
		case latest.Name != a.Status.CurrentRevision && latest.Status.Phase == v1.PhaseFailed && hookFailedOn(latest):
			p = firstPending(latest, pointPreApply)
		case latest.Name == a.Status.CurrentRevision:
			if p, point = firstPending(latest, pointPostApply), pointPostApply; p != nil && !p.failed {
				p = nil
			}
		}
	}
	if p == nil {
		return fault.Conflictf(op, "app %s has no failed hook", name)
	}
	base := r.calls.context()
	if base == nil {
		return fault.Unavailablef(op, "the App reconciler has not run yet")
	}
	r.run(base, ns, name, revs[len(revs)-1], point, p.fn)
	started = true
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24)
	return hex.EncodeToString(b)
}
