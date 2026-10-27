package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// Bucket is a namespaced blob domain (ADR-0080): the S3 *bucket*, mirroring KVStore — a named domain
// with a set of sub-domains (prefixes, the medallion layers) each with a single-writer owner, and an
// optional per-object resource policy. A function reaches a prefix only through a Function.spec.bindings.blob
// binding (default-deny); the prefix's owner is the single writer. Its status reports the blob storage's health
// (ADR-0215 Decision 7). Deletion-protected by referencing bindings and by non-empty data.
type Bucket struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       BucketSpec   `json:"spec"`
	Status     BucketStatus `json:"status,omitempty"`
}

// BucketStatus is the observed state of a Bucket (ADR-0215 Decision 7): phase Ready or Degraded, and a Ready
// condition that follows the blob storage probe.
type BucketStatus struct {
	Status `json:",inline"`
}

// BucketSpec is the desired state of a Bucket (ADR-0080): its sub-domains (prefixes) and an optional
// per-object resource policy. Each prefix is a key-prefix sub-domain with a single-writer owner.
type BucketSpec struct {
	// Prefixes are the bucket's sub-domains (ADR-0080): the medallion layers, each a key prefix with a
	// single-writer Owner. Prefix names are unique within the bucket (so a prefix has exactly one owner
	// by construction). Empty ⇒ a bucket with no writable sub-domains.
	Prefixes []BucketPrefix `json:"prefixes"`
	// Quota caps the bucket's total size and each object (ADR-0292).
	Quota BucketQuota `json:"quota,omitzero"`
}

// BucketPrefix is one sub-domain of a Bucket (ADR-0080): a named key prefix with a single-writer owner.
// Mirrors KVTable. The leading segment of an S3 key selects the prefix; the rest is the object path.
type BucketPrefix struct {
	// Name is the key prefix; a DNS-1123 label, unique within the bucket. The medallion layer.
	Name string `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Owner is the single writer, a Function or a Lake in this bucket's namespace, named with its kind (ADR-0275).
	// Zero ⇒ no writer (read-only).
	Owner PrefixOwner `json:"owner,omitzero"`
	// Quota caps the prefix's total size (ADR-0292); a Lake's prefix quota is the Lake's cap.
	Quota SizeQuota `json:"quota,omitzero"`
}

// PrefixOwner is a Bucket prefix's single writer, named with its kind (ADR-0275): a Function and a Lake may share a
// name (ADR-0152), so the name alone does not say which writes.
type PrefixOwner struct {
	Kind Kind       `json:"kind" enum:"Function,Lake"`
	Name ObjectName `json:"name"`
}

// FunctionOwner names Function name as a prefix's owner.
func FunctionOwner(name ObjectName) PrefixOwner { return PrefixOwner{Kind: KindFunction, Name: name} }

// LakeOwner names Lake name as a prefix's owner.
func LakeOwner(name ObjectName) PrefixOwner { return PrefixOwner{Kind: KindLake, Name: name} }

// IsZero reports whether no owner is set.
func (o PrefixOwner) IsZero() bool { return o == PrefixOwner{} }

// GroupVersionKind returns the constant GVK for Bucket.
func (b *Bucket) GroupVersionKind() GroupVersionKind { return KindBucket.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (b *Bucket) GetStatus() *Status { return &b.Status.Status }

// Validate performs envelope validation via the shared validateMeta helper, then the BucketSpec rules
// JSON Schema can't express: the quota (ADR-0292) and each prefix's; within the bucket, prefix names are
// unique (⇒ a prefix has exactly one owner) and each is a DNS-1123 label; and a set owner names a Function or a
// Lake by a DNS-1123 label (ADR-0275).
func (b *Bucket) Validate() error {
	const op = "Bucket.Validate"
	if err := validateMeta(b.TypeMeta, &b.ObjectMeta, KindBucket); err != nil {
		return err
	}
	if err := b.Spec.Quota.Validate(op, "spec.quota"); err != nil {
		return err
	}
	if b.Spec.Quota.MaxObjectBytes < 0 {
		return fault.Invalidf(op, "spec.quota.maxObjectBytes (%d) must not be negative", b.Spec.Quota.MaxObjectBytes)
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
		if err := p.Owner.validate(op, "spec.prefixes owner of "+p.Name); err != nil {
			return err
		}
		if err := p.Quota.Validate(op, "spec.prefixes quota of "+p.Name); err != nil {
			return err
		}
	}
	return nil
}

// validate checks a set owner: kind Function or Lake, and a DNS-1123 label as its name.
func (o PrefixOwner) validate(op, field string) error {
	if o.IsZero() {
		return nil
	}
	if o.Kind != KindFunction && o.Kind != KindLake {
		return fault.Invalidf(op, "%s: kind %q is not Function or Lake", field, o.Kind)
	}
	if !dnsLabel.MatchString(string(o.Name)) {
		return fault.Invalidf(op, "%s: name %q is not a valid DNS-1123 label", field, o.Name)
	}
	return nil
}
