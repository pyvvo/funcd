package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
)

const engineOp = "workflow.engine"

// Dispatcher is the step-invocation seam (ADR-0094): it delivers a step's input to
// its function and returns the output. Production wraps activator wake + HTTP; tests
// inject a fake. A returned error is a step failure; a Permanent error is not retried.
type Dispatcher interface {
	Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error)
}

// DispatchRequest is one step invocation. Attempt is the 1-based attempt number
// (surfaced to the function as the idempotency key, at-least-once).
type DispatchRequest struct {
	Namespace v1.NamespaceName
	Run       v1.ObjectName
	Step      v1.ObjectName
	Target    v1.ObjectName // the function to invoke (materialized name or referenced)
	Attempt   int
	Input     json.RawMessage
}

// permanentError marks a dispatch failure that must not be retried (4xx: contract
// rejection, Forbidden). Transport/timeout/5xx are retryable (the default).
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err as a non-retryable dispatch failure.
func Permanent(err error) error { return &permanentError{err: err} }

func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// Config holds the engine's tunables (ADR-0094 workflow.* keys).
type Config struct {
	DefaultMaxAttempts int           // per-step, when a step sets no retry (default 1 = no retry)
	DefaultStepTimeout time.Duration // per-step invocation bound (0 = none)
}

// Deps wires the engine (internal component, ADR-0002 §1).
type Deps struct {
	Runs     runstate.Store // durable run state (the port; Badger driver in prod, in-memory in tests)
	Dispatch Dispatcher     // the step-invocation seam
	Config   Config
	Logger   *slog.Logger
}

// Engine executes workflow runs against durable state and the dispatcher.
type Engine struct {
	runs     runstate.Store
	dispatch Dispatcher
	cfg      Config
	log      *slog.Logger
}

// New builds the engine.
func New(d Deps) (*Engine, error) {
	if d.Runs == nil {
		return nil, fault.Invalidf(engineOp, "Runs (run store) is required")
	}
	if d.Dispatch == nil {
		return nil, fault.Invalidf(engineOp, "Dispatch is required")
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	if d.Config.DefaultMaxAttempts < 1 {
		d.Config.DefaultMaxAttempts = 1
	}
	return &Engine{runs: d.Runs, dispatch: d.Dispatch, cfg: d.Config, log: log.With("component", "workflow.engine")}, nil
}

// Execute runs a workflow synchronously to a terminal phase and returns the final
// record. It is the engine core; the controller reconciler drives it asynchronously
// (wiring is a separate layer). Steps of a ready batch are dispatched sequentially in
// V1 (correct for the DAG; concurrent fan-out is a performance optimization).
func (e *Engine) Execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage) (*runstate.Record, error) {
	rs := newRunState(spec)
	outputs := map[v1.ObjectName]json.RawMessage{}
	rec := &runstate.Record{
		Namespace: ns, Name: runName, Workflow: workflow, Phase: runRunning, Input: input,
	}
	if err := e.persist(ctx, rec, rs, outputs); err != nil {
		return nil, err
	}
	return e.drive(ctx, rec, rs, outputs, spec, input)
}

// Resume continues a persisted run after a crash (ADR-0094): it rebuilds the
// scheduling state from the durable record and re-dispatches any step that was
// in-flight (with a fresh attempt), so no state is lost.
func (e *Engine) Resume(ctx context.Context, ns v1.NamespaceName, runName v1.ObjectName, spec v1.WorkflowSpec) (*runstate.Record, error) {
	rec, err := e.runs.Get(ctx, ns, runName)
	if err != nil {
		return nil, err
	}
	rs, outputs := rebuildState(spec, rec)
	rec.Paused = false // resume clears the pause
	rec.Phase = runRunning
	return e.drive(ctx, rec, rs, outputs, spec, rec.Input)
}

// rebuildState restores scheduling state from a durable record. An in-flight
// (Running) step is reset to Pending so recovery re-dispatches it.
func rebuildState(spec v1.WorkflowSpec, rec *runstate.Record) (*runState, map[v1.ObjectName]json.RawMessage) {
	rs := newRunState(spec)
	outputs := map[v1.ObjectName]json.RawMessage{}
	for _, s := range rec.Steps {
		n, ok := rs.steps[s.Name]
		if !ok {
			continue
		}
		if s.Phase == v1.StepRunning {
			n.phase = v1.StepPending // re-dispatch on recovery
			continue
		}
		n.phase = s.Phase
		if s.Phase == v1.StepSucceeded && len(s.Output) > 0 {
			outputs[s.Name] = s.Output
		}
	}
	return rs, outputs
}

