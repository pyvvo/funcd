package function

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/activator"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/contractcheck"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/invoke"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/store"
)

// runAnswer is how a run's executor ended: the phase, reason and message the pass writes, and the handler's result
// once it Succeeded (ADR-0347 Decision 5); or, for a preview call that answered, the preview, with no phase
// (ADR-0348 Decision 2).
type runAnswer struct {
	phase           v1.JobRunPhase
	reason, message string
	result          json.RawMessage
	preview         *v1.JobRunPreview // its digest and document; the pass sets when it was made
}

// runCall is which of its calls a run's executor makes: the apply, which runs the handler, or the preview, which
// calls the module's preview (ADR-0348 Decision 2).
type runCall int

const (
	callApply runCall = iota
	callPreview
)

// runLauncher boots a started run's worker and makes its one call of kind call (ADR-0347 Decision 5, ADR-0348
// Decision 2). It returns once the worker is stopped and removed; a cancelled ctx stops the worker at once.
type runLauncher func(ctx context.Context, run *v1.JobRun, job *v1.Job, rev *v1.Revision, call runCall) runAnswer

// RunsConfig tunes the run reconciler: the job.* keys (ADR-0347), the controller's enqueue, through which an executor
// that ends brings its run's pass, the holds under which it acts on no run (ADR-0206 Decision 6, ADR-0290), and the
// invoke chain and client of a run's call (ADR-0302); a nil Client sets X-Funcd-Timeout-Ms over the default transport.
type RunsConfig struct {
	MaxRunning    int
	PayloadLimit  int64
	Retention     time.Duration
	Enqueue       func(controller.Request)
	Hold          hold.Gate
	NamespaceHold hold.NamespaceGate
	Chain         *invoke.Chain
	Client        *http.Client
	// Egress is the egress gateway's worker index (ADR-0117 §5), nil while egress is off.
	Egress EgressIndex
	// Skip leaves the runs of a namespace that a worker node runs to that node (FEAT-0002); nil skips none.
	Skip func(v1.NamespaceName) bool
}

// EgressIndex is the part of the egress gateway's worker index that a run's executor writes, *egress.MemoryWorkerIndex:
// a run's worker is in it as its Job from before the call to its removal, since the worker sync's poll would find it
// only after a short run had ended (ADR-0117 §5, ADR-0351 Decision 6).
type EgressIndex interface {
	Add(ip netip.Addr, ref auth.EntityRef)
	Remove(ip netip.Addr)
}

// JobRunReconciler is the reconciler of the JobRun kind and the runs' one status writer (ADR-0347 Decisions 4 to 8,
// ADR-0348, ADR-0146 Decision 4). A pass gives a Pending run a slot once its Job is Ready and one is free, with one
// write that stores it Previewing or Running before its executor exists; it writes the executor's answer while the run
// is stored so, keeps a previewed run AwaitingApproval until a person approves or rejects it, and ends a run on a
// cancel, a rejection, a deleted Job or pin, and a restart.
type JobRunReconciler struct {
	r      *Reconciler
	cfg    RunsConfig
	launch runLauncher
	execs  *runExecutions
}

// Runs returns the reconciler of the JobRun kind, on r's deps.
func (r *Reconciler) Runs(cfg RunsConfig) *JobRunReconciler {
	if cfg.Hold == nil {
		cfg.Hold = hold.Never
	}
	if cfg.NamespaceHold == nil {
		cfg.NamespaceHold = hold.NeverNamespace
	}
	if cfg.Enqueue == nil {
		cfg.Enqueue = func(controller.Request) {}
	}
	if cfg.Skip == nil {
		cfg.Skip = func(v1.NamespaceName) bool { return false }
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Transport: activator.DeadlineTransport(httpx.NodeTransport())}
	}
	j := &JobRunReconciler{r: r, cfg: cfg, execs: &runExecutions{byUID: map[v1.UID]*execution{}}}
	j.launch = j.execute
	return j
}

