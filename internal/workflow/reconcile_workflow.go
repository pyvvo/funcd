package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

const materializeOp = "workflow.materialize"

// F65 (ADR-0098) conditions + sentinels for the typed-edge contract-check gate.
const (
	condReady          v1.ConditionType = "Ready"
	condSchemaMismatch v1.ConditionType = "SchemaMismatch"
)

// errArtifactNotReady signals a step image not yet in the registry — the reconciler requeues (the
// CatalogNotReady pattern) rather than declaring a mismatch (ADR-0098; no apply-order trap).
var errArtifactNotReady = errors.New("workflow: a step artifact is not yet pushed")

// contractRequeue backs off the not-ready requeue so a missing artifact doesn't hot-loop the reconciler
// (each attempt does registry metadata I/O).
const contractRequeue = 5 * time.Second

// mismatchError is a typed-edge / root / when failure — a reconcile-time SchemaMismatch (not requeued).
type mismatchError struct {
	reason string // EdgeTypeMismatch · RootSchemaConflict · WhenTypeError
	msg    string
}

func (e *mismatchError) Error() string { return e.msg }

// ContractResolver reads a step image's I/O contract from OCI metadata (ADR-0059/0098), mirroring
// RuntimeResolver. Production wraps artifact.InspectContract; tests inject a fake. NotFound ⇒ not pushed.
type ContractResolver interface {
	Contract(ctx context.Context, image string) (contract v1.WorkflowContract, digest string, err error)
}

// materializedHandler is the exported entrypoint a materialized step Function declares — the funcd
// convention and the nodejs/python shim's FUNCD_HANDLER default (ADR-0094).
const materializedHandler = "handle"

// stepIdleTimeout is the idle reclaim delay of a materialized step Function, so minReplicas 0
// scales it to zero between runs (ADR-0094); the blueprint's example idleTimeout. The workflow
// spec has no idle-timeout field, and a Function with idleTimeout 0 is never reclaimed (ADR-0016).
const stepIdleTimeout = 5 * time.Minute

// WorkflowReconciler is the controller.Reconciler for the Workflow kind: it brings a
// Workflow's owned step Functions and KVStores to the desired state (the Deployment→
// ReplicaSet analogy, ADR-0094) via the Materializer. Run execution is the RunReconciler's
// job; this reconciler only maintains the materialized fleet.
type WorkflowReconciler struct {
	store     store.Store
	mat       *Materializer
	contracts ContractResolver // F65: reads step I/O contracts from OCI metadata (nil ⇒ the gate is skipped)
	log       *slog.Logger
}

// NewWorkflowReconciler builds the Workflow reconciler over a Materializer + a ContractResolver (the F65
// typed-edge gate, ADR-0098). A nil ContractResolver skips the gate (materialization-only).
func NewWorkflowReconciler(s store.Store, m *Materializer, contracts ContractResolver, log *slog.Logger) *WorkflowReconciler {
	if log == nil {
		log = slog.Default()
	}
	return &WorkflowReconciler{store: s, mat: m, contracts: contracts, log: log.With("component", "workflow.reconcile")}
}

// Reconcile materializes one Workflow's owned resources, then runs the F65 typed-edge contract gate:
// resolve each step's contract from OCI metadata, type-check the edges + when: predicates, derive +
// cache the graph in status, and set Ready / SchemaMismatch (ADR-0098).
func (r *WorkflowReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil // deleted; owned resources cascade via ownerRefs
	}
	if err != nil {
		return controller.Result{}, err
	}
	wf := obj.(*v1.Workflow)
	if merr := r.mat.Materialize(ctx, wf); merr != nil {
		return controller.Result{}, merr
	}
	if r.contracts == nil {
		return controller.Result{}, nil // gate disabled (materialization-only wiring / tests)
	}

	contract, steps, cerr := r.deriveAndCheck(ctx, wf)
	switch {
	case errors.Is(cerr, errArtifactNotReady):
		// A step image is not pushed yet — requeue AFTER a backoff (not a hot loop; each attempt does
		// registry metadata I/O), leaving status untouched so there is no spurious mismatch (ADR-0098).
		return controller.Result{RequeueAfter: contractRequeue}, nil
	case cerr != nil:
		var mm *mismatchError
		if !errors.As(cerr, &mm) {
			return controller.Result{}, cerr // an infra error — requeue via the controller
		}
		wf.Status.Contract, wf.Status.Steps = contract, steps // cache what resolved
		wf.Status.Phase = v1.PhasePending
		wf.Status.Conditions.Set(v1.Condition{Type: condSchemaMismatch, Status: v1.ConditionTrue, Reason: mm.reason, Message: mm.msg})
		wf.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: mm.reason, Message: mm.msg})
	default:
		wf.Status.Contract, wf.Status.Steps = contract, steps
		wf.Status.Phase = v1.PhaseReady
		wf.Status.Conditions.Set(v1.Condition{Type: condSchemaMismatch, Status: v1.ConditionFalse, Reason: "EdgesTypeChecked"})
		wf.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "EdgesTypeChecked"})
	}
	if _, uerr := r.store.Update(ctx, wf); uerr != nil {
		return controller.Result{}, uerr
	}
	return controller.Result{}, nil
}

