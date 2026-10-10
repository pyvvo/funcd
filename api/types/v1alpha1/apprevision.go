package v1alpha1

import (
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
)

// maxAppRevisionNumber keeps <app>-<number> a DNS label for a 52-character App name (ADR-0200 Decision 1).
const maxAppRevisionNumber = 9999999999

// AppRevision is the record of one App spec and its rollout (ADR-0200, F114): the App reconciler stamps one per
// changed spec and alone writes it; its spec and metadata never change after create, and the API serves it read-only.
type AppRevision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       AppRevisionSpec   `json:"spec"`
	Status     AppRevisionStatus `json:"status,omitempty"`
}

// AppRevisionSpec names the App and the revision's number, and holds a frozen copy of the App spec.
type AppRevisionSpec struct {
	App    ObjectRef `json:"app"`
	Number int64     `json:"number" minimum:"1" maximum:"9999999999"`
	Spec   AppSpec   `json:"spec"`
	// HookInput is the data of this revision's hook calls, set by the stamp when the spec has a hook (ADR-0214
	// Decision 2).
	HookInput *AppHookInput `json:"hookInput,omitempty"`
}

// AppRevisionStatus is the rollout record (Decision 5): phase Deploying, Ready or Failed, and the conditions Applied,
// ChildrenReady and Current.
type AppRevisionStatus struct {
	Status `json:",inline"`
	// StartedAt is when the requirements were met, the stamp time when none waited; nil while the revision waits; the
	// rollout deadline (ADR-0212 Decision 6) needs it (ADR-0219 Decision 3).
	StartedAt *Timestamp `json:"startedAt,omitempty"`
	// Hooks records every hook call of the revision, retries included, in call order (ADR-0214 Decision 6).
	Hooks []AppHookCall `json:"hooks,omitempty"`
}

// AppHookCall is one recorded hook call (ADR-0214 Decision 6): its point, its Function, the Invocation that records
// it, its outcome and its end, a start term of the rollout deadline.
type AppHookCall struct {
	Point      string     `json:"point" enum:"preApply,postApply"`
	Function   ObjectName `json:"function"`
	Invocation ObjectName `json:"invocation"`
	Phase      Phase      `json:"phase"`
	EndTime    Timestamp  `json:"endTime"`
}

// AppRevisionName is the name of the App's AppRevision number n: <app>-<n>.
func AppRevisionName(app ObjectName, n int64) ObjectName {
	return ObjectName(string(app) + "-" + strconv.FormatInt(n, 10))
}

// GroupVersionKind returns the constant GVK for AppRevision.
func (r *AppRevision) GroupVersionKind() GroupVersionKind { return KindAppRevision.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (r *AppRevision) GetStatus() *Status { return &r.Status.Status }

// Validate checks the envelope, the number's bounds and that the name is AppRevisionName(spec.app.name,
// spec.number). The spec copy is not checked: the App it was taken from passed App.Validate.
func (r *AppRevision) Validate() error {
	const op = "AppRevision.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindAppRevision); err != nil {
		return err
	}
	if n := r.Spec.Number; n < 1 || n > maxAppRevisionNumber {
		return fault.Invalidf(op, "spec.number %d is outside 1 to %d", n, int64(maxAppRevisionNumber))
	}
	if want := AppRevisionName(r.Spec.App.Name, r.Spec.Number); r.Name != want {
		return fault.Invalidf(op, "metadata.name %q must be %q, the spec.app.name and spec.number", r.Name, want)
	}
	return nil
}