// Reconcile is one pass over a JobRun. While the platform or the run's namespace is held it acts on no run and leaves
// its status as it is (ADR-0347 Decision 4). A deleted run, or one re-created under its name, has its old executor,
// and so its worker, stopped (Decision 6).
func (j *JobRunReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "function.ReconcileJobRun"
	if j.cfg.Hold.Held() || j.cfg.NamespaceHold.HeldIn(req.Namespace) {
		return controller.Result{RequeueAfter: j.r.referentPoll}, nil
	}
	obj, err := j.r.store.Get(ctx, v1.KindJobRun.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		j.execs.stopNamed(req.Namespace, req.Name, "")
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get jobrun %s/%s", req.Namespace, req.Name)
	}
	run, ok := obj.(*v1.JobRun)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "object %s/%s is not a JobRun", req.Namespace, req.Name)
	}
	j.execs.stopNamed(run.Namespace, run.Name, run.UID)
	read := run.Status
	read.Conditions = slices.Clone(run.Status.Conditions)
	res, start, err := j.pass(ctx, run)
	if err != nil || reflect.DeepEqual(read, run.Status) {
		return res, err
	}
	written, uerr := j.r.store.Update(ctx, run)
	if uerr != nil {
		return controller.Result{}, retryOnConflict(uerr, op)
	}
	switch {
	case start != nil:
		// only once the run is stored Previewing or Running does its executor start (ADR-0347 Decision 4); it takes
		// the place of the preview's executor that ended
		j.execs.start(written.(*v1.JobRun), start.job, start.rev, start.call, j.launch, j.cfg.Enqueue)
	case run.Status.Phase.Ended(), run.Status.Phase == v1.JobRunAwaitingApproval:
		// once its end or its preview is stored: a write that conflicts finds the answer again on the next pass
		j.execs.forget(run.UID)
	}
	return res, nil
}

// runStart is what a pass that gives a run a slot hands its executor: the Job, the revision the run pins and the call.
type runStart struct {
	job  *v1.Job
	rev  *v1.Revision
	call runCall
}

// pass sets run's status from its Job, its executor and the clock, and returns when to look again and the start it
// made.
func (j *JobRunReconciler) pass(ctx context.Context, run *v1.JobRun) (controller.Result, *runStart, error) {
	const op = "function.ReconcileJobRun"
	if run.Status.Phase.Ended() {
		j.execs.forget(run.UID)
		return controller.Result{}, nil, nil
	}
	now := v1.NewTimestamp(j.r.clock.Now())
	job, err := j.job(ctx, run.Namespace, run.Spec.Job)
	if err != nil {
		return controller.Result{}, nil, err
	}
	gone := run.Status.JobUID != "" && (job == nil || job.UID != run.Status.JobUID)
	switch run.Status.Phase {
	case v1.JobRunRunning:
		res, err := j.running(ctx, run, gone, now)
		return res, nil, err
	case v1.JobRunPreviewing:
		return j.previewing(ctx, run, job, gone, now)
	}
	// a rejection wins over an approval that no pass has acted on yet (ADR-0348 Decision 5)
	switch {
	case run.Spec.Reject:
		reject(run, now)
		return controller.Result{}, nil, nil
	case run.Spec.Cancel:
		run.Status.End(v1.JobRunCancelled, v1.JobRunCancelledReason, "spec.cancel: the run never ran", now)
		return controller.Result{}, nil, nil
	case gone:
		run.Status.End(v1.JobRunCancelled, v1.JobRunJobDeleted, jobGoneMessage(run), now)
		return controller.Result{}, nil, nil
	case run.Status.Phase == v1.JobRunAwaitingApproval && !run.Approved():
		return j.awaiting(ctx, run, now)
	case job == nil:
		return j.pending(run, v1.JobRunJobNotFound, fmt.Sprintf("no Job %q in namespace %q", run.Spec.Job, run.Namespace)), nil, nil
	case !jobReady(job):
		return j.pending(run, v1.JobRunJobNotReady, jobNotReadyMessage(job)), nil, nil
	}
	why, err := j.slotFor(ctx, run)
	if err != nil {
		return controller.Result{}, nil, err
	}
	if why != "" {
		return j.pending(run, v1.JobRunQueued, why), nil, nil
	}
	pin := cmp.Or(run.Status.JobRevision, job.Status.CurrentRevision)
	rev, err := j.r.getRevision(ctx, run.Namespace, pin)
	if fault.KindOf(err) == fault.NotFound {
		run.Status.End(v1.JobRunCancelled, v1.JobRunJobDeleted, fmt.Sprintf("revision %q of Job %q is gone", pin, run.Spec.Job), now)
		return controller.Result{}, nil, nil
	}
	if err != nil {
		return controller.Result{}, nil, fault.Wrapf(err, fault.KindOf(err), op, "get revision %q", pin)
	}
	off, err := j.inputOff(ctx, run, job, rev)
	if err != nil {
		return controller.Result{}, nil, err
	}
	if off != "" {
		run.Status.End(v1.JobRunFailed, v1.JobRunInputSchemaMismatch, off, now)
		return controller.Result{}, nil, nil
	}
	res, start := j.take(run, job, rev, now)
	return res, start, nil
}

