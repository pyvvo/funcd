package v1alpha1

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

// GroupVersionKind returns the constant GVK for Secret.
func (s *Secret) GroupVersionKind() GroupVersionKind { return KindSecret.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (s *Secret) Validate() error { return validateMeta(s.TypeMeta, &s.ObjectMeta, KindSecret) }