// Pause requests a graceful pause: the persisted run is marked Paused so the next
// drive dispatches nothing new (in-flight steps, in the async model, finish first).
func (e *Engine) Pause(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	rec, err := e.runs.Get(ctx, ns, name)
	if err != nil {
		return err
	}
	rec.Paused = true
	rec.Phase = runPaused
	return e.runs.Put(ctx, rec)
}

// Cancel abandons a run: pending/running steps are marked Cancelled and the run ends
// Cancelled immediately (the in-flight invocation is abandoned; idempotency covers it).
func (e *Engine) Cancel(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	rec, err := e.runs.Get(ctx, ns, name)
	if err != nil {
		return err
	}
	for i := range rec.Steps {
		if rec.Steps[i].Phase == v1.StepPending || rec.Steps[i].Phase == v1.StepRunning {
			rec.Steps[i].Phase = v1.StepCancelled
		}
	}
	rec.Phase = runCancelled
	return e.runs.Put(ctx, rec)
}

// drive advances a run to a terminal phase from the given scheduling state.
func (e *Engine) drive(ctx context.Context, rec *runstate.Record, rs *runState, outputs map[v1.ObjectName]json.RawMessage, spec v1.WorkflowSpec, input json.RawMessage) (*runstate.Record, error) {
	if rec.Paused {
		rec.Phase = runPaused
		if err := e.persist(ctx, rec, rs, outputs); err != nil {
			return nil, err
		}
		return rec, nil
	}
	if to := timeoutOf(spec); to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	for {
		// 1. skip cascade: steps whose join can never be satisfied.
		for _, n := range rs.pendingToSkip() {
			n.phase = v1.StepSkipped
		}
		// 2. ready steps, filtered by their when.condition.
		batch, skipped, err := e.selectRunnable(spec, rs, input, outputs)
		if err != nil {
			return e.fail(ctx, rec, rs, outputs, spec, input, err)
		}
		for _, n := range skipped {
			n.phase = v1.StepSkipped
		}
		if len(batch) == 0 {
			if len(rs.pendingToSkip()) > 0 {
				continue // more cascade to resolve
			}
			break // terminal (or nothing left runnable)
		}
		// 3. dispatch the batch (sequential V1), fail-fast on the first permanent failure.
		for _, n := range batch {
			if ctx.Err() != nil {
				return e.fail(ctx, rec, rs, outputs, spec, input, fault.Wrapf(ctx.Err(), fault.Unavailable, engineOp, "run deadline"))
			}
			n.phase = v1.StepRunning
			out, err := e.dispatchStep(ctx, rec.Namespace, rec.Name, rec.Workflow, spec, n, input, outputs)
			if err != nil {
				n.phase = v1.StepFailed
				return e.fail(ctx, rec, rs, outputs, spec, input, err)
			}
			n.phase = v1.StepSucceeded
			outputs[n.name] = out
		}
		if err := e.persist(ctx, rec, rs, outputs); err != nil {
			return nil, err
		}
	}

	rec.Phase = rs.runPhase()
	if err := e.persist(ctx, rec, rs, outputs); err != nil {
		return nil, err
	}
	return rec, nil
}

// selectRunnable returns the ready steps that should run now (when true) and those
// to Skip (when false).
func (e *Engine) selectRunnable(spec v1.WorkflowSpec, rs *runState, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (run, skip []*stepNode, err error) {
	for _, n := range rs.ready() {
		st := specStep(spec, n.name)
		if st == nil || st.When == nil {
			run = append(run, n)
			continue
		}
		ok, err := e.evalWhen(st.When.Condition, n, input, outputs)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			run = append(run, n)
		} else {
			skip = append(skip, n)
		}
	}
	return run, skip, nil
}

// stepTarget resolves the function a step dispatches to: a step that references an existing
// function (spec.function) targets it directly; an image step targets its materialized owned
// function <workflow>-<step> (ADR-0094). An empty workflow (bare-engine tests) yields "-<step>",
// harmless because those tests key their fake dispatcher on the step name.
func stepTarget(workflow v1.ObjectName, spec v1.WorkflowSpec, step v1.ObjectName) v1.ObjectName {
	if st := specStep(spec, step); st != nil && st.Function != "" {
		return st.Function
	}
	return materializedStepName(workflow, step)
}

