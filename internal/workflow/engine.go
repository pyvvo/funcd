package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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

// stepSpanID returns the pre-minted span-id of a step by name (ADR-0105), from the scheduling state the run
// record persists. "" if the step is absent or unassigned.
func stepSpanID(rs *runState, name v1.ObjectName) string {
	if n, ok := rs.steps[name]; ok {
		return n.spanID
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
	// MaxOutput is the payload limit on the step's output (ADR-0094); 0 ⇒ unbounded. A dispatcher need
	// read no more than one byte past it, since a longer output is rejected anyway.
	MaxOutput int64
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
	PayloadLimit        int64         // max bytes for a run input (at admission and run start) and a step output; 0 = unbounded
	MaxSubworkflowDepth int           // ADR-0099: max sub-workflow nesting (default 8); a deeper chain fails cleanly
	// MaxStepsInFlight bounds the function-step dispatch attempts in flight across all runs; 0 ⇒ no cap (ADR-0146).
	MaxStepsInFlight int
	// DefaultRetryBackoff is the first retry gap of a step whose retry.backoff is unset or 0, doubled per attempt
	// (workflow.defaultRetryBackoff, ADR-0163 Decision 8); 0 ⇒ back to back.
	DefaultRetryBackoff time.Duration
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
	// Notify: a top-level run's key after each record write and on goroutine exit; nil ⇒ none; must not block
	// (ADR-0146: wired to Controller.Enqueue, so the run reconciler mirrors the record).
	Notify func(ns v1.NamespaceName, name v1.ObjectName)
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
	notify   func(ns v1.NamespaceName, name v1.ObjectName)
	slots    chan struct{} // ADR-0146 Decision 7: one per function-step attempt in flight; nil ⇒ no cap

	// The live top-level runs (ADR-0146 Decision 2), keyed by namespace and name; mu guards the maps and draining.
	mu       sync.Mutex
	running  map[runKey]*liveRun
	exits    map[runKey]runExit
	draining bool
	drainCh  chan struct{}           // closed when Run's ctx ends: no drive starts a new step or attempt
	bound    context.Context         // ends with errRunStopped at the drain bound: it cuts an onFailure handler
	endBound context.CancelCauseFunc // ends bound
	wg       sync.WaitGroup          // the live run goroutines
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
	e := &Engine{
		runs: d.Runs, dispatch: d.Dispatch, cfg: d.Config, clock: clk, children: d.Children, traces: d.Traces,
		log: log.With("component", "workflow.engine"), notify: d.Notify,
		running: map[runKey]*liveRun{}, exits: map[runKey]runExit{}, drainCh: make(chan struct{}),
	}
	if d.Config.MaxStepsInFlight > 0 {
		e.slots = make(chan struct{}, d.Config.MaxStepsInFlight)
	}
	e.bound, e.endBound = context.WithCancelCause(context.Background())
	return e, nil
}

// StartOptions consolidates run-start inputs (ADR-0107, replacing Execute's variadic contract param):
// the pinned contract (ADR-0098) + the per-step resolved digest-pinned image map (the ADR-0098 cache),
// which stamps each function step's revision so a replay can prove it re-runs the same artifact.
type StartOptions struct {
	Contract   *v1.WorkflowContract     // pinned derived contract; nil ⇒ no run-start input check
	StepImages map[v1.ObjectName]string // step name → resolved digest-pinned image; stamps stepNode.revision (function steps)
	// StepContracts is the ADR-0098 cache's per-step I/O contract, pinned on the record: a when: binds an
	// absent parent-output field to its schema default (ADR-0095). Nil ⇒ no defaults are bound.
	StepContracts map[v1.ObjectName]v1.WorkflowContract
	RunUID        v1.UID // the starting WorkflowRun's uid, stamped on the record; empty for an inline child run
}

// Execute runs a workflow synchronously until it is terminal, paused or halted, and returns the record. The
// run reconciler calls it on the run's engine-owned goroutine (start, ADR-0146). Every ready step is dispatched
// at once (ADR-0094: fan-out is parallel dispatch). ctx ends the steps; record writes outlive it.
func (e *Engine) Execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, opts StartOptions) (*runstate.Record, error) {
	return e.execute(context.WithoutCancel(ctx), ctx, ns, runName, workflow, spec, input, opts, 0, "", "") // top-level run: depth 0, fresh trace
}

