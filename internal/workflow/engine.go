package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// mintTraceContext returns a fresh W3C trace context (ADR-0102): a 16-byte trace-id and an 8-byte
// span-id, lowercase hex. Used once per run at start; the same context propagates to every step so a
// run is one trace. On the near-impossible crypto/rand error it returns empty strings (the caller
// proceeds with no traceparent — additive, never failing the run).
func mintTraceContext() (traceID, rootSpanID string) {
	var t [16]byte
	var s [8]byte
	if _, err := rand.Read(t[:]); err != nil {
		return "", ""
	}
	if _, err := rand.Read(s[:]); err != nil {
		return "", ""
	}
	return hex.EncodeToString(t[:]), hex.EncodeToString(s[:])
}

// mintSpanID returns a fresh 8-byte span-id as hex16 (ADR-0105): the engine mints one per DAG step so a
// successor can parent on it. "" on the near-impossible crypto/rand error (the step then falls back to
// minting its own span-id in the shim — additive, never failing the run).
func mintSpanID() string {
	var s [8]byte
	if _, err := rand.Read(s[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(s[:])
}

// stepSpanID returns the pre-minted span-id of a step by name (ADR-0105), read from the run record (populated
// by the pre-mint persist before drive). "" if the step is absent or unassigned.
func stepSpanID(rec *runstate.Record, name v1.ObjectName) string {
	for i := range rec.Steps {
		if rec.Steps[i].Name == name {
			return rec.Steps[i].SpanID
		}
	}
	return ""
}

const engineOp = "workflow.engine"

// maxStatusError caps the step-level error string mirrored to WorkflowRun.status (ADR-0100): the
// first line, truncated to this length. The full error/stack lives in the step's span + logs
// (F51/ADR-0101), reachable via `funcdctl workflow logs <run>` (ADR-0106) — status stays bounded so a
// pathological stack trace can never bloat the CRD.
const maxStatusError = 512

// capErr returns the first line of s truncated to maxStatusError, appending "…" when it truncated —
// the bounded troubleshooting summary mirrored to status (ADR-0100).
func capErr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxStatusError {
		return s[:maxStatusError] + "…"
	}
	return s
}

// setRunning stamps a step's start as it enters Running (ADR-0100 timings).
func (e *Engine) setRunning(n *stepNode) {
	n.phase = v1.StepRunning
	n.startedAt = e.clock.Now().UnixNano()
}

// markSucceeded records a step's terminal Succeeded transition + endedAt (ADR-0100).
func (e *Engine) markSucceeded(n *stepNode) {
	n.phase = v1.StepSucceeded
	n.endedAt = e.clock.Now().UnixNano()
}

// markFailed records a step's terminal Failed transition + endedAt + the raw step-level cause,
// capped (ADR-0100). It fills errMsg ONLY when unset: dispatchStep already stamps the bare dispatch
// cause (before its own retry-wrap and fail()'s run-wrap), so a function step keeps that un-wrapped
// cause; a builtin/sub-workflow step passes its raw cause straight in here.
func (e *Engine) markFailed(n *stepNode, cause error) {
	n.phase = v1.StepFailed
	n.endedAt = e.clock.Now().UnixNano()
	if n.errMsg == "" && cause != nil {
		n.errMsg = capErr(cause.Error())
	}
}

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
	// TraceID / ParentSpanID are the run's W3C trace context (ADR-0102): the dispatcher sets a
	// traceparent header from them so the step-function invocation's span joins the run's trace.
	// Empty TraceID ⇒ no header (additive). ParentSpanID is the step's PRIMARY predecessor (ADR-0105;
	// the run root for a true root step) — the parent edge the step nests under.
	TraceID      string // 32 hex
	ParentSpanID string // 16 hex
	// SpanID is the engine-minted span-id the step function uses as its own (ADR-0105, X-Funcd-Span-Id) so a
	// successor can parent on it. Links are the non-primary fan-in predecessors' span-ids (X-Funcd-Span-Links).
	SpanID string   // 16 hex; "" ⇒ the shim mints its own (direct invoke / additive)
	Links  []string // 16-hex span-ids, same trace
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
	DefaultMaxAttempts  int           // per-step, when a step sets no retry (default 1 = no retry)
	DefaultStepTimeout  time.Duration // per-step invocation bound (0 = none)
	PayloadLimit        int64         // max bytes for a step output (and run input, at admission); 0 = unbounded
	MaxSubworkflowDepth int           // ADR-0099: max sub-workflow nesting (default 8); a deeper chain fails cleanly
}

