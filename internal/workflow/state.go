// Package workflow implements the funcd workflow engine core (ADR-0094, FEAT-0005/F64):
// the state-machine orchestrator that drives a WorkflowRun over its steps. This file
// is the pure scheduling core — no I/O — so the semantics (implicit chaining, fan-out,
// join all/any, skip cascade, fail-fast, run-phase derivation) are unit-testable
// without Badger, dispatch, or containerd.
package workflow

import (
	"sync"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Run phases (ADR-0094): Pending → Running ⇄ Paused → Succeeded | Failed | Cancelled.
// They are stored in the generic WorkflowRun.status.phase (string-backed Phase); the
// engine writes them and they are never user-validated against the generic enum.
const (
	runPending   v1.Phase = v1.PhasePending
	runRunning   v1.Phase = "Running"
	runPaused    v1.Phase = "Paused"
	runSucceeded v1.Phase = "Succeeded"
	runFailed    v1.Phase = v1.PhaseFailed
	runCancelled v1.Phase = "Cancelled"
)

// stepNode is one step's static shape plus its live execution state within a run.
type stepNode struct {
	name      v1.ObjectName
	dependsOn []v1.ObjectName
	join      v1.JoinMode
	hasWhen   bool // whether the step carries a when.condition (evaluated by the engine)

	phase    v1.StepPhase
	spanID   string // ADR-0105: engine-minted trace span-id, so a successor parents on it (nested DAG waterfall)
	revision string // ADR-0107: the resolved digest-pinned image this step executes; stamped at start (function steps only), restored by rebuildState, preserved for copied replay steps — so the digest survives persist/Resume and is the replay drift-gate's comparison key

	// Troubleshooting lineage (ADR-0100), stamped at the step's terminal transition and mirrored to
	// status: when it started/ended (→ duration), how many dispatch attempts it took, and the raw
	// step-level failure cause (capped). Restored across Resume for already-terminal steps.
	startedAt int64  // unix nanos, stamped when the step goes Running
	endedAt   int64  // unix nanos, stamped at Succeeded/Failed
	attempts  int    // dispatch attempts (function steps); 0 for a single-shot builtin/sub-workflow
	errMsg    string // the raw step-level cause (capped to maxStatusError), Failed steps only
}

// runState is the in-memory scheduling state of one run: the step graph plus the
// derived onFailure handler (excluded from the DAG). It is the pure state machine;
// the engine persists a serialized form and mirrors phase into WorkflowRun.status.
type runState struct {
	// mu guards a run's state while its steps run concurrently; activeRun names what it covers.
	mu        sync.Mutex
	steps     map[v1.ObjectName]*stepNode
	order     []v1.ObjectName // deterministic iteration order (spec order)
	onFailure v1.ObjectName
	failFast  bool // once a step Fails, running siblings are cancelled (ADR-0094 default)
}

// newRunState builds the scheduling state from a Workflow spec. List order chains
// implicitly (v1.WorkflowSpec.EffectiveDependsOn, the graph admission checks for cycles).
// The onFailure handler is excluded from the DAG: the engine schedules it on run failure.
func newRunState(spec v1.WorkflowSpec) *runState {
	rs := &runState{
		steps:     make(map[v1.ObjectName]*stepNode, len(spec.Steps)),
		order:     make([]v1.ObjectName, 0, len(spec.Steps)),
		onFailure: spec.OnFailure,
		failFast:  true,
	}
	deps := spec.EffectiveDependsOn()
	for i := range spec.Steps {
		s := &spec.Steps[i]
		rs.steps[s.Name] = &stepNode{
			name:      s.Name,
			dependsOn: append([]v1.ObjectName(nil), deps[s.Name]...),
			join:      s.Join,
			hasWhen:   s.When != nil,
			phase:     v1.StepPending,
		}
		rs.order = append(rs.order, s.Name)
	}
	return rs
}

// descendants returns the DAG steps that transitively DEPEND ON name — the REVERSE dependsOn closure
// (name's downstream subtree, never its ancestors), computed over the post-implicit-chaining graph
// (newRunState already filled implicit list-order edges), excluding name itself. Used by replay to
// compute the re-run set {from} ∪ descendants(from) (ADR-0107). The onFailure handler is never a
// descendant (it is scheduled by fail(), not the DAG).
func (rs *runState) descendants(name v1.ObjectName) []v1.ObjectName {
	children := make(map[v1.ObjectName][]v1.ObjectName, len(rs.steps)) // parent → its direct children
	for _, s := range rs.dagSteps() {
		for _, p := range rs.steps[s].dependsOn {
			children[p] = append(children[p], s)
		}
	}
	seen := map[v1.ObjectName]bool{}
	var out []v1.ObjectName
	var walk func(n v1.ObjectName)
	walk = func(n v1.ObjectName) {
		for _, c := range children[n] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				walk(c)
			}
		}
	}
	walk(name)
	return out
}

// dagSteps returns the step names in spec order, excluding the onFailure handler.
func (rs *runState) dagSteps() []v1.ObjectName {
	out := make([]v1.ObjectName, 0, len(rs.order))
	for _, n := range rs.order {
		if n != rs.onFailure {
			out = append(out, n)
		}
	}
	return out
}

