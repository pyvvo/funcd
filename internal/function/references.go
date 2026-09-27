package function

import (
	"context"
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// resolveDataReferences enforces ADR-0121's RECONCILE-TIME cross-resource existence for a function's data
// bindings: every spec.blob (bucket, prefix) and spec.kv (store, table) must name a resource that exists in
// the function's namespace. This is the existence check the removed `blob-binding-validity` /
// `kv-binding-validity` admissions used to make synchronously at apply time — now made here so a function
// naming a not-yet-applied Bucket/KVStore is ADMITTED and converges when the referent appears.
//
// A missing referent returns requeue=true with a Reason/Message; the caller holds the function not-Ready
// (Phase=Pending) and re-reconciles. Fail-closed: the Cedar PEP denies the bound access until the referent
// exists (the binding-as-grant materializes over a resource that isn't there), so waiting merely avoids
// booting a worker whose grants can't resolve — it does not widen any authorization.
func (r *Reconciler) resolveDataReferences(ctx context.Context, fn *v1.Function) (requeue bool, reason, message string, err error) {
	const op = "function.resolveDataReferences"
	ns := fn.Namespace

	if len(fn.Spec.Blob) > 0 {
		bl, lerr := r.store.List(ctx, v1.KindBucket.GVK(), store.ListOptions{Namespace: ns})
		if lerr != nil {
			return false, "", "", fault.Wrapf(lerr, fault.KindOf(lerr), op, "list buckets in %q", ns)
		}
		prefixes := map[v1.ObjectName]map[string]bool{} // bucket name → set of prefix names
		for _, o := range bl.Items {
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
				return true, "BucketNotFound", fmt.Sprintf("spec.blob[%s] → bucket %q not found in namespace %q; waiting", bnd.Alias, bnd.Bucket, ns), nil
			}
			if !set[bnd.Prefix] {
				return true, "BucketNotFound", fmt.Sprintf("spec.blob[%s] → prefix %q not found in bucket %q; waiting", bnd.Alias, bnd.Prefix, bnd.Bucket), nil
			}
		}
	}

	if len(fn.Spec.KV) > 0 {
		sl, lerr := r.store.List(ctx, v1.KindKVStore.GVK(), store.ListOptions{Namespace: ns})
		if lerr != nil {
			return false, "", "", fault.Wrapf(lerr, fault.KindOf(lerr), op, "list kvstores in %q", ns)
		}
		tables := map[v1.ObjectName]map[string]bool{} // store name → set of table names
		for _, o := range sl.Items {
			ks, ok := o.(*v1.KVStore)
			if !ok {
				continue
			}
			set := make(map[string]bool, len(ks.Spec.Tables))
			for _, tb := range ks.Spec.Tables {
				set[tb.Name] = true
			}
			tables[ks.Name] = set
		}
		for _, bnd := range fn.Spec.KV {
			set, ok := tables[bnd.Store]
			if !ok {
				return true, "KVStoreNotFound", fmt.Sprintf("spec.kv[%s] → store %q not found in namespace %q; waiting", bnd.Alias, bnd.Store, ns), nil
			}
			if !set[bnd.Table] {
				return true, "KVStoreNotFound", fmt.Sprintf("spec.kv[%s] → table %q not found in store %q; waiting", bnd.Alias, bnd.Table, bnd.Store), nil
			}
		}
	}

	return false, "", "", nil
}
