package workflow

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
)

const runOp = "workflow.reconcileRun"

// RunReconciler drives WorkflowRun resources: it runs the engine, mirrors the coarse
// run state into WorkflowRun.status, and maintains the parent Workflow's status.runs
// link (ADR-0094). It is a controller.Reconciler.
type RunReconciler struct {
	store  store.Store
	engine *Engine
	log    *slog.Logger
}

// NewRunReconciler builds the run reconciler.
func NewRunReconciler(s store.Store, e *Engine, log *slog.Logger) *RunReconciler {
	if log == nil {
		log = slog.Default()
	}
	return &RunReconciler{store: s, engine: e, log: log.With("component", "workflow.run")}
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

	// Drive: resume if a durable record exists (recovery / unpause), else start fresh (pinning the
	// workflow's derived contract for the ADR-0098 run-start input gate).
	rec, err := r.drive(ctx, req.Namespace, req.Name, wf.Name, wf.Spec, run.Spec.Input, wf.Status.Contract)
	if err != nil && fault.KindOf(err) != fault.Unavailable && fault.KindOf(err) != fault.Invalid {
		return controller.Result{}, err // infra error; requeue via the controller
	}
	// A run failure is a terminal outcome, not a reconcile error.
	mirror(run, rec)
	if uerr := r.updateRunStatus(ctx, run); uerr != nil {
		return controller.Result{}, uerr
	}
	if lerr := r.updateWorkflowLinks(ctx, wf); lerr != nil {
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
	if lerr := r.updateWorkflowLinks(ctx, wf); lerr != nil {
		r.log.Warn("status.runs update failed after cancel", "workflow", wf.Name, "error", lerr)
	}
	return nil
}

func (r *RunReconciler) drive(ctx context.Context, ns v1.NamespaceName, name, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, contract *v1.WorkflowContract) (*runstate.Record, error) {
	if _, err := r.engine.runs.Get(ctx, ns, name); err == nil {
		// A durable record exists → resume from its PINNED spec + contract (the live wf.Spec/status is
		// not passed; an in-flight run is immune to a mid-run edit or re-push).
		return r.engine.Resume(ctx, ns, name)
	}
	return r.engine.Execute(ctx, ns, name, workflow, spec, input, contract)
}

// mirror copies the engine record's coarse state into the WorkflowRun status.
func mirror(run *v1.WorkflowRun, rec *runstate.Record) {
	if rec == nil {
		return
	}
	run.Status.Phase = rec.Phase
	run.Status.Steps = run.Status.Steps[:0]
	for _, s := range rec.Steps {
		run.Status.Steps = append(run.Status.Steps, v1.RunStepStatus{
			Name: s.Name, Phase: s.Phase, Attempts: s.Attempts, Revision: s.Revision,
		})
	}
}

func (r *RunReconciler) updateRunStatus(ctx context.Context, run *v1.WorkflowRun) error {
	if _, err := r.store.Update(ctx, run); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "update run status %q", run.Name)
	}
	return nil
}

// updateWorkflowLinks recomputes the parent Workflow's status.runs from the metastore:
// active (non-terminal) run names + lifetime terminal-phase counts (bounded — only
// active runs are enumerated).
func (r *RunReconciler) updateWorkflowLinks(ctx context.Context, wf *v1.Workflow) error {
	list, err := r.store.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{Namespace: wf.Namespace})
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
