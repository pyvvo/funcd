package v1alpha1

import (
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func catalogService(name string, catalog CatalogRef, blob ...FunctionBlob) *CatalogService {
	return &CatalogService{
		TypeMeta:   TypeMeta{APIVersion: KindCatalogService.GVK().APIVersion(), Kind: KindCatalogService},
		ObjectMeta: ObjectMeta{Name: ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       CatalogServiceSpec{Blob: blob, Catalog: catalog},
	}
}

// scenario: catalog-must-be-a-bound-blob (ADR-0086 structural rule) — CatalogService.Validate enforces
// the spec.blob structural rules (alias unique DNS-1123 label, prefix DNS-1123 label) AND that the
// catalog (bucket, prefix) is one of the spec.blob bindings (so the engine gets the per-fn S3 keypair
// and can write its own DuckLake catalog). Cross-resource existence is the admission, not Validate.
func TestCatalogServiceValidate(t *testing.T) {
	goldCatalog := CatalogRef{Bucket: "lakehouse", Prefix: "gold"}

	// valid: the catalog (lakehouse/gold) is a declared blob binding.
	if err := catalogService("ok", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}).Validate(); err != nil {
		t.Errorf("valid CatalogService rejected: %v", err)
	}

	// valid: extra bindings alongside the catalog one.
	if err := catalogService("ok2", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); err != nil {
		t.Errorf("valid CatalogService with extra bindings rejected: %v", err)
	}

	// invalid: the catalog (bucket, prefix) is NOT one of spec.blob — the engine would get no keypair.
	if err := catalogService("unbound", goldCatalog,
		FunctionBlob{Alias: "bronze", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("catalog not in spec.blob: want Invalid, got %v", err)
	}

	// invalid: a Catalog set with an empty spec.blob (implies the catalog can't be bound).
	if err := catalogService("noblob", goldCatalog).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("Catalog set with empty spec.blob: want Invalid, got %v", err)
	}

	// invalid: same bucket but a different prefix is not the catalog binding.
	if err := catalogService("wrongprefix", goldCatalog,
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("catalog prefix not bound: want Invalid, got %v", err)
	}

	// invalid: a duplicate spec.blob alias is still rejected (the structural loop runs first).
	if err := catalogService("dupalias", goldCatalog,
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "gold"},
		FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "bronze"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("duplicate spec.blob alias: want Invalid, got %v", err)
	}

	// invalid: a non-DNS-1123 catalog prefix is rejected before the binding check.
	if err := catalogService("badprefix", CatalogRef{Bucket: "lakehouse", Prefix: "Bad_Prefix"},
		FunctionBlob{Alias: "catalog", Bucket: "lakehouse", Prefix: "gold"}).Validate(); fault.KindOf(err) != fault.Invalid {
		t.Errorf("bad catalog prefix: want Invalid, got %v", err)
	}
}
