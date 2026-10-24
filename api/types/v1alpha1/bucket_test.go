package v1alpha1

import (
	"encoding/json"
	"strings"
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
		BucketPrefix{Name: "bronze", Owner: FunctionOwner("bronze-svc")},
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
	if len(b2.Spec.Prefixes) != 2 || b2.Spec.Prefixes[0].Name != "bronze" || b2.Spec.Prefixes[0].Owner != FunctionOwner("bronze-svc") {
		t.Errorf("Bucket prefixes roundtrip mismatch: %+v", b2.Spec.Prefixes)
	}
	if !b2.Spec.Prefixes[1].Owner.IsZero() {
		t.Errorf("an owner-less prefix must roundtrip empty: %+v", b2.Spec.Prefixes[1])
	}
}

// scenario: single-writer-per-prefix (structural half) — Bucket.Validate enforces unique prefix names
// within a bucket (so a prefix has exactly one owner by construction) + non-negative MaxObjectBytes.
func TestBucketValidate(t *testing.T) {
	if err := bucket("ok", 0).Validate(); err != nil {
		t.Errorf("valid Bucket rejected: %v", err)
	}
	if err := bucket("with-prefixes", 0, BucketPrefix{Name: "bronze", Owner: FunctionOwner("f")}, BucketPrefix{Name: "gold"}).Validate(); err != nil {
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
			Spec:       FunctionSpec{Bindings: FunctionBindings{Blob: blobs}},
		}
	}
	if err := mk(FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"}).Validate(); err != nil {
		t.Errorf("valid spec.blob rejected: %v", err)
	}
	if err := mk(
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "bronze"},
	).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.bindings.blob alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionBlob{Alias: "Bad Alias", Bucket: "lakehouse", Prefix: "gold"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.bindings.blob alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "Bad_Prefix"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.blob prefix: want Invalid, got %v", err)
	}
}

// scenario: alias-unique-dns1123 — spec.lakes structural rules (ADR-0091): each alias is a unique
// DNS-1123 label and lake is a DNS-1123 label; a resolvable shape validates, a duplicate alias /
// bad alias / bad lake name is rejected.
func TestFunctionLakeValidate(t *testing.T) {
	mk := func(cats ...FunctionLake) *Function {
		return &Function{
			TypeMeta:   TypeMeta{APIVersion: KindFunction.GVK().APIVersion(), Kind: KindFunction},
			ObjectMeta: ObjectMeta{Name: "f", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       FunctionSpec{Bindings: FunctionBindings{Lake: cats}},
		}
	}
	if err := mk(FunctionLake{Alias: "lake", Lake: "lake"}).Validate(); err != nil {
		t.Errorf("valid spec.lakes rejected: %v", err)
	}
	if err := mk(
		FunctionLake{Alias: "a", Lake: "lake"},
		FunctionLake{Alias: "a", Lake: "warehouse"},
	).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.bindings.lake alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionLake{Alias: "Bad Alias", Lake: "lake"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.bindings.lake alias: want Invalid, got %v", err)
	}
	if err := mk(FunctionLake{Alias: "lake", Lake: "Bad_Lake"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad spec.lakes lake name: want Invalid, got %v", err)
	}
}

// ADR-0275: a set prefix owner names a Function or a Lake, by a DNS-1123 label.
func TestBucketValidatePrefixOwner(t *testing.T) {
	for _, o := range []PrefixOwner{FunctionOwner("f"), LakeOwner("lake")} {
		if err := bucket("ok", 0, BucketPrefix{Name: "p", Owner: o}).Validate(); err != nil {
			t.Errorf("owner %+v rejected: %v", o, err)
		}
	}
	for _, o := range []PrefixOwner{{Kind: KindSite, Name: "s"}, {Kind: KindFunction}, {Name: "f"}, {Kind: KindLake, Name: "Bad_Name"}} {
		if err := bucket("bad", 0, BucketPrefix{Name: "p", Owner: o}).Validate(); fault.KindOf(err) != fault.Invalid {
			t.Errorf("owner %+v: want Invalid, got %v", o, err)
		}
	}
}

// scenario: string-owner-refused (ADR-0275) — a prefix owner written as a bare name does not decode, and the error
// names the field.
func TestScenarioStringOwnerRefused(t *testing.T) {
	var b Bucket
	err := json.Unmarshal([]byte(`{"spec":{"prefixes":[{"name":"p","owner":"ingest"}]}}`), &b)
	if err == nil || !strings.Contains(err.Error(), "spec.prefixes.owner") {
		t.Fatalf("want a decode error naming spec.prefixes.owner, got %v", err)
	}
}