// dispatchStep invokes one step with retry, building its input from its parents.
func (e *Engine) dispatchStep(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	st := specStep(spec, n.name)
	stepInput := e.stepInput(n, input, outputs, st)
	max := e.cfg.DefaultMaxAttempts
	backoff := time.Duration(0)
	if st != nil && st.Retry != nil {
		if st.Retry.MaxAttempts > 0 {
			max = st.Retry.MaxAttempts
		}
		backoff = st.Retry.Backoff
	}
	target := stepTarget(workflow, spec, n.name)
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		out, err := e.dispatch.Dispatch(ctx, DispatchRequest{
			Namespace: ns, Run: runName, Step: n.name, Target: target,
			Attempt: attempt, Input: stepInput,
		})
		if err == nil {
			return out, nil
		}
		lastErr = err
		if isPermanent(err) || attempt == max || ctx.Err() != nil {
			break
		}
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fault.Wrapf(ctx.Err(), fault.Unavailable, engineOp, "run deadline during backoff")
			case <-timer.C:
			}
		}
	}
	return nil, fault.Wrapf(lastErr, fault.Unavailable, engineOp, "step %q failed after retries", n.name)
}

// stepInput builds a step's input: a single-parent step gets the parent's output
// verbatim; a fan-in step gets a composite keyed by parent name; a root step gets the
// run input. `params` (static overlay) is merged over it (static wins).
func (e *Engine) stepInput(n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage, st *v1.WorkflowStep) json.RawMessage {
	var base json.RawMessage
	switch {
	case len(n.dependsOn) == 0:
		base = input
	case len(n.dependsOn) == 1:
		base = outputs[n.dependsOn[0]]
	default:
		composite := map[string]json.RawMessage{}
		for _, p := range n.dependsOn {
			if out, ok := outputs[p]; ok {
				composite[string(p)] = out
			}
		}
		b, _ := json.Marshal(composite)
		base = b
	}
	if st == nil || len(st.Params) == 0 {
		return base
	}
	return mergeParams(base, st.Params)
}

// mergeParams overlays static params over base (static wins on key collision).
func mergeParams(base, params json.RawMessage) json.RawMessage {
	var b map[string]json.RawMessage
	if len(base) > 0 {
		_ = json.Unmarshal(base, &b)
	}
	if b == nil {
		b = map[string]json.RawMessage{}
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return base // non-object params: leave base (checker rejects this at reconcile)
	}
	for k, v := range p {
		b[k] = v
	}
	out, _ := json.Marshal(b)
	return out
}

// fail finalizes a Failed run, invoking the onFailure handler once if present.
func (e *Engine) fail(ctx context.Context, rec *runstate.Record, rs *runState, outputs map[v1.ObjectName]json.RawMessage, spec v1.WorkflowSpec, input json.RawMessage, cause error) (*runstate.Record, error) {
	rec.Phase = runFailed
	if spec.OnFailure != "" {
		fc, _ := json.Marshal(map[string]string{
			"workflow": string(rec.Workflow), "run": string(rec.Name),
			"reason": cause.Error(),
		})
		_, _ = e.dispatch.Dispatch(ctx, DispatchRequest{
			Namespace: rec.Namespace, Run: rec.Name, Step: spec.OnFailure,
			Target:  stepTarget(rec.Workflow, spec, spec.OnFailure),
			Attempt: 1, Input: fc,
		}) // handler outcome never changes the run phase (ADR-0094)
	}
	if err := e.persist(ctx, rec, rs, outputs); err != nil {
		return nil, err
	}
	return rec, fault.Wrapf(cause, fault.KindOf(cause), engineOp, "run %q failed", rec.Name)
}

// persist writes the run record (the write-ahead intent + the coarse step mirror).
func (e *Engine) persist(ctx context.Context, rec *runstate.Record, rs *runState, outputs map[v1.ObjectName]json.RawMessage) error {
	rec.Steps = rec.Steps[:0]
	for _, name := range rs.order {
		n := rs.steps[name]
		ss := runstate.StepState{Name: n.name, Phase: n.phase}
		if out, ok := outputs[n.name]; ok {
			ss.Output = out
		}
		rec.Steps = append(rec.Steps, ss)
	}
	return e.runs.Put(ctx, rec)
}

func specStep(spec v1.WorkflowSpec, name v1.ObjectName) *v1.WorkflowStep {
	for i := range spec.Steps {
		if spec.Steps[i].Name == name {
			return &spec.Steps[i]
		}
	}
	return nil
}

func timeoutOf(spec v1.WorkflowSpec) time.Duration { return spec.Timeout }