// execute is Execute threading the sub-workflow nesting depth (ADR-0099): the public Execute starts at 0;
// runChild recurses at depth+1. inheritTraceID/inheritRootParent carry the parent run's trace context for a
// sub-workflow child (ADR-0104): empty ⇒ a top-level run mints a fresh trace with no parent; non-empty ⇒ the
// child shares the parent's TraceID (one trace) and nests its run-root span under the parent run's span.
// stop ends the run's steps: ctx for a top-level run, the parent's step context for a child (ADR-0099).
func (e *Engine) execute(ctx, stop context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, spec v1.WorkflowSpec, input json.RawMessage, opts StartOptions, depth int, inheritTraceID, inheritRootParent string) (*runstate.Record, error) {
	pinned := opts.Contract
	rs := newRunState(spec)
	// ADR-0105: pre-mint a span-id per DAG step so a successor parents on it (the nested DAG waterfall). The
	// onFailure handler is excluded (dagSteps omits it) — nothing parents on it; it mints its own id in the shim.
	for _, name := range rs.dagSteps() {
		rs.steps[name].spanID = mintSpanID()
	}
	// ADR-0107: stamp each function step's resolved digest-pinned image (the fidelity record a replay gates on).
	stampRevisions(rs, spec, opts.StepImages)
	// ADR-0102/0104: one W3C trace context per run. A top-level run mints a fresh trace; a sub-workflow child
	// inherits the parent's TraceID (shared trace) but mints its OWN RootSpanID and nests under the parent.
	traceID, rootSpanID := mintTraceContext()
	rootParentID := ""
	if inheritTraceID != "" {
		traceID = inheritTraceID         // share the parent's trace (one composition = one trace)
		rootParentID = inheritRootParent // nest the child run span under the parent run span
	}
	rec := &runstate.Record{
		Namespace: ns, Name: runName, RunUID: opts.RunUID, Workflow: workflow, Phase: runRunning, Input: input,
		Spec:          spec,   // pin the spec at run start — Resume/recovery rebuild from this, not the live Workflow
		Contract:      pinned, // pin the derived contract (ADR-0098) — the run-start input check + Resume use it
		StepContracts: opts.StepContracts,
		Depth:         depth, // sub-workflow nesting depth (ADR-0099)
		TraceID:       traceID,
		RootSpanID:    rootSpanID,
		RootParentID:  rootParentID, // ADR-0104: "" for top-level, the parent run's RootSpanID for a child
		StartedAt:     e.clock.Now().UnixNano(),
	}
	run := &activeRun{rec: rec, rs: rs, outputs: map[v1.ObjectName]json.RawMessage{}, spec: spec, input: input}
	// Run-start payload cap (ADR-0094): a run created on the internal store (a Sensor action, ADR-0109)
	// skipped the admission cap. The over-cap input stays out of the run record and the FailureContext.
	if e.cfg.PayloadLimit > 0 && int64(len(input)) > e.cfg.PayloadLimit {
		rec.Input, run.input = nil, nil
		return e.failAtStart(ctx, run, fault.Invalidf(engineOp, "run %q input %d bytes exceeds the payload limit %d (PayloadLimitExceeded) — pass large data by reference on the blob substrate", runName, len(input), e.cfg.PayloadLimit))
	}
	// Run-start contract gate (ADR-0098): a run admitted before its workflow was Ready (async/Sensor
	// start) is checked here against the now-pinned contract, and fails fast rather than dropping silently.
	if pinned != nil && len(pinned.Input) > 0 {
		if diffs := v1.CheckInput(input, pinned.Input); len(diffs) > 0 {
			return e.failAtStart(ctx, run, fault.Invalidf(engineOp, "run %q input violates the workflow contract (InputSchemaMismatch): %s", runName, v1.FieldDiffs(diffs)))
		}
	}
	if err := e.persist(ctx, run); err != nil {
		if fault.KindOf(err) != fault.PayloadTooLarge {
			return nil, err
		}
		return e.failAtStart(ctx, run, fault.Wrapf(err, fault.Invalid, engineOp, "run %q cannot be recorded (RunRecordTooLarge)", runName))
	}
	return e.drive(ctx, stop, run)
}

// failAtStart fails a run at the run-start gate. It records the Failed run before fail() fires onFailure:
// a run that cannot be stored stays unrecorded, and its requeue must not fire the handler again. A record
// over the run store's value limit is recorded without its input, like an over-cap input.
func (e *Engine) failAtStart(ctx context.Context, run *activeRun, cause error) (*runstate.Record, error) {
	run.rec.Phase, run.rec.Error = runFailed, capErr(cause.Error())
	err := e.persist(ctx, run)
	if fault.KindOf(err) == fault.PayloadTooLarge && len(run.rec.Input) > 0 {
		run.rec.Input = nil
		err = e.persist(ctx, run)
	}
	if err != nil {
		return nil, err
	}
	return e.fail(ctx, run, cause)
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
	if rec.PausedAt > 0 { // the paused interval is excluded from the run timeout (ADR-0094)
		rec.PausedNanos += e.clock.Now().UnixNano() - rec.PausedAt
		rec.PausedAt = 0
	}
	rec.Paused = false // resume clears the pause
	rec.Phase = runRunning
	return e.drive(context.WithoutCancel(ctx), ctx, &activeRun{rec: rec, rs: rs, outputs: outputs, spec: spec, input: rec.Input})
}

// Replay seeds runName from a finished source run's checkpoint and drives it (ADR-0107): a NEW run that
// re-runs seed.From + its descendants, reusing the source's recorded upstream outputs verbatim. workflow
// is the replay run's declared workflow (must match the source's). current is the workflow's live resolved
// step-image map (the ADR-0098 cache) — the drift-gate comparison key + the re-run steps' revision.
// Faults: NotFound (source absent); Invalid whose message leads with reason token SeedInvalid or
// DigestDrift (naming the offending step). The source record is never mutated.
func (e *Engine) Replay(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName, seed v1.ReplaySeed, current map[v1.ObjectName]string) (*runstate.Record, error) {
	return e.replay(ctx, ns, runName, "", workflow, seed, current)
}