// take is the one write that gives run its slot, before its executor exists, keeping the pin and the trace the run
// already holds (ADR-0347 Decision 4, ADR-0348 Decisions 2 and 4). A run of a Job with approval.bypass goes Running, as
// does an approved run whose preview is within its Job's approval.expiry; an approved run whose preview is older
// previews again in this slot, with PreviewExpired; any other run previews first.
func (j *JobRunReconciler) take(run *v1.JobRun, job *v1.Job, rev *v1.Revision, now v1.Timestamp) (controller.Result, *runStart) {
	run.Status.JobRevision, run.Status.JobUID, run.Status.ImageDigest = rev.Name, job.UID, rev.Spec.ImageDigest
	if run.Status.TraceID == "" {
		run.Status.TraceID, _ = observability.NewTraceContext()
	}
	call, reason := callApply, ""
	switch {
	case job.Spec.Approval.Bypass:
	case run.Approved():
		if !previewFresh(run, job, now) {
			call, reason = callPreview, v1.JobRunPreviewExpired
		}
	default:
		call = callPreview
	}
	if call == callPreview {
		run.Status.Phase = v1.JobRunPreviewing
		msg := fmt.Sprintf("the worker of revision %q answers the preview", rev.Name)
		if reason != "" {
			msg = fmt.Sprintf("the approved preview is older than the Job's approval.expiry %s, so the worker of revision %q "+
				"previews it again", job.Spec.Approval.ExpiryOrDefault(), rev.Name)
		}
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionUnknown, Reason: reason, Message: msg})
		return controller.Result{RequeueAfter: j.r.supervisionPeriod}, &runStart{job: job, rev: rev, call: callPreview}
	}
	run.Status.Phase, run.Status.StartedAt = v1.JobRunRunning, now
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionUnknown, Message: fmt.Sprintf("the worker of revision %q runs the handler", rev.Name)})
	return controller.Result{RequeueAfter: j.r.supervisionPeriod}, &runStart{job: job, rev: rev, call: callApply}
}

// previewFresh reports whether run's preview is within its Job's approval.expiry at now, the end included (ADR-0348
// Decision 4): checked on an approval and when the slot frees, never on a timer.
func previewFresh(run *v1.JobRun, job *v1.Job, now v1.Timestamp) bool {
	made := time.Time(run.Status.Preview.PreviewedAt)
	return !time.Time(now).After(made.Add(time.Duration(job.Spec.Approval.ExpiryOrDefault())))
}

// awaiting is the pass of a run that waits for a person to approve or reject its preview (ADR-0348 Decisions 3 to 5):
// it holds no worker and no slot and waits with no deadline, ended only once its pin goes. An approval, a rejection, a
// cancel and a change of its Job bring its next pass.
func (j *JobRunReconciler) awaiting(ctx context.Context, run *v1.JobRun, now v1.Timestamp) (controller.Result, *runStart, error) {
	_, err := j.r.getRevision(ctx, run.Namespace, run.Status.JobRevision)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		run.Status.End(v1.JobRunCancelled, v1.JobRunJobDeleted, fmt.Sprintf("revision %q of Job %q is gone", run.Status.JobRevision, run.Spec.Job), now)
	case err != nil:
		return controller.Result{}, nil, fault.Wrapf(err, fault.KindOf(err), "function.ReconcileJobRun", "get revision %q", run.Status.JobRevision)
	}
	return controller.Result{}, nil, nil
}

