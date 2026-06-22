package v1alpha1

import (
	huma "github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Default per-op caps for a KVStore (ADR-0072): applied where the spec value is read when 0.
const (
	// DefaultMaxValueBytes is the default per-value cap (1 MiB) when KVStoreSpec.MaxValueBytes is 0.
	DefaultMaxValueBytes int64 = 1 << 20
	// DefaultMaxKeyBytes is the default per-key cap (1 KiB) when KVStoreSpec.MaxKeyBytes is 0.
	DefaultMaxKeyBytes int = 1024
)

// KVStore is a namespaced, owned KV resource (ADR-0072): a named key/value store with per-op
// caps and a Ready lifecycle, reached only through a Grant (default-deny). One writer (the single
// rw Grant) + N readers; deletion-protected by referencing Grants and by non-empty data.
type KVStore struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       KVStoreSpec   `json:"spec"`
	Status     KVStoreStatus `json:"status,omitempty"`
}

// KVStoreSpec is the desired state of a KVStore (ADR-0072): the per-op caps the facade enforces
// before a write reaches the driver. A 0 value means "use the default" (read at enforcement time).
type KVStoreSpec struct {
	// MaxValueBytes caps a single value's size; 0 ⇒ DefaultMaxValueBytes (1 MiB).
	MaxValueBytes int64 `json:"maxValueBytes,omitempty" minimum:"0"`
	// MaxKeyBytes caps a single key's size; 0 ⇒ DefaultMaxKeyBytes (1 KiB).
	MaxKeyBytes int `json:"maxKeyBytes,omitempty" minimum:"0"`
}

// KVStoreStatus is the observed state of a KVStore (ADR-0072): the reconciler sets Ready and the
// count of Grants referencing it. It embeds the shared Status (Phase + Conditions) so the
// controller's generic write-back seam (StatusObject.GetStatus) points at the live status.
type KVStoreStatus struct {
	Status `json:",inline"`
	// GrantRefs is the number of Grants referencing this store (computed by the reconciler).
	GrantRefs int `json:"grantRefs,omitempty"`
}

// EffectiveMaxValueBytes returns the per-value cap, applying the default when the spec is 0.
func (s KVStoreSpec) EffectiveMaxValueBytes() int64 {
	if s.MaxValueBytes <= 0 {
		return DefaultMaxValueBytes
	}
	return s.MaxValueBytes
}

// EffectiveMaxKeyBytes returns the per-key cap, applying the default when the spec is 0.
func (s KVStoreSpec) EffectiveMaxKeyBytes() int {
	if s.MaxKeyBytes <= 0 {
		return DefaultMaxKeyBytes
	}
	return s.MaxKeyBytes
}

// GroupVersionKind returns the constant GVK for KVStore.
func (s *KVStore) GroupVersionKind() GroupVersionKind { return KindKVStore.GVK() }

// Validate performs envelope validation via the shared validateMeta helper, then the KVStoreSpec
// rule JSON Schema can't express: the caps are non-negative (0 ⇒ default).
func (s *KVStore) Validate() error {
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindKVStore); err != nil {
		return err
	}
	if s.Spec.MaxValueBytes < 0 {
		return fault.Invalidf("KVStore.Validate", "spec.maxValueBytes (%d) must not be negative", s.Spec.MaxValueBytes)
	}
	if s.Spec.MaxKeyBytes < 0 {
		return fault.Invalidf("KVStore.Validate", "spec.maxKeyBytes (%d) must not be negative", s.Spec.MaxKeyBytes)
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject (the controller's write-back seam).
func (s *KVStore) GetStatus() *Status { return &s.Status.Status }

// KVMode is a Grant's access mode over a KVStore (ADR-0072): read-only or read-write.
type KVMode string

const (
	// KVModeRO grants get/list (read sharing).
	KVModeRO KVMode = "ro"
	// KVModeRW grants get/list + put/delete (the single writer / owner).
	KVModeRW KVMode = "rw"
)

// Validate reports whether the mode is one of the known values.
func (m KVMode) Validate() error {
	switch m {
	case KVModeRO, KVModeRW:
		return nil
	default:
		return fault.Invalidf("KVMode.Validate", "mode %q must be one of ro|rw", m)
	}
}

// Schema carries KVMode's enum constraint into the generated OpenAPI (huma SchemaProvider).
func (KVMode) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(KVModeRO), string(KVModeRW))
}
