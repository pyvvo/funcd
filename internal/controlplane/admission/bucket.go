package admission

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// BlobProber probes whether a Bucket prefix holds any objects (ADR-0080): the deletion-protection
// admission uses it to block deleting a bucket (or removing a prefix) that still holds data. Declared
// HERE (like KVProber/StoreReader) so the admission package stays a near-leaf; the wiring adapts the
// blob driver's List to it. Optional — a nil prober skips the data-emptiness check (binding-protection
// still applies).
type BlobProber interface {
	// HasAny reports whether any object exists under prefix.
	HasAny(ctx context.Context, prefix string) (bool, error)
}

// bucketPrefix is the on-disk prefix a Bucket prefix sub-domain owns (ADR-0080): "<ns>/<bucket>/<prefix>/".
// The s3gateway prefixes every object key by it; the deletion-protection probe uses the same shape.
func bucketPrefix(ns v1.NamespaceName, bucket v1.ObjectName, prefix string) string {
	return string(ns) + "/" + string(bucket) + "/" + prefix + "/"
}

// --- bucket-count quota (ADR-0080): Create on Bucket ------------------------------------------

type bucketQuota struct {
	r               StoreReader
	maxPerNamespace int
}

// NewBucketQuotaAdmission returns the Validating admission that rejects creating a Bucket when its
// namespace already holds maxPerNamespace buckets (ADR-0080 bucket-count quota). maxPerNamespace ≤ 0
// disables the quota.
func NewBucketQuotaAdmission(r StoreReader, maxPerNamespace int) Admission {
	return bucketQuota{r: r, maxPerNamespace: maxPerNamespace}
}

func (bucketQuota) Name() string { return "bucket-count" }
func (bucketQuota) Phase() Phase { return Validating }

func (bucketQuota) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindBucket.GVK() && op == Create
}

func (a bucketQuota) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.bucket-count"
	if a.maxPerNamespace <= 0 {
		return req.Object, nil
	}
	b, ok := req.Object.(*v1.Bucket)
	if !ok {
		return req.Object, nil
	}
	existing, err := a.r.List(ctx, v1.KindBucket.GVK(), b.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list buckets in %q", b.Namespace)
	}
	if len(existing) >= a.maxPerNamespace {
		return nil, fault.Invalidf(op, "namespace %q already holds the maximum %d Buckets", b.Namespace, a.maxPerNamespace)
	}
	return req.Object, nil
}

// --- blob-binding-validity (ADR-0080): Create/Update on Function ------------------------------

type blobBindingValidity struct{ r StoreReader }

// NewBlobBindingValidityAdmission returns the Validating admission that enforces ADR-0080's blob binding
// rules on a Function Create/Update: every spec.blob entry names a (bucket, prefix) that exists in the
// function's namespace. (Clone of kv-binding-validity, scanning spec.blob instead of spec.kv.)
func NewBlobBindingValidityAdmission(r StoreReader) Admission { return blobBindingValidity{r: r} }

func (blobBindingValidity) Name() string { return "blob-binding-validity" }
func (blobBindingValidity) Phase() Phase { return Validating }

func (blobBindingValidity) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindFunction.GVK() && (op == Create || op == Update)
}

func (a blobBindingValidity) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.blob-binding-validity"
	fn, ok := req.Object.(*v1.Function)
	if !ok || len(fn.Spec.Blob) == 0 {
		return req.Object, nil
	}
	ns := fn.Namespace
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
	for _, bnd := range fn.Spec.Blob {
		set, ok := prefixes[bnd.Bucket]
		if !ok {
			return nil, fault.Invalidf(op, "spec.blob[%s].bucket %q does not exist in namespace %q", bnd.Alias, bnd.Bucket, ns)
		}
		if !set[bnd.Prefix] {
			return nil, fault.Invalidf(op, "spec.blob[%s].prefix %q does not exist in bucket %q", bnd.Alias, bnd.Prefix, bnd.Bucket)
		}
	}
	return req.Object, nil
}

// --- bucket-prefix-owner-exists (ADR-0080): Create/Update on Bucket ---------------------------

type bucketPrefixOwnerExists struct{ r StoreReader }

// NewBucketPrefixOwnerExistsAdmission returns the Validating admission that enforces ADR-0080's
// prefix-owner rule on a Bucket Create/Update: every prefixes[].owner (when set) is a real Function in
// the bucket's namespace. Single-owner-per-prefix is structural (unique prefix names in Validate) — this
// is the owner-EXISTS check. (Clone of kv-owner-exists, scanning spec.prefixes.)
func NewBucketPrefixOwnerExistsAdmission(r StoreReader) Admission {
	return bucketPrefixOwnerExists{r: r}
}

func (bucketPrefixOwnerExists) Name() string { return "bucket-prefix-owner-exists" }
func (bucketPrefixOwnerExists) Phase() Phase { return Validating }

func (bucketPrefixOwnerExists) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindBucket.GVK() && (op == Create || op == Update)
}