// Deps wires the engine (internal component, ADR-0002 §1).
type Deps struct {
	Runs     runstate.Store // durable run state (the port; Badger driver in prod, in-memory in tests)
	Dispatch Dispatcher     // the step-invocation seam
	Config   Config
	Clock    clock.Clock   // stamps run timestamps (retention GC input); defaults to the system clock
	Children ChildResolver // ADR-0099: resolves a child workflow's spec for a `workflow:` step (nil ⇒ rejected)
	// Traces is the shared funclog trace sink (ADR-0104): the engine emits the run-root span for INLINE
	// sub-workflow child runs (the reconciler drives only top-level runs). nil ⇒ no child run-root span.
	Traces funclog.TraceSink
	Logger *slog.Logger
}

// Engine executes workflow runs against durable state and the dispatcher.
type Engine struct {
	runs     runstate.Store
	dispatch Dispatcher
	cfg      Config
	clock    clock.Clock
	children ChildResolver
	traces   funclog.TraceSink // ADR-0104: run-root span emitter for inline child runs; nil ⇒ none
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
	if d.Config.MaxSubworkflowDepth < 1 {
		d.Config.MaxSubworkflowDepth = 8 // ADR-0099 default sub-workflow nesting cap
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Engine{runs: d.Runs, dispatch: d.Dispatch, cfg: d.Config, clock: clk, children: d.Children, traces: d.Traces, log: log.With("component", "workflow.engine")}, nil
}

// StartOptions consolidates run-start inputs (ADR-0107, replacing Execute's variadic contract param):
// the pinned contract (ADR-0098) + the per-step resolved digest-pinned image map (the ADR-0098 cache),
// which stamps each function step's revision so a replay can prove it re-runs the same artifact.
type StartOptions struct {
	Contract   *v1.WorkflowContract     // pinned derived contract; nil ⇒ no run-start input check
	StepImages map[v1.ObjectName]string // step name → resolved digest-pinned image; stamps stepNode.revision (function steps)
}

// Execute runs a workflow synchronously to a terminal phase and returns the final
// record. It is the engine core; the controller reconciler drives it asynchronously
// (wiring is a separate layer). Steps of a ready batch are dispatched sequentially in
// V1 (correct for the DAG; concurrent fan-out is a performance optimization).
func (e *Engine) Execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, opts StartOptions) (*runstate.Record, error) {
	return e.execute(ctx, ns, runName, workflow, spec, input, opts, 0, "", "") // top-level run: depth 0, fresh trace
}

