package v1alpha1

import (
	"github.com/pyvvo/funcd/api/fault"
)

// SizeQuota caps what a store or a Bucket prefix holds in total (ADR-0292): maxSize in ByteSize's grammar (ADR-0266) or
// maxBytes in bytes, at most one; neither means no cap. A write that would pass it gets 507.
type SizeQuota struct {
	MaxSize  ByteSize `json:"maxSize,omitempty"`
	MaxBytes int64    `json:"maxBytes,omitempty" minimum:"0"`
}

// Limit is the cap in bytes, ok=false without one. It assumes Validate passed: a size that does not parse reads as no
// cap.
func (q SizeQuota) Limit() (int64, bool) {
	if q.MaxBytes > 0 {
		return q.MaxBytes, true
	}
	if q.MaxSize == "" {
		return 0, false
	}
	n, err := q.MaxSize.Bytes()
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Validate refuses both forms, a size that does not parse, and a negative or zero byte count, naming path.
func (q SizeQuota) Validate(op, path string) error {
	if q.MaxSize != "" && q.MaxBytes != 0 {
		return fault.Invalidf(op, "%s: set maxSize or maxBytes, not both", path)
	}
	if q.MaxBytes < 0 {
		return fault.Invalidf(op, "%s.maxBytes (%d) must not be negative", path, q.MaxBytes)
	}
	if q.MaxSize != "" {
		n, err := q.MaxSize.Bytes()
		if err != nil {
			return fault.Invalidf(op, "%s.maxSize: %v", path, err)
		}
		if n <= 0 {
			return fault.Invalidf(op, "%s.maxSize %q must be above zero", path, q.MaxSize)
		}
	}
	return nil
}

// BucketQuota is a Bucket's caps (ADR-0292): its total size and the per-object cap moved from spec.maxObjectBytes.
type BucketQuota struct {
	SizeQuota `json:",inline"`
	// MaxObjectBytes caps one object (a write past it gets 413, ADR-0148); 0 ⇒ only s3gateway.maxUploadBytes applies.
	MaxObjectBytes int64 `json:"maxObjectBytes,omitempty" minimum:"0"`
}

// KVStoreQuota is a KVStore's caps (ADR-0292): its total size and the per-value and per-key caps moved from the spec's
// root.
type KVStoreQuota struct {
	SizeQuota `json:",inline"`
	// MaxValueBytes caps a single value's size; 0 ⇒ DefaultMaxValueBytes (1 MiB); at most MaxValueBytesLimit.
	MaxValueBytes int64 `json:"maxValueBytes,omitempty" minimum:"0" maximum:"1048576"`
	// MaxKeyBytes caps a single key's size; 0 ⇒ DefaultMaxKeyBytes (1 KiB); at most MaxKeyBytesLimit.
	MaxKeyBytes int `json:"maxKeyBytes,omitempty" minimum:"0" maximum:"64000"`
}
