package funcd

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// jobRuns is the Job facet's port over the store (ADR-0348 Decision 6): a run's worker reads its own run, which its
// fixed ref names (ADR-0351 Decision 2).
type jobRuns struct{ store store.Store }

var _ local.JobRunReader = jobRuns{}

// Run returns the run that ref's worker runs: ErrNotAJobWorker for a ref that is no Job's run, for a run that is gone,
// and for a run of another Job under that name.
func (j jobRuns) Run(ctx context.Context, ref local.Ref) (v1.JobRun, error) {
	const op = "funcd.jobRuns.Run"
	if ref.Kind != v1.KindJob || ref.Run == "" {
		return v1.JobRun{}, local.ErrNotAJobWorker.Errorf(op, "%s is no Job's worker", ref)
	}
	obj, err := j.store.Get(ctx, v1.KindJobRun.GVK(), ref.Namespace, ref.Run)
	if fault.KindOf(err) == fault.NotFound {
		return v1.JobRun{}, local.ErrNotAJobWorker.Errorf(op, "the run of %s is gone", ref)
	}
	if err != nil {
		return v1.JobRun{}, fault.Wrapf(err, fault.KindOf(err), op, "read the run of %s", ref)
	}
	run, ok := obj.(*v1.JobRun)
	if !ok || run.Spec.Job != ref.Name {
		return v1.JobRun{}, local.ErrNotAJobWorker.Errorf(op, "%s/%s is no run of Job %q", ref.Namespace, ref.Run, ref.Name)
	}
	return *run, nil
}