// execute is Execute threading the sub-workflow nesting depth (ADR-0099): the public Execute starts at 0;
// runChild recurses at depth+1. inheritTraceID/inheritRootParent carry the parent run's trace context for a
// sub-workflow child (ADR-0104): empty ⇒ a top-level run mints a fresh trace with no parent; non-empty ⇒ the
// child shares the parent's TraceID (one trace) and nests its run-root span under the parent run's span.
func (e *Engine) execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, opts StartOptions, depth int, inheritTraceID, inheritRootParent string) (*runstate.Record, error) {
	pinned := opts.Contract
	rs := newRunState(spec)
	// ADR-0105: pre-mint a span-id per DAG step so a successor parents on it (the nested DAG waterfall). The
	// onFailure handler is excluded (dagSteps omits it) — nothing parents on it; it mints its own id in the shim.
	for _, name := range rs.dagSteps() {
		rs.steps[name].spanID = mintSpanID()
	}
	// ADR-0107: stamp each function step's resolved digest-pinned image (the fidelity record a replay gates on).
	stampRevisions(rs, spec, opts.StepImages)
	outputs := map[v1.ObjectName]json.RawMessage{}
	// ADR-0102/0104: one W3C trace context per run. A top-level run mints a fresh trace; a sub-workflow child
	// inherits the parent's TraceID (shared trace) but mints its OWN RootSpanID and nests under the parent.
	traceID, rootSpanID := mintTraceContext()
	rootParentID := ""
	if inheritTraceID != "" {
		traceID = inheritTraceID         // share the parent's trace (one composition = one trace)
		rootParentID = inheritRootParent // nest the child run span under the parent run span
	}
	rec := &runstate.Record{
		Namespace: ns, Name: runName, Workflow: workflow, Phase: runRunning, Input: input,
		Spec:         spec,   // pin the spec at run start — Resume/recovery rebuild from this, not the live Workflow
		Contract:     pinned, // pin the derived contract (ADR-0098) — the run-start input check + Resume use it
		Depth:        depth,  // sub-workflow nesting depth (ADR-0099)
		TraceID:      traceID,
		RootSpanID:   rootSpanID,
		RootParentID: rootParentID, // ADR-0104: "" for top-level, the parent run's RootSpanID for a child
		StartedAt:    e.clock.Now().UnixNano(),
	}
	// Run-start contract gate (ADR-0098): a run admitted before its workflow was Ready (async/Sensor
	// start) is checked here against the now-pinned contract, and fails fast rather than dropping silently.
	if pinned != nil && len(pinned.Input) > 0 {
		if diffs := v1.CheckInput(input, pinned.Input); len(diffs) > 0 {
			rec.Phase = runFailed
			if err := e.persist(ctx, rec, rs, outputs); err != nil {
				return nil, err
			}
			return rec, fault.Invalidf(engineOp, "run %q input violates the workflow contract (InputSchemaMismatch): %s", runName, v1.FieldDiffs(diffs))
		}
	}
	if err := e.persist(ctx, rec, rs, outputs); err != nil {
		return nil, err
	}
	return e.drive(ctx, rec, rs, outputs, spec, input)
}

// Resume continues a persisted run after a crash, pause, or cancel-race (ADR-0094): it rebuilds
// the scheduling state from the durable record and re-dispatches any step that was in-flight
// (with a fresh attempt), so no state is lost. It rebuilds from the record's PINNED spec
// (rec.Spec), never the live Workflow — an in-flight run is immune to a mid-run spec edit or
// artifact re-push. The caller passes no spec; the pinned one is the truth.
func (e *Engine) Resume(ctx context.Context, ns v1.NamespaceName, runName v1.ObjectName) (*runstate.Record, error) {
	rec, err := e.runs.Get(ctx, ns, runName)
	if err != nil {
		return nil, err
	}
	if rec.Terminal() {
		return rec, nil // a finished run (succeeded/failed/cancelled) is never re-driven
	}
	spec := rec.Spec // the pinned spec — mid-run edits to the live Workflow do not reach here
	rs, outputs := rebuildState(spec, rec)
	rec.Paused = false // resume clears the pause
	rec.Phase = runRunning
	return e.drive(ctx, rec, rs, outputs, spec, rec.Input)
}

