package v1alpha1

import (
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
)

// AddonRevision is one frozen Addon spec (ADR-0339 Decision 1), cluster-scoped and named after the Addon and its
// generation: the Addon reconciler stamps it and alone writes it, and the API serves it read-only, to an admin.
type AddonRevision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AddonRevisionSpec `json:"spec"`
}

// AddonRevisionSpec names the Addon and its generation, and holds a frozen copy of its spec.
type AddonRevisionSpec struct {
	Addon  ObjectName `json:"addon" doc:"The Addon this revision belongs to."`
	Number int64      `json:"number" minimum:"1" doc:"The Addon's generation when funcd stamped this revision. Numbers may skip: funcd stamps the generation it reads."`
	Spec   AddonSpec  `json:"spec" doc:"The Addon's spec at that generation. It never changes: an object that pins this revision runs its engine from it."`
}

// AddonRevisionName is the name of the Addon's revision of generation n: <addon>-<n>, its name shortened with a hash of
// it when that would pass 63 characters.
func AddonRevisionName(addon ObjectName, n int64) ObjectName {
	return revisionLabel(string(addon), strconv.FormatInt(n, 10))
}

// GroupVersionKind returns the constant GVK for AddonRevision.
func (r *AddonRevision) GroupVersionKind() GroupVersionKind { return KindAddonRevision.GVK() }

// Validate checks the envelope, the number and that the name is AddonRevisionName(spec.addon, spec.number). The spec
// copy is not checked: the Addon it was taken from passed its admission.
func (r *AddonRevision) Validate() error {
	const op = "AddonRevision.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindAddonRevision); err != nil {
		return err
	}
	if r.Spec.Number < 1 {
		return fault.Invalidf(op, "spec.number %d is not positive", r.Spec.Number)
	}
	if want := AddonRevisionName(r.Spec.Addon, r.Spec.Number); r.Name != want {
		return fault.Invalidf(op, "metadata.name %q must be %q, the spec.addon and spec.number", r.Name, want)
	}
	return nil
}
