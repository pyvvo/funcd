package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

const runOp = "workflow.reconcileRun"

// waitRequeue re-checks a run that waits for its Workflow (ADR-0121's bounded requeue for a consumer
// whose referent is not there or not Ready).
const waitRequeue = 2 * time.Second

// RunReconciler drives WorkflowRun resources: it runs the engine, mirrors the coarse
// run state into WorkflowRun.status, and maintains the parent Workflow's status.runs
// link (ADR-0094). It is a controller.Reconciler.
type RunReconciler struct {
	store  store.Store
	engine *Engine
	traces funclog.TraceSink // ADR-0103: emits the run-root span at terminal; nil ⇒ no span (additive)
	log    *slog.Logger
}

// NewRunReconciler builds the run reconciler. traces is the shared funclog trace sink (ADR-0103): when
// non-nil, the reconciler emits one INTERNAL run-root span per terminal run so the run's step spans
// (F51, parented on the run root) nest under it. nil ⇒ no run-root span (additive).
func NewRunReconciler(s store.Store, e *Engine, traces funclog.TraceSink, log *slog.Logger) *RunReconciler {
	if log == nil {
		log = slog.Default()
	}
	return &RunReconciler{store: s, engine: e, traces: traces, log: log.With("component", "workflow.run")}
}

// buildRunSpan builds the run-root Resource + INTERNAL Span for a terminal run (ADR-0103; shared by the
// reconciler for top-level runs and the engine for inline sub-workflow child runs, ADR-0104). SpanID is the
// run's RootSpanID (ADR-0102), so the step spans parented on it nest under this root; ParentID is
// rec.RootParentID — "" for a top-level run, the parent run's RootSpanID for a sub-workflow child (ADR-0104).
func buildRunSpan(rec *runstate.Record) (funclog.Resource, funclog.Span) {
	status := funclog.StatusOk
	if rec.Phase != runSucceeded {
		status = funclog.StatusError
	}
	sp := funclog.Span{
		TraceID:  rec.TraceID,
		SpanID:   rec.RootSpanID,
		ParentID: rec.RootParentID, // ADR-0104: "" ⇒ trace root; else nests under the parent run span
		Name:     string(rec.Workflow),
		Kind:     funclog.SpanInternal,
		Start:    time.Unix(0, rec.StartedAt),
		End:      time.Unix(0, rec.UpdatedAt),
		Status:   status,
		Attrs:    map[string]string{"funcd.run": string(rec.Name), "funcd.phase": string(rec.Phase)},
	}
	res := funclog.Resource{Namespace: string(rec.Namespace), Function: string(rec.Workflow), Replica: string(rec.Name)}
	return res, sp
}

// emitRunSpan writes one run-root span to sink for a terminal run (ADR-0103/0104). Defensive no-op unless
// sink != nil, rec != nil, rec.Terminal(), and rec has a trace context. Best-effort: a sink error is logged,
// never failing the caller. Shared by the reconciler (top-level runs) and the engine (inline child runs).
func emitRunSpan(ctx context.Context, sink funclog.TraceSink, rec *runstate.Record, log *slog.Logger) {
	if sink == nil || rec == nil || !rec.Terminal() || rec.TraceID == "" {
		return
	}
	res, sp := buildRunSpan(rec)
	if err := sink.AppendSpan(ctx, res, sp); err != nil {
		log.WarnContext(ctx, "workflow: run-root span emit failed", "run", rec.Name, "error", err)
	}
}