// replay is Replay stamping the starting WorkflowRun's uid on the new record, as StartOptions.RunUID does
// for Execute.
func (e *Engine) replay(ctx context.Context, ns v1.NamespaceName, runName v1.ObjectName, runUID v1.UID, workflow v1.ObjectName, seed v1.ReplaySeed, current map[v1.ObjectName]string) (*runstate.Record, error) {
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
	if len(src.Spec.Steps) == 0 { // the retention sweep's record of a run that closed without one (#346)
		return nil, fault.Invalidf(engineOp, "SeedInvalid: source run %q has no checkpoint to replay", seed.Run)
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
		Namespace: ns, Name: runName, RunUID: runUID, Workflow: src.Workflow, Phase: runRunning, Input: src.Input,
		Spec: spec, Contract: src.Contract, StepContracts: src.StepContracts, Depth: 0,
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
	run := &activeRun{rec: rec, rs: rs, outputs: outputs, spec: spec, input: src.Input}
	if err := e.persist(context.WithoutCancel(ctx), run); err != nil {
		return nil, err
	}
	return e.drive(context.WithoutCancel(ctx), ctx, run)
}

// isCopied reports whether a source step's phase means "reuse it verbatim" in a replay (a terminal
// success/skip with a settled output), vs re-run it fresh (ADR-0107).
func isCopied(p v1.StepPhase) bool { return p == v1.StepSucceeded || p == v1.StepSkipped }

// rebuildState restores scheduling state from a durable record. An in-flight
// (Running) step is reset to Pending so recovery re-dispatches it, keeping its attempt count so the
// re-dispatch gets a fresh attempt ID.
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
			n.phase = v1.StepPending // re-dispatch on recovery — its timings/error stay zero (it re-runs fresh)
			n.attempts = s.Attempts
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
// terminal runs "swept after workflow.retention"). retention ≤ 0 disables the sweep. reclaim, when
// non-nil, runs before each expired record is deleted (the run reconciler deletes the run's
// WorkflowRun there); its error keeps the record for the next sweep. Returns the number of records
// reclaimed. Non-terminal runs are never swept. Callers invoke it periodically (pkg/funcd lifecycle);
// it is idempotent and safe to run concurrently with reconciles (the store is the single writer per
// run and a terminal run is immutable).
func (e *Engine) SweepExpired(ctx context.Context, retention time.Duration, reclaim func(context.Context, *runstate.Record) error) (int, error) {
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
		if reclaim != nil {
			if rerr := reclaim(ctx, rec); rerr != nil {
				return swept, fault.Wrapf(rerr, fault.KindOf(rerr), engineOp, "reclaim expired run %q", rec.Name)
			}
		}
		if derr := e.runs.Delete(ctx, rec.Namespace, rec.Name); derr != nil {
			return swept, fault.Wrapf(derr, fault.KindOf(derr), engineOp, "delete expired run %q", rec.Name)
		}
		swept++
	}
	return swept, nil
}

// Pause requests a graceful pause (ADR-0146 Decision 5). A live run is only signalled: its goroutine lets
// the in-flight calls finish, starts nothing new and persists Paused. Otherwise the record is marked Paused,
// so the next drive dispatches nothing. A terminal or already Paused record is left unchanged.
func (e *Engine) Pause(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	e.mu.Lock()
	if lr, ok := e.running[runKey{ns, name}]; ok {
		if lr.pausedAt == 0 {
			lr.pausedAt = e.clock.Now().UnixNano()
			close(lr.pause)
		}
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()
	rec, err := e.runs.Get(ctx, ns, name)
	if err != nil {
		return err
	}
	if rec.Terminal() || rec.Paused {
		return nil
	}
	rec.PausedAt = e.clock.Now().UnixNano()
	rec.Paused = true
	rec.Phase = runPaused
	return e.runs.Put(ctx, rec)
}

// Cancel abandons a run (ADR-0146 Decision 3). A live run's context is cancelled with a cancel cause: its
// in-flight calls close and its goroutine records the run Cancelled. Otherwise the record is written here:
// pending and running steps Cancelled (a running one with its end time and the cancel error) and the run
// Cancelled. A terminal record is left unchanged (WorkflowRunSpec.Cancel).
func (e *Engine) Cancel(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	if e.cancelLive(ns, name) {
		return nil
	}
	rec, err := e.runs.Get(ctx, ns, name)
	if err != nil {
		return err
	}
	if rec.Terminal() {
		return nil
	}
	now := e.clock.Now().UnixNano()
	for i := range rec.Steps {
		switch rec.Steps[i].Phase {
		case v1.StepRunning:
			rec.Steps[i].EndedAt, rec.Steps[i].Error = now, cancelledStepError
			rec.Steps[i].Phase = v1.StepCancelled
		case v1.StepPending:
			rec.Steps[i].Phase = v1.StepCancelled
		}
	}
	rec.Phase = runCancelled
	return e.runs.Put(ctx, rec)
}

// runKey names a top-level run in the registry.
type runKey struct {
	ns   v1.NamespaceName
	name v1.ObjectName
}

// liveRun is a top-level run whose goroutine is live: its WorkflowRun's uid, the cancel of its context, and
// its pause signal (closed once, at pausedAt).
type liveRun struct {
	uid      v1.UID
	cancel   context.CancelCauseFunc
	pause    chan struct{}
	pausedAt int64
}

// runExit is what a run goroutine exited with when it left no terminal record: start returns it once to the
// next start of the same uid.
type runExit struct {
	uid v1.UID
	rec *runstate.Record
	err error
}

// cancelCause is the cause a run's context is cancelled with on spec.cancel or the WorkflowRun's deletion.
type cancelCause struct{ at int64 }

func (c *cancelCause) Error() string { return "run cancelled" }

// cancelledStepError is the error a step that was running at the cancel records (ADR-0146 Decision 3).
const cancelledStepError = "cancelled while running (spec.cancel); the step's call may have completed"

var (
	// errRunStopped is the cause the shutdown drain cancels the in-flight calls with at its bound.
	errRunStopped = errors.New("run stopped by the shutdown drain")
	// errHalted ends a step or an inline child that did not start its next attempt or step: the run paused
	// or the engine drains. The step goes back to Pending with its attempts kept.
	errHalted = errors.New("run halted before its next attempt")
	// errDraining is start's refusal once the engine drains.
	errDraining = errors.New("the workflow engine is draining")
)

// cancelOf returns the cancel cause ctx ended with, nil when it did not end by a cancel.
func cancelOf(ctx context.Context) *cancelCause {
	var c *cancelCause
	if errors.As(context.Cause(ctx), &c) {
		return c
	}
	return nil
}

// start runs drive on an engine-owned goroutine, returning at once (ADR-0146 Decision 2). drive gets the
// run's own context, which only Cancel, a deletion and the drain bound end. A live run ⇒ (nil, nil); the
// record and error a previous goroutine of uid exited with are returned once instead; draining ⇒
// fault.Unavailable.
func (e *Engine) start(uid v1.UID, ns v1.NamespaceName, name v1.ObjectName, drive func(ctx context.Context) (*runstate.Record, error)) (*runstate.Record, error) {
	key := runKey{ns, name}
	e.mu.Lock()
	defer e.mu.Unlock()
	if x, ok := e.exits[key]; ok {
		delete(e.exits, key)
		if x.uid == uid {
			return x.rec, x.err
		}
	}
	if e.draining {
		return nil, fault.Wrapf(errDraining, fault.Unavailable, engineOp, "start run %q", name)
	}
	if _, ok := e.running[key]; ok {
		return nil, nil
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	e.running[key] = &liveRun{uid: uid, cancel: cancel, pause: make(chan struct{})}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		rec, err := drive(ctx)
		e.mu.Lock()
		delete(e.running, key)
		if err != nil && !errors.Is(err, errHalted) && (rec == nil || !rec.Terminal()) {
			e.exits[key] = runExit{uid: uid, rec: rec, err: err}
		}
		e.mu.Unlock()
		cancel(nil)
		if e.notify != nil {
			e.notify(ns, name)
		}
	}()
	return nil, nil
}

// live reports the uid of the run's live goroutine, if one is live.
func (e *Engine) live(ns v1.NamespaceName, name v1.ObjectName) (v1.UID, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	lr, ok := e.running[runKey{ns, name}]
	if !ok {
		return "", false
	}
	return lr.uid, true
}

// cancelLive cancels the run's live goroutine, reporting whether one was live.
func (e *Engine) cancelLive(ns v1.NamespaceName, name v1.ObjectName) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	lr, ok := e.running[runKey{ns, name}]
	if ok {
		lr.cancel(&cancelCause{at: e.clock.Now().UnixNano()})
	}
	return ok
}