// previewing is the pass of a run stored Previewing (ADR-0348 Decisions 2, 4 and 5). A rejection, a cancel, or its Job
// or pin gone stops the preview's executor and, once its worker is gone, ends the run. The preview's answer is stored
// with the time it was made: an approved run previewed again whose document keeps the approved digest goes on to
// Running in the slot it holds, any other waits AwaitingApproval for a new approval; a failed preview ends the run.
func (j *JobRunReconciler) previewing(ctx context.Context, run *v1.JobRun, job *v1.Job, gone bool, now v1.Timestamp) (controller.Result, *runStart, error) {
	x := j.execs.get(run.UID)
	if x == nil {
		// no executor in this process: funcd stopped during the preview, which is called again (ADR-0348 Decision 2)
		repreview(run)
		return controller.Result{Requeue: true}, nil, nil
	}
	reason, why := "", ""
	var rev *v1.Revision
	switch {
	case run.Spec.Reject:
		reason = v1.JobRunRejectedReason
	case run.Spec.Cancel:
		reason, why = v1.JobRunCancelledReason, "spec.cancel: the preview's worker was stopped, and the handler never ran"
	case gone:
		reason, why = v1.JobRunJobDeleted, jobGoneMessage(run)
	default:
		var err error
		rev, err = j.r.getRevision(ctx, run.Namespace, run.Status.JobRevision)
		if fault.KindOf(err) == fault.NotFound {
			reason, why = v1.JobRunJobDeleted, fmt.Sprintf("revision %q of Job %q is gone", run.Status.JobRevision, run.Spec.Job)
		} else if err != nil {
			return controller.Result{}, nil, fault.Wrapf(err, fault.KindOf(err), "function.ReconcileJobRun", "get revision %q", run.Status.JobRevision)
		}
	}
	if reason != "" {
		x.cancel()
	}
	if !x.finished() {
		return controller.Result{RequeueAfter: j.r.supervisionPeriod}, nil, nil // its executor's end enqueues the run
	}
	switch reason {
	case "":
	case v1.JobRunRejectedReason:
		reject(run, now)
		return controller.Result{}, nil, nil
	default:
		run.Status.End(v1.JobRunCancelled, reason, why, now)
		return controller.Result{}, nil, nil
	}
	if x.answer.preview == nil {
		run.Status.End(x.answer.phase, x.answer.reason, x.answer.message, now)
		return controller.Result{}, nil, nil
	}
	preview := *x.answer.preview
	preview.PreviewedAt = now
	run.Status.Preview = &preview
	if run.Approved() {
		// a stale approved preview made again with the approved digest: exactly what was approved runs (Decision 4)
		run.Status.Phase, run.Status.StartedAt = v1.JobRunRunning, now
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionUnknown, Message: fmt.Sprintf("the worker of revision %q runs the handler", rev.Name)})
		return controller.Result{RequeueAfter: j.r.supervisionPeriod}, &runStart{job: job, rev: rev, call: callApply}, nil
	}
	run.Status.ApprovedBy, run.Status.ApprovedAt = "", v1.Timestamp{}
	run.Status.Phase = v1.JobRunAwaitingApproval
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionUnknown,
		Message: fmt.Sprintf("the run waits for a person to approve or reject its preview %s", preview.Digest)})
	return controller.Result{}, nil, nil
}

// repreview puts a run that funcd stopped while it was Previewing back to Pending, keeping its pin, its trace and an
// approval it holds, so it previews again once it has a slot (ADR-0348 Decision 2).
func repreview(run *v1.JobRun) {
	run.Status.Phase = v1.JobRunPending
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse,
		Message: "funcd stopped while the run was Previewing: it previews again once it has a slot"})
}

// reject ends run Rejected, with spec.rejectReason as its message (ADR-0348 Decision 5).
func reject(run *v1.JobRun, now v1.Timestamp) {
	run.Status.End(v1.JobRunRejected, v1.JobRunRejectedReason, cmp.Or(run.Spec.RejectReason, "spec.reject: the run was rejected before it ran"), now)
}

