package app

import (
	"context"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// Planner answers what a write of an App would make its rollout do (ADR-0220 Decision 5), by the reconciler's own
// rules through the helpers the pass calls. It only reads the store; the control plane calls it as its AppPlanner.
type Planner struct {
	reader
}

// NewPlanner builds the Planner over st.
func NewPlanner(st store.Store) *Planner { return &Planner{reader: reader{store: st}} }

// PlanApp plans a, the admitted object of a dry-run write, whose UID is the stored App's or empty for a create. The
// revision is the one the stamp would name. A namesake held by another owner stops it: the plan lists only that
// AppRevision. Otherwise the parts are the first stop of the pass alone, ChildNotOwned then a declared Secret missing
// or lacking a key, or else every part a write would create or update, in section order, then every object the App
// controls that no entry names, as prune. The hooks are listed when a revision is named. A hold, a pause and an unmet
// requirement delay the rollout and are not shown (ADR-0220 open question 1).
func (p *Planner) PlanApp(ctx context.Context, a *v1.App) (v1.AppPlan, error) {
	var plan v1.AppPlan
	next, halt, err := p.planRevision(ctx, a)
	if err != nil {
		return v1.AppPlan{}, err
	}
	if halt != nil {
		return v1.AppPlan{Parts: []v1.PlanPart{stopLine(halt, v1.PlanCreate)}}, nil
	}
	if next != nil {
		plan.Revision, plan.Hooks = next.name, hookNames(a.Spec)
	}
	ents := entries(a)
	objs := make(map[v1.ObjectRef]v1.Object, len(ents))
	if err := p.readParts(ctx, a, ents, objs); err != nil {
		return v1.AppPlan{}, err
	}
	if halt = notOwned(a, ents, objs); halt != nil {
		plan.Parts = []v1.PlanPart{stopLine(halt, v1.PlanUpdate)}
		return plan, nil
	}
	if halt, err = p.checkSecrets(ctx, a); err != nil {
		return v1.AppPlan{}, err
	}
	if halt != nil {
		plan.Parts = []v1.PlanPart{stopLine(halt, "")}
		return plan, nil
	}
	if plan.Parts, err = changedParts(a, ents, objs); err != nil {
		return v1.AppPlan{}, err
	}
	dropped, err := p.dropped(ctx, a, ents)
	if err != nil {
		return v1.AppPlan{}, err
	}
	for _, o := range dropped {
		k := keyOf(o)
		plan.Parts = append(plan.Parts, v1.PlanPart{Kind: k.Kind, Name: k.Name, Action: v1.PlanPrune})
	}
	return plan, nil
}

// planRevision is the revision a write of a would stamp, nil when the latest holds its spec, or the stop of a namesake
// held by another owner.
func (p *Planner) planRevision(ctx context.Context, a *v1.App) (*stampTarget, *stop, error) {
	var revs []*v1.AppRevision
	if a.UID != "" {
		var err error
		if revs, err = p.revisions(ctx, a); err != nil {
			return nil, nil, err
		}
	}
	next, err := nextStamp(a, revs)
	if err != nil || next == nil {
		return nil, nil, err
	}
	old, err := p.get(ctx, v1.KindAppRevision, a.Namespace, next.name)
	if err != nil {
		return nil, nil, err
	}
	halt, _, err := namesake(a, v1.ObjectRef{Kind: v1.KindAppRevision, Namespace: a.Namespace, Name: next.name}, old)
	return next, halt, err
}

// changedParts lists every part a write of a would create or update, in section order.
func changedParts(a *v1.App, ents []entry, objs map[v1.ObjectRef]v1.Object) ([]v1.PlanPart, error) {
	parts := partsOf(a)
	var out []v1.PlanPart
	for _, e := range ents {
		if e.ref {
			continue
		}
		cur := objs[e.key(a.Namespace)]
		changed, _, err := differs(desired(a, parts, e), cur)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		action := v1.PlanUpdate
		if cur == nil {
			action = v1.PlanCreate
		}
		out = append(out, v1.PlanPart{Kind: e.kind, Name: e.name, Action: action})
	}
	return out, nil
}

// stopLine is the plan's line of the part where a pass stops.
func stopLine(s *stop, action v1.PlanAction) v1.PlanPart {
	return v1.PlanPart{Kind: s.part.Kind, Name: s.part.Name, Action: action, Reason: s.reason}
}