// RuntimeResolver resolves a step image's runtime class from its OCI manifest
// (the dev.funcd.runtime.v1 annotation, ADR-0059 family). Production reads it via
// internal/artifact.Inspect; tests inject a fake.
type RuntimeResolver interface {
	Runtime(ctx context.Context, image string) (v1.RuntimeName, error)
}

// Materializer creates and updates a Workflow's owned step Functions and KVStores
// (the Deployment→ReplicaSet analogy, ADR-0094). It owns the ADR-0073 apply-order
// cycle internally: Functions without kv → KVStores → patch Functions with kv.
type Materializer struct {
	store    store.Store
	runtimes RuntimeResolver
	log      *slog.Logger
}

// NewMaterializer builds the materializer.
func NewMaterializer(s store.Store, r RuntimeResolver, log *slog.Logger) *Materializer {
	if log == nil {
		log = slog.Default()
	}
	return &Materializer{store: s, runtimes: r, log: log.With("component", "workflow.materialize")}
}

// Materialize brings the workflow's owned resources to the desired state.
func (m *Materializer) Materialize(ctx context.Context, wf *v1.Workflow) error {
	owner := ownerRef(wf)
	// 1. Owned Functions (without kv, so the store owners exist before the KVStores).
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Function == nil || st.Function.Image == "" { // only owned (function.image) steps are materialized
			continue
		}
		rt, err := m.runtimes.Runtime(ctx, st.Function.Image)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "resolve runtime for step %q", st.Name)
		}
		fn := buildFunction(wf, st, rt, owner)
		if err := m.ensureFunction(ctx, fn); err != nil {
			return err
		}
	}
	// 2. Owned KVStores (owner = the materialized step Function).
	for i := range wf.Spec.KV {
		kv := &wf.Spec.KV[i]
		st := buildKVStore(wf, kv, owner)
		if err := m.ensureKVStore(ctx, st); err != nil {
			return err
		}
	}
	// 3. Patch owned Functions that carry kv bindings (the binding IS the capability).
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Function == nil || st.Function.Image == "" || len(st.Function.KV) == 0 {
			continue
		}
		if err := m.patchFunctionKV(ctx, wf, st); err != nil {
			return err
		}
	}
	return nil
}

// materializedName is the owned Function's name: <workflow>-<step>.
func materializedName(wf *v1.Workflow, step v1.ObjectName) v1.ObjectName {
	return materializedStepName(wf.Name, step)
}

// materializedStepName is the owned Function's name for a step, given the workflow name:
// <workflow>-<step>. The engine uses it to resolve an image step's dispatch target without
// the Workflow object in hand.
func materializedStepName(workflow, step v1.ObjectName) v1.ObjectName {
	return v1.ObjectName(string(workflow) + "-" + string(step))
}

func ownerRef(wf *v1.Workflow) v1.OwnerReference {
	return v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: wf.Namespace, Name: wf.Name},
		UID:                wf.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
}

