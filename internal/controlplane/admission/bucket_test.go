package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

// blobReader is a multi-kind fake StoreReader: it returns the stored objects of the requested kind in ns.
type blobReader struct {
	buckets []*v1.Bucket
	fns     []*v1.Function
	css     []*v1.CatalogService // add-on providers (ADR-0088): a valid prefix owner too
}

func (r blobReader) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	switch gvk.Kind {
	case v1.KindBucket:
		for _, b := range r.buckets {
			if b.Namespace == ns {
				out = append(out, b)
			}
		}
	case v1.KindFunction:
		for _, f := range r.fns {
			if f.Namespace == ns {
				out = append(out, f)
			}
		}
	case v1.KindCatalogService:
		for _, c := range r.css {
			if c.Namespace == ns {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

type blobFakeProber struct{ has bool }

func (p blobFakeProber) HasAny(_ context.Context, _ string) (bool, error) { return p.has, nil }

func mkBucket(name string, prefixes ...v1.BucketPrefix) *v1.Bucket {
	b := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	b.Name, b.Namespace, b.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	b.Spec.Prefixes = prefixes
	return b
}

func mkBlobFn(name string, blobs ...v1.FunctionBlob) *v1.Function {
	f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	f.Spec.Blob = blobs
	return f
}

func blobBnd(alias, bucket, prefix string) v1.FunctionBlob {
	return v1.FunctionBlob{Alias: alias, Bucket: v1.ObjectName(bucket), Prefix: prefix}
}

// scenario: bucket-count-quota — a namespace at its Bucket cap rejects another Create (Invalid).
func TestScenarioBucketCountQuota(t *testing.T) {
	gvk := v1.KindBucket.GVK()
	r := blobReader{buckets: []*v1.Bucket{mkBucket("a"), mkBucket("b")}}
	adm := admission.NewBucketQuotaAdmission(r, 2)
	require.True(t, adm.Handles(gvk, admission.Create))
	require.False(t, adm.Handles(gvk, admission.Delete))

	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkBucket("c")})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "over-quota Create ⇒ Invalid")

	adm2 := admission.NewBucketQuotaAdmission(blobReader{buckets: []*v1.Bucket{mkBucket("a")}}, 2)
	_, err = adm2.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkBucket("c")})
	require.NoError(t, err)
}

// scenario: deletion-protected — a bucket named by some Function.spec.blob or still holding objects can't
// be deleted (Conflict); a clean bucket is deletable. The data check is gated on the optional prober.
func TestScenarioBucketDeletionProtection(t *testing.T) {
	gvk := v1.KindBucket.GVK()

	rBound := blobReader{fns: []*v1.Function{mkBlobFn("svc", blobBnd("c", "lakehouse", "gold"))}}
	adm := admission.NewBucketDeletionProtectionAdmission(rBound, blobFakeProber{has: false})
	require.True(t, adm.Handles(gvk, admission.Delete))
	require.True(t, adm.Handles(gvk, admission.Update))
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkBucket("lakehouse", v1.BucketPrefix{Name: "gold"})})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a bound bucket can't be deleted")

	// no bindings but non-empty data ⇒ Conflict
	adm = admission.NewBucketDeletionProtectionAdmission(blobReader{}, blobFakeProber{has: true})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkBucket("lakehouse", v1.BucketPrefix{Name: "gold"})})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty bucket can't be deleted")

	// no bindings, no data ⇒ allowed
	adm = admission.NewBucketDeletionProtectionAdmission(blobReader{}, blobFakeProber{has: false})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkBucket("lakehouse", v1.BucketPrefix{Name: "gold"})})
	require.NoError(t, err, "a clean bucket is deletable")

	// a nil prober skips the data check (binding-protection still applies) — a clean bucket is deletable.
	admNil := admission.NewBucketDeletionProtectionAdmission(blobReader{}, nil)
	_, err = admNil.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkBucket("lakehouse", v1.BucketPrefix{Name: "gold"})})
	require.NoError(t, err, "a nil prober skips the data check")
}

// scenario: prefix-removal-protected (admission half) — removing a prefix from spec.prefixes[] while it
// is still bound by some Function.spec.blob is rejected on Update (Conflict); removing an unbound prefix
// is allowed.
func TestScenarioBucketPrefixRemovalProtected(t *testing.T) {
	gvk := v1.KindBucket.GVK()
	rBound := blobReader{fns: []*v1.Function{mkBlobFn("svc", blobBnd("c", "lakehouse", "bronze"))}}
	adm := admission.NewBucketDeletionProtectionAdmission(rBound, blobFakeProber{has: false})

	old := mkBucket("lakehouse",
		v1.BucketPrefix{Name: "gold", Owner: "svc"},
		v1.BucketPrefix{Name: "bronze", Owner: "svc"})
	// the update drops "bronze", which is still bound ⇒ Conflict
	newB := mkBucket("lakehouse", v1.BucketPrefix{Name: "gold", Owner: "svc"})
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: old, Object: newB})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "removing a still-bound prefix ⇒ Conflict")

	// dropping an UNbound prefix is allowed
	rUnbound := blobReader{fns: []*v1.Function{mkBlobFn("svc", blobBnd("c", "lakehouse", "gold"))}}
	adm2 := admission.NewBucketDeletionProtectionAdmission(rUnbound, blobFakeProber{has: false})
	_, err = adm2.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: old, Object: newB})
	require.NoError(t, err, "removing an unbound prefix is allowed")
}