// running is the pass of a run stored Running. A cancel, or its Job or pin gone, stops its executor and, once the worker
// is gone, ends it Cancelled; the executor's answer ends it; with no executor in this process, a restart cut it.
func (j *JobRunReconciler) running(ctx context.Context, run *v1.JobRun, gone bool, now v1.Timestamp) (controller.Result, error) {
	x := j.execs.get(run.UID)
	if x == nil {
		run.Status.End(v1.JobRunFailed, v1.JobRunInterrupted, interruptedMessage, now)
		return controller.Result{}, nil
	}
	reason, why := "", ""
	switch {
	case run.Spec.Cancel:
		reason, why = v1.JobRunCancelledReason, "spec.cancel: the worker was stopped, and the handler may have done part of its work"
	case gone:
		reason, why = v1.JobRunJobDeleted, jobGoneMessage(run)
	default:
		_, err := j.r.getRevision(ctx, run.Namespace, run.Status.JobRevision)
		if fault.KindOf(err) == fault.NotFound {
			reason, why = v1.JobRunJobDeleted, fmt.Sprintf("revision %q of Job %q is gone", run.Status.JobRevision, run.Spec.Job)
		} else if err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "function.ReconcileJobRun", "get revision %q", run.Status.JobRevision)
		}
	}
	if reason != "" {
		x.cancel()
	}
	if !x.finished() {
		return controller.Result{RequeueAfter: j.r.supervisionPeriod}, nil // its executor's end enqueues the run
	}
	if reason != "" {
		run.Status.End(v1.JobRunCancelled, reason, why, now)
		return controller.Result{}, nil
	}
	if x.answer.phase == v1.JobRunSucceeded {
		run.Status.Result = x.answer.result
	}
	run.Status.End(x.answer.phase, x.answer.reason, x.answer.message, now)
	return controller.Result{}, nil
}

// interruptedMessage ends a run that funcd stopped while it was Running (ADR-0347 Decision 7).
const interruptedMessage = "funcd stopped while the run was Running: its handler may have run in part or in whole, and the run does not start again"

func jobGoneMessage(run *v1.JobRun) string {
	return fmt.Sprintf("Job %q was deleted or re-created under a UID other than %q", run.Spec.Job, run.Status.JobUID)
}

// pending keeps run Pending with reason and looks again after the referent poll.
func (j *JobRunReconciler) pending(run *v1.JobRun, reason, message string) controller.Result {
	run.Status.Phase = v1.JobRunPending
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message})
	return controller.Result{RequeueAfter: j.r.referentPoll}
}

// job reads Job name of namespace ns, nil when none is stored.
func (j *JobRunReconciler) job(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Job, error) {
	obj, err := j.r.store.Get(ctx, v1.KindJob.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "function.ReconcileJobRun", "get job %s/%s", ns, name)
	}
	job, _ := obj.(*v1.Job)
	return job, nil
}

// jobReady reports whether job is Ready at its generation, so its current revision is its spec's.
func jobReady(job *v1.Job) bool {
	c, ok := job.Status.Conditions.Get(condReady)
	return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == job.Generation && job.Status.CurrentRevision != ""
}

func jobNotReadyMessage(job *v1.Job) string {
	if c, ok := job.Status.Conditions.Get(condReady); ok && c.Reason != "" && c.ObservedGeneration == job.Generation {
		return fmt.Sprintf("Job %q is not Ready (%s): %s", job.Name, c.Reason, c.Message)
	}
	return fmt.Sprintf("Job %q is not Ready yet", job.Name)
}

type jobKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

// holdsSlot reports whether run holds a worker: Running, or Previewing (ADR-0348 Decision 2).
func holdsSlot(run *v1.JobRun) bool {
	return run.Status.Phase == v1.JobRunRunning || run.Status.Phase == v1.JobRunPreviewing
}

