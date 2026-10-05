package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// Bucket is a namespaced blob domain (ADR-0080): the S3 *bucket*, mirroring KVStore — a named domain
// with a set of sub-domains (prefixes, the medallion layers) each with a single-writer owner, and an
// optional per-object resource policy. A function reaches a prefix only through a Function.spec.blob
// binding (default-deny); the prefix's owner is the single writer. Unlike KVStore, Bucket carries NO
// observed status (no reconciler) — it is a pure data-model resource. Deletion-protected by referencing
// bindings and by non-empty data.
type Bucket struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       BucketSpec `json:"spec"`
}

// BucketSpec is the desired state of a Bucket (ADR-0080): its sub-domains (prefixes) and an optional
// per-object resource policy. Each prefix is a key-prefix sub-domain with a single-writer owner.
type BucketSpec struct {
	// Prefixes are the bucket's sub-domains (ADR-0080): the medallion layers, each a key prefix with a
	// single-writer Owner. Prefix names are unique within the bucket (so a prefix has exactly one owner
	// by construction). Empty ⇒ a bucket with no writable sub-domains.
	Prefixes []BucketPrefix `json:"prefixes"`
	// MaxObjectBytes is the per-object resource policy on THIS bucket (a write past it ⇒ PayloadTooLarge);
	// 0 ⇒ unset (no per-bucket cap; the daemon-wide s3gateway.maxUploadBytes still applies).
	MaxObjectBytes int64 `json:"maxObjectBytes,omitempty" minimum:"0"`
}

// BucketPrefix is one sub-domain of a Bucket (ADR-0080): a named key prefix with a single-writer owner.
// Mirrors KVTable. The leading segment of an S3 key selects the prefix; the rest is the object path.
type BucketPrefix struct {
	// Name is the key prefix; a DNS-1123 label, unique within the bucket. The medallion layer.
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Owner is the single writer: a Function in this bucket's namespace. Empty ⇒ no writer (read-only).
	// Cross-resource validity (the owner exists) is an admission, not Validate.
	Owner ObjectName `json:"owner,omitempty"`
}

// GroupVersionKind returns the constant GVK for Bucket.
func (b *Bucket) GroupVersionKind() GroupVersionKind { return KindBucket.GVK() }

// Validate performs envelope validation via the shared validateMeta helper, then the BucketSpec rules
// JSON Schema can't express: MaxObjectBytes is non-negative; and within the bucket, prefix names are
// unique (⇒ a prefix has exactly one owner) and each is a DNS-1123 label. Cross-resource rules (the
// owner exists) are an admission (ADR-0080), not structural Validate.
func (b *Bucket) Validate() error {
	const op = "Bucket.Validate"
	if err := validateMeta(b.TypeMeta, &b.ObjectMeta, KindBucket); err != nil {
		return err
	}
	if b.Spec.MaxObjectBytes < 0 {
		return fault.Invalidf(op, "spec.maxObjectBytes (%d) must not be negative", b.Spec.MaxObjectBytes)
	}
	seen := make(map[string]bool, len(b.Spec.Prefixes))
	for _, p := range b.Spec.Prefixes {
		if !dnsLabel.MatchString(p.Name) {
			return fault.Invalidf(op, "spec.prefixes name %q is not a valid DNS-1123 label", p.Name)
		}
		if seen[p.Name] {
			return fault.Invalidf(op, "spec.prefixes name %q is duplicated", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}
