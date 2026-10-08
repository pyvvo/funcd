package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
	// waitRequeue is controller.referentPollInterval (ADR-0163).
	waitRequeue time.Duration
}

// NewRunReconciler builds the run reconciler. traces is the shared funclog trace sink (ADR-0103): when
// non-nil, the reconciler emits one INTERNAL run-root span per terminal run so the run's step spans
// (F51, parented on the run root) nest under it. nil ⇒ no run-root span (additive). referentPollInterval re-checks a
// run waiting for its Workflow; 0 ⇒ waitRequeue.
func NewRunReconciler(s store.Store, e *Engine, traces funclog.TraceSink, log *slog.Logger, referentPollInterval time.Duration) *RunReconciler {
	if log == nil {
		log = slog.Default()
	}
	if referentPollInterval <= 0 {
		referentPollInterval = waitRequeue
	}
	return &RunReconciler{store: s, engine: e, traces: traces, log: log.With("component", "workflow.run"), waitRequeue: referentPollInterval}
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
		return controller.Result{}, r.engine.forget(ctx, req.Namespace, req.Name) // a deleted run ends Cancelled (ADR-0146)
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
	// Cancel wins over a concurrent pause. A started run whose Workflow is gone or re-created is cancelled too
	// (ADR-0190 Decision 10), so a re-created namesake never hides the delete.
	cancel := run.Spec.Cancel
	if !cancel && run.Status.WorkflowUID != "" {
		if cancel, err = r.workflowGone(ctx, run); err != nil {
			return controller.Result{}, err
		}
	}
	var fallback v1.RunPhase
	switch {
	case cancel:
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
		if res, done, err := r.start(ctx, run, &before); done || err != nil {
			return res, err
		}
	}
	res, err := r.syncStatus(ctx, run, before, fallback)
	if err == nil && res == (controller.Result{}) && run.Status.WorkflowUID != "" && !isRunTerminal(run.Status.Phase) {
		res.RequeueAfter = r.waitRequeue // the Workflow UID check needs no Workflow event (ADR-0190 Decision 10)
	}
	return res, err
}

// workflowGone reports whether a started run's Workflow is deleted or re-created under another UID.
func (r *RunReconciler) workflowGone(ctx context.Context, run *v1.WorkflowRun) (bool, error) {
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
	if fault.KindOf(err) == fault.NotFound {
		return true, nil
	}
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), runOp, "get workflow %q", run.Spec.Workflow)
	}
	return obj.GetObjectMeta().UID != run.Status.WorkflowUID, nil
}