// Replay seeds runName from a finished source run's checkpoint and drives it (ADR-0107): a NEW run that
// re-runs seed.From + its descendants, reusing the source's recorded upstream outputs verbatim. workflow
// is the replay run's declared workflow (must match the source's). current is the workflow's live resolved
// step-image map (the ADR-0098 cache) — the drift-gate comparison key + the re-run steps' revision.
// Faults: NotFound (source absent); Invalid whose message leads with reason token SeedInvalid or
// DigestDrift (naming the offending step). The source record is never mutated.
func (e *Engine) Replay(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, seed v1.ReplaySeed, current map[v1.ObjectName]string) (*runstate.Record, error) {
	src, err := e.runs.Get(ctx, ns, seed.Run)
	if err != nil {
		return nil, err // NotFound (source absent) propagates
	}
	if !src.Terminal() {
		return nil, fault.Invalidf(engineOp, "SeedInvalid: source run %q is not terminal (%s) — replay a finished run", seed.Run, src.Phase)
	}
	if src.Workflow != workflow {
		return nil, fault.Invalidf(engineOp, "SeedInvalid: replay workflow %q does not match the source run's workflow %q", workflow, src.Workflow)
	}
	spec := src.Spec
	rs := newRunState(spec)
	if _, ok := rs.steps[seed.From]; !ok || seed.From == rs.onFailure {
		return nil, fault.Invalidf(engineOp, "SeedInvalid: %q is not a DAG step of workflow %q", seed.From, workflow)
	}
	// The replay set: from + everything downstream of it (re-run); the rest is reused.
	replaySet := map[v1.ObjectName]bool{seed.From: true}
	for _, d := range rs.descendants(seed.From) {
		replaySet[d] = true
	}
	srcStep := make(map[v1.ObjectName]runstate.StepState, len(src.Steps))
	for _, s := range src.Steps {
		srcStep[s.Name] = s
	}
	// Classify + gate every DAG step (the onFailure handler is neither in the set nor checked — it is
	// seeded Pending below and fires only if the REPLAY fails).
	for _, name := range rs.dagSteps() {
		if replaySet[name] {
			// Drift gate: an image function step whose resolved digest moved since the source ⇒ reject,
			// unless allowDrift. Ref/builtin/workflow steps are not gated (documented workarounds).
			if fn := functionOf(specStep(spec, name)); fn != nil && fn.Image != "" && !seed.AllowDrift {
				if cur, ok := current[name]; ok && srcStep[name].Revision != "" && cur != srcStep[name].Revision {
					return nil, fault.Invalidf(engineOp, "DigestDrift: step %q artifact changed since the source run (source %q, current %q) — pass --allow-drift to re-run against current code", name, srcStep[name].Revision, cur)
				}
			}
			continue
		}
		switch srcStep[name].Phase {
		case v1.StepSucceeded, v1.StepSkipped, v1.StepPending, "":
			// copied (Succeeded/Skipped) or seeded-Pending-and-run (Pending/absent — never executed).
		default: // Failed / Cancelled
			return nil, fault.Invalidf(engineOp, "SeedInvalid: step %q is %s outside the replay set — replay --from it (or an ancestor) to re-run it", name, srcStep[name].Phase)
		}
	}
	// Build the new record: fresh trace, copy the source's pinned spec/contract/input, provenance.
	traceID, rootSpanID := mintTraceContext()
	rec := &runstate.Record{
		Namespace: ns, Name: runName, Workflow: src.Workflow, Phase: runRunning, Input: src.Input,
		Spec: spec, Contract: src.Contract, Depth: 0,
		TraceID: traceID, RootSpanID: rootSpanID, RootParentID: "",
		SourceRun: seed.Run, SourceFrom: seed.From,
		StartedAt: e.clock.Now().UnixNano(),
	}
	// Fresh span-ids for every DAG step (re-run/Pending steps use them); copied steps clear theirs below.
	for _, name := range rs.dagSteps() {
		rs.steps[name].spanID = mintSpanID()
	}
	outputs := map[v1.ObjectName]json.RawMessage{}
	for _, name := range rs.order {
		n := rs.steps[name]
		if name == rs.onFailure || replaySet[name] || !isCopied(srcStep[name].Phase) {
			// Handler, replay-set, and never-run (Pending) steps run fresh: Pending, fresh span-id, and
			// their revision stamps from the CURRENT image (re-run against current code).
			n.revision = revisionFor(spec, name, current)
			continue
		}
		// Copied step (Succeeded/Skipped outside the set): keep the source's phase/output/revision, but
		// CLEAR the span-id (so a re-run successor parents on the replay's run root, never a source span)
		// and leave execution facts zero (it did not run here).
		s := srcStep[name]
		n.phase = s.Phase
		n.revision = s.Revision // keep the SOURCE revision — replay chains stay gateable
		n.spanID = ""
		if s.Phase == v1.StepSucceeded && len(s.Output) > 0 {
			outputs[name] = s.Output
		}
	}
	if err := e.persist(ctx, rec, rs, outputs); err != nil {
		return nil, err
	}
	return e.drive(ctx, rec, rs, outputs, spec, src.Input)
}

