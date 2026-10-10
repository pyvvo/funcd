package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
)

const (
	condReady         = v1.ConditionType("Ready")
	condShapeValid    = v1.ConditionType("ShapeValid")
	condRevisionReady = v1.ConditionType("RevisionReady")
)

// The App's Paused condition (ADR-0212 Decisions 4 and 5): True SpecPaused while spec.paused is set, False Resumed
// after a resume, absent on an App never paused.
const (
	condPaused    = v1.ConditionType("Paused")
	reasonPaused  = "SpecPaused"
	reasonResumed = "Resumed"
	messagePaused = "spec.paused is set: the App writes no part until it is resumed"
)

// The App's Ready reasons (ADR-0199 Decisions 4 and 5) and the Pruning reasons (ADR-0199 Decision 6).
const (
	reasonProgressing   = "Progressing"
	reasonChildNotReady = "ChildNotReady"
	reasonNotStarted    = "NotStarted"
	reasonChildNotOwned = "ChildNotOwned"
	reasonChildInvalid  = "ChildInvalid"
	reasonRefNotFound   = "RefNotFound"
	reasonInUse         = "InUse"
	reasonRunHeld       = "RunHeld"
)

// The stops of the Secret check, on the App's Ready and the AppRevision's Applied (ADR-0213 Decision 8).
const (
	reasonSecretNotFound   = "SecretNotFound"
	reasonSecretKeyMissing = "SecretKeyMissing"
)

// verdict is one declared entry's state: its child line, and for a Pending or NotStarted one the App's Ready
// reason it gives and the part's own message.
type verdict struct {
	child   v1.AppChild
	cause   string
	message string
}

func partName(k v1.ObjectRef) string { return fmt.Sprintf("%s/%s", k.Kind, k.Name) }

// readiness judges every declared entry in section order (Decision 5); the part a stopped pass names is Pending
// with the stop's reason.
func readiness(a *v1.App, ents []entry, objs map[v1.ObjectRef]v1.Object, halt *stop) []verdict {
	out := make([]verdict, 0, len(ents))
	for _, e := range ents {
		k := e.key(a.Namespace)
		var v verdict
		switch obj := objs[k]; {
		case halt != nil && halt.part == k:
			v = pending(halt.reason, halt.reason, "")
		case obj == nil && e.ref:
			v = pending(reasonRefNotFound, reasonRefNotFound, "no such object in the namespace")
		case obj == nil:
			v = pending(reasonProgressing, reasonProgressing, "not written yet")
		default:
			v = judge(obj)
		}
		v.child.Kind, v.child.Name = e.kind, e.name
		out = append(out, v)
	}
	return out
}

func pending(cause, reason, message string) verdict {
	return verdict{child: v1.AppChild{State: v1.AppChildPending, Reason: reason}, cause: cause, message: message}
}

// judge applies Decision 5 to an existing part: a Function by its own rule, a kind without status (a Bucket) is Ready
// once it exists, and any other part by its Ready condition at its generation.
func judge(obj v1.Object) verdict {
	if fn, ok := obj.(*v1.Function); ok {
		return judgeFunction(fn)
	}
	so, ok := obj.(v1.StatusObject)
	if !ok {
		return verdict{child: v1.AppChild{State: v1.AppChildReady}}
	}
	c, ok := so.GetStatus().Conditions.Get(condReady)
	switch {
	case !ok || c.ObservedGeneration != obj.GetObjectMeta().Generation:
		return pending(reasonProgressing, reasonProgressing, "")
	case c.Status == v1.ConditionTrue:
		return verdict{child: v1.AppChild{State: v1.AppChildReady}}
	}
	return pending(reasonChildNotReady, cmp.Or(c.Reason, reasonChildNotReady), c.Message)
}

// judgeFunction: a Function is Ready when its generation has served (ShapeValid True for it, ADR-0174) and its phase is
// Ready or Idle with RevisionReady True, or it is waking: Deploying with RevisionReady True (the activator writes only
// the phase) or False Progressing. It is NotStarted while RevisionReady is Unknown NotStarted for its generation; an
// idle Function reports Ready=False NoReplicas while it serves, so its Ready condition does not decide.
func judgeFunction(fn *v1.Function) verdict {
	gen, phase, conds := fn.Generation, fn.Status.Phase, fn.Status.Conditions
	rr, ok := conds.Get(condRevisionReady)
	if !ok || rr.ObservedGeneration != gen {
		return pending(reasonProgressing, reasonProgressing, "")
	}
	if rr.Status == v1.ConditionUnknown && rr.Reason == reasonNotStarted {
		return verdict{child: v1.AppChild{State: v1.AppChildNotStarted, Reason: reasonNotStarted}, cause: reasonNotStarted, message: rr.Message}
	}
	sv, _ := conds.Get(condShapeValid)
	served := sv.Status == v1.ConditionTrue && sv.ObservedGeneration == gen
	waking := rr.Status == v1.ConditionTrue || rr.Status == v1.ConditionFalse && rr.Reason == reasonProgressing
	if served && ((phase == v1.PhaseReady || phase == v1.PhaseIdle) && rr.Status == v1.ConditionTrue || phase == v1.PhaseDeploying && waking) {
		return verdict{child: v1.AppChild{State: v1.AppChildReady}}
	}
	reason, msg := rr.Reason, rr.Message
	if c, ok := conds.Get(condReady); ok && c.Status != v1.ConditionTrue {
		reason, msg = c.Reason, c.Message
	}
	cause := reasonChildNotReady
	if phase == v1.PhaseDeploying || phase == v1.PhasePending || phase == "" || reason == reasonProgressing {
		cause = reasonProgressing
	}
	return pending(cause, cmp.Or(reason, string(phase), reasonChildNotReady), msg)
}