// start starts the run's goroutine, routed as before ADR-0146: a run record ⇒ resume; else spec.replay ⇒
// replay; else execute. A run that has not started waits while its Workflow is missing (ADR-0121) or not
// Ready=True for its current generation (ADR-0146, #756): not checked since it was applied or edited, a step
// artifact not pushed, or held Ready=False by the F65 gate (a WorkflowCycle, a type mismatch). It maps the
// error a previous goroutine of the run exited with. A run that starts fresh pins its step Function revisions, and
// writes them with its Workflow's UID to its status before its goroutine starts (ADR-0190); before is then the
// written status. done reports that the pass ends with res and err, without the status sync.
func (r *RunReconciler) start(ctx context.Context, run *v1.WorkflowRun, before *[]byte) (res controller.Result, done bool, err error) {
	rec, err := r.ownRecord(ctx, run)
	if err != nil {
		return controller.Result{}, true, err
	}
	var wf *v1.Workflow
	var pins map[v1.ObjectName]runstate.ChildPin
	var childImages map[v1.ObjectName]map[v1.ObjectName]string
	var revPins map[v1.ObjectName]v1.RevisionPin
	if rec == nil {
		wfObj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
		if err != nil && fault.KindOf(err) != fault.NotFound {
			return controller.Result{}, true, fault.Wrapf(err, fault.KindOf(err), runOp, "get workflow %q", run.Spec.Workflow)
		}
		wf, _ = wfObj.(*v1.Workflow)
		if wf == nil {
			res, err := r.wait(ctx, run, *before, "WorkflowNotFound", fmt.Sprintf("workflow %q not found; waiting", run.Spec.Workflow))
			return res, true, err
		}
		if !ready(wf) {
			res, err := r.wait(ctx, run, *before, "WorkflowNotReady", fmt.Sprintf("workflow %q %s; waiting", wf.Name, notReadyCause(wf)))
			return res, true, err
		}
		var tw *treeWait
		children := pins
		if run.Spec.Replay == nil {
			pins, tw, err = r.pinTree(ctx, wf)
			children = pins
		} else {
			childImages, tw, err = r.replayTree(ctx, run.Namespace, *run.Spec.Replay)
			if err == nil && tw == nil {
				children, err = r.replayChildren(ctx, run.Namespace, run.Spec.Replay.Run)
			}
		}
		var list []v1.RevisionPin
		if err == nil && tw == nil {
			list, tw, err = r.pinRevisions(ctx, wf, children)
		}
		if err != nil {
			return controller.Result{}, true, err
		}
		if tw != nil {
			res, err := r.wait(ctx, run, *before, tw.reason, tw.msg)
			return res, true, err
		}
		if res, done, err := r.writePins(ctx, run, before, wf, list); done || err != nil {
			return res, done, err
		}
		revPins = make(map[v1.ObjectName]v1.RevisionPin, len(list))
		for _, p := range list {
			revPins[p.Function] = p
		}
	}
	endWait(run)
	if rec != nil && rec.Terminal() {
		return controller.Result{}, false, nil
	}
	prev, err := r.engine.start(run.UID, run.Namespace, run.Name, r.driveFunc(run, wf, rec != nil, pins, childImages, revPins))
	if errors.Is(err, errDraining) || err == nil {
		return controller.Result{}, false, nil
	}
	// A first record over the run store's value limit even without its input is refused on every start.
	if prev == nil && rec == nil && fault.KindOf(err) == fault.PayloadTooLarge {
		res, err := r.failUnrecorded(ctx, run, *before, v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: "RunRecordTooLarge", Message: capErr(err.Error())})
		return res, true, err
	}
	// ADR-0107: a replay seed rejection (SeedInvalid/DigestDrift) produces no record — fail the run with
	// a ReplaySeeded=False condition so it terminates (never silently re-reconciles).
	if prev == nil && run.Spec.Replay != nil && fault.KindOf(err) == fault.Invalid {
		res, err := r.failUnrecorded(ctx, run, *before, v1.Condition{
			Type: "ReplaySeeded", Status: v1.ConditionFalse,
			Reason: replayReason(err), Message: capErr(err.Error()),
		})
		return res, true, err
	}
	return controller.Result{}, true, err // a run-store fault: the next pass resumes from the durable record
}

// driveFunc is the run's drive on its engine-owned goroutine: resume from the record's PINNED spec and
// contract when started (the live wf.Spec/status is not passed; an in-flight run is immune to a mid-run edit
// or re-push; covers replay recovery too), else a replay seeded from a source run with the current images of
// the children it runs fresh, else a fresh execute pinning the ADR-0098 contract for the run-start input gate
// and the child tree (ADR-0189).
func (r *RunReconciler) driveFunc(run *v1.WorkflowRun, wf *v1.Workflow, started bool, pins map[v1.ObjectName]runstate.ChildPin, childImages map[v1.ObjectName]map[v1.ObjectName]string, revPins map[v1.ObjectName]v1.RevisionPin) func(context.Context) (*runstate.Record, error) {
	ns, name, uid, replay, input := run.Namespace, run.Name, run.UID, run.Spec.Replay, run.Spec.Input
	return func(ctx context.Context) (*runstate.Record, error) {
		if started {
			return r.engine.Resume(ctx, ns, name)
		}
		images := stepImages(wf) // the ADR-0098 cache: step → resolved digest-pinned image (ADR-0107)
		if replay != nil {
			// ADR-0107: seed a replay from the source run's checkpoint + gate on digest drift. A source with no
			// run record (swept by retention) can never seed it, so that is a seed rejection, not a retry.
			rec, err := r.engine.replay(ctx, ns, name, uid, wf.Name, *replay, images, childImages, revPins)
			if fault.KindOf(err) == fault.NotFound {
				err = fault.Wrapf(err, fault.Invalid, runOp, "SeedInvalid: replay source run %q has no run record", replay.Run)
			}
			return rec, err
		}
		return r.engine.Execute(ctx, ns, name, wf.Name, wf.Spec, input, StartOptions{Contract: wf.Status.Contract, StepImages: images, StepContracts: stepContracts(wf), RunUID: uid, ChildPins: pins, Pins: revPins})
	}
}

