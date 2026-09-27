package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

func svc(typ v1.ServiceType, kv *v1.KVServiceSpec) *v1.Service {
	s := &v1.Service{Spec: v1.ServiceSpec{Type: typ, KV: kv}}
	s.TypeMeta = v1.TypeMeta{APIVersion: v1.KindService.GVK().APIVersion(), Kind: v1.KindService}
	s.Name, s.Namespace, s.ResourceGroup = "s", "default", "rg1"
	return s
}

func nsValid() *v1.Namespace {
	n := &v1.Namespace{}
	n.TypeMeta = v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}
	n.Name, n.ResourceGroup = "team-a", "rg1"
	return n
}

// the validate admission delegates to obj.Validate() for Create/Update — a matrix over valid +
// invalid objects (the deep per-field value matrix lives in api/types/v1alpha1; here we prove the
// admission correctly admits the valid and rejects the invalid as fault.Invalid).
func TestValidateAdmission(t *testing.T) {
	va := admission.NewValidateAdmission()
	require.Equal(t, "validate", va.Name())
	require.Equal(t, admission.Validating, va.Phase())
	require.True(t, va.Handles(v1.KindService.GVK(), admission.Create))
	require.True(t, va.Handles(v1.KindService.GVK(), admission.Update))
	require.False(t, va.Handles(v1.KindService.GVK(), admission.Delete), "validate is Create/Update only")

	for _, tc := range []struct {
		name  string
		obj   v1.Object
		valid bool
	}{
		{"valid kv service", svc(v1.ServiceTypeKV, &v1.KVServiceSpec{Binding: "b"}), true},
		{"kv service missing sub-spec", svc(v1.ServiceTypeKV, nil), false},
		{"kv service empty binding", svc(v1.ServiceTypeKV, &v1.KVServiceSpec{Binding: ""}), false},
		{"unknown service type", svc("nope", nil), false},
		{"valid namespace", nsValid(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := va.Admit(context.Background(), admission.Request{
				Operation: admission.Create, GVK: tc.obj.GroupVersionKind(), Object: tc.obj,
			})
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, tc.obj, out, "a Validating admission returns the object unchanged")
			} else {
				require.Equal(t, fault.Invalid, fault.KindOf(err), "an invalid object ⇒ fault.Invalid")
			}
		})
	}
}
