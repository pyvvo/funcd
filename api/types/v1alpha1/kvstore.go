package v1alpha1

import (
	"github.com/green-0-rabbit/funcd/api/fault"
)

// Default per-op caps for a KVStore (ADR-0072): applied where the spec value is read when 0.
const (
	// DefaultMaxValueBytes is the default per-value cap (1 MiB) when KVStoreSpec.MaxValueBytes is 0.
	DefaultMaxValueBytes int64 = 1 << 20
	// DefaultMaxKeyBytes is the default per-key cap (1 KiB) when KVStoreSpec.MaxKeyBytes is 0.
	DefaultMaxKeyBytes int = 1024
)

// KVStore is a namespaced, owned KV resource (ADR-0072, reshaped by ADR-0073): a named domain with
// per-op caps, a set of sub-domains (tables) each with a single-writer owner, and a Ready lifecycle.
// A function reaches a table only through a Function.spec.kv binding (default-deny); the table's owner
// is the single writer. The store is one Badger prefix <ns>/<store>/; a table is <ns>/<store>/<table>/.
// Deletion-protected by referencing bindings and by non-empty data.
type KVStore struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       KVStoreSpec   `json:"spec"`
	Status     KVStoreStatus `json:"status,omitempty"`
}

// KVStoreSpec is the desired state of a KVStore (ADR-0072/0073): the per-op caps the facade enforces
// before a write reaches the driver, and the store's sub-domains (tables). A 0 cap means "use the
// default" (read at enforcement time).
type KVStoreSpec struct {
	// MaxValueBytes caps a single value's size; 0 ⇒ DefaultMaxValueBytes (1 MiB).
	MaxValueBytes int64 `json:"maxValueBytes,omitempty" minimum:"0"`
	// MaxKeyBytes caps a single key's size; 0 ⇒ DefaultMaxKeyBytes (1 KiB).
	MaxKeyBytes int `json:"maxKeyBytes,omitempty" minimum:"0"`
	// Tables are the store's sub-domains (ADR-0073): each is a key space <ns>/<store>/<table>/ with a
	// single-writer Owner. Table names are unique within the store (so a table has exactly one owner by
	// construction). Empty ⇒ a store with no writable sub-domains.
	Tables []KVTable `json:"tables,omitempty"`
}

// KVTable is one sub-domain of a KVStore (ADR-0073): a named key space with a single-writer owner.
// The typed-record engine (backlog) attaches its Schema/Indexes here (table-scoped) without a re-model.
type KVTable struct {
	// Name is the sub-domain name; a DNS-1123 label, unique within the store.
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Owner is the single writer: a Function in this store's namespace. Empty ⇒ no writer (read-only
	// through the facade). Cross-resource validity (the owner exists) is an admission, not Validate.
	Owner ObjectName `json:"owner,omitempty"`
}

// KVStoreStatus is the observed state of a KVStore (ADR-0072/0073): the reconciler sets Ready and the
// count of tables + the count of Function.spec.kv bindings referencing it. It embeds the shared Status
// (Phase + Conditions) so the controller's generic write-back seam (StatusObject.GetStatus) points at
// the live status.
type KVStoreStatus struct {
	Status `json:",inline"`
	// Tables is the number of sub-domains declared on the store (computed by the reconciler).
	Tables int `json:"tables,omitempty"`
	// Bindings is the number of Function.spec.kv entries referencing this store (computed by the reconciler).
	Bindings int `json:"bindings,omitempty"`
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

// Validate performs envelope validation via the shared validateMeta helper, then the KVStoreSpec rules
// JSON Schema can't express: the caps are non-negative (0 ⇒ default); and within the store, table names
// are unique (⇒ a table has exactly one owner) and each is a DNS-1123 label. Cross-resource rules (the
// owner exists) are an admission (ADR-0073), not structural Validate.
func (s *KVStore) Validate() error {
	const op = "KVStore.Validate"
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindKVStore); err != nil {
		return err
	}
	if s.Spec.MaxValueBytes < 0 {
		return fault.Invalidf(op, "spec.maxValueBytes (%d) must not be negative", s.Spec.MaxValueBytes)
	}
	if s.Spec.MaxKeyBytes < 0 {
		return fault.Invalidf(op, "spec.maxKeyBytes (%d) must not be negative", s.Spec.MaxKeyBytes)
	}
	seen := make(map[string]bool, len(s.Spec.Tables))
	for _, tb := range s.Spec.Tables {
		if !dnsLabel.MatchString(tb.Name) {
			return fault.Invalidf(op, "spec.tables name %q is not a valid DNS-1123 label", tb.Name)
		}
		if seen[tb.Name] {
			return fault.Invalidf(op, "spec.tables name %q is duplicated", tb.Name)
		}
		seen[tb.Name] = true
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject (the controller's write-back seam).
func (s *KVStore) GetStatus() *Status { return &s.Status.Status }