func (a bucketPrefixOwnerExists) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.bucket-prefix-owner-exists"
	b, ok := req.Object.(*v1.Bucket)
	if !ok {
		return req.Object, nil
	}
	// Only list functions if at least one prefix declares an owner.
	hasOwner := false
	for _, p := range b.Spec.Prefixes {
		if p.Owner != "" {
			hasOwner = true
			break
		}
	}
	if !hasOwner {
		return req.Object, nil
	}
	ns := b.Namespace
	fns, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	// ADR-0088: an add-on provider (CatalogService) is a first-class prefix owner too, so an owner may
	// be a Function OR a CatalogService (single-writer unchanged — one owner name per prefix).
	css, err := a.r.List(ctx, v1.KindCatalogService.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list catalogservices in %q", ns)
	}
	for _, p := range b.Spec.Prefixes {
		if p.Owner != "" && !nameExists(fns, p.Owner) && !nameExists(css, p.Owner) {
			return nil, fault.Invalidf(op, "spec.prefixes[%s].owner %q is not a Function or CatalogService in namespace %q", p.Name, p.Owner, ns)
		}
	}
	return req.Object, nil
}

// --- bucket-deletion-protection (ADR-0080): Delete AND Update on Bucket ------------------------

type bucketDeletionProtection struct {
	r StoreReader
	p BlobProber
}

// NewBucketDeletionProtectionAdmission returns the Validating admission that protects Bucket data
// (ADR-0080). On Delete: Conflict if any Function.spec.blob names the bucket OR any prefix still holds
// objects. On Update: Conflict if a prefix removed from spec.prefixes[] is still named by some
// Function.spec.blob OR still holds objects. (Clone of kvstore-deletion-protection, scanning spec.blob.)
// The prober is optional — a nil prober skips the data-emptiness check (binding-protection still applies).
func NewBucketDeletionProtectionAdmission(r StoreReader, p BlobProber) Admission {
	return bucketDeletionProtection{r: r, p: p}
}

func (bucketDeletionProtection) Name() string { return "bucket-deletion-protection" }
func (bucketDeletionProtection) Phase() Phase { return Validating }

func (bucketDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindBucket.GVK() && (op == Delete || op == Update)
}

func (a bucketDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	if req.Operation == Update {
		return a.admitUpdate(ctx, req)
	}
	return a.admitDelete(ctx, req)
}

// admitDelete blocks deleting a bucket still bound by a Function.spec.blob or still holding objects.
func (a bucketDeletionProtection) admitDelete(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.bucket-deletion-protection"
	if req.Old == nil {
		return nil, nil
	}
	oldB, ok := req.Old.(*v1.Bucket)
	if !ok {
		return req.Old, nil
	}
	bucket := oldB.Name
	ns := oldB.Namespace

	fns, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	for _, o := range fns {
		f, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		for _, bnd := range f.Spec.Blob {
			if bnd.Bucket == bucket {
				return nil, fault.Conflictf(op, "Bucket %q is bound by function %q (alias %q); remove the binding first", bucket, f.Name, bnd.Alias)
			}
		}
	}

	if a.p != nil {
		for _, p := range oldB.Spec.Prefixes {
			has, perr := a.p.HasAny(ctx, bucketPrefix(ns, bucket, p.Name))
			if perr != nil {
				return nil, fault.Wrapf(perr, fault.Internal, op, "probe bucket %q prefix %q for data", bucket, p.Name)
			}
			if has {
				return nil, fault.Conflictf(op, "Bucket %q prefix %q still holds objects; drain it first", bucket, p.Name)
			}
		}
	}
	return req.Old, nil
}

// admitUpdate blocks removing a prefix from spec.prefixes[] while it is still named by some
// Function.spec.blob or still holds objects.
func (a bucketDeletionProtection) admitUpdate(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.bucket-deletion-protection"
	if req.Old == nil {
		return req.Object, nil
	}
	oldB, ok := req.Old.(*v1.Bucket)
	if !ok {
		return req.Object, nil
	}
	newB, ok := req.Object.(*v1.Bucket)
	if !ok {
		return req.Object, nil
	}
	bucket := oldB.Name
	ns := oldB.Namespace

	kept := make(map[string]bool, len(newB.Spec.Prefixes))
	for _, p := range newB.Spec.Prefixes {
		kept[p.Name] = true
	}
	var removed []string
	for _, p := range oldB.Spec.Prefixes {
		if !kept[p.Name] {
			removed = append(removed, p.Name)
		}
	}
	if len(removed) == 0 {
		return req.Object, nil
	}

	fns, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	for _, name := range removed {
		for _, o := range fns {
			f, ok := o.(*v1.Function)
			if !ok {
				continue
			}
			for _, bnd := range f.Spec.Blob {
				if bnd.Bucket == bucket && bnd.Prefix == name {
					return nil, fault.Conflictf(op, "prefix %q of Bucket %q is still bound by function %q (alias %q); remove the binding first", name, bucket, f.Name, bnd.Alias)
				}
			}
		}
		if a.p != nil {
			has, perr := a.p.HasAny(ctx, bucketPrefix(ns, bucket, name))
			if perr != nil {
				return nil, fault.Wrapf(perr, fault.Internal, op, "probe bucket %q prefix %q for data", bucket, name)
			}
			if has {
				return nil, fault.Conflictf(op, "prefix %q of Bucket %q still holds objects; drain it first", name, bucket)
			}
		}
	}
	return req.Object, nil
}