// Reconcile drives one WorkflowRun toward its terminal phase.
func (r *RunReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil // deleted
	}
	if err != nil {
		return controller.Result{}, err
	}
	run := obj.(*v1.WorkflowRun)
	if isRunTerminal(run.Status.Phase) {
		return controller.Result{}, nil
	}
	started, err := r.started(ctx, run)
	if err != nil {
		return controller.Result{}, err
	}

	wfObj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), req.Namespace, run.Spec.Workflow)
	if err != nil && fault.KindOf(err) != fault.NotFound {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), runOp, "get workflow %q", run.Spec.Workflow)
	}
	wf, _ := wfObj.(*v1.Workflow) // nil when the Workflow is missing (not created yet, or deleted)

	// Cancel request (declarative, ADR-0094): abandon in-flight work and terminate Cancelled.
	// Checked before pause/drive — cancel wins over a concurrent pause. The controller workqueue
	// delivered this reconcile because spec.cancel was written; there is no synchronous path.
	if run.Spec.Cancel {
		return controller.Result{}, r.cancelRun(ctx, run)
	}

	// Pause request: mark Paused, dispatch nothing. A run that finished before the pause keeps its phase.
	if run.Spec.Paused {
		return controller.Result{}, r.applyRequest(ctx, run, r.engine.Pause, runPaused)
	}

	// A run that has not started waits while its Workflow is missing (ADR-0121) or the F65 gate holds it
	// Ready=False (a WorkflowCycle, a type mismatch): such a workflow never runs (ADR-0098/0099). A
	// started run resumes its pinned spec.
	if !started {
		if wf == nil {
			return r.wait(ctx, run, "WorkflowNotFound", fmt.Sprintf("workflow %q not found; waiting", run.Spec.Workflow))
		}
		if c, ok := wf.Status.Conditions.Get(condReady); ok && c.Status == v1.ConditionFalse {
			return r.wait(ctx, run, "WorkflowNotReady", fmt.Sprintf("workflow %q is not Ready (%s): %s; waiting", wf.Name, c.Reason, c.Message))
		}
	}
	if c, ok := run.Status.Conditions.Get(condReady); ok && c.Status == v1.ConditionFalse {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue}) // the wait is over
	}

	// Drive: resume if a durable record exists (recovery / unpause), else start fresh — a plain run
	// (pinning the ADR-0098 contract for the run-start input gate) or a replay seeded from a source run.
	rec, err := r.drive(withTransitions(ctx, r.mirrorTransition(run)), run, wf, started)
	// A first record over the run store's value limit even without its input is refused on every requeue.
	if rec == nil && !started && fault.KindOf(err) == fault.PayloadTooLarge {
		return r.failUnrecorded(ctx, run, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: failureReason(err.Error()), Message: capErr(err.Error())})
	}
	if err != nil && fault.KindOf(err) != fault.Unavailable && fault.KindOf(err) != fault.Invalid {
		return controller.Result{}, err // infra error; requeue via the controller
	}
	// ADR-0107: a replay seed rejection (SeedInvalid/DigestDrift) produces no record — fail the run with
	// a ReplaySeeded=False condition so it terminates (never silently re-reconciles).
	if rec == nil && run.Spec.Replay != nil && fault.KindOf(err) == fault.Invalid {
		return r.failUnrecorded(ctx, run, v1.Condition{
			Type: "ReplaySeeded", Status: v1.ConditionFalse,
			Reason: replayReason(err), Message: capErr(err.Error()),
		})
	}
	// A run failure is a terminal outcome, not a reconcile error.
	mirror(run, rec)
	if uerr := r.updateRunStatus(ctx, run); uerr != nil {
		return controller.Result{}, uerr
	}
	emitRunSpan(ctx, r.traces, rec, r.log) // ADR-0103: one run-root span at the terminal transition (no-op if non-terminal)
	r.linkRun(ctx, run)
	return controller.Result{}, nil
}

// cancelRun abandons a run's in-flight work and terminates it Cancelled (ADR-0094), then mirrors
// the terminal state into WorkflowRun.status (so describe sees it and the reconciler's terminal
// short-circuit keeps it from being re-driven) and refreshes the parent's status.runs. It runs
// on the controller workqueue when it observes spec.cancel — the declarative cancel path.
func (r *RunReconciler) cancelRun(ctx context.Context, run *v1.WorkflowRun) error {
	return r.applyRequest(ctx, run, r.engine.Cancel, runCancelled)
}

// applyRequest applies a declarative cancel or pause through op, then mirrors the run record into
// WorkflowRun.status (fallback when the run has no record) and refreshes the parent's status.runs. op
// leaves a terminal record unchanged, so a run that finished first is mirrored with its own phase.
func (r *RunReconciler) applyRequest(ctx context.Context, run *v1.WorkflowRun, op func(context.Context, v1.NamespaceName, v1.ObjectName) error, fallback v1.Phase) error {
	if err := op(ctx, run.Namespace, run.Name); err != nil && fault.KindOf(err) != fault.NotFound {
		return err
	}
	rec, gerr := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	if gerr == nil {
		mirror(run, rec)
	} else {
		run.Status.Phase = fallback
	}
	if uerr := r.updateRunStatus(ctx, run); uerr != nil {
		return uerr
	}
	emitRunSpan(ctx, r.traces, rec, r.log) // ADR-0103: the cancelled or finished run's root span (the distinct second emit site)
	r.linkRun(ctx, run)
	return nil
}

