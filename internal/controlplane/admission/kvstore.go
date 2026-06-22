package admission

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// KVProber probes whether a KVStore holds any keys (ADR-0072): the deletion-protection admission
// uses it to block deleting a store that still holds data. Declared HERE (like StoreReader) so the
// admission package stays a near-leaf; the wiring adapts the kvstore driver's List to it.
type KVProber interface {
	// HasAny reports whether any key exists under prefix.
	HasAny(ctx context.Context, prefix string) (bool, error)
}

// storePrefix is the on-disk prefix a KVStore owns (ADR-0072): "<ns>/<store>/". The facade prefixes
// every key by it; the deletion-protection probe and the reconciler's DropPrefix use the same shape.
func storePrefix(ns v1.NamespaceName, store v1.ObjectName) string {
	return string(ns) + "/" + string(store) + "/"
}

// --- store-count quota (ADR-0072): Create on KVStore ------------------------------------------

type kvStoreQuota struct {
	r               StoreReader
	maxPerNamespace int
}

// NewKVStoreQuotaAdmission returns the Validating admission that rejects creating a KVStore when its
// namespace already holds maxPerNamespace stores (ADR-0072 store-count quota). maxPerNamespace ≤ 0
// disables the quota.
func NewKVStoreQuotaAdmission(r StoreReader, maxPerNamespace int) Admission {
	return kvStoreQuota{r: r, maxPerNamespace: maxPerNamespace}
}

func (kvStoreQuota) Name() string { return "kvstore-quota" }
func (kvStoreQuota) Phase() Phase { return Validating }

func (kvStoreQuota) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindKVStore.GVK() && op == Create
}

func (a kvStoreQuota) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.kvstore-quota"
	if a.maxPerNamespace <= 0 {
		return req.Object, nil
	}
	ks, ok := req.Object.(*v1.KVStore)
	if !ok {
		return req.Object, nil
	}
	existing, err := a.r.List(ctx, v1.KindKVStore.GVK(), ks.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list kvstores in %q", ks.Namespace)
	}
	if len(existing) >= a.maxPerNamespace {
		return nil, fault.Invalidf(op, "namespace %q already holds the maximum %d KVStores", ks.Namespace, a.maxPerNamespace)
	}
	return req.Object, nil
}

// --- grant-validity (ADR-0072): Create/Update on Grant ----------------------------------------

type grantValidity struct{ r StoreReader }

// NewGrantValidityAdmission returns the Validating admission that enforces ADR-0072's KV Grant rules
// on Create/Update: the referenced KVStore and subject Function exist in the Grant's namespace, and
// an rw Grant is the single writer (no OTHER rw Grant references the same store ⇒ Conflict).
func NewGrantValidityAdmission(r StoreReader) Admission { return grantValidity{r: r} }

func (grantValidity) Name() string { return "grant-validity" }
func (grantValidity) Phase() Phase { return Validating }

func (grantValidity) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindGrant.GVK() && (op == Create || op == Update)
}

func (a grantValidity) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.grant-validity"
	g, ok := req.Object.(*v1.Grant)
	if !ok {
		return req.Object, nil
	}
	ns := g.Namespace

	// The referenced KVStore must exist in the Grant's namespace.
	stores, err := a.r.List(ctx, v1.KindKVStore.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list kvstores in %q", ns)
	}
	if !nameExists(stores, g.Spec.Store) {
		return nil, fault.Invalidf(op, "spec.store %q does not exist in namespace %q", g.Spec.Store, ns)
	}

	// The subject Function must exist in the Grant's namespace.
	fns, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	if !nameExists(fns, g.Spec.Function) {
		return nil, fault.Invalidf(op, "spec.function %q does not exist in namespace %q", g.Spec.Function, ns)
	}

	// Single-writer: at most one rw Grant per store (ADR-0072). A second rw Grant for the same
	// store ⇒ Conflict. A Grant updating itself does not conflict with its own prior rw row.
	if g.Spec.Mode == v1.KVModeRW {
		grants, lerr := a.r.List(ctx, v1.KindGrant.GVK(), ns)
		if lerr != nil {
			return nil, fault.Wrapf(lerr, fault.Internal, op, "list grants in %q", ns)
		}
		for _, o := range grants {
			other, ok := o.(*v1.Grant)
			if !ok || other.Name == g.Name {
				continue
			}
			if other.Spec.Mode == v1.KVModeRW && other.Spec.Store == g.Spec.Store {
				return nil, fault.Conflictf(op, "store %q already has an rw Grant (%q); a store has a single writer", g.Spec.Store, other.Name)
			}
		}
	}
	return req.Object, nil
}

// --- kvstore-deletion-protection (ADR-0072): Delete on KVStore --------------------------------

type kvStoreDeletionProtection struct {
	r StoreReader
	p KVProber
}

// NewKVStoreDeletionProtectionAdmission returns the Validating admission that rejects deleting a
// KVStore that is still referenced by a Grant OR still holds keys (ADR-0072): remove grants /
// drain first. It reads Request.Old.
func NewKVStoreDeletionProtectionAdmission(r StoreReader, p KVProber) Admission {
	return kvStoreDeletionProtection{r: r, p: p}
}

func (kvStoreDeletionProtection) Name() string { return "kvstore-deletion-protection" }
func (kvStoreDeletionProtection) Phase() Phase { return Validating }

func (kvStoreDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindKVStore.GVK() && op == Delete
}

func (a kvStoreDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.kvstore-deletion-protection"
	if req.Old == nil {
		return nil, nil
	}
	store := req.Old.GetObjectMeta().Name
	ns := req.Old.GetObjectMeta().Namespace

	grants, err := a.r.List(ctx, v1.KindGrant.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list grants in %q", ns)
	}
	for _, o := range grants {
		g, ok := o.(*v1.Grant)
		if ok && g.Spec.Store == store {
			return nil, fault.Conflictf(op, "KVStore %q is referenced by Grant %q; remove grants first", store, g.Name)
		}
	}

	if a.p != nil {
		has, perr := a.p.HasAny(ctx, storePrefix(ns, store))
		if perr != nil {
			return nil, fault.Wrapf(perr, fault.Internal, op, "probe kvstore %q for data", store)
		}
		if has {
			return nil, fault.Conflictf(op, "KVStore %q still holds keys; drain it first", store)
		}
	}
	return req.Old, nil
}

// nameExists reports whether any object in objs has the given name.
func nameExists(objs []v1.Object, name v1.ObjectName) bool {
	for _, o := range objs {
		if o.GetObjectMeta().Name == name {
			return true
		}
	}
	return false
}