func anyPending(vs []verdict) bool {
	for _, v := range vs {
		if v.child.State == v1.AppChildPending {
			return true
		}
	}
	return false
}

// readyCondition is the App's Ready (Decision 5): False with the stop's reason; else False naming the first Pending
// part; else Unknown NotStarted naming the first NotStarted Function; else True.
func readyCondition(vs []verdict, halt *stop) v1.Condition {
	c := v1.Condition{Type: condReady, Status: v1.ConditionTrue}
	if halt != nil {
		c.Status, c.Reason, c.Message = v1.ConditionFalse, halt.reason, halt.msg()
		return c
	}
	for _, state := range []v1.AppChildState{v1.AppChildPending, v1.AppChildNotStarted} {
		for _, v := range vs {
			if v.child.State != state {
				continue
			}
			c.Status = v1.ConditionFalse
			if state == v1.AppChildNotStarted {
				c.Status = v1.ConditionUnknown
			}
			c.Reason = v.cause
			c.Message = fmt.Sprintf("%s/%s: %s", v.child.Kind, v.child.Name, v.child.Reason)
			if v.message != "" {
				c.Message += ": " + v.message
			}
			return c
		}
	}
	return c
}

// appPhase sets the App's phase and returns its Ready condition (ADR-0200 Decision 6): Deploying while the latest
// revision is Deploying or none exists; Failed while it is Failed, with Ready=False HookFailed when a pre-hook failed
// it, else ChildNotReady, and its message unless the pass stopped; once it is current, ADR-0199 Decision 5: Ready once
// no part is Pending, which records the generation in status.observedGeneration, then Degraded while a part is Pending,
// a post-hook failed (HookFailed, ranked after a stop and before a part, ADR-0214 Decision 4) or a pass stops,
// whatever the generation: a pause or a resume bumps it without a stamp (ADR-0212 Decision 7). ChildNotReady is for
// Failed and Degraded only, so a Deploying App reports Progressing instead, naming the pre-hook not done yet.
func appPhase(a *v1.App, latest *v1.AppRevision, vs []verdict, halt *stop, hp hookPass) v1.Condition {
	ready := readyCondition(vs, halt)
	switch {
	case latest != nil && latest.Status.Phase == v1.PhaseFailed:
		a.Status.Phase = v1.PhaseFailed
		if halt == nil {
			c, _ := latest.Status.Conditions.Get(condChildrenReady)
			reason := reasonChildNotReady
			if hookFailedOn(latest) {
				c, _ = latest.Status.Conditions.Get(condApplied)
				reason = reasonHookFailed
			}
			ready = v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: c.Message}
		}
	case latest == nil || latest.Name != a.Status.CurrentRevision:
		a.Status.Phase = v1.PhaseDeploying
		switch {
		case halt == nil && hp.pre != nil:
			ready = v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reasonProgressing, Message: preHookMessage(hp.pre)}
		case ready.Reason == reasonChildNotReady:
			ready.Reason = reasonProgressing
		}
	case halt == nil && hp.post != nil && hp.post.failed:
		a.Status.Phase = v1.PhaseDegraded
		ready = v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reasonHookFailed, Message: hp.failure}
	case ready.Status != v1.ConditionFalse:
		a.Status.Phase, a.Status.ObservedGeneration = v1.PhaseReady, a.Generation
	default:
		a.Status.Phase = v1.PhaseDegraded
	}
	return ready
}

// publish writes the App's status, before any AppRevision status (ADR-0200 Decision 6), and reports whether it did:
// a Conflict means the App changed, and its watch brings the next pass. A stopped pass, or one that leaves a Pruning
// object, requeues after the supervision period, since what blocks it does not requeue the App; a rollout short of
// its deadline requeues at the deadline if that is earlier (left).
func (r *Reconciler) publish(ctx context.Context, a *v1.App, revs []*v1.AppRevision, vs []verdict, pruning []v1.AppChild, halt *stop,
	left time.Duration, hp hookPass) (controller.Result, bool, error) {
	var latest *v1.AppRevision
	if len(revs) > 0 {
		latest = revs[len(revs)-1]
	}
	ready := appPhase(a, latest, vs, halt, hp)
	ready.ObservedGeneration = a.Generation
	r.setCondition(&a.Status.Conditions, ready)
	a.Status.Children = make([]v1.AppChild, 0, len(vs)+len(pruning))
	for _, v := range vs {
		a.Status.Children = append(a.Status.Children, v.child)
	}
	a.Status.Children = append(a.Status.Children, pruning...)
	var res controller.Result
	if halt != nil || len(pruning) > 0 {
		res.RequeueAfter = r.supervisionPeriod
	}
	if left > 0 && (res.RequeueAfter == 0 || left < res.RequeueAfter) {
		res.RequeueAfter = left
	}
	if _, err := r.store.Update(ctx, a); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			return res, false, nil
		}
		return controller.Result{}, false, fault.Wrapf(err, fault.KindOf(err), op, "update app status %s/%s", a.Namespace, a.Name)
	}
	return res, true, nil
}

// setCondition is Conditions.Set with the transition time read from the injected clock, as every time this package
// reads (ADR-0200).
func (r *Reconciler) setCondition(cs *v1.Conditions, c v1.Condition) {
	old, ok := cs.Get(c.Type)
	cs.Set(c)
	if !ok || old.Status != c.Status {
		i := slices.IndexFunc(*cs, func(x v1.Condition) bool { return x.Type == c.Type })
		(*cs)[i].LastTransitionTime = v1.NewTimestamp(r.clock.Now())
	}
}
