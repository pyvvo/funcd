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
	grants []*v1.Grant
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
	case v1.KindGrant:
		for _, g := range r.grants {
			if g.Namespace == ns {
				out = append(out, g)
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

func mkStore(name string) *v1.KVStore {
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	ks.Name, ks.Namespace, ks.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return ks
}

func mkGrant(name, fn, store string, mode v1.KVMode) *v1.Grant {
	g := &v1.Grant{TypeMeta: v1.TypeMeta{APIVersion: v1.KindGrant.GVK().APIVersion(), Kind: v1.KindGrant}}
	g.Name, g.Namespace, g.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	g.Spec = v1.GrantSpec{Function: v1.ObjectName(fn), Binding: "b", Store: v1.ObjectName(store), Mode: mode}
	return g
}

func mkFn(name string) *v1.Function {
	f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return f
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

// scenario: single-writer-enforced (admission half) — a second rw Grant for a store ⇒ Conflict.
func TestScenarioSingleWriterEnforcedAdmission(t *testing.T) {
	gvk := v1.KindGrant.GVK()
	r := kvReader{
		stores: []*v1.KVStore{mkStore("s")},
		fns:    []*v1.Function{mkFn("f1"), mkFn("f2")},
		grants: []*v1.Grant{mkGrant("g1", "f1", "s", v1.KVModeRW)},
	}
	adm := admission.NewGrantValidityAdmission(r)
	require.True(t, adm.Handles(gvk, admission.Create))
	require.True(t, adm.Handles(gvk, admission.Update))

	// a second rw Grant for s ⇒ Conflict
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkGrant("g2", "f2", "s", v1.KVModeRW)})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a store has a single writer")

	// a second RO Grant for s ⇒ allowed (read sharing)
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkGrant("g3", "f2", "s", v1.KVModeRO)})
	require.NoError(t, err)

	// updating the SAME rw Grant ⇒ allowed (no self-conflict)
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Update, GVK: gvk, Object: mkGrant("g1", "f1", "s", v1.KVModeRW)})
	require.NoError(t, err)
}

// scenario: grant-validity-references — missing store or function ⇒ Invalid.
func TestScenarioGrantValidityReferences(t *testing.T) {
	gvk := v1.KindGrant.GVK()
	r := kvReader{stores: []*v1.KVStore{mkStore("s")}, fns: []*v1.Function{mkFn("f1")}}
	adm := admission.NewGrantValidityAdmission(r)

	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkGrant("g", "f1", "missing", v1.KVModeRO)})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "missing store ⇒ Invalid")

	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkGrant("g", "missing", "s", v1.KVModeRO)})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "missing function ⇒ Invalid")

	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Create, GVK: gvk, Object: mkGrant("g", "f1", "s", v1.KVModeRW)})
	require.NoError(t, err, "all refs present ⇒ allowed")
}

// scenario: deletion-protected-by-grants / -by-data — a referenced or non-empty store can't be deleted.
func TestScenarioKVStoreDeletionProtection(t *testing.T) {
	gvk := v1.KindKVStore.GVK()

	// referenced by a Grant ⇒ Conflict
	rGrant := kvReader{grants: []*v1.Grant{mkGrant("g", "f", "s", v1.KVModeRW)}}
	adm := admission.NewKVStoreDeletionProtectionAdmission(rGrant, fakeProber{has: false})
	require.True(t, adm.Handles(gvk, admission.Delete))
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("s")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a referenced store can't be deleted")

	// no grants but non-empty data ⇒ Conflict
	adm = admission.NewKVStoreDeletionProtectionAdmission(kvReader{}, fakeProber{has: true})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("s")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a non-empty store can't be deleted")

	// no grants, no data ⇒ allowed
	adm = admission.NewKVStoreDeletionProtectionAdmission(kvReader{}, fakeProber{has: false})
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: mkStore("s")})
	require.NoError(t, err, "a clean store is deletable")
}
