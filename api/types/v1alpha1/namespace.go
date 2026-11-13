package v1alpha1

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// Namespace is a cluster-scoped resource that defines a tenant boundary.
// Status-bearing.
type Namespace struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       NamespaceSpec   `json:"spec,omitempty"`
	Status     NamespaceStatus `json:"status,omitempty"`
}

// NamespaceSpec is the tenant's edge posture (ADR-0110, F79). A Spec-less namespace (the
// pre-F79 shape) has the zero value, which normalizes to `implicit` — so existing deployments
// are unchanged on upgrade.
type NamespaceSpec struct {
	// DefaultExposure switches the public data-plane front door. Empty/absent ⇒ implicit.
	DefaultExposure ExposureMode `json:"defaultExposure,omitempty" doc:"Whether the namespace's Functions and Sites answer at their own address, such as /function/<name>, while their spec.ingress.enabled is unset: implicit, or empty, turns it on, and explicit turns it off. Routes you write and Routes an App declares stay on, though in the default namespace explicit refuses one with no host (HostRequired)."`
	// EdgeDefaults carries the namespace-wide edge policy defaults merged under each Route
	// (F74–F78 add fields; empty in F79).
	EdgeDefaults *EdgeDefaults `json:"edgeDefaults,omitempty" doc:"The namespace's edge defaults, which apply where a Route or a Function's or Site's spec.ingress sets no value of its own."`
	// VolumeBudgetBytes caps the volume space the namespace's Functions reserve (ADR-0262 Decision 5); 0 ⇒ only
	// volumes.capacityBytes limits it.
	VolumeBudgetBytes int64 `json:"volumeBudgetBytes,omitempty" minimum:"0" doc:"The same budget as volumeBudget, as a number of bytes. Set volumeBudget or volumeBudgetBytes, not both; 0 means no budget."`
	// VolumeBudget is the same budget with a binary unit, "4Gi" (ADR-0266); at most one of the two is set.
	VolumeBudget ByteSize `json:"volumeBudget,omitempty" doc:"The most volume space the namespace's Functions may reserve: each local volume's size from apply until the volume is removed, and each worker's emptyDir while the worker exists. Apply refuses a local volume that does not fit (VolumeBudget), and a worker whose emptyDir does not fit is not created. Set volumeBudget or volumeBudgetBytes, not both; neither, or 0, means no budget."`
}

// VolumeBudgetOf is the namespace's volume budget in bytes, from whichever form is set; 0 is none.
func (s NamespaceSpec) VolumeBudgetOf() int64 {
	if s.VolumeBudget != "" {
		n, _ := s.VolumeBudget.Bytes()
		return n
	}
	return s.VolumeBudgetBytes
}

// ExposureMode selects whether an unrouted Function is reachable at the public edge.
type ExposureMode string

const (
	// ExposureImplicit keeps the /function/<name> path reachable (default, back-compat).
	ExposureImplicit ExposureMode = "implicit"
	// ExposureExplicit gates the edge: no Route ⇒ private (default-deny ingress).
	ExposureExplicit ExposureMode = "explicit"
)

// Schema renders the huma enum for ExposureMode (empty accepted, normalizes to implicit).
func (ExposureMode) Schema(huma.Registry) *huma.Schema {
	return enumSchema("", string(ExposureImplicit), string(ExposureExplicit))
}

// Normalized returns the effective mode, mapping the empty/absent zero value to implicit.
func (m ExposureMode) Normalized() ExposureMode {
	if m == ExposureExplicit {
		return ExposureExplicit
	}
	return ExposureImplicit
}

// EdgeDefaults carries namespace-wide edge policy defaults (F74–F78 add typed sub-structs).
type EdgeDefaults struct {
	// Auth is the namespace-default edge auth stance (ADR-0113, F77); a Route overrides it. nil ⇒
	// open (the phased default — a namespace opts into default-deny by setting Auth.Mode=authenticated).
	Auth *EdgeAuth `json:"auth,omitempty" doc:"The default edge authentication of the namespace's Routes and of its Functions' and Sites' own addresses, used where a rule, its Route and spec.ingress.auth set none; unset is open. It is a default, not a floor: a rule or a Route that sets open stays open."`
}

// EdgeAuth is the F77 namespace-default auth stance.
type EdgeAuth struct {
	Mode AuthMode `json:"mode,omitempty" doc:"authenticated admits only a caller whose token, sent as Authorization: Bearer or X-Api-Key, may get the namespace's Functions; others get 401 or 403, and every caller gets 401 while server.auth.edge is off. open, or empty, admits anyone."`
}

// NamespaceStatus holds the observed state.
type NamespaceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Namespace.
func (n *Namespace) GroupVersionKind() GroupVersionKind { return KindNamespace.GVK() }

// Validate performs envelope + spec validation.
func (n *Namespace) Validate() error {
	if err := validateMeta(n.TypeMeta, &n.ObjectMeta, KindNamespace); err != nil {
		return err
	}
	switch n.Spec.DefaultExposure {
	case "", ExposureImplicit, ExposureExplicit:
	default:
		return fault.Invalidf("Namespace.Validate", "spec.defaultExposure %q must be implicit or explicit", n.Spec.DefaultExposure)
	}
	if n.Spec.EdgeDefaults != nil && n.Spec.EdgeDefaults.Auth != nil {
		if err := validateAuthMode(n.Spec.EdgeDefaults.Auth.Mode, "spec.edgeDefaults.auth.mode"); err != nil {
			return err
		}
	}
	// ADR-0266 Decision 3: at most one form of the volume budget.
	if n.Spec.VolumeBudget != "" {
		if n.Spec.VolumeBudgetBytes != 0 {
			return fault.Invalidf("Namespace.Validate", "spec.volumeBudget and spec.volumeBudgetBytes are both set: want at most one")
		}
		if _, err := n.Spec.VolumeBudget.Bytes(); err != nil {
			return fault.Wrapf(err, fault.Invalid, "Namespace.Validate", "spec.volumeBudget")
		}
	}
	return nil
}

// validateAuthMode checks an AuthMode is empty | authenticated | open (ADR-0113).
func validateAuthMode(m AuthMode, field string) error {
	switch m {
	case "", AuthAuthenticated, AuthOpen:
		return nil
	default:
		return fault.Invalidf("Validate", "%s %q must be authenticated or open", field, m)
	}
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (n *Namespace) GetStatus() *Status { return &n.Status.Status }
