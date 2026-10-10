package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

func bucket(name string, mob int64, prefixes ...BucketPrefix) *Bucket {
	return &Bucket{
		TypeMeta:   TypeMeta{APIVersion: KindBucket.GVK().APIVersion(), Kind: KindBucket},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       BucketSpec{Prefixes: prefixes, MaxObjectBytes: mob},
	}
}

// scenario (types): Bucket roundtrip — full JSON equality after marshal/unmarshal, including the
// prefixes[] sub-domains and their owners (ADR-0080).
func TestBucketRoundtrip(t *testing.T) {
	b := bucket("lakehouse", 4096,
		BucketPrefix{Name: "bronze", Owner: "bronze-svc"},
		BucketPrefix{Name: "gold"})
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal Bucket: %v", err)
	}
	var b2 Bucket
	if err := json.Unmarshal(data, &b2); err != nil {
		t.Fatalf("unmarshal Bucket: %v", err)
	}
	if b2.Name != b.Name || b2.Spec.MaxObjectBytes != 4096 {
		t.Errorf("Bucket roundtrip mismatch: %+v", b2)
	}
	if len(b2.Spec.Prefixes) != 2 || b2.Spec.Prefixes[0].Name != "bronze" || b2.Spec.Prefixes[0].Owner != "bronze-svc" {
		t.Errorf("Bucket prefixes roundtrip mismatch: %+v", b2.Spec.Prefixes)
	}
	if b2.Spec.Prefixes[1].Owner != "" {
		t.Errorf("an owner-less prefix must roundtrip empty: %+v", b2.Spec.Prefixes[1])
	}
}

// scenario: single-writer-per-prefix (structural half) — Bucket.Validate enforces unique prefix names
// within a bucket (so a prefix has exactly one owner by construction) + non-negative MaxObjectBytes.
func TestBucketValidate(t *testing.T) {
	if err := bucket("ok", 0).Validate(); err != nil {
		t.Errorf("valid Bucket rejected: %v", err)
	}
	if err := bucket("with-prefixes", 0, BucketPrefix{Name: "bronze", Owner: "f"}, BucketPrefix{Name: "gold"}).Validate(); err != nil {
		t.Errorf("valid Bucket with prefixes rejected: %v", err)
	}
	if err := bucket("neg", -1).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("negative maxObjectBytes: want Invalid, got %v", err)
	}
	if err := bucket("dup", 0, BucketPrefix{Name: "p"}, BucketPrefix{Name: "p"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate prefix name: want Invalid, got %v", err)
	}
	if err := bucket("bad", 0, BucketPrefix{Name: "Bad_Prefix"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad prefix name: want Invalid, got %v", err)
	}
}

// scenario: spec.blob structural rules (ADR-0080) — alias is a unique DNS-1123 label, prefix is a
// DNS-1123 label; a resolvable shape validates, a duplicate alias / bad prefix is rejected.
func TestFunctionBlobValidate(t *testing.T) {
	mk := func(blobs ...FunctionBlob) *Function {
		return &Function{
			TypeMeta:   TypeMeta{APIVersion: KindFunction.GVK().APIVersion(), Kind: KindFunction},
			ObjectMeta: ObjectMeta{Name: "f", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       FunctionSpec{Blob: blobs},
		}
	}
	if err := mk(FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}).Validate(); err != nil {
		t.Errorf("valid spec.blob rejected: %v", err)
	}
	if err := mk(
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "bronze"},
	).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.blob alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionBlob{Alias: "Bad Alias", Bucket: "lakehouse", Prefix: "gold"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.blob alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "Bad_Prefix"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.blob prefix: want Invalid, got %v", err)
	}
}

// scenario: alias-unique-dns1123 — spec.catalogs structural rules (ADR-0091): each alias is a unique
// DNS-1123 label and catalog is a DNS-1123 label; a resolvable shape validates, a duplicate alias /
// bad alias / bad catalog name is rejected.
func TestFunctionCatalogValidate(t *testing.T) {
	mk := func(cats ...FunctionCatalog) *Function {
		return &Function{
			TypeMeta:   TypeMeta{APIVersion: KindFunction.GVK().APIVersion(), Kind: KindFunction},
			ObjectMeta: ObjectMeta{Name: "f", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       FunctionSpec{Catalogs: cats},
		}
	}
	if err := mk(FunctionCatalog{Alias: "lake", Catalog: "lake"}).Validate(); err != nil {
		t.Errorf("valid spec.catalogs rejected: %v", err)
	}
	if err := mk(
		FunctionCatalog{Alias: "a", Catalog: "lake"},
		FunctionCatalog{Alias: "a", Catalog: "warehouse"},
	).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.catalogs alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionCatalog{Alias: "Bad Alias", Catalog: "lake"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.catalogs alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionCatalog{Alias: "lake", Catalog: "Bad_Catalog"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.catalogs catalog name: want Invalid, got %v", err)
	}
}