// waitsForSlot reports whether run waits for a slot: Pending, or AwaitingApproval with an approval recorded, and
// neither cancelled nor rejected (ADR-0347 Decision 4, ADR-0348 Decision 4). A run that waits for a person takes none.
func waitsForSlot(run *v1.JobRun) bool {
	switch run.Status.Phase {
	case "", v1.JobRunPending:
	case v1.JobRunAwaitingApproval:
		if !run.Approved() {
			return false
		}
	default:
		return false
	}
	return !run.Spec.Cancel && !run.Spec.Reject
}

// slotFor is why run takes no slot now, "" when it takes one (ADR-0347 Decision 4): a slot needs fewer of its Job's
// runs holding a worker, Running or Previewing, than the Job's concurrency (unset 1) and fewer of the node's than
// job.maxRunning. The node's free slots go to the oldest waiting runs whose Job is Ready and has one free, by creation
// time then name, so a Job at its limit holds no other back. A run of a held namespace, a cancelled or rejected run, one
// that waits for a person and one whose Job changed UID take none.
func (j *JobRunReconciler) slotFor(ctx context.Context, run *v1.JobRun) (string, error) {
	runs, err := j.ownRuns(ctx)
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "function.jobRunSlots", "list the runs")
	}
	running, total := map[jobKey]int{}, 0
	var waiting []*v1.JobRun
	for _, other := range runs {
		switch {
		case other.Status.Phase.Ended():
		case holdsSlot(other):
			running[jobKey{other.Namespace, other.Spec.Job}]++
			total++
		case waitsForSlot(other) && !j.cfg.NamespaceHold.HeldIn(other.Namespace):
			waiting = append(waiting, other)
		}
	}
	slices.SortFunc(waiting, func(a, b *v1.JobRun) int {
		return cmp.Or(time.Time(a.CreationTime).Compare(time.Time(b.CreationTime)), strings.Compare(string(a.Name), string(b.Name)),
			strings.Compare(string(a.Namespace), string(b.Namespace)))
	})
	jobs := map[jobKey]*v1.Job{}
	startable := func(w *v1.JobRun) (*v1.Job, error) {
		k := jobKey{w.Namespace, w.Spec.Job}
		job, seen := jobs[k]
		if !seen {
			if job, err = j.job(ctx, k.ns, k.name); err != nil {
				return nil, err
			}
			if job != nil && !jobReady(job) {
				job = nil
			}
			jobs[k] = job
		}
		if job == nil || (w.Status.JobUID != "" && w.Status.JobUID != job.UID) {
			return nil, nil
		}
		return job, nil
	}
	free, taken := j.cfg.MaxRunning-total, map[jobKey]int{}
	for _, w := range waiting {
		if free <= 0 {
			break
		}
		job, err := startable(w)
		if err != nil {
			return "", err
		}
		k := jobKey{w.Namespace, w.Spec.Job}
		if job == nil || running[k]+taken[k] >= max(job.Spec.Concurrency, 1) {
			continue
		}
		if w.UID == run.UID {
			return "", nil
		}
		taken[k]++
		free--
	}
	k := jobKey{run.Namespace, run.Spec.Job}
	if job := jobs[k]; job != nil && running[k] >= max(job.Spec.Concurrency, 1) {
		return fmt.Sprintf("Job %q is at its concurrency of %d", run.Spec.Job, max(job.Spec.Concurrency, 1)), nil
	}
	if total >= j.cfg.MaxRunning {
		return fmt.Sprintf("the node is at job.maxRunning %d", j.cfg.MaxRunning), nil
	}
	return fmt.Sprintf("the node's %d free slots of job.maxRunning %d go to older runs first", j.cfg.MaxRunning-total, j.cfg.MaxRunning), nil
}

// ownRuns lists the runs of the namespaces this process runs: those that a worker node runs take none of its slots, and
// its boot and its sweep leave them alone.
func (j *JobRunReconciler) ownRuns(ctx context.Context) ([]*v1.JobRun, error) {
	l, err := j.r.store.List(ctx, v1.KindJobRun.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*v1.JobRun, 0, len(l.Items))
	elsewhere := map[v1.NamespaceName]bool{}
	for _, o := range l.Items {
		run, ok := o.(*v1.JobRun)
		if !ok {
			continue
		}
		skip, seen := elsewhere[run.Namespace]
		if !seen {
			skip = j.cfg.Skip(run.Namespace)
			elsewhere[run.Namespace] = skip
		}
		if !skip {
			out = append(out, run)
		}
	}
	return out, nil
}

