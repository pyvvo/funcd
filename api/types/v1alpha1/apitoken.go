package v1alpha1

import "github.com/pyvvo/funcd/api/fault"

// APIToken is an API token kept as a hash (FEAT-0002 F248, decision 7): an admin declares the role and the namespaces
// of its caller, then mints it. funcd shows the token once and keeps only the SHA-256 of its secret, with an expiry.
// The API and the edge of every node check it, so it works, changes and ends with no restart.
type APIToken struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       APITokenSpec   `json:"spec"`
	Status     APITokenStatus `json:"status,omitempty"`
}

// APITokenSpec is who the token's caller is, as an auth.credentials entry says it.
type APITokenSpec struct {
	Role       string          `json:"role" doc:"The role of the token's caller: admin, developer or viewer, as in auth.credentials."`
	Namespaces []NamespaceName `json:"namespaces,omitempty" doc:"The namespaces that a developer or a viewer acts in. An admin spans every namespace and takes none."`
}

// APITokenStatus is the minted token, as a hash.
type APITokenStatus struct {
	TokenHash string    `json:"tokenHash,omitempty" doc:"The SHA-256 of the minted token's secret part, in hexadecimal; empty until a token is minted."`
	IssuedAt  Timestamp `json:"issuedAt,omitzero" doc:"When the token was minted."`
	ExpiresAt Timestamp `json:"expiresAt,omitzero" doc:"When the token stops working."`
}

// GroupVersionKind returns the constant GVK for APIToken.
func (t *APIToken) GroupVersionKind() GroupVersionKind { return KindAPIToken.GVK() }

// Validate performs envelope validation and checks the caller: an admin takes no namespaces, and a developer or a viewer
// takes at least one. The name is a DNS-1123 label, as the token carries it.
func (t *APIToken) Validate() error {
	const op = "APIToken.Validate"
	if err := validateMeta(t.TypeMeta, &t.ObjectMeta, KindAPIToken); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(t.Name)) {
		return fault.Invalidf(op, "metadata.name %q is not a valid DNS-1123 label", t.Name)
	}
	switch t.Spec.Role {
	case "admin":
		if len(t.Spec.Namespaces) > 0 {
			return fault.Invalidf(op, "spec.namespaces is set on an admin, which spans every namespace: remove it")
		}
	case "developer", "viewer":
		if len(t.Spec.Namespaces) == 0 {
			return fault.Invalidf(op, "spec.namespaces is empty: a %s acts in at least one namespace", t.Spec.Role)
		}
	default:
		return fault.Invalidf(op, "spec.role %q is not admin, developer or viewer", t.Spec.Role)
	}
	for i, ns := range t.Spec.Namespaces {
		if !dnsLabel.MatchString(string(ns)) {
			return fault.Invalidf(op, "spec.namespaces[%d] %q is not a valid DNS-1123 label", i, ns)
		}
	}
	return nil
}