// forget cancels the run's live goroutine and drops what an exited one left: its WorkflowRun is gone.
func (e *Engine) forget(ns v1.NamespaceName, name v1.ObjectName) {
	e.cancelLive(ns, name)
	e.mu.Lock()
	delete(e.exits, runKey{ns, name})
	e.mu.Unlock()
}

// Run blocks until ctx ends, then drains (ADR-0146 Decision 6) for at most drain: start refuses, every drive
// starts no new step or attempt, and the in-flight calls may finish; at the bound they are cancelled with
// errRunStopped and return to Pending. Run returns when every run goroutine exited.
func (e *Engine) Run(ctx context.Context, drain time.Duration) {
	<-ctx.Done()
	e.mu.Lock()
	if !e.draining {
		e.draining = true
		close(e.drainCh)
	}
	e.mu.Unlock()
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(max(drain, 0))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	e.mu.Lock()
	for _, lr := range e.running {
		lr.cancel(errRunStopped)
	}
	e.mu.Unlock()
	e.endBound(errRunStopped)
	<-done
}

// pauseOf returns the pause signal of a top-level run's live goroutine; nil (never closed) for an inline
// child, which does not see the pause, or a run driven outside start.
func (e *Engine) pauseOf(rec *runstate.Record) chan struct{} {
	if rec.Depth != 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.running[runKey{rec.Namespace, rec.Name}]; ok && (rec.RunUID == "" || rec.RunUID == lr.uid) {
		return lr.pause
	}
	return nil
}

// pausedAt is the time a run's pause was signalled, 0 when none was.
func (e *Engine) pausedAt(rec *runstate.Record) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.running[runKey{rec.Namespace, rec.Name}]; ok {
		return lr.pausedAt
	}
	return 0
}

// halted reports whether the run may start no new step or attempt: it paused or the engine drains.
func (e *Engine) halted(run *activeRun) bool {
	select {
	case <-e.drainCh:
		return true
	case <-run.pause:
		return true
	default:
		return false
	}
}

// acquire takes a step-call slot (ADR-0146 Decision 7), waiting while the cap is reached. It returns the
// slot's release; ctx ending ends the wait with its error, a halt signal with errHalted.
func (e *Engine) acquire(ctx context.Context, pause, drain <-chan struct{}) (func(), error) {
	if e.slots == nil {
		return func() {}, nil
	}
	release := func() { <-e.slots }
	select {
	case e.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pause:
		return nil, errHalted
	case <-drain:
		return nil, errHalted
	}
}

// activeRun is the per-run state the engine threads through a run's drive: its record, its scheduling state,
// the recorded step outputs, the pinned spec, the run input and its pause signal. While the steps run
// concurrently, rs.mu guards rec, the step nodes and outputs; spec, input and pause are only read.
type activeRun struct {
	rec     *runstate.Record
	rs      *runState
	outputs map[v1.ObjectName]json.RawMessage
	spec    v1.WorkflowSpec
	input   json.RawMessage
	pause   chan struct{} // closed when the run's live goroutine is paused; nil ⇒ never
}

// drive advances a run from the given scheduling state until it is terminal, paused or halted by the drain.
// Its steps run until stop ends; its record and onFailure handler use ctx, so a child its parent stopped still
// records and handles its failure. A cancel cause on stop ends it Cancelled (ADR-0146 Decision 3).
func (e *Engine) drive(ctx, stop context.Context, run *activeRun) (*runstate.Record, error) {
	run.pause = e.pauseOf(run.rec)
	if run.rec.Paused {
		run.rec.Phase = runPaused
		if err := e.persist(ctx, run); err != nil {
			return nil, err
		}
		return run.rec, nil
	}
	// Run-timeout is start-relative and excludes paused time (ADR-0094 guarantee, ADR-0096): a run spans
	// drives (pause, restart), so a single-drive ctx deadline can't bound it. runCtx bounds the steps only:
	// fail() runs the onFailure handler on ctx, so a RunTimedOut run still invokes it.
	runCtx := stop
	if run.spec.Timeout > 0 {
		if e.clock.Now().UnixNano() > runDeadline(run.rec, run.spec) {
			return e.fail(ctx, run, runTimedOut(context.DeadlineExceeded))
		}
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(stop, time.Unix(0, runDeadline(run.rec, run.spec)))
		defer cancel()
	}

	// Every step that may run is started on its own goroutine, and each successor as soon as its join
	// settles (ADR-0094: fan-out is parallel dispatch). The first failure ends the run fail-fast: it
	// cancels the running siblings and records each as it returns, then the run ends.
	stepCtx, cancelSteps := context.WithCancel(runCtx)
	defer cancelSteps()
	results := make(chan stepResult)
	running := 0
	var end func() (*runstate.Record, error) // set by the first failure; nothing new starts after it
	for {
		if end == nil {
			started, err := e.startReady(ctx, stepCtx, runCtx, run, results)
			running += started
			if err != nil {
				end = func() (*runstate.Record, error) { return e.fail(ctx, run, err) }
				cancelSteps()
			}
		}
		if running == 0 {
			break
		}
		r := <-results
		running--
		if f := e.settle(ctx, runCtx, run, r, end != nil); f != nil && end == nil {
			end = f
			cancelSteps()
		}
	}
	if c := cancelOf(runCtx); c != nil {
		return e.finishCancelled(ctx, run, c)
	}
	if end != nil {
		return end()
	}

	run.rec.Phase = run.rs.runPhase()
	if run.rec.Phase == runRunning && (e.halted(run) || errors.Is(context.Cause(runCtx), errRunStopped)) {
		return e.finishHalted(ctx, run)
	}
	if err := e.persist(ctx, run); err != nil {
		return nil, err
	}
	return run.rec, nil
}