// writePins writes the run's revision pins and its Workflow's UID to its status before its goroutine starts
// (ADR-0190 Decision 3): a write conflict requeues without starting. On success before is the written status.
func (r *RunReconciler) writePins(ctx context.Context, run *v1.WorkflowRun, before *[]byte, wf *v1.Workflow, pins []v1.RevisionPin) (controller.Result, bool, error) {
	run.Status.Pins, run.Status.WorkflowUID = pins, wf.UID
	obj, err := r.store.Update(ctx, run)
	if fault.KindOf(err) == fault.Conflict {
		return controller.Result{Requeue: true}, true, nil
	}
	if err != nil {
		return controller.Result{}, true, fault.Wrapf(err, fault.KindOf(err), runOp, "write the revision pins of run %q", run.Name)
	}
	run.ResourceVersion = obj.GetObjectMeta().ResourceVersion
	if *before, err = json.Marshal(run.Status); err != nil {
		return controller.Result{}, true, fault.Wrapf(err, fault.Internal, runOp, "encode run status %q", run.Name)
	}
	r.linkRun(ctx, run)
	return controller.Result{}, false, nil
}

// replayChildren returns the live specs of the children a replay's source pinned (ADR-0189), so the replay gates and
// pins their step Functions fresh, as ADR-0107 records the current images. A source that is absent or not terminal
// has none (the replay rejects it); a child that is gone has no step Function to pin.
func (r *RunReconciler) replayChildren(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) (map[v1.ObjectName]runstate.ChildPin, error) {
	src, err := r.engine.runs.Get(ctx, ns, source)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), runOp, "get replay source run %q", source)
	}
	if !src.Terminal() || len(src.ChildPins) == 0 {
		return nil, nil
	}
	live := make(map[v1.ObjectName]runstate.ChildPin, len(src.ChildPins))
	for name := range src.ChildPins {
		obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, name)
		if fault.KindOf(err) == fault.NotFound {
			continue
		}
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), runOp, "get child workflow %q", name)
		}
		live[name] = runstate.ChildPin{Spec: obj.(*v1.Workflow).Spec}
	}
	return live, nil
}

// pinRevisions pins every step Function the run can reach (ADR-0190 Decisions 1 and 2): the steps of wf and of
// each pinned child, function.ref targets included, each to its currentRevision. The run waits, WorkflowNotReady
// naming the Function, while one is absent or its currentRevision does not hold its spec's content: the image, the
// image digest the spec sets, the runtime and the handler, and for a materialized step the step's image. A file://
// artifact has no digest: the image reference is compared in its place and the pin carries none.
func (r *RunReconciler) pinRevisions(ctx context.Context, wf *v1.Workflow, children map[v1.ObjectName]runstate.ChildPin) ([]v1.RevisionPin, *treeWait, error) {
	images := map[v1.ObjectName]string{}
	var names []v1.ObjectName
	add := func(workflow v1.ObjectName, spec v1.WorkflowSpec) {
		for i := range spec.Steps {
			st := &spec.Steps[i]
			if st.Function == nil {
				continue
			}
			name := stepTarget(workflow, spec, st.Name)
			if _, seen := images[name]; !seen {
				names = append(names, name)
			}
			if st.Function.Image != "" {
				images[name] = st.Function.Image
			} else if _, ok := images[name]; !ok {
				images[name] = ""
			}
		}
	}
	add(wf.Name, wf.Spec)
	childNames := slices.Sorted(maps.Keys(children))
	for _, child := range childNames {
		add(child, children[child].Spec)
	}
	pins := make([]v1.RevisionPin, 0, len(names))
	for _, name := range names {
		pin, why, err := r.pinFunction(ctx, wf.Namespace, name, images[name])
		if err != nil {
			return nil, nil, err
		}
		if why != "" {
			return nil, &treeWait{reason: "WorkflowNotReady", msg: fmt.Sprintf("step function %q %s; waiting", name, why)}, nil
		}
		pins = append(pins, pin)
	}
	return pins, nil, nil
}