// isCopied reports whether a source step's phase means "reuse it verbatim" in a replay (a terminal
// success/skip with a settled output), vs re-run it fresh (ADR-0107).
func isCopied(p v1.StepPhase) bool { return p == v1.StepSucceeded || p == v1.StepSkipped }

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
		n.spanID = s.SpanID     // ADR-0105: restore the pre-minted span-id UNCONDITIONALLY (incl. the in-flight step
		n.revision = s.Revision // ADR-0107: restore the recorded revision, so the digest survives Resume
		if n.revision == "" {   // a record that never recorded one (pre-ADR / hand-seeded) back-fills from the PINNED spec
			n.revision = revisionFor(spec, s.Name, nil)
		} //                    being re-dispatched) so successors' parent edges never dangle across a restart.
		if s.Phase == v1.StepRunning {
			n.phase = v1.StepPending // re-dispatch on recovery — its lineage stays zero (it re-runs fresh)
			continue
		}
		n.phase = s.Phase
		// ADR-0100: restore an already-terminal step's troubleshooting lineage so a resumed run keeps
		// its history (recovery re-runs only the in-flight step, reset to Pending above).
		n.attempts = s.Attempts
		n.startedAt = s.StartedAt
		n.endedAt = s.EndedAt
		n.errMsg = s.Error
		if s.Phase == v1.StepSucceeded && len(s.Output) > 0 {
			outputs[s.Name] = s.Output
		}
	}
	return rs, outputs
}