// finishCancelled ends a cancelled run (ADR-0146 Decision 3): each step still Pending is Cancelled without
// timings (the in-flight ones were recorded Cancelled as they returned) and the run is Cancelled, with no
// retry and no onFailure.
func (e *Engine) finishCancelled(ctx context.Context, run *activeRun, c *cancelCause) (*runstate.Record, error) {
	run.rs.mu.Lock()
	for _, n := range run.rs.steps {
		switch n.phase {
		case v1.StepPending:
			n.phase = v1.StepCancelled
		case v1.StepRunning:
			n.phase, n.endedAt, n.errMsg = v1.StepCancelled, c.at, cancelledStepError
		}
	}
	run.rs.mu.Unlock()
	run.rec.Phase = runCancelled
	if err := e.persist(ctx, run); err != nil {
		return nil, err
	}
	return run.rec, nil
}

// finishHalted ends a drive that stopped with steps left (ADR-0146 Decisions 5 and 6): a paused run persists
// Paused from the signal's time; a run the drain halted persists Running, its unfinished steps Pending, and
// returns errHalted, so an inline child's parent step returns to Pending too.
func (e *Engine) finishHalted(ctx context.Context, run *activeRun) (*runstate.Record, error) {
	select {
	case <-run.pause:
		if !run.rec.Paused {
			run.rec.PausedAt = e.pausedAt(run.rec)
		}
		run.rec.Paused, run.rec.Phase = true, runPaused
		if err := e.persist(ctx, run); err != nil {
			return nil, err
		}
		return run.rec, nil
	default:
	}
	if err := e.persist(ctx, run); err != nil {
		return nil, err
	}
	return run.rec, fault.Wrapf(errHalted, fault.Unavailable, engineOp, "run %q halted by the shutdown drain", run.rec.Name)
}

// recordFailed ends a drive whose run record could not be written: a record the run store cannot hold
// is a run outcome, not a retryable store error, so the run fails; any other error goes back to the
// reconciler, which resumes from the durable record.
func (e *Engine) recordFailed(ctx context.Context, run *activeRun, err error) (*runstate.Record, error) {
	if fault.KindOf(err) == fault.PayloadTooLarge {
		return e.fail(ctx, run, fault.Wrapf(err, fault.Invalid, engineOp, "record the step outputs"))
	}
	return nil, err
}

// stepResult is how one running step returned.
type stepResult struct {
	n   *stepNode
	out json.RawMessage
	err error
}

// startReady settles the skip cascade and starts every step that may run now (its join satisfied, its
// when.condition true) on its own goroutine, which reports to results. It returns how many it started;
// an error fails the run: a when.condition that cannot be evaluated, or the end of runCtx.
func (e *Engine) startReady(ctx, stepCtx, runCtx context.Context, run *activeRun, results chan<- stepResult) (int, error) {
	run.rs.mu.Lock()
	defer run.rs.mu.Unlock()
	started := 0
	for {
		for _, n := range run.rs.pendingToSkip() {
			n.phase = v1.StepSkipped
		}
		batch, skipped, err := e.selectRunnable(run)
		if err != nil {
			return started, err
		}
		for _, n := range skipped {
			n.phase = v1.StepSkipped
		}
		if len(batch) == 0 {
			if len(run.rs.pendingToSkip()) > 0 {
				continue // more cascade to resolve
			}
			return started, nil
		}
		if e.halted(run) {
			return started, nil
		}
		if err := runCtx.Err(); err != nil { // the run's context ended between steps
			return started, runStopped(err)
		}
		for _, n := range batch {
			e.setRunning(n)
			parents := parentOutputs(n, run.outputs)
			started++
			go func() {
				out, err := e.runStep(ctx, stepCtx, run, n, parents)
				results <- stepResult{n: n, out: out, err: err}
			}()
		}
	}
}

// capOutput fails a step output over the payload limit. The failure is permanent: a retry cannot
// shrink it (ADR-0094 payload cap).
func (e *Engine) capOutput(n *stepNode, out json.RawMessage) error {
	if e.cfg.PayloadLimit > 0 && int64(len(out)) > e.cfg.PayloadLimit {
		return Permanent(fault.Invalidf(engineOp, "step %q output exceeds payload limit %d", n.name, e.cfg.PayloadLimit))
	}
	return nil
}

