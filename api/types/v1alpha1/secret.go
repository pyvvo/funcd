package v1alpha1

import (
	huma "github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Secret is a namespaced, pure-data resource for sensitive configuration.
// Secret does NOT have status — it implements Object, not StatusObject.
type Secret struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SecretSpec `json:"spec"`
}

// SecretSpec holds the secret data. SecretType is an open typed-string discriminator
// whose enumerated values are owned by the secrets ADR (F15).
type SecretSpec struct {
	Type SecretType        `json:"type,omitempty"`
	Data map[string][]byte `json:"data,omitempty"`
}

// SecretType is an open typed-string discriminator for secret kinds.
// Enumerated values are defined by the secrets ADR (F15).
type SecretType string

const (
	// SecretTypeOpaque is the default generic key/value secret (ADR-0022, F15).
	SecretTypeOpaque SecretType = "Opaque"
)

// Schema carries SecretType's enum constraint into the generated OpenAPI (ADR-0048).
func (SecretType) Schema(huma.Registry) *huma.Schema { return enumSchema(string(SecretTypeOpaque)) }

// GroupVersionKind returns the constant GVK for Secret.
func (s *Secret) GroupVersionKind() GroupVersionKind { return KindSecret.GVK() }

// Validate performs envelope validation, then the data-key rule JSON Schema can't express
// (ADR-0048): every data key is non-empty. The `type` enum is schema-enforced at the edge.
func (s *Secret) Validate() error {
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindSecret); err != nil {
		return err
	}
	for k := range s.Spec.Data {
		if k == "" {
			return fault.Invalidf("Secret.Validate", "spec.data keys must not be empty")
		}
	}
	return nil
}
