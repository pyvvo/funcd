package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// VolumeBooker books a local volume's size at apply (ADR-0262 Decision 5), under the kind and name of its workload, a
// Function or a Job (ADR-0347 Decision 1); its error carries the reason (VolumeCapacity, VolumeBudget, a changed
// size). Fits answers what Book would and books nothing: a dry run's (#1205).
type VolumeBooker interface {
	Book(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, workload, name v1.ObjectName, sizeBytes int64) error
	Fits(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, workload, name v1.ObjectName, sizeBytes int64) error
}

// --- function-volumes (ADR-0262 Decisions 5 and 10, ADR-0347 Decision 1): Create and Update on Function and Job ------

type functionVolumes struct{ b VolumeBooker }

// NewFunctionVolumesAdmission returns the Validating admission that refuses a Function or a Job with a volume while
// volumes are off (b nil: a process-runtime daemon, or no volumes.capacityBytes), and books each local volume's size
// otherwise.
func NewFunctionVolumesAdmission(b VolumeBooker) Admission { return functionVolumes{b: b} }

func (functionVolumes) Name() string { return "function-volumes" }
func (functionVolumes) Phase() Phase { return Validating }

func (functionVolumes) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return (gvk == v1.KindFunction.GVK() || gvk == v1.KindJob.GVK()) && (op == Create || op == Update)
}

func (a functionVolumes) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.function-volumes"
	var vols []v1.VolumeEntry
	switch w := req.Object.(type) {
	case *v1.Function:
		vols = w.Spec.Volumes
	case *v1.Job:
		vols = w.Spec.Volumes
	}
	if len(vols) == 0 {
		return req.Object, nil
	}
	if a.b == nil {
		return nil, fault.Invalidf(op, "VolumesOff: spec.volumes: volumes are off on this daemon (set volumes.capacityBytes on the containerd runtime)")
	}
	book := a.b.Book
	if req.DryRun {
		book = a.b.Fits
	}
	m, kind := req.Object.GetObjectMeta(), req.Object.GroupVersionKind().Kind
	for _, v := range vols {
		if v.Type != v1.VolumeLocal {
			continue
		}
		if err := book(ctx, m.Namespace, kind, m.Name, v.VolumeName(), v.Bytes()); err != nil {
			return nil, err
		}
	}
	return req.Object, nil
}
