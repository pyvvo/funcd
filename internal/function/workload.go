package function

import (
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// workload is what the workload hooks and the revision stamp serve (ADR-0347 Decisions 2 and 5): a Function, or a Job
// through the fields its spec shares with a Function's.
type workload struct {
	owner v1.Object        // the *v1.Function or the *v1.Job: its kind, its metadata and its UID
	spec  *v1.FunctionSpec // the shared fields; a Job's leaves the Function's own fields zero
}

func functionWorkload(fn *v1.Function) workload { return workload{owner: fn, spec: &fn.Spec} }

func jobWorkload(j *v1.Job) workload {
	s := j.Spec.FunctionSpec()
	return workload{owner: j, spec: &s}
}

func (w workload) kind() v1.Kind        { return w.owner.GroupVersionKind().Kind }
func (w workload) meta() *v1.ObjectMeta { return w.owner.GetObjectMeta() }

// fn is the workload as a Function, for the checks that read only the shared fields and the metadata: the Function
// itself, or the Job's shared fields under the Job's metadata.
func (w workload) fn() *v1.Function {
	if f, ok := w.owner.(*v1.Function); ok {
		return f
	}
	return &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: *w.meta(), Spec: *w.spec}
}

// observedGeneration is the generation the workload's status last reflects.
func (w workload) observedGeneration() int64 {
	if s, ok := w.owner.(v1.StatusObject); ok {
		return s.GetStatus().ObservedGeneration
	}
	return 0
}

// controllerRef is the reference a Revision of the workload carries to it (ADR-0172 Decision 5).
func (w workload) controllerRef() v1.OwnerReference {
	m := w.meta()
	return v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: w.kind(), Namespace: m.Namespace, Name: m.Name}, UID: m.UID, Controller: true}
}
