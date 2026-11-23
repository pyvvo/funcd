package function

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// ContractInspector reads the contract an image delivers, by its digest and from its registry metadata, without its
// bundle (ADR-0347 Decision 2): fault.NotFound when the image carries none.
type ContractInspector interface {
	Inspect(ctx context.Context, image, digest string) ([]byte, error)
}

// jobNeedsContract is the one switch of a Job whose image carries no contract.
//
// TODO(ADR-0347 Decision 2, a question for the decider): F137 checks a run's input against "the Job's contract, which
// its image carries as a Function's does", which argues for the Function's ContractMissing gate (#1084), while Decision
// 2 says an image with none leaves the input unchecked. false follows Decision 2.
const jobNeedsContract = false

// JobReconciler is the reconciler of the Job kind (ADR-0347 Decision 2), on the Function reconciler's deps, stamp and
// workload hooks: it stamps a Revision per generation, reads its contract by digest into status.contract, sets Ready
// once the checks a Function passes before its first worker pass, and deletes the Revisions that neither the current
// spec nor a run that has not ended pins.
type JobReconciler struct{ r *Reconciler }

// Jobs returns the reconciler of the Job kind.
func (r *Reconciler) Jobs() *JobReconciler { return &JobReconciler{r: r} }

// Reconcile is one pass over a stored Job. A deleted Job's local volumes follow their deletion policy, as a deleted
// Function's do; its Revisions and the stores it owns go with the owner collector (ADR-0170).
func (j *JobReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "function.ReconcileJob"
	r := j.r
	obj, err := r.store.Get(ctx, v1.KindJob.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, r.volumesGone(ctx, v1.KindJob, req.Namespace, req.Name)
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get job %s/%s", req.Namespace, req.Name)
	}
	job, ok := obj.(*v1.Job)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a Job", req.Namespace, req.Name)
	}
	read := job.Status
	read.Conditions = slices.Clone(job.Status.Conditions)
	requeue, err := j.pass(ctx, job)
	if err != nil {
		return controller.Result{}, err
	}
	if !reflect.DeepEqual(read, job.Status) {
		if _, uerr := r.store.Update(ctx, job); uerr != nil {
			return controller.Result{}, retryOnConflict(uerr, op)
		}
	}
	return controller.Result{RequeueAfter: requeue}, nil
}

// pass sets job's status from its current generation and returns when to look again.
func (j *JobReconciler) pass(ctx context.Context, job *v1.Job) (time.Duration, error) {
	r := j.r
	// status.contract is the current revision's, so a pass that stops before reading it leaves none (ADR-0347 Decision 2)
	job.Status.ObservedGeneration, job.Status.Contract = job.Generation, nil
	w := jobWorkload(job)
	pinned, err := r.stamp(ctx, stampSubject{
		w:          w,
		revision:   v1.JobRevisionName(job.Name, job.Generation),
		current:    job.Status.CurrentRevision,
		setCurrent: func(rev v1.ObjectName) { job.Status.CurrentRevision = rev },
		judge:      func(rev *v1.Revision) revisionOwner { return jobRevisionOf(rev, job) },
	})
	switch {
	case errors.Is(err, errArtifactUnresolved):
		return notReady(job, gateFailure{reason: "ArtifactUnresolved", message: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}), nil
	case errors.Is(err, errRevisionMissing):
		return notReady(job, gateFailure{reason: "RevisionMissing", message: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}), nil
	case errors.Is(err, errRevisionStampFailed):
		return notReady(job, gateFailure{reason: "RevisionStampFailed", message: err.Error(), phase: v1.PhaseFailed, requeue: r.supervisionPeriod}), nil
	case err != nil:
		return 0, err
	}
	if err := j.collectRevisions(ctx, job); err != nil {
		return 0, err
	}
	if verr := r.validator.Validate(ctx, w.fn()); verr != nil {
		return notReady(job, gateFailure{reason: "ShapeInvalid", message: verr.Error(), phase: v1.PhaseFailed}), nil
	}
	if msg, missing := r.runtimeUnavailable(w.fn()); missing {
		return notReady(job, gateFailure{reason: reasonRuntimeUnavailable, message: msg, phase: v1.PhaseFailed}), nil
	}
	env, err := r.bindings(ctx, w)
	if err != nil {
		return 0, err
	}
	if env.gate != nil {
		return notReady(job, *env.gate), nil
	}
	c, g, err := j.contract(ctx, job, pinned)
	if err != nil {
		return 0, err
	}
	if g != nil {
		return notReady(job, *g), nil
	}
	job.Status.Contract = c
	job.Status.Phase = v1.PhaseReady
	job.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, ObservedGeneration: job.Generation})
	return 0, r.pruneConfigMaps(ctx, w)
}