// inputOff is how run's input breaks the input schema of the revision it pins, "" when it does not (ADR-0347 Decision
// 4). The Job's status.contract is its current revision's; a pin of another revision reads that one's.
func (j *JobRunReconciler) inputOff(ctx context.Context, run *v1.JobRun, job *v1.Job, rev *v1.Revision) (string, error) {
	c := job.Status.Contract
	if rev.Name != job.Status.CurrentRevision {
		read, g, err := j.r.Jobs().contract(ctx, job, rev.Spec.ImageDigest)
		if err != nil {
			return "", err
		}
		if g != nil {
			return g.message, nil
		}
		c = read
	}
	if c == nil || len(c.Input) == 0 {
		return "", nil
	}
	v, err := contract.Compile(c.Input, contract.WithCallBounds(contractcheck.CallBudget, contractcheck.MaxDigits))
	if err != nil {
		return observability.StatusError(fmt.Sprintf("the input schema of revision %q does not compile: %v", rev.Name, err)), nil
	}
	input := run.Spec.Input
	if len(input) == 0 {
		input = json.RawMessage("null")
	}
	details, err := v.Validate(input)
	if err != nil {
		return observability.StatusError(fmt.Sprintf("the input is not JSON: %v", err)), nil
	}
	if len(details) == 0 {
		return "", nil
	}
	parts := make([]string, len(details))
	for i, d := range details {
		parts[i] = cmp.Or(d.Path, "/") + ": " + d.Message
	}
	return observability.StatusError(fmt.Sprintf("the input does not match the input schema of revision %q: %s", rev.Name,
		strings.Join(parts, "; "))), nil
}

// Boot ends every run stored Running Failed with Interrupted (ADR-0347 Decision 7), and puts every run stored
// Previewing back to Pending, so its preview is called again (ADR-0348 Decision 2). It runs after the drivers' sweeps
// removed every leftover worker and before the run reconciler starts any run, held or not, since it starts nothing:
// no handler is called twice, and no run's apply starts again on its own.
func (j *JobRunReconciler) Boot(ctx context.Context) error {
	const op = "function.BootJobRuns"
	runs, err := j.ownRuns(ctx)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list the runs")
	}
	now := v1.NewTimestamp(j.r.clock.Now())
	for _, run := range runs {
		switch run.Status.Phase {
		case v1.JobRunRunning:
			run.Status.End(v1.JobRunFailed, v1.JobRunInterrupted, interruptedMessage, now)
		case v1.JobRunPreviewing:
			repreview(run)
		default:
			continue
		}
		if _, err := j.r.store.Update(ctx, run); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "end jobrun %s/%s", run.Namespace, run.Name)
		}
	}
	return nil
}

// SweepExpired deletes every run that ended job.retention ago or more, and none while the platform or the run's
// namespace is held (ADR-0347 Decision 8). It returns how many it deleted.
func (j *JobRunReconciler) SweepExpired(ctx context.Context) (int, error) {
	const op = "function.SweepExpiredJobRuns"
	if j.cfg.Retention <= 0 || j.cfg.Hold.Held() {
		return 0, nil
	}
	runs, err := j.ownRuns(ctx)
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "list the runs")
	}
	cutoff, n := j.r.clock.Now().Add(-j.cfg.Retention), 0
	for _, run := range runs {
		if !run.Status.Phase.Ended() || run.Status.EndedAt.IsZero() || time.Time(run.Status.EndedAt).After(cutoff) ||
			j.cfg.NamespaceHold.HeldIn(run.Namespace) {
			continue
		}
		err := j.r.store.Delete(ctx, v1.KindJobRun.GVK(), run.Namespace, run.Name, run.ResourceVersion)
		switch {
		case err == nil:
			n++
		case fault.KindOf(err) != fault.NotFound && fault.KindOf(err) != fault.Conflict:
			return n, fault.Wrapf(err, fault.KindOf(err), op, "delete jobrun %s/%s", run.Namespace, run.Name)
		}
	}
	return n, nil
}

