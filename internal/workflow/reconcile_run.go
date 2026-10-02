package workflow

import (
	"context"
	"log/slog"
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

	wfObj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), req.Namespace, run.Spec.Workflow)
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), runOp, "get workflow %q", run.Spec.Workflow)
	}
	wf := wfObj.(*v1.Workflow)

	// Cancel request (declarative, ADR-0094): abandon in-flight work and terminate Cancelled.
	// Checked before pause/drive — cancel wins over a concurrent pause. The controller workqueue
	// delivered this reconcile because spec.cancel was written; there is no synchronous path.
	if run.Spec.Cancel {
		return controller.Result{}, r.cancelRun(ctx, run, wf)
	}

	// Pause request: mark Paused, dispatch nothing.
	if run.Spec.Paused {
		if err := r.engine.Pause(ctx, req.Namespace, req.Name); err != nil && fault.KindOf(err) != fault.NotFound {
			return controller.Result{}, err
		}
		run.Status.Phase = runPaused
		if err := r.updateRunStatus(ctx, run); err != nil {
			return controller.Result{}, err
		}
		return controller.Result{}, nil
	}

	// Drive: resume if a durable record exists (recovery / unpause), else start fresh — a plain run
	// (pinning the ADR-0098 contract for the run-start input gate) or a replay seeded from a source run.
	rec, err := r.drive(withTransitions(ctx, r.mirrorTransition(run)), run, wf)
	if err != nil && fault.KindOf(err) != fault.Unavailable && fault.KindOf(err) != fault.Invalid {
		return controller.Result{}, err // infra error; requeue via the controller
	}
	// ADR-0107: a replay seed rejection (SeedInvalid/DigestDrift) produces no record — fail the run with
	// a ReplaySeeded=False condition so it terminates (never silently re-reconciles).
	if rec == nil && run.Spec.Replay != nil && fault.KindOf(err) == fault.Invalid {
		run.Status.Phase = runFailed
		run.Status.Conditions.Set(v1.Condition{
			Type: "ReplaySeeded", Status: v1.ConditionFalse,
			Reason: replayReason(err), Message: capErr(err.Error()),
		})
		if uerr := r.updateRunStatus(ctx, run); uerr != nil {
			return controller.Result{}, uerr
		}
		return controller.Result{}, nil
	}
	// A run failure is a terminal outcome, not a reconcile error.
	mirror(run, rec)
	if uerr := r.updateRunStatus(ctx, run); uerr != nil {
		return controller.Result{}, uerr
	}
	emitRunSpan(ctx, r.traces, rec, r.log) // ADR-0103: one run-root span at the terminal transition (no-op if non-terminal)
	if lerr := r.updateWorkflowLinks(ctx, wf.Namespace, wf.Name); lerr != nil {
		r.log.Warn("status.runs update failed", "workflow", wf.Name, "error", lerr)
	}
	return controller.Result{}, nil
}

// cancelRun abandons a run's in-flight work and terminates it Cancelled (ADR-0094), then mirrors
// the terminal state into WorkflowRun.status (so describe sees it and the reconciler's terminal
// short-circuit keeps it from being re-driven) and refreshes the parent's status.runs. It runs
// on the controller workqueue when it observes spec.cancel — the declarative cancel path.
func (r *RunReconciler) cancelRun(ctx context.Context, run *v1.WorkflowRun, wf *v1.Workflow) error {
	if err := r.engine.Cancel(ctx, run.Namespace, run.Name); err != nil && fault.KindOf(err) != fault.NotFound {
		return err
	}
	rec, gerr := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	if gerr == nil {
		mirror(run, rec)
	} else {
		run.Status.Phase = runCancelled
	}
	if uerr := r.updateRunStatus(ctx, run); uerr != nil {
		return uerr
	}
	emitRunSpan(ctx, r.traces, rec, r.log) // ADR-0103: the cancelled run's root span (the distinct second emit site)
	if lerr := r.updateWorkflowLinks(ctx, wf.Namespace, wf.Name); lerr != nil {
		r.log.Warn("status.runs update failed after cancel", "workflow", wf.Name, "error", lerr)
	}
	return nil
}

func (r *RunReconciler) drive(ctx context.Context, run *v1.WorkflowRun, wf *v1.Workflow) (*runstate.Record, error) {
	ns, name := run.Namespace, run.Name
	if _, err := r.engine.runs.Get(ctx, ns, name); err == nil {
		// A durable record exists → resume from its PINNED spec + contract (the live wf.Spec/status is
		// not passed; an in-flight run is immune to a mid-run edit or re-push). Covers replay recovery too.
		return r.engine.Resume(ctx, ns, name)
	}
	images := stepImages(wf) // the ADR-0098 cache: step → resolved digest-pinned image (ADR-0107)
	if run.Spec.Replay != nil {
		// ADR-0107: seed a replay from the source run's checkpoint + gate on digest drift.
		return r.engine.Replay(ctx, ns, name, wf.Name, *run.Spec.Replay, images)
	}
	return r.engine.Execute(ctx, ns, name, wf.Name, wf.Spec, run.Spec.Input, StartOptions{Contract: wf.Status.Contract, StepImages: images})
}

// replayReason extracts the leading reason token (SeedInvalid / DigestDrift) from a replay-seed
// rejection's fault message for the ReplaySeeded condition; "ReplayRejected" if none matches.
func replayReason(err error) string {
	msg := err.Error()
	for _, tok := range []string{"SeedInvalid", "DigestDrift"} {
		if strings.Contains(msg, tok+":") {
			return tok
		}
	}
	return "ReplayRejected"
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
		if err := r.updateWorkflowLinks(ctx, run.Namespace, run.Spec.Workflow); err != nil {
			r.log.Warn("status.runs update failed", "workflow", run.Spec.Workflow, "error", err)
		}
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

// updateWorkflowLinks recomputes the parent Workflow's status.runs from the metastore:
// active (non-terminal) run names + lifetime terminal-phase counts (bounded — only
// active runs are enumerated). It re-reads the Workflow, because each run transition
// rewrites it.
func (r *RunReconciler) updateWorkflowLinks(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, name)
	if err != nil {
		return err
	}
	wf := obj.(*v1.Workflow)
	list, err := r.store.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return err
	}
	links := &v1.WorkflowRunLinks{}
	for _, o := range list.Items {
		run := o.(*v1.WorkflowRun)
		if run.Spec.Workflow != wf.Name {
			continue
		}
		switch run.Status.Phase {
		case runSucceeded:
			links.Succeeded++
		case runFailed:
			links.Failed++
		case runCancelled:
			links.Cancelled++
		default:
			links.Active = append(links.Active, run.Name)
		}
	}
	wf.Status.Runs = links
	if _, err := r.store.Update(ctx, wf); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "update workflow links %q", wf.Name)
	}
	return nil
}

func isRunTerminal(p v1.Phase) bool {
	switch p {
	case runSucceeded, runFailed, runCancelled:
		return true
	default:
		return false
	}
}
