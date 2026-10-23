package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

type booker struct {
	booked []string
	err    error
}

func (b *booker) Book(_ context.Context, ns v1.NamespaceName, fn, name v1.ObjectName, size int64) error {
	b.booked = append(b.booked, string(ns)+"/"+string(fn)+"/"+string(name))
	return b.err
}

func volumeFn(vs ...v1.VolumeEntry) *v1.Function {
	f := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	f.Name, f.Namespace, f.Spec.Volumes = "cart", "shop", vs
	return f
}

// TestScenarioVolumesOff: without a manager every Function with a volume is refused VolumesOff; one without passes.
// scenario: volumes-off
func TestScenarioVolumesOff(t *testing.T) {
	a := admission.NewFunctionVolumesAdmission(nil)
	_, err := a.Admit(t.Context(), admission.Request{Operation: admission.Create, Object: volumeFn(v1.VolumeEntry{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 1})})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "VolumesOff")
	_, err = a.Admit(t.Context(), admission.Request{Operation: admission.Create, Object: volumeFn()})
	require.NoError(t, err)
}

// TestFunctionVolumesBooksLocal (Decision 5): each local volume is booked, by name or ref; an emptyDir is booked at
// worker create, not at apply; the booker's refusal is the admission's.
func TestFunctionVolumesBooksLocal(t *testing.T) {
	b := &booker{}
	a := admission.NewFunctionVolumesAdmission(b)
	require.True(t, a.Handles(v1.KindFunction.GVK(), admission.Update))
	require.False(t, a.Handles(v1.KindFunction.GVK(), admission.Delete))
	fn := volumeFn(
		v1.VolumeEntry{Name: "state", Type: v1.VolumeLocal, SizeBytes: 4},
		v1.VolumeEntry{Ref: "old", Type: v1.VolumeLocal, SizeBytes: 2},
		v1.VolumeEntry{Name: "cache", Type: v1.VolumeEmptyDir, SizeBytes: 1},
	)
	_, err := a.Admit(t.Context(), admission.Request{Operation: admission.Create, Object: fn})
	require.NoError(t, err)
	require.Equal(t, []string{"shop/cart/state", "shop/cart/old"}, b.booked)
	b.err = fault.Conflictf("volume.Book", "VolumeCapacity")
	_, err = a.Admit(t.Context(), admission.Request{Operation: admission.Update, Object: fn})
	require.Equal(t, fault.Conflict, fault.KindOf(err))
}
