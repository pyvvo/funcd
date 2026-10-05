package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// RunReconciler drives WorkflowRun resources: it starts, signals and stops their engine runs, mirrors the
// coarse run state into WorkflowRun.status (its only writer, ADR-0146), and maintains the parent Workflow's
// status.runs link (ADR-0094). It is a controller.Reconciler.
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

// Reconcile starts, signals or stops one WorkflowRun and mirrors its run record into status, never waiting
// for a step (ADR-0146 Decision 1): the run executes on an engine-owned goroutine, whose record writes and
// exit enqueue the run again.
func (r *RunReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		r.engine.forget(req.Namespace, req.Name) // a deleted run stops (ADR-0146)
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, err
	}
	run := obj.(*v1.WorkflowRun)
	if isRunTerminal(run.Status.Phase) {
		return controller.Result{}, nil
	}
	before, err := json.Marshal(run.Status)
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.Internal, runOp, "encode run status %q", run.Name)
	}
	uid, live := r.engine.live(run.Namespace, run.Name)
	if live && uid != run.UID { // deleted and re-created: stop the earlier run; its exit enqueues this one
		r.engine.cancelLive(run.Namespace, run.Name)
		return controller.Result{}, nil
	}
	// Cancel wins over a concurrent pause.
	var fallback v1.Phase
	switch {
	case run.Spec.Cancel:
		if err := r.engine.Cancel(ctx, run.Namespace, run.Name); err != nil && fault.KindOf(err) != fault.NotFound {
			return controller.Result{}, err
		}
		fallback = runCancelled
	case run.Spec.Paused:
		if err := r.engine.Pause(ctx, run.Namespace, run.Name); err != nil && fault.KindOf(err) != fault.NotFound {
			return controller.Result{}, err
		}
		fallback = runPaused
	case !live:
		if res, done, err := r.start(ctx, run, before); done || err != nil {
			return res, err
		}
	}
	return r.syncStatus(ctx, run, before, fallback)
}

// start starts the run's goroutine, routed as before ADR-0146: a run record ⇒ resume; else spec.replay ⇒
// replay; else execute. A run that has not started waits while its Workflow is missing (ADR-0121) or the F65
// gate holds it Ready=False (a WorkflowCycle, a type mismatch). It maps the error a previous goroutine of the
// run exited with. done reports that the pass ends with res and err, without the status sync.
func (r *RunReconciler) start(ctx context.Context, run *v1.WorkflowRun, before []byte) (res controller.Result, done bool, err error) {
	rec, err := r.ownRecord(ctx, run)
	if err != nil {
		return controller.Result{}, true, err
	}
	var wf *v1.Workflow
	if rec == nil {
		wfObj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
		if err != nil && fault.KindOf(err) != fault.NotFound {
			return controller.Result{}, true, fault.Wrapf(err, fault.KindOf(err), runOp, "get workflow %q", run.Spec.Workflow)
		}
		wf, _ = wfObj.(*v1.Workflow)
		if wf == nil {
			res, err := r.wait(ctx, run, before, "WorkflowNotFound", fmt.Sprintf("workflow %q not found; waiting", run.Spec.Workflow))
			return res, true, err
		}
		if c, ok := wf.Status.Conditions.Get(condReady); ok && c.Status == v1.ConditionFalse {
			res, err := r.wait(ctx, run, before, "WorkflowNotReady", fmt.Sprintf("workflow %q is not Ready (%s): %s; waiting", wf.Name, c.Reason, c.Message))
			return res, true, err
		}
	}
	if c, ok := run.Status.Conditions.Get(condReady); ok && c.Status == v1.ConditionFalse {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue}) // the wait is over
	}
	if rec != nil && rec.Terminal() {
		return controller.Result{}, false, nil
	}
	prev, err := r.engine.start(run.UID, run.Namespace, run.Name, r.driveFunc(run, wf, rec != nil))
	if errors.Is(err, errDraining) || err == nil {
		return controller.Result{}, false, nil
	}
	// A first record over the run store's value limit even without its input is refused on every start.
	if prev == nil && rec == nil && fault.KindOf(err) == fault.PayloadTooLarge {
		res, err := r.failUnrecorded(ctx, run, before, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "RunRecordTooLarge", Message: capErr(err.Error())})
		return res, true, err
	}
	// ADR-0107: a replay seed rejection (SeedInvalid/DigestDrift) produces no record — fail the run with
	// a ReplaySeeded=False condition so it terminates (never silently re-reconciles).
	if prev == nil && run.Spec.Replay != nil && fault.KindOf(err) == fault.Invalid {
		res, err := r.failUnrecorded(ctx, run, before, v1.Condition{
			Type: "ReplaySeeded", Status: v1.ConditionFalse,
			Reason: replayReason(err), Message: capErr(err.Error()),
		})
		return res, true, err
	}
	return controller.Result{}, true, err // a run-store fault: the next pass resumes from the durable record
}