// MapJob maps a Job to its runs, whose passes start, wait or end on its change.
func (j *JobRunReconciler) MapJob(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	return j.runsWhere(ctx, m.Namespace, func(run *v1.JobRun) bool { return run.Spec.Job == m.Name && !run.Status.Phase.Ended() })
}

// MapRun maps a run that holds no slot, one that ended or waits for a person, to the waiting runs of the node, one of
// which may take the slot it freed.
func (j *JobRunReconciler) MapRun(ctx context.Context, obj v1.Object) []controller.Request {
	if run, ok := obj.(*v1.JobRun); !ok || (!run.Status.Phase.Ended() && run.Status.Phase != v1.JobRunAwaitingApproval) {
		return nil
	}
	return j.runsWhere(ctx, "", waitsForSlot)
}

func (j *JobRunReconciler) runsWhere(ctx context.Context, ns v1.NamespaceName, keep func(*v1.JobRun) bool) []controller.Request {
	l, err := j.r.store.List(ctx, v1.KindJobRun.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil
	}
	var out []controller.Request
	for _, o := range l.Items {
		if run, ok := o.(*v1.JobRun); ok && keep(run) {
			out = append(out, controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: run.Namespace, Name: run.Name})
		}
	}
	return out
}

// Close stops every executor, and so every run's worker, and waits for them to end, as funcd stops.
func (j *JobRunReconciler) Close() { j.execs.stopAll() }

// runExecutions are the executors of this process's runs, keyed by the run's UID (ADR-0347 Decision 4).
type runExecutions struct {
	mu    sync.Mutex
	byUID map[v1.UID]*execution
	live  sync.WaitGroup // every executor's goroutine, those already dropped from byUID too
}

// execution is one run's executor: its cancel, and its answer once done is closed.
type execution struct {
	ns     v1.NamespaceName
	name   v1.ObjectName
	cancel context.CancelFunc
	done   chan struct{}
	answer runAnswer
}

func (x *execution) finished() bool {
	select {
	case <-x.done:
		return true
	default:
		return false
	}
}

// start runs launch for run's call on a goroutine of its own, which keeps the answer and enqueues the run when it ends.
// It takes the place of the run's executor that ended, its preview's.
func (e *runExecutions) start(run *v1.JobRun, job *v1.Job, rev *v1.Revision, call runCall, launch runLauncher, enqueue func(controller.Request)) {
	ctx, cancel := context.WithCancel(context.Background())
	x := &execution{ns: run.Namespace, name: run.Name, cancel: cancel, done: make(chan struct{})}
	e.mu.Lock()
	e.byUID[run.UID] = x
	e.mu.Unlock()
	e.live.Add(1)
	go func() {
		defer e.live.Done()
		defer cancel()
		x.answer = launch(ctx, run, job, rev, call)
		close(x.done)
		enqueue(controller.Request{GVK: v1.KindJobRun.GVK(), Namespace: run.Namespace, Name: run.Name})
	}()
}

func (e *runExecutions) get(uid v1.UID) *execution {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.byUID[uid]
}

// forget cancels the executor of uid, a no-op once it ended, and drops it.
func (e *runExecutions) forget(uid v1.UID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if x, ok := e.byUID[uid]; ok {
		x.cancel()
		delete(e.byUID, uid)
	}
}

// stopNamed cancels and drops the executors of the run ns/name other than keep's: a deleted run's, or that of a run
// re-created under its name (ADR-0347 Decision 6).
func (e *runExecutions) stopNamed(ns v1.NamespaceName, name v1.ObjectName, keep v1.UID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for uid, x := range e.byUID {
		if x.ns == ns && x.name == name && uid != keep {
			x.cancel()
			delete(e.byUID, uid)
		}
	}
}

func (e *runExecutions) stopAll() {
	e.mu.Lock()
	for _, x := range e.byUID {
		x.cancel()
	}
	e.mu.Unlock()
	e.live.Wait()
}
