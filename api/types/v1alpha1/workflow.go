package v1alpha1

import (
	"encoding/json"
	"fmt"
	"strings"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/pyvvo/funcd/api/fault"
)

// Workflow is a namespaced, status-bearing resource: a declarative multi-step run
// definition whose steps are functions (ADR-0094, FEAT-0005/F64). The engine owns
// and materializes the step Functions; a WorkflowRun is one execution.
type Workflow struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       WorkflowSpec   `json:"spec"`
	Status     WorkflowStatus `json:"status,omitempty"`
}

// WorkflowSpec is the desired state: the ordered steps, workflow-owned KV stores, an
// optional declared I/O contract, a run-level timeout, and an optional onFailure
// handler step.
type WorkflowSpec struct {
	// Steps are the workflow's steps. List order chains implicitly (no dependsOn ⇒
	// the previous step); dependsOn declares explicit edges.
	Steps []WorkflowStep `json:"steps"`
	// KV declares workflow-owned KVStores whose table owners name a step (materialized
	// to the owning Function). Empty ⇒ no owned stores.
	KV []WorkflowKVStore `json:"kv,omitempty"`
	// Contract is the optional declared I/O contract (ADR-0090 shape); nil ⇒ the
	// effective contract is derived (F65). Every optional property must carry a
	// default (the total-defaults rule).
	Contract *WorkflowContract `json:"contract,omitempty"`
	// Timeout is the wall-clock bound on a whole run (paused time excluded); 0 ⇒ none.
	Timeout Duration `json:"timeout,omitempty" doc:"The wall-clock bound on a whole run, paused time excluded: 0s to 168h; 0s or unset means no bound."`
	// OnFailure names a handler function step (defined in Steps, excluded from the DAG)
	// invoked once when the run ends Failed. Empty ⇒ no handler.
	OnFailure ObjectName `json:"onFailure,omitempty"`
	// Pooling configures how the workflow's materialized step Functions are pooled and
	// kept warm (ADR-0046 worker pooling + ADR-0016 scaling). Empty ⇒ the default: all
	// image steps share one pool. A step may override it with its own step.pooling.
	Pooling WorkflowPooling `json:"pooling,omitempty"`
}