// driveFunc is the run's drive on its engine-owned goroutine: resume from the record's PINNED spec and
// contract when started (the live wf.Spec/status is not passed; an in-flight run is immune to a mid-run edit
// or re-push; covers replay recovery too), else a replay seeded from a source run, else a fresh execute
// pinning the ADR-0098 contract for the run-start input gate.
func (r *RunReconciler) driveFunc(run *v1.WorkflowRun, wf *v1.Workflow, started bool) func(context.Context) (*runstate.Record, error) {
	ns, name, uid, replay, input := run.Namespace, run.Name, run.UID, run.Spec.Replay, run.Spec.Input
	return func(ctx context.Context) (*runstate.Record, error) {
		if started {
			return r.engine.Resume(ctx, ns, name)
		}
		images := stepImages(wf) // the ADR-0098 cache: step → resolved digest-pinned image (ADR-0107)
		if replay != nil {
			// ADR-0107: seed a replay from the source run's checkpoint + gate on digest drift. A source with no
			// run record (swept by retention) can never seed it, so that is a seed rejection, not a retry.
			rec, err := r.engine.replay(ctx, ns, name, uid, wf.Name, *replay, images)
			if fault.KindOf(err) == fault.NotFound {
				err = fault.Wrapf(err, fault.Invalid, runOp, "SeedInvalid: replay source run %q has no run record", replay.Run)
			}
			return rec, err
		}
		return r.engine.Execute(ctx, ns, name, wf.Name, wf.Spec, input, StartOptions{Contract: wf.Status.Contract, StepImages: images, StepContracts: stepContracts(wf), RunUID: uid})
	}
}

// syncStatus mirrors the run record into WorkflowRun.status (ADR-0146 Decision 4: the only status writer).
// With no record of its own, a cancel or pause sets fallback. A terminal phase, from the record or the
// fallback, is written only once the run's goroutine exited: its exit enqueues the run again.
func (r *RunReconciler) syncStatus(ctx context.Context, run *v1.WorkflowRun, before []byte, fallback v1.Phase) (controller.Result, error) {
	rec, err := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound || err == nil && foreignRecord(rec, run.UID):
		rec = nil
		if fallback != "" {
			run.Status.Phase = fallback
		}
	case err != nil:
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), runOp, "get run record %q", run.Name)
	}
	mirror(run, rec)
	if isRunTerminal(run.Status.Phase) {
		if _, live := r.engine.live(run.Namespace, run.Name); live {
			return controller.Result{}, nil
		}
	}
	return r.writeStatus(ctx, run, before, rec)
}

// writeStatus writes run's status when it differs from before (an unchanged pass writes nothing,
// ADR-0047/0142), emits the run-root span of a terminal record (ADR-0103: the one emit site of a top-level
// run) and folds the status into the Workflow's status.runs. A Conflict requeues: the next pass reads the
// newer object.
func (r *RunReconciler) writeStatus(ctx context.Context, run *v1.WorkflowRun, before []byte, rec *runstate.Record) (controller.Result, error) {
	after, err := json.Marshal(run.Status)
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.Internal, runOp, "encode run status %q", run.Name)
	}
	if bytes.Equal(before, after) {
		return controller.Result{}, nil
	}
	if _, err := r.store.Update(ctx, run); err != nil {
		if fault.KindOf(err) == fault.Conflict {
			return controller.Result{Requeue: true}, nil
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), runOp, "update run status %q", run.Name)
	}
	if rec != nil && isRunTerminal(run.Status.Phase) {
		emitRunSpan(ctx, r.traces, rec, r.log)
	}
	r.linkRun(ctx, run)
	return controller.Result{}, nil
}

