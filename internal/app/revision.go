package app

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// The AppRevision conditions (ADR-0200 Decision 5) and the reasons ADR-0200 adds: Superseded and Replaced on Current,
// NotCurrent on a Pruning child (Decision 7).
const (
	condApplied       = v1.ConditionType("Applied")
	condChildrenReady = v1.ConditionType("ChildrenReady")
	condCurrent       = v1.ConditionType("Current")

	reasonSuperseded = "Superseded"
	reasonReplaced   = "Replaced"
	reasonNotCurrent = "NotCurrent"
)

// The values of an unset Deps.UpgradeTimeout and Deps.RevisionHistory.
const (
	defaultUpgradeTimeout  = 5 * time.Minute
	defaultRevisionHistory = 10
)

// revisions lists the AppRevisions whose controller reference names a's UID, by number (Decision 3).
func (r *Reconciler) revisions(ctx context.Context, a *v1.App) ([]*v1.AppRevision, error) {
	res, err := r.store.List(ctx, v1.KindAppRevision.GVK(), store.ListOptions{Namespace: a.Namespace})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list app revisions in %q", a.Namespace)
	}
	var out []*v1.AppRevision
	for _, o := range res.Items {
		if rev, ok := o.(*v1.AppRevision); ok && v1.ControlledBy(rev.OwnerReferences, v1.KindApp, a.UID) {
			out = append(out, rev)
		}
	}
	slices.SortFunc(out, func(x, y *v1.AppRevision) int { return cmp.Compare(x.Spec.Number, y.Spec.Number) })
	return out, nil
}

// stamp creates <app>-<n+1>, n the latest revision's number or 0, when none exists or a's spec without its pause
// (ADR-0212 Decision 3) differs from the latest's spec.spec by json.Marshal, byte for byte, as the store's
// specChanged compares (Decision 3). A stored namesake whose controller reference names this App's kind and name with
// another UID is deleted at its resourceVersion first; any other owner stops the pass with ChildNotOwned naming it,
// before any part is written.
func (r *Reconciler) stamp(ctx context.Context, a *v1.App, revs []*v1.AppRevision) ([]*v1.AppRevision, *stop, error) {
	declared := a.Spec.WithoutPause()
	want, err := json.Marshal(declared)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.Internal, op, "marshal the spec of app %s/%s", a.Namespace, a.Name)
	}
	var n int64
	if len(revs) > 0 {
		latest := revs[len(revs)-1]
		have, err := json.Marshal(latest.Spec.Spec)
		if err != nil {
			return nil, nil, fault.Wrapf(err, fault.Internal, op, "marshal the spec of app revision %s", latest.Name)
		}
		if bytes.Equal(want, have) {
			return revs, nil, nil
		}
		n = latest.Spec.Number
	}
	name := v1.AppRevisionName(a.Name, n+1)
	k := v1.ObjectRef{Kind: v1.KindAppRevision, Namespace: a.Namespace, Name: name}
	old, err := r.get(ctx, v1.KindAppRevision, a.Namespace, name)
	if err != nil {
		return nil, nil, err
	}
	if old != nil {
		m := old.GetObjectMeta()
		c, ok := v1.ControllerOf(m.OwnerReferences)
		switch {
		case !ok || c.Kind != v1.KindApp || c.Name != a.Name:
			return revs, &stop{reason: reasonChildNotOwned, part: k, detail: "exists and is not owned by App/" + string(a.Name)}, nil
		case c.UID == a.UID:
			return nil, nil, fault.Conflictf(op, "%s was stamped after the revisions were listed", partName(k))
		}
		if err := r.store.Delete(ctx, k.Kind.GVK(), a.Namespace, name, m.ResourceVersion); err != nil && fault.KindOf(err) != fault.NotFound {
			return nil, nil, fault.Wrapf(err, fault.KindOf(err), op, "drop %s of a deleted app", partName(k))
		}
	}
	obj, _ := v1.NewObject(v1.KindAppRevision)
	rev := obj.(*v1.AppRevision)
	rev.ObjectMeta = v1.ObjectMeta{Name: name, Namespace: a.Namespace, ResourceGroup: a.ResourceGroup, OwnerReferences: []v1.OwnerReference{controllerRef(a)}}
	rev.Spec = v1.AppRevisionSpec{App: v1.ObjectRef{Kind: v1.KindApp, Namespace: a.Namespace, Name: a.Name}, Number: n + 1, Spec: declared}
	started := v1.NewTimestamp(r.clock.Now())
	rev.Status = v1.AppRevisionStatus{Status: v1.Status{Phase: v1.PhaseDeploying}, StartedAt: &started}
	created, err := r.store.Create(ctx, rev)
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), op, "stamp %s", partName(k))
	}
	r.log.InfoContext(ctx, "stamped", "namespace", string(a.Namespace), "app", string(a.Name), "revision", string(name))
	return append(revs, created.(*v1.AppRevision)), nil, nil
}