// buildFunction constructs an owned Function for a step (without kv — added in the
// patch phase). Pooling and scaling come from the effective pooling policy.
func buildFunction(wf *v1.Workflow, st *v1.WorkflowStep, rt v1.RuntimeName, owner v1.OwnerReference) *v1.Function {
	pool := effectivePooling(wf, st)
	fn := &v1.Function{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{
			Name: materializedName(wf, st.Name), Namespace: wf.Namespace, ResourceGroup: wf.ResourceGroup,
			OwnerReferences: []v1.OwnerReference{owner},
		},
		Spec: v1.FunctionSpec{
			Runtime: rt,
			// Handler is the exported entrypoint the shim resolves (ADR-0094: materialization
			// supplies runtime + handler). funcd's convention (and the shim's FUNCD_HANDLER default)
			// is the `handle` export; a materialized step function carries it so it passes shape
			// validation and serves without the author restating it on every step.
			Handler:  materializedHandler,
			Image:    st.Function.Image,
			Scaling:  v1.Scaling{MinReplicas: pool.MinReplicas, IdleTimeout: stepIdleTimeout},
			Blob:     st.Function.Blob,
			Secrets:  st.Function.Secrets,
			Config:   st.Function.Config,
			Catalogs: st.Function.Catalogs,
		},
	}
	if pool.Mode != v1.PoolingIsolated { // shared (default): co-locate under a pool worker
		worker := pool.Worker
		if worker == "" {
			worker = string(wf.Name)
		}
		fn.Spec.Pooling = v1.Pooling{Worker: worker}
	}
	return fn
}

// effectivePooling resolves a step's pooling: its own override, else the workflow default.
func effectivePooling(wf *v1.Workflow, st *v1.WorkflowStep) v1.WorkflowPooling {
	if st.Function != nil && st.Function.Pooling != nil {
		return *st.Function.Pooling
	}
	return wf.Spec.Pooling
}

func buildKVStore(wf *v1.Workflow, kv *v1.WorkflowKVStore, owner v1.OwnerReference) *v1.KVStore {
	tables := make([]v1.KVTable, len(kv.Tables))
	for i, t := range kv.Tables {
		tables[i] = t
		if t.Owner != "" { // owner is a step name → the materialized Function name
			tables[i].Owner = materializedName(wf, t.Owner)
		}
	}
	meta := v1.ObjectMeta{Name: kv.Name, Namespace: wf.Namespace, ResourceGroup: wf.ResourceGroup}
	// deletion policy (ADR-0094): `delete` attaches a cascading owner reference so the store is
	// reclaimed with the workflow; `retain` (default) attaches none, so the store outlives it.
	if kv.Deletion == v1.DeletionDelete {
		meta.OwnerReferences = []v1.OwnerReference{owner}
	}
	return &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: meta,
		Spec:       v1.KVStoreSpec{Tables: tables},
	}
}

// ensureFunction creates the Function or updates it if its spec drifted.
func (m *Materializer) ensureFunction(ctx context.Context, fn *v1.Function) error {
	existing, err := m.store.Get(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name)
	if fault.KindOf(err) == fault.NotFound {
		if _, err := m.store.Create(ctx, fn); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "create function %q", fn.Name)
		}
		return nil
	}
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "get function %q", fn.Name)
	}
	cur := existing.(*v1.Function)
	fn.ObjectMeta = cur.ObjectMeta // preserve UID/RV; keep our owner + spec
	fn.Spec.KV = cur.Spec.KV       // kv is applied in the patch phase
	fn.Status = cur.Status         // the Function reconciler owns the status, which tracks a redeploy (ADR-0143)
	if _, err := m.store.Update(ctx, fn); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "update function %q", fn.Name)
	}
	return nil
}

func (m *Materializer) ensureKVStore(ctx context.Context, st *v1.KVStore) error {
	existing, err := m.store.Get(ctx, v1.KindKVStore.GVK(), st.Namespace, st.Name)
	if fault.KindOf(err) == fault.NotFound {
		if _, err := m.store.Create(ctx, st); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "create kvstore %q", st.Name)
		}
		return nil
	}
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "get kvstore %q", st.Name)
	}
	cur := existing.(*v1.KVStore)
	owners := st.OwnerReferences
	st.ObjectMeta = cur.ObjectMeta // preserve UID/RV
	st.OwnerReferences = owners    // re-derived from the current deletion policy (ADR-0094)
	st.Status = cur.Status         // the KVStore reconciler owns the status (ADR-0073)
	if _, err := m.store.Update(ctx, st); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "update kvstore %q", st.Name)
	}
	return nil
}

