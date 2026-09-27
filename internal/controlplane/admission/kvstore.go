package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// KVProber probes whether a KVStore holds any keys (ADR-0072): the deletion-protection admission uses
// it to block deleting a store that still holds data. Declared HERE (like StoreReader) so the admission
// package stays a near-leaf; the wiring adapts the kvstore driver's List to it.
type KVProber interface {
	// HasAny reports whether any key exists under prefix.
	HasAny(ctx context.Context, prefix string) (bool, error)
}

// storePrefix is the on-disk prefix a KVStore owns (ADR-0072/0073): "<ns>/<store>/". The facade prefixes
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

// --- kvstore-deletion-protection (ADR-0073): Delete AND Update on KVStore ----------------------

type kvStoreDeletionProtection struct {
	r StoreReader
	p KVProber
}

// NewKVStoreDeletionProtectionAdmission returns the Validating admission that protects KVStore data
// (ADR-0073). On Delete: Conflict if any Function.spec.kv names the store OR it still holds keys. On
// Update: Conflict if a table removed from spec.tables[] is still named by some Function.spec.kv. (Clone
// of link-deletion-protection, scanning spec.kv.)
func NewKVStoreDeletionProtectionAdmission(r StoreReader, p KVProber) Admission {
	return kvStoreDeletionProtection{r: r, p: p}
}

func (kvStoreDeletionProtection) Name() string { return "kvstore-deletion-protection" }
func (kvStoreDeletionProtection) Phase() Phase { return Validating }

func (kvStoreDeletionProtection) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindKVStore.GVK() && (op == Delete || op == Update)
}

func (a kvStoreDeletionProtection) Admit(ctx context.Context, req Request) (v1.Object, error) {
	if req.Operation == Update {
		return a.admitUpdate(ctx, req)
	}
	return a.admitDelete(ctx, req)
}

// admitDelete blocks deleting a store still bound by a Function.spec.kv or still holding keys.
func (a kvStoreDeletionProtection) admitDelete(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.kvstore-deletion-protection"
	if req.Old == nil {
		return nil, nil
	}
	store := req.Old.GetObjectMeta().Name
	ns := req.Old.GetObjectMeta().Namespace

	fns, err := a.r.List(ctx, v1.KindFunction.GVK(), ns)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list functions in %q", ns)
	}
	for _, o := range fns {
		f, ok := o.(*v1.Function)
		if !ok {
			continue
		}
		for _, b := range f.Spec.KV {
			if b.Store == store {
				return nil, fault.Conflictf(op, "KVStore %q is bound by function %q (alias %q); remove the binding first", store, f.Name, b.Alias)
			}
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

// admitUpdate blocks removing a table from spec.tables[] while it is still named by some Function.spec.kv.
func (a kvStoreDeletionProtection) admitUpdate(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.kvstore-deletion-protection"
	if req.Old == nil {
		return req.Object, nil
	}
	oldKS, ok := req.Old.(*v1.KVStore)
	if !ok {
		return req.Object, nil
	}
	newKS, ok := req.Object.(*v1.KVStore)
	if !ok {
		return req.Object, nil
	}
	store := oldKS.Name
	ns := oldKS.Namespace

	kept := make(map[string]bool, len(newKS.Spec.Tables))
	for _, tb := range newKS.Spec.Tables {
		kept[tb.Name] = true
	}
	var removed []string
	for _, tb := range oldKS.Spec.Tables {
		if !kept[tb.Name] {
			removed = append(removed, tb.Name)
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
			for _, b := range f.Spec.KV {
				if b.Store == store && b.Table == name {
					return nil, fault.Conflictf(op, "table %q of KVStore %q is still bound by function %q (alias %q); remove the binding first", name, store, f.Name, b.Alias)
				}
			}
		}
	}
	return req.Object, nil
}
