package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// VolumeBooker books a local volume's size at apply (ADR-0262 Decision 5); its error carries the reason
// (VolumeCapacity, VolumeBudget, a changed size).
type VolumeBooker interface {
	Book(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName, sizeBytes int64) error
}

// --- function-volumes (ADR-0262 Decisions 5 and 10): Create and Update on Function -------------------------

type functionVolumes struct{ b VolumeBooker }

// NewFunctionVolumesAdmission returns the Validating admission that refuses a Function with a volume while volumes are
// off (b nil: a process-runtime daemon, or no volumes.capacityBytes), and books each local volume's size otherwise.
func NewFunctionVolumesAdmission(b VolumeBooker) Admission { return functionVolumes{b: b} }

func (functionVolumes) Name() string { return "function-volumes" }
func (functionVolumes) Phase() Phase { return Validating }

func (functionVolumes) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindFunction.GVK() && (op == Create || op == Update)
}

func (a functionVolumes) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.function-volumes"
	fn, ok := req.Object.(*v1.Function)
	if !ok || len(fn.Spec.Volumes) == 0 {
		return req.Object, nil
	}
	if a.b == nil {
		return nil, fault.Invalidf(op, "VolumesOff: spec.volumes: volumes are off on this daemon (set volumes.capacityBytes on the containerd runtime)")
	}
	for _, v := range fn.Spec.Volumes {
		if v.Type != v1.VolumeLocal {
			continue
		}
		if err := a.b.Book(ctx, fn.Namespace, fn.Name, v.VolumeName(), v.SizeBytes); err != nil {
			return nil, err
		}
	}
	return req.Object, nil
}
