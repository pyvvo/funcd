package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// IdentityType is the flavor of a managed Identity. V1 supports only "external" — a non-funcd caller that
// authenticates with an issued SigV4 keypair. A Function's identity is system-assigned (implicit) and is
// not modeled as an Identity resource.
type IdentityType string

// IdentityTypeExternal is a user-assigned managed identity for a non-funcd (external) caller.
const IdentityTypeExternal IdentityType = "external"

// Identity is a user-assigned managed identity (ADR-0135, FEAT-0008/F100): a first-class, namespace-scoped
// principal for a non-funcd caller. Its controller issues a revocable SigV4 keypair into an owned Secret
// and registers it so the caller resolves (via the S3 gateway's principalFor) to Identity::"<ns>/<name>".
// Issuing a credential is AUTHENTICATION only — authorization is granted separately (ADR-0136
// RolesAssignment); an Identity with no assignment is default-deny.
type Identity struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       IdentitySpec   `json:"spec"`
	Status     IdentityStatus `json:"status,omitempty"`
}

// IdentitySpec declares a managed identity and how its credential is issued.
type IdentitySpec struct {
	// Type is the identity flavor. V1: only "external". Required.
	Type IdentityType `json:"type" enum:"external"`
	// CredentialSecretName is the owned Secret the issued keypair is written to. Empty ⇒ the Identity's
	// own name.
	CredentialSecretName ObjectName `json:"credentialSecretName,omitempty"`
	// Rotate, when incremented past status.observedRotate, forces the controller to reissue the secret
	// (rotation). The access key id is stable; only the secret changes.
	Rotate int64 `json:"rotate,omitempty"`
}

// IdentityStatus is the observed credential state.
type IdentityStatus struct {
	Status `json:",inline"`
	// AccessKeyID is the issued, stable SigV4 access key id (the secret lives only in the owned Secret).
	AccessKeyID string `json:"accessKeyId,omitempty"`
	// ObservedRotate mirrors the spec.rotate the currently-issued secret corresponds to.
	ObservedRotate int64 `json:"observedRotate,omitempty"`
}

// GroupVersionKind returns the constant GVK for Identity.
func (i *Identity) GroupVersionKind() GroupVersionKind { return KindIdentity.GVK() }

// GetStatus returns the shared status base (Identity is a StatusObject — its reconciler reports credential
// issuance).
func (i *Identity) GetStatus() *Status { return &i.Status.Status }

// Validate performs envelope + structural validation. Cross-resource checks (e.g. Secret existence) are
// reconcile-time, not here (ADR-0121).
func (i *Identity) Validate() error {
	const op = "Identity.Validate"
	if err := validateMeta(i.TypeMeta, &i.ObjectMeta, KindIdentity); err != nil {
		return err
	}
	if i.Spec.Type != IdentityTypeExternal {
		return fault.Invalidf(op, "spec.type must be %q (got %q)", IdentityTypeExternal, i.Spec.Type)
	}
	return nil
}