// pinFunction pins Function name to its currentRevision, or says why the run waits for it.
func (r *RunReconciler) pinFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, stepImage string) (v1.RevisionPin, string, error) {
	obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return v1.RevisionPin{}, "is not found", nil
	}
	if err != nil {
		return v1.RevisionPin{}, "", fault.Wrapf(err, fault.KindOf(err), runOp, "get step function %q", name)
	}
	fn := obj.(*v1.Function)
	if stepImage != "" && fn.Spec.Image != stepImage {
		return v1.RevisionPin{}, fmt.Sprintf("has image %q, not the step's %q", fn.Spec.Image, stepImage), nil
	}
	current := v1.ObjectName(fn.Status.CurrentRevision)
	if current == "" {
		return v1.RevisionPin{}, "has no revision yet", nil
	}
	robj, err := r.store.Get(ctx, v1.KindRevision.GVK(), ns, current)
	if fault.KindOf(err) == fault.NotFound {
		return v1.RevisionPin{}, fmt.Sprintf("revision %q is not found", current), nil
	}
	if err != nil {
		return v1.RevisionPin{}, "", fault.Wrapf(err, fault.KindOf(err), runOp, "get revision %q", current)
	}
	rev := robj.(*v1.Revision)
	if owner, ok := v1.ControllerOf(rev.OwnerReferences); !ok || owner.UID != fn.UID {
		return v1.RevisionPin{}, fmt.Sprintf("revision %q is not its own yet", current), nil
	}
	if rev.Spec.Image != fn.Spec.Image || rev.Spec.Runtime != fn.Spec.Runtime || rev.Spec.Handler != fn.Spec.Handler ||
		fn.Spec.ImageDigest != "" && rev.Spec.ImageDigest != fn.Spec.ImageDigest {
		return v1.RevisionPin{}, fmt.Sprintf("current revision %q does not hold its spec yet", current), nil
	}
	return v1.RevisionPin{Function: name, FunctionUID: fn.UID, Revision: current, ImageDigest: rev.Spec.ImageDigest}, "", nil
}

// treeWait is why a run waits for a child Workflow of its tree (ADR-0189): reason WorkflowNotFound or
// WorkflowNotReady, and a message naming the child.
type treeWait struct{ reason, msg string }

// pinTree pins each child Workflow wf reaches through workflow: steps, in spec step order and depth first, once
// per child: its spec, step images and step contracts of its current generation (ADR-0189). The walk starts with
// wf seen, so wf is never pinned and a cycle ends it. A child that is absent or not Ready for its current
// generation makes the run wait. The pins are nil when wf has no workflow: step.
func (r *RunReconciler) pinTree(ctx context.Context, wf *v1.Workflow) (map[v1.ObjectName]runstate.ChildPin, *treeWait, error) {
	if !slices.ContainsFunc(wf.Spec.Steps, func(st v1.WorkflowStep) bool { return st.Workflow != nil }) {
		return nil, nil, nil
	}
	pins := map[v1.ObjectName]runstate.ChildPin{}
	seen := map[v1.ObjectName]bool{wf.Name: true}
	var walk func(parent *v1.Workflow) (*treeWait, error)
	walk = func(parent *v1.Workflow) (*treeWait, error) {
		for i := range parent.Spec.Steps {
			st := &parent.Spec.Steps[i]
			if st.Workflow == nil || seen[st.Workflow.Ref] {
				continue
			}
			seen[st.Workflow.Ref] = true
			child, tw, err := r.readyChild(ctx, wf.Namespace, childRef{child: st.Workflow.Ref, step: st.Name, workflow: parent.Name})
			if tw != nil || err != nil {
				return tw, err
			}
			pins[child.Name] = runstate.ChildPin{Generation: child.Generation, Spec: child.Spec, StepImages: stepImages(child), StepContracts: stepContracts(child)}
			if tw, err := walk(child); tw != nil || err != nil {
				return tw, err
			}
		}
		return nil, nil
	}
	if tw, err := walk(wf); tw != nil || err != nil {
		return nil, tw, err
	}
	return pins, nil, nil
}