// SweepExpired deletes terminal run records whose last update is older than retention (ADR-0094:
// terminal runs "swept after workflow.retention"). retention ≤ 0 disables the sweep. Returns the
// number of records reclaimed. Non-terminal runs are never swept. Callers invoke it periodically
// (pkg/funcd lifecycle); it is idempotent and safe to run concurrently with reconciles (the store
// is the single writer per run and a terminal run is immutable).
func (e *Engine) SweepExpired(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	recs, err := e.runs.List(ctx, runstate.ListOptions{})
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), engineOp, "list runs for retention sweep")
	}
	cutoff := e.clock.Now().Add(-retention).UnixNano()
	swept := 0
	for _, rec := range recs {
		if !rec.Terminal() || rec.UpdatedAt == 0 || rec.UpdatedAt >= cutoff {
			continue
		}
		if derr := e.runs.Delete(ctx, rec.Namespace, rec.Name); derr != nil {
			return swept, fault.Wrapf(derr, fault.KindOf(derr), engineOp, "delete expired run %q", rec.Name)
		}
		swept++
	}
	return swept, nil
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
	// Run-timeout is start-relative and excludes paused time (ADR-0094 guarantee, ADR-0096): a run
	// now spans reconciles (a builtin wait yields), so a single-drive ctx deadline can't bound it.
	if spec.Timeout > 0 {
		if e.clock.Now().UnixNano() > runDeadline(rec, spec) {
			return e.fail(ctx, rec, rs, outputs, spec, input, runTimedOut(context.DeadlineExceeded))
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Unix(0, runDeadline(rec, spec)))
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
		// 3. run the batch (sequential V1), fail-fast on the first permanent failure. A step is
		//    either a builtin (run in-engine) or a function (dispatched); a builtin wait may PARK
		//    the run (yield), returning the non-terminal record so the reconciler requeues.
		for _, n := range batch {
			if ctx.Err() != nil { // run deadline hit between steps
				return e.fail(ctx, rec, rs, outputs, spec, input, runTimedOut(ctx.Err()))
			}
			st := specStep(spec, n.name)
			if st != nil && st.Builtin != nil {
				// A builtin is a normal step run in-engine: a wait blocks (on ctx), a pass transforms;
				// then it Succeeds. No dispatch, no special state (ADR-0096).
				e.setRunning(n)
				out, err := e.runBuiltin(ctx, st, n, input, outputs)
				if err != nil {
					e.markFailed(n, err)  // ADR-0100: builtin passes its raw cause straight in
					if ctx.Err() != nil { // the run deadline interrupted a blocking wait
						return e.fail(ctx, rec, rs, outputs, spec, input, runTimedOut(ctx.Err()))
					}
					return e.fail(ctx, rec, rs, outputs, spec, input, err)
				}
				e.markSucceeded(n)
				outputs[n.name] = out
				continue
			}
			if st != nil && st.Workflow != nil { // a sub-workflow step runs a child workflow inline (ADR-0099)
				e.setRunning(n)
				out, cerr := e.runChild(ctx, rec, st.Workflow.Ref, n, input, outputs)
				if cerr != nil {
					e.markFailed(n, cerr) // ADR-0100: the child's raw failure cause
					return e.fail(ctx, rec, rs, outputs, spec, input, cerr)
				}
				e.markSucceeded(n)
				outputs[n.name] = out
				continue
			}
			e.setRunning(n)
			out, err := e.dispatchStep(ctx, rec, spec, n, input, outputs)
			if err != nil {
				e.markFailed(n, err)  // ADR-0100: errMsg already stamped (bare cause) by dispatchStep
				if ctx.Err() != nil { // the run deadline (not a per-step timeout) caused the failure
					return e.fail(ctx, rec, rs, outputs, spec, input, runTimedOut(ctx.Err()))
				}
				return e.fail(ctx, rec, rs, outputs, spec, input, err)
			}
			e.markSucceeded(n)
			outputs[n.name] = out
		}
		if err := e.persist(ctx, rec, rs, outputs); err != nil {
			if fault.KindOf(err) == fault.PayloadTooLarge { // a run outcome, not a retryable store error
				return e.fail(ctx, rec, rs, outputs, spec, input, fault.Wrapf(err, fault.Invalid, engineOp, "record the step outputs"))
			}
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
	if st := specStep(spec, step); st != nil && st.Function != nil && st.Function.Ref != "" {
		return st.Function.Ref
	}
	return materializedStepName(workflow, step)
}

// dispatchStep invokes one step with retry, building its input from its parents. It reads the run's
// pinned identity + trace context off rec (ADR-0102: every attempt propagates the run's traceparent).
func (e *Engine) dispatchStep(ctx context.Context, rec *runstate.Record, spec v1.WorkflowSpec, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	ns, runName, workflow := rec.Namespace, rec.Name, rec.Workflow
	st := specStep(spec, n.name)
	stepInput := e.stepInput(n, input, outputs, st)
	max := e.cfg.DefaultMaxAttempts
	backoff := time.Duration(0)
	fn := functionOf(st) // dispatch knobs live on FunctionStep (ADR-0096)
	if fn != nil && fn.Retry != nil {
		if fn.Retry.MaxAttempts > 0 {
			max = fn.Retry.MaxAttempts
		}
		backoff = fn.Retry.Backoff
	}
	target := stepTarget(workflow, spec, n.name)
	// ADR-0105: nest the step span under its DAG predecessor. The primary parent is the first (post-implicit-
	// chaining) dependency's pre-minted span-id; a true root step (no dependency) parents on the run root. The
	// remaining dependencies become fan-in span links. Same for every attempt (one span-id per step).
	parentSpan := rec.RootSpanID
	var links []string
	if len(n.dependsOn) > 0 {
		if pid := stepSpanID(rec, n.dependsOn[0]); pid != "" {
			parentSpan = pid
		}
		for _, dep := range n.dependsOn[1:] {
			if id := stepSpanID(rec, dep); id != "" {
				links = append(links, id)
			}
		}
	}
	// Per-step invocation bound: the step's own timeout, else the engine default (0 ⇒ none).
	// A step-timeout is a retryable failure on a CHILD ctx; the parent (run) deadline is checked
	// separately in drive and maps to RunTimedOut.
	stepTimeout := e.cfg.DefaultStepTimeout
	if fn != nil && fn.Timeout > 0 {
		stepTimeout = fn.Timeout
	}
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		attemptCtx := ctx
		var cancel context.CancelFunc
		if stepTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, stepTimeout)
		}
		out, err := e.dispatch.Dispatch(attemptCtx, DispatchRequest{
			Namespace: ns, Run: runName, Step: n.name, Target: target,
			Attempt: attempt, Input: stepInput,
			TraceID: rec.TraceID, ParentSpanID: parentSpan, // ADR-0102/0105: run trace + the predecessor edge
			SpanID: n.spanID, Links: links, // ADR-0105: the step's own span-id + fan-in links
		})
		if cancel != nil {
			cancel()
		}
		n.attempts = attempt // ADR-0100: record the dispatch attempt count (both exit paths)
		if err == nil {
			if e.cfg.PayloadLimit > 0 && int64(len(out)) > e.cfg.PayloadLimit {
				// An over-cap output is permanent — a retry cannot shrink it (ADR-0094 payload cap).
				return nil, Permanent(fault.Invalidf(engineOp, "step %q output %d bytes exceeds payload limit %d", n.name, len(out), e.cfg.PayloadLimit))
			}
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
	// ADR-0100: stamp the BARE dispatch cause here (before this retry-wrap and fail()'s run-wrap), so
	// describe names the step's actual error (e.g. "scorer returned 503"), not the engine envelope.
	n.errMsg = capErr(lastErr.Error())
	return nil, fault.Wrapf(lastErr, fault.Unavailable, engineOp, "step %q failed after retries", n.name)
}