// WorkflowPooling governs how a workflow's materialized step Functions are grouped
// into worker pools and kept warm. It is applied to each owned Function at
// materialization (a step's own Pooling overrides it).
type WorkflowPooling struct {
	// Mode is "shared" (default — image steps of the same runtime co-locate in one
	// worker pool, saving memory) or "isolated" (each step runs in its own solo
	// worker / container).
	Mode PoolingMode `json:"mode,omitempty"`
	// Worker names the shared pool when Mode is shared; empty ⇒ the workflow's name.
	// Steps of different runtimes never share a worker (the pool is per-runtime); this
	// is the logical pool name they group under.
	Worker string `json:"worker,omitempty" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// MinReplicas keeps warm workers: 0 scales to zero (cold start on the first step of
	// the pool), ≥1 avoids the cold-start latency.
	MinReplicas int `json:"minReplicas,omitempty" minimum:"0" maximum:"15"`
}

// PoolingMode selects shared-pool vs isolated-container materialization.
type PoolingMode string

const (
	// PoolingShared co-locates same-runtime image steps in one worker pool (default).
	PoolingShared PoolingMode = "shared"
	// PoolingIsolated gives each step its own solo worker/container.
	PoolingIsolated PoolingMode = "isolated"
)

// Schema carries PoolingMode's enum into the generated OpenAPI (ADR-0048).
func (PoolingMode) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(PoolingShared), string(PoolingIsolated))
}

// WorkflowStep is one step — a kind-keyed union of exactly one of Function / Builtin / Workflow
// (Workflow runs a child workflow, F70), plus the orchestration fields common to every kind (ADR-0096).
type WorkflowStep struct {
	// Name is the step's name; a DNS-1123 label, unique within the workflow.
	Name ObjectName `json:"name"`
	// Function is a dispatched step: an owned image (materialized) or a ref to an existing Function.
	Function *FunctionStep `json:"function,omitempty"`
	// Builtin is an engine-native step (wait / pass) — run in-process, never dispatched.
	Builtin *BuiltinStep `json:"builtin,omitempty"`
	// Workflow runs a child Workflow as a step (a sub-workflow, F70/ADR-0099).
	Workflow *WorkflowRef `json:"workflow,omitempty"`
	// DependsOn names the parent steps; empty ⇒ follows the previous step in list order.
	DependsOn []ObjectName `json:"dependsOn,omitempty"`
	// Join is the fan-in mode: "all" (default) requires every parent Succeeded, "any"
	// requires one (exclusive-branch merges).
	Join JoinMode `json:"join,omitempty"`
	// When is an optional gate condition (ADR-0095 native-JS boolean); false ⇒ Skipped.
	When *StepWhen `json:"when,omitempty"`
	// Params is a static overlay merged over the step's flowing input (static wins). For a function
	// step it overlays the dispatched input; a pass expression sees the overlaid input; a wait
	// ignores it (output = input verbatim). The onFailure handler takes none (FailureContext only).
	Params json.RawMessage `json:"params,omitempty"`
}

// FunctionStep is a dispatched step (ADR-0096): sourced from an OWNED Image (materialized into
// <workflow>-<step>) OR a Ref to an existing Function (exactly one). All dispatch-only knobs live
// here, so an engine-native (Builtin) step cannot express them by construction.
type FunctionStep struct {
	// Image is a full OCI artifact reference the workflow owns and materializes into a Function.
	Image string `json:"image,omitempty"`
	// Ref references an existing Function in the namespace (a shared service).
	Ref ObjectName `json:"ref,omitempty"`
	// Retry is the per-step retry policy; nil ⇒ the engine default.
	Retry *StepRetry `json:"retry,omitempty"`
	// Timeout is the per-step invocation bound; 0 ⇒ the engine default.
	Timeout Duration `json:"timeout,omitempty" doc:"The per-step invocation bound: 0s to 24h; 0s or unset means workflow.defaultStepTimeout."`
	// Pooling overrides the workflow-level pooling for this step's materialized Function
	// (nil ⇒ inherit spec.pooling); image steps only.
	Pooling *WorkflowPooling `json:"pooling,omitempty"`
	// KV/Blob/Secrets/Config/Catalogs are the step's bindings (Function.spec shapes).
	KV       []FunctionKV      `json:"kv,omitempty"`
	Blob     []FunctionBlob    `json:"blob,omitempty"`
	Secrets  []ObjectName      `json:"secrets,omitempty"`
	Config   []ObjectName      `json:"config,omitempty"`
	Catalogs []FunctionCatalog `json:"catalogs,omitempty"`
}

// BuiltinStep is an engine-native step (ADR-0096) — a nested kind-union of exactly one of Wait /
// Pass, evaluated in-process (no container, no dispatch). Room to grow (Gate, F66).
type BuiltinStep struct {
	// Wait is a timer: a duration string ("30s", ADR-0194) or a ${{ }} goja Select expression evaluating
	// to one. The step blocks in-engine for the duration (on the run context, so the
	// run-timeout interrupts it), then passes its flowing input through as output. A normal step —
	// Running then Succeeded; no special state.
	Wait string `json:"wait,omitempty"`
	// Pass is a ${{ }} goja Select expression over the step's flowing input + prior step outputs;
	// its result is the step's output. No dispatch.
	Pass string `json:"pass,omitempty"`
}

// WorkflowRef references a child Workflow by name — a sub-workflow step (F70/ADR-0099).
type WorkflowRef struct {
	Ref ObjectName `json:"ref,omitempty"`
}

// JoinMode is a step's fan-in mode.
type JoinMode string

const (
	// JoinAll requires every parent to have Succeeded (the default).
	JoinAll JoinMode = "all"
	// JoinAny requires at least one parent to have Succeeded (exclusive-branch merge).
	JoinAny JoinMode = "any"
)

// Schema carries JoinMode's enum into the generated OpenAPI (ADR-0048).
func (JoinMode) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(JoinAll), string(JoinAny))
}

// StepWhen holds a native-JS boolean condition (ADR-0095 Condition mode) evaluated
// against direct-parent outputs and the run input.
type StepWhen struct {
	Condition string `json:"condition"`
}

// StepRetry is a per-step retry policy: attempts and exponential backoff.
type StepRetry struct {
	MaxAttempts int      `json:"maxAttempts,omitempty" minimum:"1" maximum:"100"`
	Backoff     Duration `json:"backoff,omitempty" doc:"The base of the exponential retry backoff: 0s to 1h; 0s or unset means workflow.defaultRetryBackoff."`
}

// WorkflowKVStore declares a workflow-owned KVStore whose table owners name a step.
type WorkflowKVStore struct {
	// Name is the store name; a DNS-1123 label.
	Name ObjectName `json:"name"`
	// Deletion is "retain" (default — the store outlives the workflow) or "delete".
	Deletion DeletionPolicy `json:"deletion,omitempty"`
	// Tables are the store's sub-domains; each Owner names a step.
	Tables []KVTable `json:"tables"`
}

// DeletionPolicy is a workflow-owned store's delete behavior.
type DeletionPolicy string

const (
	// DeletionRetain leaves the store on workflow delete (the default).
	DeletionRetain DeletionPolicy = "retain"
	// DeletionDelete cascades the store on workflow delete.
	DeletionDelete DeletionPolicy = "delete"
)

// Schema carries DeletionPolicy's enum into the generated OpenAPI (ADR-0048).
func (DeletionPolicy) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(DeletionRetain), string(DeletionDelete))
}

// WorkflowContract is a declared or derived I/O contract (ADR-0090 ContractBlob shape).
type WorkflowContract struct {
	Dialect string          `json:"dialect,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// WorkflowStatus is the observed state: phase, the effective contract, the resolved
// step graph (F65 fills it), and the run link (CronJob status.active pattern).
type WorkflowStatus struct {
	Status   `json:",inline"`
	Contract *WorkflowContract    `json:"contract,omitempty"`
	Steps    []WorkflowStepStatus `json:"steps,omitempty"`
	Runs     *WorkflowRunLinks    `json:"runs,omitempty"`
}

// WorkflowStepStatus is one step's resolved digest-pinned image + cached contract.
type WorkflowStepStatus struct {
	Name     ObjectName        `json:"name"`
	Image    string            `json:"image,omitempty"`
	Contract *WorkflowContract `json:"contract,omitempty"`
}

// WorkflowRunLinks links a Workflow to its runs: active (in-flight) names + lifetime
// terminal-phase counts. Bounded (active-only); history lives in engine state.
type WorkflowRunLinks struct {
	Active    []ObjectName `json:"active,omitempty"`
	Succeeded int          `json:"succeeded,omitempty"`
	Failed    int          `json:"failed,omitempty"`
	Cancelled int          `json:"cancelled,omitempty"`
}

// StepFunctionName is the name of the Function an image step materializes into: <workflow>-<step> (ADR-0094).
func StepFunctionName(workflow, step ObjectName) ObjectName {
	return ObjectName(string(workflow) + "-" + string(step))
}

// GroupVersionKind returns the constant GVK for Workflow.
func (w *Workflow) GroupVersionKind() GroupVersionKind { return KindWorkflow.GVK() }

// GetStatus returns the embedded Status for the controller's write-back seam.
func (w *Workflow) GetStatus() *Status { return &w.Status.Status }

// Validate enforces the Workflow rules JSON Schema can't express (ADR-0094): the
// step kind-union, unique step names, each image step's <workflow>-<step> Function
// name fitting a DNS label, dependsOn edge validity + acyclicity, the reserved
// workflow: kind, the onFailure handler constraints, workflow-owned store owners
// naming a step, the declared-contract total-defaults rule, the duration bounds and a
// literal wait's grammar (ADR-0194). Field-format constraints (retry attempts, enums) are
// schema-enforced at the edge.
func (w *Workflow) Validate() error {
	const op = "Workflow.Validate"
	if err := validateMeta(w.TypeMeta, &w.ObjectMeta, KindWorkflow); err != nil {
		return err
	}
	if err := CheckDuration(op, "spec.timeout", w.Spec.Timeout, 0, MaxWorkflowTimeout); err != nil {
		return err
	}
	if len(w.Spec.Steps) == 0 {
		return fault.Invalidf(op, "spec.steps must not be empty")
	}
	names := make(map[ObjectName]bool, len(w.Spec.Steps))
	for i := range w.Spec.Steps {
		s := &w.Spec.Steps[i]
		if s.Name == "" || !dnsLabel.MatchString(string(s.Name)) {
			return fault.Invalidf(op, "spec.steps[%d].name %q is not a valid DNS-1123 label", i, s.Name)
		}
		if names[s.Name] {
			return fault.Invalidf(op, "duplicate step name %q", s.Name)
		}
		names[s.Name] = true
		if err := s.validateKind(op); err != nil {
			return err
		}
		if err := s.validateDurations(op, i); err != nil {
			return err
		}
		if s.Join != "" && s.Join != JoinAll && s.Join != JoinAny {
			return fault.Invalidf(op, "step %q: join must be %q or %q", s.Name, JoinAll, JoinAny)
		}
		if s.Function != nil {
			if err := validatePoolingMode(op, s.Function.Pooling); err != nil {
				return err
			}
			if fn := StepFunctionName(w.Name, s.Name); s.Function.Image != "" && !dnsLabel.MatchString(string(fn)) {
				return fault.Invalidf(op, "step %q: its function name %q (<workflow>-<step>) is longer than 63 bytes", s.Name, fn)
			}
		}
	}
	if err := validatePoolingMode(op, &w.Spec.Pooling); err != nil {
		return err
	}
	// dependsOn edges reference real steps, no self-edge; the graph is acyclic.
	for i := range w.Spec.Steps {
		s := &w.Spec.Steps[i]
		for _, d := range s.DependsOn {
			if d == s.Name {
				return fault.Invalidf(op, "step %q depends on itself", s.Name)
			}
			if !names[d] {
				return fault.Invalidf(op, "step %q depends on unknown step %q", s.Name, d)
			}
		}
	}
	if err := w.validateAcyclic(op); err != nil {
		return err
	}
	if err := w.validateOnFailure(op, names); err != nil {
		return err
	}
	if err := w.validateOwnedStores(op, names); err != nil {
		return err
	}
	if w.Spec.Contract != nil {
		if err := validateTotalDefaults(op, "input", w.Spec.Contract.Input); err != nil {
			return err
		}
		if err := validateTotalDefaults(op, "output", w.Spec.Contract.Output); err != nil {
			return err
		}
	}
	return nil
}

// validateKind enforces the step kind-union (ADR-0096): exactly one of function/builtin; a function
// sets exactly one of image/ref; a builtin sets exactly one of wait/pass; the workflow: kind is
// (a sub-workflow, F70). Dispatch knobs live only on FunctionStep, so a builtin cannot carry
// them by construction — no runtime check needed.
func (s *WorkflowStep) validateKind(op string) error {
	set := 0
	if s.Function != nil {
		set++
		fset := 0
		if s.Function.Image != "" {
			fset++
		}
		if s.Function.Ref != "" {
			fset++
		}
		if fset != 1 {
			return fault.Invalidf(op, "step %q: function must set exactly one of image or ref", s.Name)
		}
	}
	if s.Builtin != nil {
		set++
		bset := 0
		if s.Builtin.Wait != "" {
			bset++
		}
		if s.Builtin.Pass != "" {
			bset++
		}
		if bset != 1 {
			return fault.Invalidf(op, "step %q: builtin must set exactly one of wait or pass", s.Name)
		}
	}
	if s.Workflow != nil { // a sub-workflow step (F70/ADR-0099): targets a child Workflow by name
		set++
		if !dnsLabel.MatchString(string(s.Workflow.Ref)) {
			return fault.Invalidf(op, "step %q: workflow.ref %q is not a valid Workflow name", s.Name, s.Workflow.Ref)
		}
	}
	if set != 1 {
		return fault.Invalidf(op, "step %q must set exactly one of function, builtin, or workflow", s.Name)
	}
	return nil
}

// validateDurations bounds a function step's timeout and retry backoff and parses a literal wait (ADR-0194); a
// ${{ }} wait expression is checked when it is evaluated.
func (s *WorkflowStep) validateDurations(op string, i int) error {
	if f := s.Function; f != nil {
		if err := CheckDuration(op, fmt.Sprintf("spec.steps[%d].function.timeout", i), f.Timeout, 0, MaxStepTimeout); err != nil {
			return err
		}
		if f.Retry != nil {
			if err := CheckDuration(op, fmt.Sprintf("spec.steps[%d].function.retry.backoff", i), f.Retry.Backoff, 0, MaxRetryBackoff); err != nil {
				return err
			}
		}
	}
	if b := s.Builtin; b != nil && b.Wait != "" && !isWaitExpression(b.Wait) {
		if _, err := ParseDuration(b.Wait); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "step %q: builtin.wait", s.Name)
		}
	}
	return nil
}

