package local

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// JobRunReader reads the run whose worker calls the local API (ADR-0348 Decision 6).
type JobRunReader interface {
	// Run returns the run whose worker ref is the caller's; ErrNotAJobWorker, a fault.Forbidden, for a caller that is
	// no Job's worker, or whose run is gone.
	Run(ctx context.Context, ref Ref) (v1.JobRun, error)
}

// ErrNotAJobWorker is the answer of GET /job/run to a caller that is no Job's worker (ADR-0348 Decision 6).
var ErrNotAJobWorker = fault.NewKnownCause(fault.Forbidden, "job.not-a-job-worker",
	"The caller is no Job's worker: a Function's worker, or the worker of a run that funcd removed.",
	"Read the run only from a Job's handler, as context.approval and context.run do.")

// JobFacet serves GET /job/run over runs (ADR-0348 Decision 6): a Job's worker reads its own run, with the approval
// that its handler's context carries. It also serves a Function's start and read of the runs of the Jobs it binds,
// POST /job/{alias} and GET /job/{alias}/{run}, over grants and starter (ADR-0349 Decisions 2 to 4).
func JobFacet(runs JobRunReader, starter JobRunStarter, grants JobGrants) Facet {
	return Facet{segment: "job", register: func(mux facetMux, caller Ref, logger *slog.Logger) {
		registerJob(mux, caller, runs, logger)
		registerJobStart(mux, caller, starter, grants, logger)
	}}
}

// JobRunInfo is the body of GET /job/run: the caller's own run.
type JobRunInfo struct {
	Name      v1.ObjectName  `json:"name"`
	Job       v1.ObjectName  `json:"job"`
	Revision  v1.ObjectName  `json:"revision"`
	Phase     v1.JobRunPhase `json:"phase"`
	StartedBy string         `json:"startedBy"`
	// Approval is null for a run with no recorded approval, as a run of a Job with approval.bypass.
	Approval *RunApproval `json:"approval"`
}

// RunApproval is a run's recorded approval: who approved it, the digest that the approval names, and the document of
// that preview.
type RunApproval struct {
	ApprovedBy string          `json:"approvedBy"`
	Digest     string          `json:"digest"`
	Preview    json.RawMessage `json:"preview"`
}

// registerJob adds GET /job/run, answered to a Job's worker only.
func registerJob(mux facetMux, caller Ref, runs JobRunReader, logger *slog.Logger) {
	handle(mux, logger, "GET /job/run", []*fault.KnownCause{ErrNotAJobWorker}, func(w http.ResponseWriter, r *http.Request) error {
		const op = "workernode.local.job.run"
		if caller.Kind != v1.KindJob {
			return ErrNotAJobWorker.Errorf(op, "%s is a %s's worker, not a Job's", caller, caller.Kind)
		}
		run, err := runs.Run(r.Context(), caller)
		if err != nil {
			logger.Warn("job run read refused/failed", "caller", caller.String(), "err", err.Error())
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runInfo(&run))
		return nil
	})
}

// runInfo is run as its worker reads it: its approval only once admission recorded one that names its preview.
func runInfo(run *v1.JobRun) JobRunInfo {
	info := JobRunInfo{Name: run.Name, Job: run.Spec.Job, Revision: run.Status.JobRevision, Phase: run.Status.Phase,
		StartedBy: run.Status.StartedBy}
	if run.Approved() {
		info.Approval = &RunApproval{ApprovedBy: run.Status.ApprovedBy, Digest: run.Spec.Approve,
			Preview: run.Status.Preview.Document}
	}
	return info
}