// stepInput builds a step's input: a single-parent step gets the parent's output
// verbatim; a fan-in step gets a composite keyed by parent name; a root step gets the
// run input. `params` (static overlay) is merged over it (static wins).
func (e *Engine) stepInput(n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage, st *v1.WorkflowStep) json.RawMessage {
	base := e.flowingInput(n, input, outputs)
	if st == nil || len(st.Params) == 0 {
		return base
	}
	return mergeParams(base, st.Params)
}

// flowingInput is the step's flowing input BEFORE the params overlay: a root step gets the run
// input; a single-parent step its parent's output verbatim; a fan-in step a composite keyed by
// parent name. A builtin wait passes this through as its output verbatim (params does not apply).
func (e *Engine) flowingInput(n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) json.RawMessage {
	switch {
	case len(n.dependsOn) == 0:
		return input
	case len(n.dependsOn) == 1:
		return outputs[n.dependsOn[0]]
	default:
		composite := map[string]json.RawMessage{}
		for _, p := range n.dependsOn {
			if out, ok := outputs[p]; ok {
				composite[string(p)] = out
			}
		}
		b, _ := json.Marshal(composite)
		return b
	}
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
			TraceID: rec.TraceID, ParentSpanID: rec.RootSpanID, // ADR-0102: the handler joins the run's trace too
		}) // handler outcome never changes the run phase (ADR-0094)
	}
	err := e.persist(ctx, rec, rs, outputs)
	if fault.KindOf(err) == fault.PayloadTooLarge {
		if err = e.dropUnrecordedOutputs(ctx, rec, rs, outputs, err); err == nil {
			err = e.persist(ctx, rec, rs, outputs)
		}
	}
	if err != nil {
		return nil, err
	}
	return rec, fault.Wrapf(cause, fault.KindOf(cause), engineOp, "run %q failed", rec.Name)
}