// failUnrecorded ends a run that has no run record, and never gets one, Failed with c saying why.
func (r *RunReconciler) failUnrecorded(ctx context.Context, run *v1.WorkflowRun, before []byte, c v1.Condition) (controller.Result, error) {
	run.Status.Phase = runFailed
	run.Status.Conditions.Set(c)
	return r.writeStatus(ctx, run, before, nil)
}

// wait holds a run that has not started Pending with a Ready=False condition saying why, lists it in
// its Workflow's status.runs.active, and re-checks it after waitRequeue.
func (r *RunReconciler) wait(ctx context.Context, run *v1.WorkflowRun, before []byte, reason, msg string) (controller.Result, error) {
	run.Status.Phase = runPending
	run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: capErr(msg)})
	res, err := r.writeStatus(ctx, run, before, nil)
	if err != nil || res.Requeue {
		return res, err
	}
	return controller.Result{RequeueAfter: waitRequeue}, nil
}

// ownRecord returns run's own engine record, nil when it has none. The record that an earlier WorkflowRun of
// the same name, since deleted, left behind is deleted instead, so run starts fresh on its own spec and
// input rather than resuming or reporting that run.
func (r *RunReconciler) ownRecord(ctx context.Context, run *v1.WorkflowRun) (*runstate.Record, error) {
	rec, err := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), runOp, "get run record %q", run.Name)
	case foreignRecord(rec, run.UID):
		if err := r.engine.runs.Delete(ctx, run.Namespace, run.Name); err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), runOp, "delete the record of an earlier run %q", run.Name)
		}
		return nil, nil
	}
	return rec, nil
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

// failureReason is the Ready=False reason of a Failed run: the run failure reason its cause names (the
// ADR-0094/0099 reasons, or a run-start payload cap or run record size refusal), else StepFailed (every
// other run failure is a step's).
func failureReason(cause string) string {
	return reasonToken(cause, "StepFailed", "InputSchemaMismatch", "RunTimedOut", "SubworkflowDepthExceeded", "PayloadLimitExceeded", "RunRecordTooLarge")
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

// stepContracts returns the ADR-0098 cache's per-step I/O contracts, pinned on the run so a when: binds
// a schema default at evaluation (ADR-0095).
func stepContracts(wf *v1.Workflow) map[v1.ObjectName]v1.WorkflowContract {
	if len(wf.Status.Steps) == 0 {
		return nil
	}
	m := make(map[v1.ObjectName]v1.WorkflowContract, len(wf.Status.Steps))
	for _, s := range wf.Status.Steps {
		if s.Contract != nil {
			m[s.Name] = *s.Contract
		}
	}
	return m
}

// mirror copies the engine record's coarse state into the WorkflowRun status; nil leaves it unchanged.
func mirror(run *v1.WorkflowRun, rec *runstate.Record) {
	if rec == nil {
		return
	}
	run.Status.Phase = rec.Phase
	run.Status.TraceID = rec.TraceID // ADR-0100: mirror the run trace so describe + workflow logs (ADR-0106) find it
	if rec.Phase == runFailed && rec.Error != "" {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: failureReason(rec.Error), Message: rec.Error})
	}
	steps := make([]v1.RunStepStatus, 0, len(rec.Steps))
	for _, s := range rec.Steps {
		steps = append(steps, v1.RunStepStatus{
			Name: s.Name, Phase: s.Phase, Attempts: s.Attempts, Revision: s.Revision,
			StartedAt: s.StartedAt, EndedAt: s.EndedAt, Error: s.Error, // ADR-0100 troubleshooting facts
		})
	}
	run.Status.Steps = steps
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
