package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
)

func mkCatalogService(name string, catalog v1.CatalogRef, blob ...v1.FunctionBlob) *v1.CatalogService {
	cs := &v1.CatalogService{TypeMeta: v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}}
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cs.Spec.Blob = blob
	cs.Spec.Catalog = catalog
	return cs
}

// scenario: catalog-blob-validity — a CatalogService whose spec.blob OR spec.catalog references a
// missing Bucket/prefix is rejected (Invalid); a fully-resolvable one passes. (Mirrors the bucket
// blob-binding-validity admission, adding the spec.catalog (bucket, prefix) check.)
func TestScenarioCatalogBlobValidity(t *testing.T) {
	gvk := v1.KindCatalogService.GVK()
	r := blobReader{buckets: []*v1.Bucket{mkBucket("lakehouse",
		v1.BucketPrefix{Name: "gold", Owner: "lake-duckdb"},
		v1.BucketPrefix{Name: "bronze", Owner: "lake-duckdb"})}}
	adm := admission.NewCatalogBlobValidityAdmission(r)
	require.True(t, adm.Handles(gvk, admission.Create))
	require.True(t, adm.Handles(gvk, admission.Update))
	require.False(t, adm.Handles(v1.KindBucket.GVK(), admission.Create))

	goldCatalog := v1.CatalogRef{Bucket: "lakehouse", Prefix: "gold"}

	// spec.blob names a missing bucket ⇒ Invalid.
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk,
		Object: mkCatalogService("lake", goldCatalog, blobBnd("b", "missing", "gold"))})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "spec.blob to a missing bucket ⇒ Invalid")

	// spec.blob names a missing prefix ⇒ Invalid.
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk,
		Object: mkCatalogService("lake", goldCatalog, blobBnd("b", "lakehouse", "missing"))})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "spec.blob to a missing prefix ⇒ Invalid")

	// spec.catalog names a missing prefix ⇒ Invalid.
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk,
		Object: mkCatalogService("lake", v1.CatalogRef{Bucket: "lakehouse", Prefix: "missing"},
			blobBnd("b", "lakehouse", "gold"))})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "spec.catalog to a missing prefix ⇒ Invalid")

	// spec.catalog names a missing bucket ⇒ Invalid.
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk,
		Object: mkCatalogService("lake", v1.CatalogRef{Bucket: "missing", Prefix: "gold"},
			blobBnd("b", "lakehouse", "gold"))})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "spec.catalog to a missing bucket ⇒ Invalid")

	// every reference resolves ⇒ allowed.
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk,
		Object: mkCatalogService("lake", goldCatalog,
			blobBnd("g", "lakehouse", "gold"), blobBnd("b", "lakehouse", "bronze"))})
	require.NoError(t, err, "a fully-resolvable CatalogService is allowed")
}