// dropUnrecordedOutputs fails each step whose output the durable record does not hold yet, with the
// store's cause: those outputs are what overflow the run store's value limit (the record is size-bounded
// by the engine, runstate.Record). The run then ends Failed instead of re-running the step on every requeue.
func (e *Engine) dropUnrecordedOutputs(ctx context.Context, rec *runstate.Record, rs *runState, outputs map[v1.ObjectName]json.RawMessage, cause error) error {
	durable, err := e.runs.Get(ctx, rec.Namespace, rec.Name)
	if err != nil {
		return err
	}
	recorded := make(map[v1.ObjectName]bool, len(durable.Steps))
	for _, st := range durable.Steps {
		recorded[st.Name] = len(st.Output) > 0
	}
	for name, out := range outputs {
		if len(out) > 0 && !recorded[name] {
			delete(outputs, name)
			e.markFailed(rs.steps[name], cause)
		}
	}
	return nil
}

// stampRevisions sets each function step's revision to its resolved digest-pinned image (ADR-0107):
// images[name] when the ADR-0098 cache carries it, else the bare spec ref (bare-engine tests / a
// not-yet-materialized step). Builtin/`workflow:` steps get no revision. Called once at run start;
// persist then carries the value across every write, and rebuildState restores it on Resume.
func stampRevisions(rs *runState, spec v1.WorkflowSpec, images map[v1.ObjectName]string) {
	for _, name := range rs.order {
		rs.steps[name].revision = revisionFor(spec, name, images)
	}
}

// revisionFor resolves one step's recorded revision: the ADR-0098 cache value when present, else the
// pinned spec image; "" for a non-function step.
func revisionFor(spec v1.WorkflowSpec, name v1.ObjectName, images map[v1.ObjectName]string) string {
	fn := functionOf(specStep(spec, name))
	if fn == nil {
		return ""
	}
	if img, ok := images[name]; ok && img != "" {
		return img
	}
	return fn.Image
}

// persist writes the run record (the write-ahead intent + the coarse step mirror).
func (e *Engine) persist(ctx context.Context, rec *runstate.Record, rs *runState, outputs map[v1.ObjectName]json.RawMessage) error {
	rec.Steps = rec.Steps[:0]
	for _, name := range rs.order {
		n := rs.steps[name]
		// SpanID: ADR-0105 (persisted for Resume). StartedAt/EndedAt/Attempts/Error: ADR-0100 troubleshooting lineage.
		// Revision: ADR-0107 — the resolved digest-pinned image, carried from stepNode so it survives every
		// persist/Resume cycle and copied replay steps keep the source's digest.
		ss := runstate.StepState{
			Name: n.name, Phase: n.phase, SpanID: n.spanID, Revision: n.revision,
			Attempts: n.attempts, StartedAt: n.startedAt, EndedAt: n.endedAt, Error: n.errMsg,
		}
		if out, ok := outputs[n.name]; ok {
			ss.Output = out
		}
		rec.Steps = append(rec.Steps, ss)
	}
	rec.UpdatedAt = e.clock.Now().UnixNano()
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

// functionOf returns a step's FunctionStep (dispatch shape), or nil for a builtin/workflow/absent
// step. Dispatch knobs (image/ref/retry/timeout/bindings/pooling) live only here (ADR-0096).
func functionOf(st *v1.WorkflowStep) *v1.FunctionStep {
	if st == nil {
		return nil
	}
	return st.Function
}

// runDeadline is the absolute unix-nanos the run must finish by: start + timeout + accumulated
// paused time (paused time excluded from the clock, ADR-0094; wait time counts, ADR-0096).
func runDeadline(rec *runstate.Record, spec v1.WorkflowSpec) int64 {
	return rec.StartedAt + int64(spec.Timeout) + rec.PausedNanos
}

// runTimedOut wraps a run-deadline cause as the ADR-0094 RunTimedOut failure reason (distinct
// from a per-step timeout, which is a retryable step failure).
func runTimedOut(cause error) error {
	return fault.Wrapf(cause, fault.Unavailable, engineOp, "RunTimedOut: run deadline exceeded")
}
