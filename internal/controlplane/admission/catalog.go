package admission

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// --- catalog-blob-validity (ADR-0086): Create/Update on CatalogService ------------------------

type catalogBlobValidity struct{ r StoreReader }

// NewCatalogBlobValidityAdmission returns the Validating admission that enforces ADR-0086's blob
// rules on a CatalogService Create/Update: every spec.blob entry AND spec.catalog name a
// (bucket, prefix) that exists in the service's namespace. (Clone of blob-binding-validity, adding
// the spec.catalog (bucket, prefix) check.) Cross-resource existence only — the owner-is-this-service
// single-writer rule is structural to the materialized Function's prefix ownership (ADR-0080).
func NewCatalogBlobValidityAdmission(r StoreReader) Admission { return catalogBlobValidity{r: r} }

func (catalogBlobValidity) Name() string { return "catalog-blob-validity" }
func (catalogBlobValidity) Phase() Phase { return Validating }

func (catalogBlobValidity) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindCatalogService.GVK() && (op == Create || op == Update)
}

func (a catalogBlobValidity) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.catalog-blob-validity"
	cs, ok := req.Object.(*v1.CatalogService)
	if !ok {
		return req.Object, nil
	}
	ns := cs.Namespace
	buckets, err := a.r.List(ctx, v1.KindBucket.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list buckets in %q", ns)
	}
	prefixes := map[v1.ObjectName]map[string]bool{} // bucket name → set of prefix names
	for _, o := range buckets {
		b, ok := o.(*v1.Bucket)
		if !ok {
			continue
		}
		set := make(map[string]bool, len(b.Spec.Prefixes))
		for _, p := range b.Spec.Prefixes {
			set[p.Name] = true
		}
		prefixes[b.Name] = set
	}
	exists := func(bucket v1.ObjectName, prefix string) (bucketOK, prefixOK bool) {
		set, ok := prefixes[bucket]
		if !ok {
			return false, false
		}
		return true, set[prefix]
	}
	for _, bnd := range cs.Spec.Blob {
		bucketOK, prefixOK := exists(bnd.Bucket, bnd.Prefix)
		if !bucketOK {
			return nil, fault.Invalidf(op, "spec.blob[%s].bucket %q does not exist in namespace %q", bnd.Alias, bnd.Bucket, ns)
		}
		if !prefixOK {
			return nil, fault.Invalidf(op, "spec.blob[%s].prefix %q does not exist in bucket %q", bnd.Alias, bnd.Prefix, bnd.Bucket)
		}
	}
	bucketOK, prefixOK := exists(cs.Spec.Catalog.Bucket, cs.Spec.Catalog.Prefix)
	if !bucketOK {
		return nil, fault.Invalidf(op, "spec.catalog.bucket %q does not exist in namespace %q", cs.Spec.Catalog.Bucket, ns)
	}
	if !prefixOK {
		return nil, fault.Invalidf(op, "spec.catalog.prefix %q does not exist in bucket %q", cs.Spec.Catalog.Prefix, cs.Spec.Catalog.Bucket)
	}
	return req.Object, nil
}