// replayTree waits until every pinned child the replay seeded by seed runs fresh is Ready, then captures their
// current step images (ADR-0189). A source the replay rejects, or one with no pins, has nothing to gate.
func (r *RunReconciler) replayTree(ctx context.Context, ns v1.NamespaceName, seed v1.ReplaySeed) (map[v1.ObjectName]map[v1.ObjectName]string, *treeWait, error) {
	src, err := r.engine.runs.Get(ctx, ns, seed.Run)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), runOp, "get replay source run %q", seed.Run)
	}
	if !src.Terminal() || src.ChildPins == nil {
		return nil, nil, nil
	}
	refs := freshRefs(src, seed.From)
	children := make([]*v1.Workflow, 0, len(refs))
	for _, ref := range refs {
		child, tw, err := r.readyChild(ctx, ns, ref)
		if tw != nil || err != nil {
			return nil, tw, err
		}
		children = append(children, child)
	}
	if len(children) == 0 {
		return nil, nil, nil
	}
	images := make(map[v1.ObjectName]map[v1.ObjectName]string, len(children))
	for _, child := range children {
		images[child.Name] = stepImages(child)
	}
	return images, nil, nil
}

// readyChild reads the child ref names, or says why the run waits for it: it is absent, or not Ready for its
// current generation. pinTree and replayTree share this check.
func (r *RunReconciler) readyChild(ctx context.Context, ns v1.NamespaceName, ref childRef) (*v1.Workflow, *treeWait, error) {
	who := fmt.Sprintf("child workflow %q, called by step %q of workflow %q,", ref.child, ref.step, ref.workflow)
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, ref.child)
	if fault.KindOf(err) == fault.NotFound {
		return nil, &treeWait{reason: "WorkflowNotFound", msg: who + " not found; waiting"}, nil
	}
	if err != nil {
		return nil, nil, fault.Wrapf(err, fault.KindOf(err), runOp, "get child workflow %q", ref.child)
	}
	child := obj.(*v1.Workflow)
	if !ready(child) {
		return nil, &treeWait{reason: "WorkflowNotReady", msg: fmt.Sprintf("%s %s; waiting", who, notReadyCause(child))}, nil
	}
	return child, nil, nil
}

// notReadyCause says why wf is not Ready for its current generation.
func notReadyCause(wf *v1.Workflow) string {
	c, ok := wf.Status.Conditions.Get(condReady)
	switch {
	case stale(wf):
		return fmt.Sprintf("generation %d is not type-checked yet", wf.Generation)
	case ok:
		return fmt.Sprintf("is not Ready (%s): %s", c.Reason, c.Message)
	}
	return "is not Ready yet"
}

// syncStatus mirrors the run record into WorkflowRun.status (ADR-0146 Decision 4: the only status writer).
// With no record of its own, a cancel or pause sets fallback. A terminal phase, from the record or the
// fallback, is written only once the run's goroutine exited: its exit enqueues the run again. Liveness is read
// before the record, so a goroutine seen gone wrote its last record before the read (#846).
func (r *RunReconciler) syncStatus(ctx context.Context, run *v1.WorkflowRun, before []byte, fallback v1.RunPhase) (controller.Result, error) {
	_, live := r.engine.live(run.Namespace, run.Name)
	rec, err := r.engine.runs.Get(ctx, run.Namespace, run.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound || err == nil && ForeignRecord(rec, run.UID):
		rec = nil
		if fallback != "" {
			run.Status.Phase = fallback
		}
	case err != nil:
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), runOp, "get run record %q", run.Name)
	}
	mirror(run, rec)
	if live && isRunTerminal(run.Status.Phase) {
		return controller.Result{}, nil
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
	return controller.Result{RequeueAfter: r.waitRequeue}, nil
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
	case ForeignRecord(rec, run.UID):
		if err := r.engine.runs.Delete(ctx, run.Namespace, run.Name); err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), runOp, "delete the record of an earlier run %q", run.Name)
		}
		return nil, nil
	}
	return rec, nil
}