// isWaitExpression reports whether a builtin wait is a ${{ }} expression rather than a literal duration.
func isWaitExpression(wait string) bool { return strings.HasPrefix(strings.TrimSpace(wait), "${{") }

// EffectiveDependsOn returns each step's parents as the engine schedules them (ADR-0094 Control flow):
// its dependsOn, else the previous step in list order. The onFailure handler is outside the DAG: it gets
// no implicit parent and is never the previous step.
func (s WorkflowSpec) EffectiveDependsOn() map[ObjectName][]ObjectName {
	deps := make(map[ObjectName][]ObjectName, len(s.Steps))
	var prev ObjectName
	for i := range s.Steps {
		st := &s.Steps[i]
		deps[st.Name] = st.DependsOn
		if st.Name == s.OnFailure {
			continue
		}
		if len(st.DependsOn) == 0 && prev != "" {
			deps[st.Name] = []ObjectName{prev}
		}
		prev = st.Name
	}
	return deps
}

// validateAcyclic rejects a cycle in the scheduled graph (EffectiveDependsOn) via DFS: a cycle is
// unbuildable, including one that closes only through an implicit list-order edge.
func (w *Workflow) validateAcyclic(op string) error {
	deps := w.Spec.EffectiveDependsOn()
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[ObjectName]int, len(deps))
	var visit func(n ObjectName) error
	visit = func(n ObjectName) error {
		color[n] = gray
		for _, d := range deps[n] {
			switch color[d] {
			case gray:
				return fault.Invalidf(op, "dependsOn cycle through step %q (a step without dependsOn follows the previous step in list order)", d)
			case white:
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		color[n] = black
		return nil
	}
	for n := range deps {
		if color[n] == white {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateOnFailure checks the handler names a real function step (ADR-0096: the engine dispatches it)
// outside the DAG (no dependsOn/when, and no step depends on it) whose input is the FailureContext alone
// (ADR-0094: no params).
func (w *Workflow) validateOnFailure(op string, names map[ObjectName]bool) error {
	if w.Spec.OnFailure == "" {
		return nil
	}
	if !names[w.Spec.OnFailure] {
		return fault.Invalidf(op, "spec.onFailure %q is not a step", w.Spec.OnFailure)
	}
	for i := range w.Spec.Steps {
		s := &w.Spec.Steps[i]
		if s.Name == w.Spec.OnFailure {
			if s.Function == nil {
				return fault.Invalidf(op, "onFailure handler %q must be a function step (image or ref)", s.Name)
			}
			if len(s.DependsOn) != 0 || s.When != nil {
				return fault.Invalidf(op, "onFailure handler %q must have no dependsOn and no when", s.Name)
			}
			if len(s.Params) != 0 {
				return fault.Invalidf(op, "onFailure handler %q must have no params: its input is the engine's FailureContext", s.Name)
			}
			continue
		}
		for _, d := range s.DependsOn {
			if d == w.Spec.OnFailure {
				return fault.Invalidf(op, "step %q may not depend on the onFailure handler %q", s.Name, d)
			}
		}
	}
	return nil
}

// validateOwnedStores checks each owned store's table owner names a step.
func (w *Workflow) validateOwnedStores(op string, names map[ObjectName]bool) error {
	for i := range w.Spec.KV {
		st := &w.Spec.KV[i]
		if st.Name == "" || !dnsLabel.MatchString(string(st.Name)) {
			return fault.Invalidf(op, "spec.kv[%d].name %q is not a valid DNS-1123 label", i, st.Name)
		}
		if st.Deletion != "" && st.Deletion != DeletionRetain && st.Deletion != DeletionDelete {
			return fault.Invalidf(op, "spec.kv[%d].deletion must be %q or %q", i, DeletionRetain, DeletionDelete)
		}
		for j := range st.Tables {
			if o := st.Tables[j].Owner; o != "" && !names[o] {
				return fault.Invalidf(op, "spec.kv[%d].tables[%d].owner %q is not a step", i, j, o)
			}
		}
	}
	return nil
}

// validatePoolingMode checks a pooling block's Mode is a known value (nil ⇒ ok).
func validatePoolingMode(op string, p *WorkflowPooling) error {
	if p == nil {
		return nil
	}
	if p.Mode != "" && p.Mode != PoolingShared && p.Mode != PoolingIsolated {
		return fault.Invalidf(op, "pooling.mode must be %q or %q", PoolingShared, PoolingIsolated)
	}
	return nil
}

// validateTotalDefaults enforces that every optional property of a declared contract
// schema carries a default — so the workflow boundary is total (ADR-0094).
func validateTotalDefaults(op, which string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fault.Invalidf(op, "spec.contract.%s is not a JSON Schema object: %v", which, err)
	}
	required := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		required[r] = true
	}
	for name, propRaw := range schema.Properties {
		if required[name] {
			continue
		}
		var prop map[string]json.RawMessage
		if err := json.Unmarshal(propRaw, &prop); err != nil {
			return fault.Invalidf(op, "spec.contract.%s.properties.%s is malformed: %v", which, name, err)
		}
		if _, ok := prop["default"]; !ok {
			return fault.Invalidf(op, "spec.contract.%s optional property %q must declare a default (total-defaults rule)", which, name)
		}
	}
	return nil
}