// ready returns the steps eligible to dispatch now: Pending steps whose join over
// their parents is satisfied and whose parents are all terminal. A ready step with a
// when.condition still needs the engine to evaluate it (evaluateReady handles that);
// ready reports schedulability, not the when result.
func (rs *runState) ready() []*stepNode {
	var out []*stepNode
	for _, name := range rs.dagSteps() {
		n := rs.steps[name]
		if n.phase != v1.StepPending {
			continue
		}
		state, ok := rs.joinState(n)
		if !ok {
			continue // a parent is not yet terminal
		}
		if state == joinSkip {
			// join not satisfiable (skip cascade) — the engine marks it Skipped.
			continue
		}
		out = append(out, n)
	}
	return out
}

// joinResult classifies a step's readiness given its parents' terminal states.
type joinResult int

const (
	joinPending joinResult = iota // at least one parent not terminal
	joinRun                       // join satisfied — the step may run
	joinSkip                      // join can never be satisfied — the step is Skipped
)

// joinState evaluates a step's join over its (terminal) parents. ok is false when a
// parent is not yet terminal.
func (rs *runState) joinState(n *stepNode) (joinResult, bool) {
	if len(n.dependsOn) == 0 {
		return joinRun, true // a root DAG step is immediately runnable
	}
	succeeded, terminal := 0, 0
	for _, p := range n.dependsOn {
		pn := rs.steps[p]
		if pn == nil || !isTerminal(pn.phase) {
			return joinPending, false
		}
		terminal++
		if pn.phase == v1.StepSucceeded {
			succeeded++
		}
	}
	switch effectiveJoin(n.join) {
	case v1.JoinAny:
		if succeeded >= 1 {
			return joinRun, true
		}
		return joinSkip, true // all parents terminal, none succeeded → skip
	default: // JoinAll
		if succeeded == terminal {
			return joinRun, true
		}
		return joinSkip, true // a parent skipped/failed/cancelled → cascade skip
	}
}

// pendingToSkip returns the Pending steps whose join can never be satisfied (skip
// cascade), so the engine can mark them Skipped without dispatching.
func (rs *runState) pendingToSkip() []*stepNode {
	var out []*stepNode
	for _, name := range rs.dagSteps() {
		n := rs.steps[name]
		if n.phase != v1.StepPending {
			continue
		}
		if state, ok := rs.joinState(n); ok && state == joinSkip {
			out = append(out, n)
		}
	}
	return out
}

// runPhase derives the run phase from the DAG steps. Any Failed step ⇒ Failed
// (fail-fast). All terminal and none Failed ⇒ Succeeded (Skipped steps are fine).
// A Cancelled step ⇒ the run is Cancelled. Otherwise Running.
func (rs *runState) runPhase() v1.Phase {
	allTerminal, anyFailed, anyCancelled := true, false, false
	for _, name := range rs.dagSteps() {
		switch rs.steps[name].phase {
		case v1.StepFailed:
			anyFailed = true
		case v1.StepCancelled:
			anyCancelled = true
		case v1.StepPending, v1.StepRunning:
			allTerminal = false
		}
	}
	switch {
	case anyCancelled:
		return runCancelled
	case anyFailed && allTerminal:
		return runFailed
	case allTerminal:
		return runSucceeded
	default:
		return runRunning
	}
}

// failedStep names the run's Failed DAG step (the first in spec order), "" when none failed.
func (rs *runState) failedStep() v1.ObjectName {
	for _, name := range rs.dagSteps() {
		if rs.steps[name].phase == v1.StepFailed {
			return name
		}
	}
	return ""
}

// skipFailedDownstream marks Skipped the Pending descendants of every Failed DAG step: fail-fast ends
// the run, so they never run (ADR-0094). A Pending step outside those subtrees, like a cancelled
// sibling, stays Pending (ADR-0107).
func (rs *runState) skipFailedDownstream() {
	for _, name := range rs.dagSteps() {
		if rs.steps[name].phase != v1.StepFailed {
			continue
		}
		for _, d := range rs.descendants(name) {
			if n := rs.steps[d]; n.phase == v1.StepPending {
				n.phase = v1.StepSkipped
			}
		}
	}
}

// leaves returns the DAG steps that no other DAG step depends on — the run output
// composite is keyed by their names.
func (rs *runState) leaves() []v1.ObjectName {
	hasChild := make(map[v1.ObjectName]bool, len(rs.steps))
	for _, name := range rs.dagSteps() {
		for _, p := range rs.steps[name].dependsOn {
			hasChild[p] = true
		}
	}
	var out []v1.ObjectName
	for _, name := range rs.dagSteps() {
		if !hasChild[name] {
			out = append(out, name)
		}
	}
	return out
}

func effectiveJoin(j v1.JoinMode) v1.JoinMode {
	if j == v1.JoinAny {
		return v1.JoinAny
	}
	return v1.JoinAll
}

func isTerminal(p v1.StepPhase) bool {
	switch p {
	case v1.StepSucceeded, v1.StepFailed, v1.StepSkipped, v1.StepCancelled:
		return true
	default:
		return false
	}
}