// failUnrecorded ends a run that has no run record, and never gets one, Failed with c saying why.
func (r *RunReconciler) failUnrecorded(ctx context.Context, run *v1.WorkflowRun, c v1.Condition) (controller.Result, error) {
	run.Status.Phase = runFailed
	run.Status.Conditions.Set(c)
	if err := r.updateRunStatus(ctx, run); err != nil {
		return controller.Result{}, err
	}
	r.linkRun(ctx, run)
	return controller.Result{}, nil
}

// wait holds a run that has not started Pending with a Ready=False condition saying why, lists it in
// its Workflow's status.runs.active, and re-checks it after waitRequeue.
func (r *RunReconciler) wait(ctx context.Context, run *v1.WorkflowRun, reason, msg string) (controller.Result, error) {
	run.Status.Phase = runPending
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: capErr(msg)})
	if err := r.updateRunStatus(ctx, run); err != nil {
		return controller.Result{}, err
	}
	r.linkRun(ctx, run)
	return controller.Result{RequeueAfter: waitRequeue}, nil
}

func (r *RunReconciler) drive(ctx context.Context, run *v1.WorkflowRun, wf *v1.Workflow, started bool) (*runstate.Record, error) {
	ns, name := run.Namespace, run.Name
	if started {
		// A durable record exists → resume from its PINNED spec + contract (the live wf.Spec/status is
		// not passed; an in-flight run is immune to a mid-run edit or re-push). Covers replay recovery too.
		return r.engine.Resume(ctx, ns, name)
	}
	images := stepImages(wf) // the ADR-0098 cache: step → resolved digest-pinned image (ADR-0107)
	if run.Spec.Replay != nil {
		// ADR-0107: seed a replay from the source run's checkpoint + gate on digest drift. A source with no
		// run record (swept by retention) can never seed it, so that is a seed rejection, not a retry.
		rec, err := r.engine.replay(ctx, ns, name, run.UID, wf.Name, *run.Spec.Replay, images)
		if fault.KindOf(err) == fault.NotFound {
			err = fault.Wrapf(err, fault.Invalid, runOp, "SeedInvalid: replay source run %q has no run record", run.Spec.Replay.Run)
		}
		return rec, err
	}
	return r.engine.Execute(ctx, ns, name, wf.Name, wf.Spec, run.Spec.Input, StartOptions{Contract: wf.Status.Contract, StepImages: images, RunUID: run.UID})
}

// started reports whether run has an engine record of its own. The record that an earlier WorkflowRun of
// the same name, since deleted, left behind is deleted instead, so run starts fresh on its own spec and
// input rather than resuming or reporting that run.
func (r *RunReconciler) started(ctx context.Context, run *v1.WorkflowRun) (bool, error) {
	rec, err := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return false, nil
	case err != nil:
		return false, fault.Wrapf(err, fault.KindOf(err), runOp, "get run record %q", run.Name)
	case foreignRecord(rec, run.UID):
		if err := r.engine.runs.Delete(ctx, run.Namespace, run.Name); err != nil {
			return false, fault.Wrapf(err, fault.KindOf(err), runOp, "delete the record of an earlier run %q", run.Name)
		}
		return false, nil
	}
	return true, nil
}

// foreignRecord reports whether rec belongs to a WorkflowRun other than the one with uid: an earlier one
// of the same name. A record without a uid predates the stamp and is matched by name.
func foreignRecord(rec *runstate.Record, uid v1.UID) bool {
	return rec.RunUID != "" && rec.RunUID != uid
}

// replayReason extracts the reason token (SeedInvalid / DigestDrift) from a replay-seed rejection's
// fault message for the ReplaySeeded condition; "ReplayRejected" if none matches.
func replayReason(err error) string {
	return reasonToken(err.Error(), "ReplayRejected", "SeedInvalid", "DigestDrift")
}

