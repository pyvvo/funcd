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
	DefaultExposure ExposureMode `json:"defaultExposure,omitempty"`
	// EdgeDefaults carries the namespace-wide edge policy defaults merged under each Route
	// (F74–F78 add fields; empty in F79).
	EdgeDefaults *EdgeDefaults `json:"edgeDefaults,omitempty"`
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
	Auth *EdgeAuth `json:"auth,omitempty"`
}

// EdgeAuth is the F77 namespace-default auth stance.
type EdgeAuth struct {
	Mode AuthMode `json:"mode,omitempty"`
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