// settle derives the rollout record of this pass from the App's currentRevision and the highest number (Decisions 5
// and 6), in memory: latestRevision is the highest; the latest, while Deploying, gets Applied and ChildrenReady, and
// switches in a pass that was not stopped, wrote no part and left none Pending; short of a switch it turns Failed once
// the clock reaches startedAt + UpgradeTimeout. Then every revision's phase and Current follow. It returns the time
// left to the latest's deadline, zero when none runs.
func (r *Reconciler) settle(ctx context.Context, a *v1.App, revs []*v1.AppRevision, vs []verdict, halt *stop, wrote *v1.ObjectRef) time.Duration {
	if len(revs) == 0 {
		return 0
	}
	latest := revs[len(revs)-1]
	a.Status.LatestRevision = latest.Name
	var left time.Duration
	if latest.Status.Phase == v1.PhaseDeploying {
		r.setCondition(&latest.Status.Conditions, appliedCondition(halt))
		ready := childrenReady(vs)
		if latest.Name != a.Status.CurrentRevision {
			now, deadline := r.clock.Now(), r.deadline(a, latest)
			switch {
			case halt == nil && wrote == nil && !anyPending(vs):
				a.Status.CurrentRevision, a.Status.Version = latest.Name, latest.Spec.Spec.Version
				r.log.InfoContext(ctx, "switched", "namespace", string(a.Namespace), "app", string(a.Name), "revision", string(latest.Name))
			case now.Before(deadline):
				left = max(deadline.Sub(now), time.Millisecond)
			default:
				latest.Status.Phase = v1.PhaseFailed
				ready = v1.Condition{Type: condChildrenReady, Status: v1.ConditionFalse, Reason: reasonChildNotReady, Message: failure(vs, halt, wrote)}
				r.setCondition(&latest.Status.Conditions, v1.Condition{Type: condCurrent, Status: v1.ConditionFalse, Reason: reasonChildNotReady})
				r.log.WarnContext(ctx, "rollout failed", "namespace", string(a.Namespace), "app", string(a.Name), "revision", string(latest.Name), "reason", ready.Message)
			}
		}
		r.setCondition(&latest.Status.Conditions, ready)
	}
	for _, rev := range revs {
		r.setCurrent(rev, latest, a.Status.CurrentRevision)
	}
	return left
}

// setCurrent sets rev's phase and Current condition (Decision 5): the current revision is Ready and Current; an older
// Deploying one turns Failed, Superseded by the latest; the latest Deploying one is Progressing; one that was Current
// is Replaced and stays Ready. A Failed revision keeps its Current condition.
func (r *Reconciler) setCurrent(rev, latest *v1.AppRevision, current v1.ObjectName) {
	c := v1.Condition{Type: condCurrent, Status: v1.ConditionFalse}
	switch cur, _ := rev.Status.Conditions.Get(condCurrent); {
	case rev.Name == current:
		rev.Status.Phase, c.Status = v1.PhaseReady, v1.ConditionTrue
	case rev.Status.Phase == v1.PhaseDeploying && rev != latest:
		rev.Status.Phase, c.Reason, c.Message = v1.PhaseFailed, reasonSuperseded, "superseded by "+string(latest.Name)
	case rev.Status.Phase == v1.PhaseDeploying:
		c.Reason = reasonProgressing
	case rev.Status.Phase == v1.PhaseReady && cur.Status == v1.ConditionTrue:
		c.Reason, c.Message = reasonReplaced, "replaced by "+string(current)
	default:
		return
	}
	r.setCondition(&rev.Status.Conditions, c)
}

