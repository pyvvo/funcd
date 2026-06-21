package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane/admission"
)

type fakeReader struct{ fns []*v1.Function }

func (f fakeReader) List(_ context.Context, _ v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	for _, fn := range f.fns {
		if fn.Namespace == ns {
			out = append(out, fn)
		}
	}
	return out, nil
}

func fn(name string, links ...v1.FunctionLink) *v1.Function {
	f := &v1.Function{Spec: v1.FunctionSpec{Links: links}}
	f.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return f
}

func lnk(alias, target string) v1.FunctionLink {
	return v1.FunctionLink{Alias: alias, Target: v1.ObjectName(target)}
}

// link-validity admission — table over {valid, target-missing, self-link, cycle} (ADR-0064).
func TestLinkValidityAdmission(t *testing.T) {
	gvk := v1.KindFunction.GVK()
	adm := func(store ...*v1.Function) admission.Admission {
		return admission.NewLinkValidityAdmission(fakeReader{fns: store})
	}
	require.True(t, adm().Handles(gvk, admission.Create))
	require.True(t, adm().Handles(gvk, admission.Update))
	require.False(t, adm().Handles(gvk, admission.Delete), "link-validity is Create/Update only")
	require.False(t, adm().Handles(v1.KindService.GVK(), admission.Create), "Function kind only")

	for _, tc := range []struct {
		name    string
		applied *v1.Function
		store   []*v1.Function
		valid   bool
	}{
		{"no links", fn("a"), []*v1.Function{fn("b")}, true},
		{"valid link to existing", fn("a", lnk("toB", "b")), []*v1.Function{fn("b")}, true},
		{"target missing", fn("a", lnk("toX", "x")), []*v1.Function{fn("b")}, false},
		{"self-link", fn("a", lnk("self", "a")), []*v1.Function{fn("b")}, false},
		{"two-node cycle", fn("a", lnk("toB", "b")), []*v1.Function{fn("b", lnk("toA", "a"))}, false},
		{"valid chain a→b→c", fn("a", lnk("toB", "b")), []*v1.Function{fn("b", lnk("toC", "c")), fn("c")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adm(tc.store...).Admit(context.Background(), admission.Request{
				Operation: admission.Create, GVK: gvk, Object: tc.applied,
			})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "an invalid link set ⇒ fault.Invalid")
			}
		})
	}
}

// link-deletion-protection admission — reject deleting a linked-to Function (ADR-0064).
func TestLinkDeletionProtectionAdmission(t *testing.T) {
	gvk := v1.KindFunction.GVK()
	adm := admission.NewLinkDeletionProtectionAdmission(fakeReader{fns: []*v1.Function{fn("a", lnk("toB", "b"))}})
	require.True(t, adm.Handles(gvk, admission.Delete))
	require.False(t, adm.Handles(gvk, admission.Create), "deletion-protection is Delete only")

	// deleting b (linked by a) → Conflict
	_, err := adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: fn("b")})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a linked-to function cannot be deleted")

	// deleting a (no dependents) → allowed
	_, err = adm.Admit(context.Background(), admission.Request{Operation: admission.Delete, GVK: gvk, Old: fn("a", lnk("toB", "b"))})
	require.NoError(t, err, "a function with no dependents is deletable")
}