// patchFunctionKV adds a step's kv bindings after its owned store exists.
func (m *Materializer) patchFunctionKV(ctx context.Context, wf *v1.Workflow, st *v1.WorkflowStep) error {
	name := materializedName(wf, st.Name)
	obj, err := m.store.Get(ctx, v1.KindFunction.GVK(), wf.Namespace, name)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "get function %q for kv patch", name)
	}
	fn := obj.(*v1.Function)
	fn.Spec.KV = st.Function.KV
	if _, err := m.store.Update(ctx, fn); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "patch kv on function %q", name)
	}
	return nil
}

const checkOp = "workflow.contract-check"

// deriveAndCheck resolves every function step's contract from OCI metadata, type-checks the edges +
// when: predicates + root merge, and returns the derived workflow contract + per-step statuses (ADR-0098).
// A not-yet-pushed image ⇒ errArtifactNotReady (requeue); a typing failure ⇒ *mismatchError.
func (r *WorkflowReconciler) deriveAndCheck(ctx context.Context, wf *v1.Workflow) (*v1.WorkflowContract, []v1.WorkflowStepStatus, error) {
	rs := newRunState(wf.Spec)
	contracts := map[v1.ObjectName]v1.WorkflowContract{}
	var stepStatuses []v1.WorkflowStepStatus

	// 0. Reject a sub-workflow reference cycle (ADR-0099) before resolving — a cycle can never run.
	if cyc, cerr := r.formsCycle(ctx, wf.Namespace, wf.Name); cerr != nil {
		return nil, nil, cerr
	} else if cyc {
		return nil, nil, &mismatchError{reason: "WorkflowCycle", msg: fmt.Sprintf("workflow %q transitively references itself via a sub-workflow step", wf.Name)}
	}

	// 1. Resolve each step's contract: a function step from OCI metadata (never the bytes); a sub-workflow
	//    step from the child workflow's cached status.contract. Builtins are untyped.
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Workflow != nil { // a sub-workflow step's contract IS the child's derived status.contract (F65)
			childC, cerr := r.childContract(ctx, wf.Namespace, st.Workflow.Ref)
			if cerr != nil {
				return nil, nil, cerr // child absent or not-Ready ⇒ errArtifactNotReady (requeue)
			}
			contracts[st.Name] = childC
			cc := childC
			stepStatuses = append(stepStatuses, v1.WorkflowStepStatus{Name: st.Name, Image: "workflow://" + string(st.Workflow.Ref), Contract: &cc})
			continue
		}
		image, ok, err := r.stepImage(ctx, wf, st)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		c, digest, cerr := r.contracts.Contract(ctx, image)
		if fault.KindOf(cerr) == fault.NotFound {
			return nil, nil, errArtifactNotReady
		}
		if cerr != nil {
			return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), checkOp, "resolve contract for step %q", st.Name)
		}
		contracts[st.Name] = c
		pinned := image
		if digest != "" {
			pinned = image + "@" + digest
		}
		cc := c
		stepStatuses = append(stepStatuses, v1.WorkflowStepStatus{Name: st.Name, Image: pinned, Contract: &cc})
	}

	// 2. Type-check every edge (required-primitive subsumption + void + fan-in composite + params).
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		child, ok := contracts[st.Name]
		if !ok {
			continue
		}
		producer, has := producerSchema(rs.steps[st.Name], contracts)
		if !has {
			continue // a root, or all parents untyped
		}
		if diffs := checkEdge(producer, child.Input, paramsKeys(st)); len(diffs) > 0 {
			return nil, nil, &mismatchError{reason: "EdgeTypeMismatch", msg: fmt.Sprintf("edge into step %q: %s", st.Name, v1.FieldDiffs(diffs))}
		}
	}

	// 3. Derive the workflow contract (root-combined input + leaf output/composite).
	wc, derr := deriveWorkflowContract(rs, contracts)
	if derr != nil {
		var sc *schemaConflict
		if errors.As(derr, &sc) {
			return nil, nil, &mismatchError{reason: "RootSchemaConflict", msg: sc.Error()}
		}
		return nil, nil, derr
	}

	// 4. Type-check when: predicates against the parents' cached output schemas + the derived input
	//    (ADR-0095 Condition mode) — a bad path/type fails here, never at runtime.
	if err := checkWhenConditions(wf.Spec, rs, contracts, wc.Input); err != nil {
		return nil, nil, err
	}
	return &wc, stepStatuses, nil
}