// runStep runs one started step: a builtin in-engine, a sub-workflow inline, or a function by dispatch.
// It builds its input from parents (its parents' outputs); the shared outputs are only persisted. The step
// runs on stepCtx; a sub-workflow child also gets the run's ctx for its own record and handler. Whatever
// its kind, the step's output passes the payload-limit check here, before settle stores it.
func (e *Engine) runStep(ctx, stepCtx context.Context, run *activeRun, n *stepNode, parents map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	st := specStep(run.spec, n.name)
	if functionOf(st) == nil { // a builtin or sub-workflow step; dispatchStep records each attempt itself
		if err := e.persist(ctx, run); err != nil {
			return nil, &writeAheadError{err: err}
		}
	}
	var out json.RawMessage
	var err error
	switch {
	case st != nil && st.Builtin != nil:
		// A builtin is a normal step run in-engine: a wait blocks (on ctx), a pass transforms;
		// then it Succeeds. No dispatch, no special state (ADR-0096).
		out, err = e.runBuiltin(stepCtx, run.rec, st, n, run.input, parents)
	case st != nil && st.Workflow != nil: // a sub-workflow step runs a child workflow inline (ADR-0099)
		out, err = e.runChild(ctx, stepCtx, run.rec, st.Workflow.Ref, n, run.input, parents)
	default:
		out, err = e.dispatchStep(ctx, stepCtx, run, n, e.stepInput(n, run.input, parents, st))
	}
	if err != nil {
		return nil, err
	}
	if err := e.capOutput(n, out); err != nil {
		return nil, err
	}
	return out, nil
}

// parentOutputs copies the outputs of n's parents for its running step, which never reads the shared map.
func parentOutputs(n *stepNode, outputs map[v1.ObjectName]json.RawMessage) map[v1.ObjectName]json.RawMessage {
	parents := make(map[v1.ObjectName]json.RawMessage, len(n.dependsOn))
	for _, p := range n.dependsOn {
		if out, ok := outputs[p]; ok {
			parents[p] = out
		}
	}
	return parents
}

// settle records how a step returned. A failure returns how the run ends (the first one decides). A
// sibling that fail-fast cancelled (ending) goes back to Pending: it never finished, so a replay of the
// failed run runs it (ADR-0107); like recovery, it keeps its attempt count and drops its timings. So does a
// step the pause or the drain halted (ADR-0146). After a cancel, any result is discarded: the step is
// Cancelled.
func (e *Engine) settle(ctx, runCtx context.Context, run *activeRun, r stepResult, ending bool) func() (*runstate.Record, error) {
	run.rs.mu.Lock()
	if c := cancelOf(runCtx); c != nil {
		r.n.phase, r.n.endedAt, r.n.errMsg = v1.StepCancelled, c.at, cancelledStepError
		run.rs.mu.Unlock()
		return func() (*runstate.Record, error) { return e.finishCancelled(ctx, run, c) }
	}
	halted := r.err != nil && (errors.Is(r.err, errHalted) || errors.Is(context.Cause(runCtx), errRunStopped))
	switch {
	case r.err == nil:
		e.markSucceeded(r.n)
		run.outputs[r.n.name] = r.out
	case halted || ending && (errors.Is(r.err, context.Canceled) || errors.Is(r.err, context.DeadlineExceeded)):
		r.n.phase, r.n.startedAt, r.n.endedAt, r.n.errMsg = v1.StepPending, 0, 0, ""
	default:
		e.markFailed(r.n, r.err) // ADR-0100: a builtin/sub-workflow passes its raw cause; dispatchStep stamped its own
	}
	run.rs.mu.Unlock()
	if halted {
		return nil
	}
	if r.err == nil {
		if ending {
			return nil
		}
		if err := e.persist(ctx, run); err != nil {
			return func() (*runstate.Record, error) { return e.recordFailed(ctx, run, err) }
		}
		return nil
	}
	var wa *writeAheadError
	st := specStep(run.spec, r.n.name)
	switch {
	case errors.As(r.err, &wa): // the run store refused the intent: the step never ran
		return func() (*runstate.Record, error) { return e.recordFailed(ctx, run, wa.err) }
	case runCtx.Err() != nil && (st == nil || st.Workflow == nil): // the end of the run's context, not the step, failed it
		return func() (*runstate.Record, error) { return e.fail(ctx, run, runStopped(runCtx.Err())) }
	default:
		return func() (*runstate.Record, error) { return e.fail(ctx, run, r.err) }
	}
}

// writeAheadError is a failed write-ahead of a step or a dispatch attempt: a run-store outcome for
// drive, not a step failure.
type writeAheadError struct{ err error }

func (e *writeAheadError) Error() string { return e.err.Error() }
func (e *writeAheadError) Unwrap() error { return e.err }

// selectRunnable returns the ready steps that should run now (when true) and those
// to Skip (when false).
func (e *Engine) selectRunnable(run *activeRun) (runnable, skip []*stepNode, err error) {
	for _, n := range run.rs.ready() {
		st := specStep(run.spec, n.name)
		if st == nil || st.When == nil {
			runnable = append(runnable, n)
			continue
		}
		ok, err := e.evalWhen(st.When.Condition, n, run.rec, run.input, run.outputs)
		if err != nil {
			e.markFailed(n, err) // the step whose condition cannot be evaluated carries the cause (ADR-0100)
			return nil, nil, err
		}
		if ok {
			runnable = append(runnable, n)
		} else {
			skip = append(skip, n)
		}
	}
	return runnable, skip, nil
}

// stepTarget resolves the function a step dispatches to: a step that references an existing
// function (spec.function) targets it directly; an image step targets its materialized owned
// function <workflow>-<step> (ADR-0094). An empty workflow (bare-engine tests) yields "-<step>",
// harmless because those tests key their fake dispatcher on the step name.
func stepTarget(workflow v1.ObjectName, spec v1.WorkflowSpec, step v1.ObjectName) v1.ObjectName {
	if st := specStep(spec, step); st != nil && st.Function != nil && st.Function.Ref != "" {
		return st.Function.Ref
	}
	return v1.StepFunctionName(workflow, step)
}