// notReady writes the gate g that stops job, with the Function's reason, and returns its requeue.
func notReady(job *v1.Job, g gateFailure) time.Duration {
	msg := g.readyMessage
	if msg == "" {
		msg = g.message
	}
	job.Status.Phase = g.phase
	job.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: g.reason, Message: msg, ObservedGeneration: job.Generation})
	return g.requeue
}

// contract reads the contract that job's image at digest delivers into the shape of status.contract, and holds job with
// ContractInvalid when it does not compile, as a Function's revision is held (ADR-0303 Decision 3). An image with no
// digest, as a file artifact, or with no contract leaves it nil.
func (j *JobReconciler) contract(ctx context.Context, job *v1.Job, digest string) (*v1.WorkflowContract, *gateFailure, error) {
	r := j.r
	if digest == "" || r.inspector == nil {
		return nil, nil, nil
	}
	blob, err := r.inspector.Inspect(ctx, job.Spec.Image, digest)
	switch {
	case fault.KindOf(err) == fault.NotFound && jobNeedsContract:
		msg := contractMissingError{digest: digest}.Error()
		return nil, &gateFailure{reason: reasonContractMissing, message: msg, phase: v1.PhaseFailed}, nil
	case fault.KindOf(err) == fault.NotFound:
		return nil, nil, nil
	case err != nil:
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), "function.jobContract", "read the contract of %s@%s", job.Spec.Image, digest)
	}
	var c v1.WorkflowContract
	if uerr := json.Unmarshal(blob, &c); uerr != nil {
		msg := contractInvalidError{digest: digest, err: uerr}.Error()
		return nil, &gateFailure{reason: reasonContractInvalid, message: msg, phase: v1.PhaseFailed}, nil
	}
	bounds := contract.WithCallBounds(contractcheck.CallBudget, contractcheck.MaxDigits)
	for _, side := range []json.RawMessage{c.Input, c.Output} {
		if _, cerr := contract.Compile(side, bounds); cerr != nil {
			msg := contractInvalidError{digest: digest, err: cerr}.Error()
			return nil, &gateFailure{reason: reasonContractInvalid, message: msg, phase: v1.PhaseFailed}, nil
		}
	}
	return &c, nil, nil
}

// collectRevisions deletes the Revisions job controls that are neither its current one nor pinned by one of its runs
// that has not ended (ADR-0347 Decision 2), so a run keeps the revision it started on until it ends.
func (j *JobReconciler) collectRevisions(ctx context.Context, job *v1.Job) error {
	const op = "function.collectJobRevisions"
	r := j.r
	keep := map[v1.ObjectName]bool{job.Status.CurrentRevision: true}
	runs, err := r.store.List(ctx, v1.KindJobRun.GVK(), store.ListOptions{Namespace: job.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list the runs in %q", job.Namespace)
	}
	for _, o := range runs.Items {
		if run, ok := o.(*v1.JobRun); ok && run.Spec.Job == job.Name && !run.Status.Phase.Ended() {
			keep[run.Status.JobRevision] = true
		}
	}
	revs, err := r.store.List(ctx, v1.KindRevision.GVK(), store.ListOptions{Namespace: job.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list the revisions in %q", job.Namespace)
	}
	for _, o := range revs.Items {
		m := o.GetObjectMeta()
		if keep[m.Name] || !v1.ControlledBy(m.OwnerReferences, v1.KindJob, job.UID) {
			continue
		}
		if err := r.store.Delete(ctx, v1.KindRevision.GVK(), m.Namespace, m.Name, m.ResourceVersion); err != nil && fault.KindOf(err) != fault.NotFound {
			return fault.Wrapf(err, fault.KindOf(err), op, "delete revision %q", m.Name)
		}
	}
	return nil
}

// jobRevisionOf is ADR-0172 Decision 5's table for a Job: a controller reference decides (this Job revSelf, another UID
// of its name revNamesake, any other owner revOther); a ref-less Revision of another subject is revOther, else a
// namesake's.
func jobRevisionOf(rev *v1.Revision, job *v1.Job) revisionOwner {
	if o, ok := v1.ControllerOf(rev.OwnerReferences); ok {
		switch {
		case o.Kind != v1.KindJob || o.Name != job.Name:
			return revOther
		case o.UID == job.UID:
			return revSelf
		}
		return revNamesake
	}
	if rev.Spec.Job != job.Name {
		return revOther
	}
	return revNamesake
}

// MapRun maps a JobRun to its Job, whose pass collects the revisions that the run no longer pins once it ends.
func (j *JobReconciler) MapRun(_ context.Context, obj v1.Object) []controller.Request {
	run, ok := obj.(*v1.JobRun)
	if !ok || run.Spec.Job == "" {
		return nil
	}
	return []controller.Request{{GVK: v1.KindJob.GVK(), Namespace: run.Namespace, Name: run.Spec.Job}}
}