// stepImage returns the OCI ref whose contract types a step: an owned image directly, or a function-ref's
// referenced Function image (store lookup). A builtin/workflow step, or a ref not present yet, is untyped.
func (r *WorkflowReconciler) stepImage(ctx context.Context, wf *v1.Workflow, st *v1.WorkflowStep) (string, bool, error) {
	if st.Function == nil {
		return "", false, nil
	}
	if st.Function.Image != "" {
		return st.Function.Image, true, nil
	}
	if st.Function.Ref != "" {
		obj, err := r.store.Get(ctx, v1.KindFunction.GVK(), wf.Namespace, st.Function.Ref)
		if fault.KindOf(err) == fault.NotFound {
			return "", false, nil // the referenced Function isn't present yet — untyped this round
		}
		if err != nil {
			return "", false, err
		}
		return obj.(*v1.Function).Spec.Image, true, nil
	}
	return "", false, nil
}

// childContract returns a sub-workflow step's contract — the referenced child Workflow's cached
// status.contract, read from the store (ADR-0099; the ChildResolver is the engine's execution seam, not
// reconcile's). An absent or not-yet-Ready child ⇒ errArtifactNotReady (requeue, the CatalogNotReady
// pattern), so a sub-workflow that hasn't been type-checked yet defers, it isn't a mismatch.
func (r *WorkflowReconciler) childContract(ctx context.Context, ns v1.NamespaceName, child v1.ObjectName) (v1.WorkflowContract, error) {
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, child)
	if fault.KindOf(err) == fault.NotFound {
		return v1.WorkflowContract{}, errArtifactNotReady // the child workflow isn't applied yet
	}
	if err != nil {
		return v1.WorkflowContract{}, err
	}
	cw := obj.(*v1.Workflow)
	if cw.Status.Contract == nil {
		return v1.WorkflowContract{}, errArtifactNotReady // the child hasn't derived its contract yet (not Ready)
	}
	return *cw.Status.Contract, nil
}

// formsCycle reports whether `start` transitively references itself through `workflow:` steps (ADR-0099).
// A visited-set bounds the walk; an absent child is not a cycle (it defers via childContract's requeue).
func (r *WorkflowReconciler) formsCycle(ctx context.Context, ns v1.NamespaceName, start v1.ObjectName) (bool, error) {
	seen := map[v1.ObjectName]bool{}
	var reaches func(name v1.ObjectName) (bool, error)
	reaches = func(name v1.ObjectName) (bool, error) {
		obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, name)
		if fault.KindOf(err) == fault.NotFound {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for i := range obj.(*v1.Workflow).Spec.Steps {
			st := &obj.(*v1.Workflow).Spec.Steps[i]
			if st.Workflow == nil {
				continue
			}
			if st.Workflow.Ref == start {
				return true, nil // reaches start again ⇒ a cycle through start
			}
			if seen[st.Workflow.Ref] {
				continue
			}
			seen[st.Workflow.Ref] = true
			if cyc, err := reaches(st.Workflow.Ref); err != nil || cyc {
				return cyc, err
			}
		}
		return false, nil
	}
	return reaches(start)
}

// producerSchema is the output schema a step's parents present to it: a single typed parent's output
// verbatim, or the fan-in composite keyed by parent name. false ⇒ no typed parent (a root).
func producerSchema(n *stepNode, contracts map[v1.ObjectName]v1.WorkflowContract) (json.RawMessage, bool) {
	var typed []v1.ObjectName
	for _, p := range n.dependsOn {
		if _, ok := contracts[p]; ok {
			typed = append(typed, p)
		}
	}
	switch len(typed) {
	case 0:
		return nil, false
	case 1:
		return contracts[typed[0]].Output, true
	default:
		outs := make(map[v1.ObjectName]json.RawMessage, len(typed))
		for _, p := range typed {
			outs[p] = contracts[p].Output
		}
		return compositeSchema(outs), true
	}
}

// paramsKeys is the set of top-level fields a step's spec.params supplies (not required from a parent).
func paramsKeys(st *v1.WorkflowStep) map[string]bool {
	if len(st.Params) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(st.Params, &m) != nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