// dispatchStep invokes one step with retry, sending it stepInput. It reads the run's
// pinned identity + trace context off rec (ADR-0102: every attempt propagates the run's traceparent).
// Each attempt is persisted on rctx before it goes out on ctx (the ADR-0094 write-ahead intent), so
// recovery knows the attempts already made: a recovered in-flight step continues with a fresh attempt ID and
// the rest of its retry budget, and always gets its re-dispatch. Each attempt holds a step-call slot while
// its call is out (ADR-0146 Decision 7); a pause or the drain ends the step with errHalted instead of its
// next attempt.
func (e *Engine) dispatchStep(rctx, ctx context.Context, run *activeRun, n *stepNode, stepInput json.RawMessage) (json.RawMessage, error) {
	ns, runName, workflow := run.rec.Namespace, run.rec.Name, run.rec.Workflow
	st := specStep(run.spec, n.name)
	maxAttempts := e.cfg.DefaultMaxAttempts
	backoff := time.Duration(0)
	fn := functionOf(st) // dispatch knobs live on FunctionStep (ADR-0096)
	if fn != nil && fn.Retry != nil {
		if fn.Retry.MaxAttempts > 0 {
			maxAttempts = fn.Retry.MaxAttempts
		}
		backoff = fn.Retry.Backoff
	}
	if backoff == 0 {
		backoff = e.cfg.DefaultRetryBackoff
	}
	first := n.attempts + 1
	target := stepTarget(workflow, run.spec, n.name)
	// ADR-0105: nest the step span under its DAG predecessor. The primary parent is the first (post-implicit-
	// chaining) dependency's pre-minted span-id; a true root step (no dependency) parents on the run root. The
	// remaining dependencies become fan-in span links. Same for every attempt (one span-id per step).
	parentSpan := run.rec.RootSpanID
	var links []string
	if len(n.dependsOn) > 0 {
		if pid := stepSpanID(run.rs, n.dependsOn[0]); pid != "" {
			parentSpan = pid
		}
		for _, dep := range n.dependsOn[1:] {
			if id := stepSpanID(run.rs, dep); id != "" {
				links = append(links, id)
			}
		}
	}
	// A step-timeout is a retryable failure on a CHILD ctx; the parent (run) deadline is checked
	// separately in drive and maps to RunTimedOut.
	stepTimeout := e.stepTimeout(fn)
	var lastErr, stopped error
	for attempt := first; attempt <= max(maxAttempts, first); attempt++ {
		release, err := e.acquire(ctx, run.pause, e.drainCh)
		if err != nil {
			return nil, err
		}
		run.rs.mu.Lock()
		n.attempts = attempt // ADR-0100: the dispatch attempt count
		run.rs.mu.Unlock()
		if err := e.persist(rctx, run); err != nil {
			release()
			return nil, &writeAheadError{err: err}
		}
		attemptCtx := ctx
		var cancel context.CancelFunc
		if stepTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, stepTimeout)
		}
		out, err := e.dispatch.Dispatch(attemptCtx, DispatchRequest{
			Namespace: ns, Run: runName, Step: n.name, Target: target,
			Attempt: attempt, Input: stepInput, MaxOutput: e.cfg.PayloadLimit,
			TraceID: run.rec.TraceID, ParentSpanID: parentSpan, // ADR-0102/0105: run trace + the predecessor edge
			SpanID: n.spanID, Links: links, // ADR-0105: the step's own span-id + fan-in links
		})
		if cancel != nil {
			cancel()
		}
		release()
		if err == nil {
			return out, nil
		}
		lastErr = err
		if isPermanent(err) || attempt >= maxAttempts || ctx.Err() != nil {
			break
		}
		if e.halted(run) {
			return nil, errHalted
		}
		if backoff > 0 {
			timer := time.NewTimer(retryBackoff(backoff, attempt))
			select {
			case <-ctx.Done():
				stopped = runStopped(ctx.Err())
			case <-run.pause:
				stopped = errHalted
			case <-e.drainCh:
				stopped = errHalted
			case <-timer.C:
			}
			timer.Stop()
			if stopped != nil {
				break
			}
		}
	}
	// ADR-0100: stamp the BARE dispatch cause here (before this retry-wrap and fail()'s run-wrap), so
	// describe names the step's actual error (e.g. "scorer returned 503"), not the engine envelope.
	run.rs.mu.Lock()
	n.errMsg = capErr(lastErr.Error())
	run.rs.mu.Unlock()
	if stopped != nil { // the backoff was ended by the step's context, a pause or the drain, not the step
		return nil, stopped
	}
	return nil, fault.Wrapf(lastErr, fault.Unavailable, engineOp, "step %q failed after retries", n.name)
}

// maxRetryBackoff caps one retry gap at the largest backoff StepRetry admits, so the doubling never overflows.
const maxRetryBackoff = time.Hour

// retryBackoff is the gap after a step's attempt-th failed dispatch: backoff·2^(attempt-1), capped at
// maxRetryBackoff (ADR-0094: exponential backoff). A recovered step continues the schedule from its
// persisted attempt count.
func retryBackoff(backoff time.Duration, attempt int) time.Duration {
	d := backoff
	for i := 1; i < attempt && d < maxRetryBackoff; i++ {
		d *= 2
	}
	return min(d, maxRetryBackoff)
}

