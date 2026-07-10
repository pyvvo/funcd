package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
)

// kvReader is a multi-kind fake StoreReader: it returns the stored objects of the requested kind in ns.
type kvReader struct {
	stores []*v1.KVStore
	fns    []*v1.Function
}

func (r kvReader) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	switch gvk.Kind {
	case v1.KindKVStore:
		for _, s := range r.stores {
			if s.Namespace == ns {
				out = append(out, s)
			}
		}
	case v1.KindFunction:
		for _, f := range r.fns {
			if f.Namespace == ns {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

type fakeProber struct{ has bool }

func (p fakeProber) HasAny(_ context.Context, _ string) (bool, error) { return p.has, nil }

func mkStore(name string, tables ...v1.KVTable) *v1.KVStore {
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	ks.Name, ks.Namespace, ks.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	ks.Spec.Tables = tables
	return ks
}

func mkFn(name string, kv ...v1.FunctionKV) *v1.Function {
	f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	f.Spec.KV = kv
	return f
}

func kvb(alias, store, table string) v1.FunctionKV {
	return v1.FunctionKV{Alias: alias, Store: v1.ObjectName(store), Table: table}
}

// scenario: store-count-quota — a namespace at its KVStore cap rejects another Create (Invalid).
func TestScenarioStoreCountQuota(t *testing.T) {
	gvk := v1.KindKVStore.GVK()
	r := kvReader{stores: []*v1.KVStore{mkStore("a"), mkStore("b")}}
	adm := admission.NewKVStoreQuotaAdmission(r, 2)
	require.True(t, adm.Handles(gvk, admission.Create))
	require.False(t, adm.Handles(gvk, admission.Delete))

	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkStore("c")})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "over-quota Create ⇒ Invalid")

	// under cap ⇒ allowed
	adm2 := admission.NewKVStoreQuotaAdmission(kvReader{stores: []*v1.KVStore{mkStore("a")}}, 2)
	_, err = adm2.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkStore("c")})
	require.NoError(t, err)
}

// scenario: deletion-protected — a store named by some Function.spec.kv or still holding keys can't be
// deleted (Conflict); a clean store is deletable.
func TestScenarioKVStoreDeletionProtection(t *testing.T) {
	gvk := v1.KindKVStore.GVK()

	// bound by a Function.spec.kv ⇒ Conflict
	rBound := kvReader{fns: []*v1.Function{mkFn("svc", kvb("c", "orders", "customers"))}}
	adm := admission.NewKVStoreDeletionProtectionAdmission(rBound, fakeProber{has: false})
	require.True(t, adm.Handles(gvk, admission.Delete))
	require.True(t, adm.Handles(gvk, admission.Update))
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("orders")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a bound store can't be deleted")

	// no bindings but non-empty data ⇒ Conflict
	adm = admission.NewKVStoreDeletionProtectionAdmission(kvReader{}, fakeProber{has: true})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("orders")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty store can't be deleted")

	// no bindings, no data ⇒ allowed
	adm = admission.NewKVStoreDeletionProtectionAdmission(kvReader{}, fakeProber{has: false})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("orders")})
	require.NoError(t, err, "a clean store is deletable")
}

// scenario: table-removal-protected-and-reclaimed (admission half) — removing a table from spec.tables[]
// while it is still bound by some Function.spec.kv is rejected on Update (Conflict); removing an unbound
// table is allowed.
func TestScenarioTableRemovalProtected(t *testing.T) {
	gvk := v1.KindKVStore.GVK()
	rBound := kvReader{fns: []*v1.Function{mkFn("svc", kvb("c", "orders", "fulfillment"))}}
	adm := admission.NewKVStoreDeletionProtectionAdmission(rBound, fakeProber{has: false})

	old := mkStore("orders",
		v1.KVTable{Name: "customers", Owner: "svc"},
		v1.KVTable{Name: "fulfillment", Owner: "svc"})
	// the update drops "fulfillment", which is still bound ⇒ Conflict
	newKS := mkStore("orders", v1.KVTable{Name: "customers", Owner: "svc"})
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: old, Object: newKS})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "removing a still-bound table ⇒ Conflict")

	// dropping an UNbound table is allowed
	rUnbound := kvReader{fns: []*v1.Function{mkFn("svc", kvb("c", "orders", "customers"))}}
	adm2 := admission.NewKVStoreDeletionProtectionAdmission(rUnbound, fakeProber{has: false})
	_, err = adm2.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Old: old, Object: newKS})
	require.NoError(t, err, "removing an unbound table is allowed")
}