// ForeignRecord reports whether rec belongs to a WorkflowRun other than the one with uid: an earlier one
// of the same name. A record without a uid predates the stamp and is matched by name.
func ForeignRecord(rec *runstate.Record, uid v1.UID) bool {
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

// endWait clears the Ready=False of a run that waited for its Workflow: the run has started.
func endWait(run *v1.WorkflowRun) {
	if c, ok := run.Status.Conditions.Get(condReady); ok && c.Status == v1.ConditionFalse {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue})
	}
}

// mirror copies the engine record's coarse state into the WorkflowRun status; nil leaves it unchanged. A
// record of its own means the run started, so its wait is over even on a pass that found the goroutine
// live and skipped start (#658).
func mirror(run *v1.WorkflowRun, rec *runstate.Record) {
	if rec == nil {
		return
	}
	run.Status.Phase = rec.Phase
	run.Status.TraceID = rec.TraceID // ADR-0100: mirror the run trace so describe + workflow logs (ADR-0106) find it
	if rec.Phase == runFailed && rec.Error != "" {
		run.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: failureReason(rec.Error), Message: rec.Error})
	} else {
		endWait(run)
	}
	steps := make([]v1.RunStepStatus, 0, len(rec.Steps))
	for _, s := range rec.Steps {
		steps = append(steps, v1.RunStepStatus{
			Name: s.Name, Phase: s.Phase, Attempts: s.Attempts, Revision: s.Revision,
			StartedAt: stepTime(s.StartedAt), EndedAt: stepTime(s.EndedAt), Error: s.Error, // ADR-0100 troubleshooting facts
		})
	}
	run.Status.Steps = steps
}

// stepTime converts a runstate int64 nanosecond instant to the API form (ADR-0196): 0, a step not yet started or
// ended, gives the zero Timestamp so the field is omitted.
func stepTime(ns int64) v1.Timestamp {
	if ns == 0 {
		return v1.Timestamp{}
	}
	return v1.NewTimestamp(time.Unix(0, ns))
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
	if err := r.cancelDeletedRuns(ctx); err != nil {
		return 0, err
	}
	return r.engine.SweepExpired(ctx, retention, r.deleteRun)
}

// cancelDeletedRuns ends Cancelled each open top-level run record whose WorkflowRun is gone, so the record sweep
// reclaims it: a deletion the reconcile never saw, because the daemon stopped first, is not in the controller's
// initial list. An inline child run (ADR-0099) has no WorkflowRun; its parent's drive closes it.
func (r *RunReconciler) cancelDeletedRuns(ctx context.Context) error {
	recs, err := r.engine.runs.List(ctx, runstate.ListOptions{})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), runOp, "list run records for retention sweep")
	}
	for _, rec := range recs {
		if rec.Terminal() || rec.Depth != 0 {
			continue
		}
		_, gerr := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), rec.Namespace, rec.Name)
		if gerr == nil {
			continue
		}
		if fault.KindOf(gerr) != fault.NotFound {
			return fault.Wrapf(gerr, fault.KindOf(gerr), runOp, "get run %q for retention sweep", rec.Name)
		}
		if err := r.engine.cancelOrphan(ctx, rec); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), runOp, "cancel the record of deleted run %q", rec.Name)
		}
	}
	return nil
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
		rec := &runstate.Record{Namespace: run.Namespace, Name: run.Name, Workflow: run.Spec.Workflow, Phase: run.Status.Phase, StartedAt: now, UpdatedAt: now, RunUID: run.UID}
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
	if ForeignRecord(rec, meta.UID) {
		return nil
	}
	err = r.store.Delete(ctx, v1.KindWorkflowRun.GVK(), rec.Namespace, rec.Name, meta.ResourceVersion)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

func isRunTerminal(p v1.RunPhase) bool {
	switch p {
	case runSucceeded, runFailed, runCancelled:
		return true
	default:
		return false
	}
}