// failureReason is the Ready=False reason of a Failed run: the ADR-0094/0099 run failure reason its
// cause names, else StepFailed (every other run failure is a step's).
func failureReason(cause string) string {
	return reasonToken(cause, "StepFailed", "InputSchemaMismatch", "RunTimedOut", "SubworkflowDepthExceeded")
}

// reasonToken returns the first of tokens that msg names, else fallback.
func reasonToken(msg, fallback string, tokens ...string) string {
	for _, tok := range tokens {
		if strings.Contains(msg, tok) {
			return tok
		}
	}
	return fallback
}

// stepImages projects the workflow's cached resolved step images (ADR-0098 status.steps[].Image) into
// the name→image map the engine stamps as each step's revision and the replay gate compares (ADR-0107).
func stepImages(wf *v1.Workflow) map[v1.ObjectName]string {
	if len(wf.Status.Steps) == 0 {
		return nil
	}
	m := make(map[v1.ObjectName]string, len(wf.Status.Steps))
	for _, s := range wf.Status.Steps {
		if s.Image != "" {
			m[s.Name] = s.Image
		}
	}
	return m
}

// mirror copies the engine record's coarse state into the WorkflowRun status.
func mirror(run *v1.WorkflowRun, rec *runstate.Record) {
	if rec == nil {
		return
	}
	run.Status.Phase = rec.Phase
	run.Status.TraceID = rec.TraceID // ADR-0100: mirror the run trace so describe + workflow logs (ADR-0106) find it
	if rec.Phase == runFailed && rec.Error != "" {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: failureReason(rec.Error), Message: rec.Error})
	}
	run.Status.Steps = run.Status.Steps[:0]
	for _, s := range rec.Steps {
		run.Status.Steps = append(run.Status.Steps, v1.RunStepStatus{
			Name: s.Name, Phase: s.Phase, Attempts: s.Attempts, Revision: s.Revision,
			StartedAt: s.StartedAt, EndedAt: s.EndedAt, Error: s.Error, // ADR-0100 troubleshooting facts
		})
	}
}

// mirrorTransition returns the engine's write observer for run: each non-terminal write of run's own
// record is mirrored into WorkflowRun.status and the parent's status.runs as it happens (ADR-0094 "per
// transition"). An inline sub-workflow child's record is not run's; the terminal write is mirrored after
// drive returns, with the run-root span. Best-effort: a failed write is logged, never failing the run.
func (r *RunReconciler) mirrorTransition(run *v1.WorkflowRun) func(context.Context, *runstate.Record) {
	return func(ctx context.Context, rec *runstate.Record) {
		if rec.Namespace != run.Namespace || rec.Name != run.Name || rec.Terminal() {
			return
		}
		mirror(run, rec)
		if err := r.updateRunStatus(ctx, run); err != nil {
			r.log.Warn("run status update failed", "run", run.Name, "error", err)
			return
		}
		r.linkRun(ctx, run)
	}
}

// updateRunStatus writes run and adopts the new resourceVersion, so a later write in the same
// reconcile (the next transition) is not rejected as stale.
func (r *RunReconciler) updateRunStatus(ctx context.Context, run *v1.WorkflowRun) error {
	out, err := r.store.Update(ctx, run)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "update run status %q", run.Name)
	}
	run.ResourceVersion = out.GetObjectMeta().ResourceVersion
	return nil
}

// linkAttempts bounds the optimistic-concurrency retries of a status.runs update: the counts are
// incremental, so an update lost to a Conflict would lose a count.
const linkAttempts = 5

// linkRun folds the run's new status into its Workflow's status.runs; a failure is logged, never
// failing the reconcile.
func (r *RunReconciler) linkRun(ctx context.Context, run *v1.WorkflowRun) {
	var err error
	for range linkAttempts {
		if err = r.updateWorkflowLinks(ctx, run); fault.KindOf(err) != fault.Conflict {
			break
		}
	}
	if err != nil {
		r.log.Warn("status.runs update failed", "workflow", run.Spec.Workflow, "run", run.Name, "error", err)
	}
}

