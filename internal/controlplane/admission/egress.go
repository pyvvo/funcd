package admission

import (
	"context"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// --- EgressPolicy appliesTo kinds (ADR-0352 Decision 1): Create and Update on EgressPolicy --------------------------------

type egressAppliesTo struct{ r StoreReader }

// NewEgressAppliesToAdmission returns the Validating admission that refuses an EgressPolicy item of an add-on kind that no
// stored Addon registers, spelled the same, as a kind whose objects run an engine: a settings kind (ADR-0340) and an
// engineless kind (ADR-0345) run none.
// The Addons are read through r, as the add-on object quota reads them; the object the item names need not exist.
func NewEgressAppliesToAdmission(r StoreReader) Admission { return egressAppliesTo{r: r} }

func (egressAppliesTo) Name() string { return "egresspolicy-appliesto" }
func (egressAppliesTo) Phase() Phase { return Validating }

func (egressAppliesTo) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindEgressPolicy.GVK() && (op == Create || op == Update)
}

func (a egressAppliesTo) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.egresspolicy-appliesto"
	ep, ok := req.Object.(*v1.EgressPolicy)
	if !ok || !slices.ContainsFunc(ep.Spec.AppliesTo, v1.AppliesToRef.IsAddon) {
		return req.Object, nil
	}
	kinds, err := engineKinds(ctx, a.r)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list Addons")
	}
	for i, ref := range ep.Spec.AppliesTo {
		if ref.IsAddon() && !kinds[ref.Kind] {
			return nil, fault.Invalidf(op, "spec.appliesTo[%d].kind: no Addon registers %q as a kind whose objects run an engine", i, ref.Kind)
		}
	}
	return req.Object, nil
}

// engineKinds are the kinds of the stored Addons whose objects run an engine, each as its Addon spells it: the kind of
// the runtime entry that serves Engine (ADR-0344 Decision 2), never an Engineless entry's (ADR-0345 Decision 1).
func engineKinds(ctx context.Context, r StoreReader) (map[string]bool, error) {
	addons, err := r.List(ctx, v1.KindAddon.GVK(), "")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, obj := range addons {
		a, ok := obj.(*v1.Addon)
		if !ok {
			continue
		}
		if rt, ok := a.Spec.KindRuntime(); ok && rt.Serves(v1.WorkloadEngine) {
			out[a.Spec.Kind()] = true
		}
	}
	return out, nil
}