// stepTimeout is a function step's per-invocation bound: its own timeout, else the engine default
// (0 ⇒ none).
func (e *Engine) stepTimeout(fn *v1.FunctionStep) time.Duration {
	if fn != nil && fn.Timeout > 0 {
		return fn.Timeout
	}
	return e.cfg.DefaultStepTimeout
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

// failureContext is the onFailure handler's input (ADR-0094 FailureContext): failedStep is empty when
// no step failed (the run-start input gate), and input is the run's original input, verbatim.
type failureContext struct {
	Workflow   v1.ObjectName   `json:"workflow"`
	Run        v1.ObjectName   `json:"run"`
	FailedStep v1.ObjectName   `json:"failedStep"`
	Reason     string          `json:"reason"`
	Input      json.RawMessage `json:"input"`
}

// failureContextSchema is failureContext's schema: the producer the reconcile gate checks the onFailure
// handler's input against (ADR-0094). The run input is an object (the derived contract input's type).
func failureContextSchema() json.RawMessage {
	return marshalObjectSchema(
		map[string]string{"workflow": "string", "run": "string", "failedStep": "string", "reason": "string", "input": "object"},
		nil,
		map[string]bool{"workflow": true, "run": true, "failedStep": true, "reason": true, "input": true},
	)
}

// fail finalizes a Failed run, invoking the onFailure handler once if present. The handler's call holds a
// step-call slot and ends at its step timeout or the drain bound (ADR-0146).
func (e *Engine) fail(ctx context.Context, run *activeRun, cause error) (*runstate.Record, error) {
	run.rec.Phase, run.rec.Error = runFailed, capErr(cause.Error())
	if run.spec.OnFailure != "" {
		fc, _ := json.Marshal(failureContext{
			Workflow: run.rec.Workflow, Run: run.rec.Name, FailedStep: run.rs.failedStep(),
			Reason: cause.Error(), Input: run.input,
		})
		hctx, cancelBound := context.WithCancelCause(ctx)
		defer cancelBound(nil)
		defer context.AfterFunc(e.bound, func() { cancelBound(errRunStopped) })()
		if d := e.stepTimeout(functionOf(specStep(run.spec, run.spec.OnFailure))); d > 0 {
			var cancel context.CancelFunc
			hctx, cancel = context.WithTimeout(hctx, d)
			defer cancel()
		}
		h := run.rs.steps[run.spec.OnFailure]
		if h != nil {
			e.setRunning(h)
			h.attempts = 1
		}
		release, herr := e.acquire(hctx, nil, nil)
		if herr == nil {
			_, herr = e.dispatch.Dispatch(hctx, DispatchRequest{
				Namespace: run.rec.Namespace, Run: run.rec.Name, Step: run.spec.OnFailure,
				Target:  stepTarget(run.rec.Workflow, run.spec, run.spec.OnFailure),
				Attempt: 1, Input: fc, MaxOutput: e.cfg.PayloadLimit,
				TraceID: run.rec.TraceID, ParentSpanID: run.rec.RootSpanID, // ADR-0102: the handler joins the run's trace too
			})
			release()
		}
		if h != nil { // the handler's outcome is recorded but never changes the run phase (ADR-0094)
			if herr != nil {
				e.markFailed(h, herr)
			} else {
				e.markSucceeded(h)
			}
		}
	}
	run.rs.skipFailedDownstream()
	err := e.persist(ctx, run)
	if fault.KindOf(err) == fault.PayloadTooLarge {
		if err = e.dropUnrecordedOutputs(ctx, run, err); err == nil {
			run.rs.skipFailedDownstream()
			err = e.persist(ctx, run)
		}
	}
	if err != nil {
		return nil, err
	}
	return run.rec, fault.Wrapf(cause, fault.KindOf(cause), engineOp, "run %q failed", run.rec.Name)
}

// dropUnrecordedOutputs fails each step whose output the durable record does not hold yet, with the
// store's cause: those outputs are what overflow the run store's value limit (the record is size-bounded
// by the engine, runstate.Record). The run then ends Failed instead of re-running the step on every requeue.
func (e *Engine) dropUnrecordedOutputs(ctx context.Context, run *activeRun, cause error) error {
	durable, err := e.runs.Get(ctx, run.rec.Namespace, run.rec.Name)
	if err != nil {
		return err
	}
	recorded := make(map[v1.ObjectName]bool, len(durable.Steps))
	for _, st := range durable.Steps {
		recorded[st.Name] = len(st.Output) > 0
	}
	for name, out := range run.outputs {
		if len(out) > 0 && !recorded[name] {
			delete(run.outputs, name)
			e.markFailed(run.rs.steps[name], cause)
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

// persist writes the run record (the write-ahead intent + the coarse step mirror) and notifies a top-level
// run's key.
func (e *Engine) persist(ctx context.Context, run *activeRun) error {
	run.rs.mu.Lock()
	defer run.rs.mu.Unlock()
	run.rec.Steps = run.rec.Steps[:0]
	for _, name := range run.rs.order {
		n := run.rs.steps[name]
		// SpanID: ADR-0105 (persisted for Resume). StartedAt/EndedAt/Attempts/Error: ADR-0100 troubleshooting lineage.
		// Revision: ADR-0107 — the resolved digest-pinned image, carried from stepNode so it survives every
		// persist/Resume cycle and copied replay steps keep the source's digest.
		ss := runstate.StepState{
			Name: n.name, Phase: n.phase, SpanID: n.spanID, Revision: n.revision,
			Attempts: n.attempts, StartedAt: n.startedAt, EndedAt: n.endedAt, Error: n.errMsg,
		}
		if out, ok := run.outputs[n.name]; ok {
			ss.Output = out
		}
		run.rec.Steps = append(run.rec.Steps, ss)
	}
	run.rec.UpdatedAt = e.clock.Now().UnixNano()
	if err := e.runs.Put(ctx, run.rec); err != nil {
		return err
	}
	if run.rec.Depth == 0 && e.notify != nil { // ADR-0146: the run reconciler mirrors the record on its next pass
		e.notify(run.rec.Namespace, run.rec.Name)
	}
	return nil
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

// runStopped is the failure of a run whose context ended under its steps: RunTimedOut when a deadline
// passed (its own, or the parent's a child inherits, ADR-0099); a child its parent's fail-fast stopped did
// not time out.
func runStopped(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return runTimedOut(err)
	}
	return fault.Wrapf(err, fault.Unavailable, engineOp, "run stopped")
}

// runTimedOut wraps a run-deadline cause as the ADR-0094 RunTimedOut failure reason (distinct
// from a per-step timeout, which is a retryable step failure).
func runTimedOut(cause error) error {
	return fault.Wrapf(cause, fault.Unavailable, engineOp, "RunTimedOut: run deadline exceeded")
}