// updateWorkflowLinks maintains the parent Workflow's status.runs (ADR-0094) from the run whose status
// this reconcile just wrote: a non-terminal run joins active (newest first); a terminal one leaves it
// and is counted. The reconciler writes a run's terminal status once, so it is counted once, and the
// counts are lifetime: the retention sweep deleting closed runs never lowers them. Only the runs in
// active are read (bounded); one deleted or closed without this update is dropped from active.
func (r *RunReconciler) updateWorkflowLinks(ctx context.Context, run *v1.WorkflowRun) error {
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
	if fault.KindOf(err) == fault.NotFound { // the Workflow is missing: there is no status.runs to keep
		return nil
	}
	if err != nil {
		return err
	}
	wf := obj.(*v1.Workflow)
	links := v1.WorkflowRunLinks{}
	if wf.Status.Runs != nil {
		links = *wf.Status.Runs
	}
	prev := links.Active
	links.Active = nil
	switch run.Status.Phase {
	case runSucceeded:
		links.Succeeded++
	case runFailed:
		links.Failed++
	case runCancelled:
		links.Cancelled++
	default:
		if !slices.Contains(prev, run.Name) {
			links.Active = append(links.Active, run.Name)
		}
	}
	for _, name := range prev {
		phase := run.Status.Phase
		if name != run.Name {
			o, gerr := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), run.Namespace, name)
			if fault.KindOf(gerr) == fault.NotFound {
				continue
			}
			if gerr != nil {
				return gerr
			}
			phase = o.(*v1.WorkflowRun).Status.Phase
		}
		if !isRunTerminal(phase) {
			links.Active = append(links.Active, name)
		}
	}
	wf.Status.Runs = &links
	if _, err := r.store.Update(ctx, wf); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "update workflow links %q", wf.Name)
	}
	return nil
}

// SweepExpired reclaims the closed runs older than retention (ADR-0094): each expired engine record
// and the WorkflowRun object that `workflow runs` lists, whose status is swept with the run (ADR-0100).
// Returns the number of runs reclaimed.
func (r *RunReconciler) SweepExpired(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	if err := r.recordClosedRuns(ctx); err != nil {
		return 0, err
	}
	return r.engine.SweepExpired(ctx, retention, r.deleteRun)
}

// recordClosedRuns gives each closed WorkflowRun that has no engine record (cancelled before its first
// drive, a rejected replay seed, or a record an earlier sweep deleted alone) a terminal record stamped
// now, so the record sweep reclaims it retention after it is first seen. A closed run is never driven
// again, so the record has no other writer.
func (r *RunReconciler) recordClosedRuns(ctx context.Context) error {
	list, err := r.store.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "list runs for retention sweep")
	}
	now := r.engine.clock.Now().UnixNano()
	for _, obj := range list.Items {
		run := obj.(*v1.WorkflowRun)
		if !isRunTerminal(run.Status.Phase) {
			continue
		}
		_, gerr := r.engine.runs.Get(ctx, run.Namespace, run.Name)
		if gerr == nil {
			continue
		}
		if fault.KindOf(gerr) != fault.NotFound {
			return fault.Wrapf(gerr, fault.KindOf(gerr), runOp, "get record of closed run %q", run.Name)
		}
		rec := &runstate.Record{Namespace: run.Namespace, Name: run.Name, Workflow: run.Spec.Workflow, Phase: run.Status.Phase, StartedAt: now, UpdatedAt: now}
		if err := r.engine.runs.Put(ctx, rec); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), runOp, "record closed run %q", run.Name)
		}
	}
	return nil
}

// deleteRun deletes the WorkflowRun object of an expired run record. An inline sub-workflow child run
// has none, and a WorkflowRun re-created under the record's name is not the record's, so it is kept.
func (r *RunReconciler) deleteRun(ctx context.Context, rec *runstate.Record) error {
	obj, err := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), rec.Namespace, rec.Name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	meta := obj.GetObjectMeta()
	if foreignRecord(rec, meta.UID) {
		return nil
	}
	err = r.store.Delete(ctx, v1.KindWorkflowRun.GVK(), rec.Namespace, rec.Name, meta.ResourceVersion)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

func isRunTerminal(p v1.Phase) bool {
	switch p {
	case runSucceeded, runFailed, runCancelled:
		return true
	default:
		return false
	}
}