// deadline is the latest of the revision's startedAt, resumedAt(a) and the hold's ReleasedAt, plus UpgradeTimeout
// (ADR-0212 Decision 6): paused and held time do not count. A record without startedAt gets the clock's time, so
// every term is read from the store and the gate on every pass, a restart included; resumedAt and ReleasedAt are
// zero when absent and never start a deadline alone.
func (r *Reconciler) deadline(a *v1.App, rev *v1.AppRevision) time.Time {
	if rev.Status.StartedAt == nil {
		t := v1.NewTimestamp(r.clock.Now())
		rev.Status.StartedAt = &t
	}
	start := time.Time(*rev.Status.StartedAt)
	var released time.Time
	if r.hold != nil {
		released = r.hold.ReleasedAt()
	}
	for _, t := range []time.Time{resumedAt(a), released} {
		if t.After(start) {
			start = t
		}
	}
	return start.Add(r.upgradeTimeout)
}

// resumedAt is the lastTransitionTime of a False Paused condition, the zero time when there is none.
func resumedAt(a *v1.App) time.Time {
	if c, ok := a.Status.Conditions.Get(condPaused); ok && c.Status == v1.ConditionFalse {
		return time.Time(c.LastTransitionTime)
	}
	return time.Time{}
}

// appliedCondition is True once a pass wrote or found equal every owned part, else False with the stop's reason.
func appliedCondition(halt *stop) v1.Condition {
	if halt != nil {
		return v1.Condition{Type: condApplied, Status: v1.ConditionFalse, Reason: halt.reason, Message: halt.msg()}
	}
	return v1.Condition{Type: condApplied, Status: v1.ConditionTrue}
}

// childrenReady is ADR-0199 Decision 5's Ready value over the parts: True, Unknown NotStarted, or False naming the
// first Pending part with RefNotFound or else Progressing.
func childrenReady(vs []verdict) v1.Condition {
	c := readyCondition(vs, nil)
	c.Type = condChildrenReady
	if c.Status == v1.ConditionFalse && c.Reason != reasonRefNotFound {
		c.Reason = reasonProgressing
	}
	return c
}

// failure is the message of a revision that reached its deadline (Decision 6): the stop of a stopped pass, else the
// first Pending part and its reason, else the part the pass wrote.
func failure(vs []verdict, halt *stop, wrote *v1.ObjectRef) string {
	if halt != nil {
		return partName(halt.part) + ": " + halt.reason + ": " + halt.detail
	}
	if c := readyCondition(vs, nil); c.Status == v1.ConditionFalse {
		return c.Message
	}
	if wrote != nil {
		return partName(*wrote) + ": " + reasonProgressing
	}
	return ""
}

// statuses snapshots each revision's status before the pass derives it, so record writes only the changed ones.
func statuses(revs []*v1.AppRevision) map[v1.ObjectName]v1.AppRevisionStatus {
	out := make(map[v1.ObjectName]v1.AppRevisionStatus, len(revs))
	for _, rev := range revs {
		s := rev.Status
		s.Conditions = slices.Clone(s.Conditions)
		out[rev.Name] = s
	}
	return out
}

// record writes, after the App's status, the status of each kept revision the pass changed, skipping an unchanged
// write as writeHeld does, then deletes at its resourceVersion every revision that is neither current nor among the
// newest RevisionHistory others by number (Decision 8). A Conflict is left to the next pass, which derives the record
// again.
func (r *Reconciler) record(ctx context.Context, a *v1.App, revs []*v1.AppRevision, stored map[v1.ObjectName]v1.AppRevisionStatus) error {
	others := slices.DeleteFunc(slices.Clone(revs), func(rev *v1.AppRevision) bool { return rev.Name == a.Status.CurrentRevision })
	drop := others[:max(0, len(others)-r.revisionHistory)]
	for _, rev := range revs {
		if slices.Contains(drop, rev) || reflect.DeepEqual(stored[rev.Name], rev.Status) {
			continue
		}
		if _, err := r.store.Update(ctx, rev); err != nil && fault.KindOf(err) != fault.Conflict {
			return fault.Wrapf(err, fault.KindOf(err), op, "update app revision status %s/%s", rev.Namespace, rev.Name)
		}
	}
	for _, rev := range drop {
		err := r.store.Delete(ctx, v1.KindAppRevision.GVK(), rev.Namespace, rev.Name, rev.ResourceVersion)
		switch fault.KindOf(err) {
		case "", fault.NotFound, fault.Conflict:
		default:
			return fault.Wrapf(err, fault.KindOf(err), op, "delete app revision %s/%s", rev.Namespace, rev.Name)
		}
	}
	return nil
}
