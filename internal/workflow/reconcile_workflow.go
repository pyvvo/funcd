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
	// contractRequeue is workflow.artifactPollInterval (ADR-0163).
	contractRequeue time.Duration
}

// NewWorkflowReconciler builds the Workflow reconciler over a Materializer + a ContractResolver (the F65
// typed-edge gate, ADR-0098). A nil ContractResolver skips the gate (materialization-only). artifactPollInterval
// re-checks a Workflow whose step artifact is not pushed; 0 ⇒ contractRequeue.
func NewWorkflowReconciler(s store.Store, m *Materializer, contracts ContractResolver, log *slog.Logger, artifactPollInterval time.Duration) *WorkflowReconciler {
	if log == nil {
		log = slog.Default()
	}
	if artifactPollInterval <= 0 {
		artifactPollInterval = contractRequeue
	}
	return &WorkflowReconciler{store: s, mat: m, contracts: contracts, log: log.With("component", "workflow.reconcile"), contractRequeue: artifactPollInterval}
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
		var no *notOwnedError
		if !errors.As(merr, &no) {
			// A failed materialization, a failed strip write included, leaves no Ready Workflow (ADR-0178
			// Decision 2); the returned error requeues with the controller's backoff.
			if uerr := r.notReady(ctx, wf, "MaterializeFailed", merr.Error()); uerr != nil {
				return controller.Result{}, uerr
			}
			return controller.Result{}, merr
		}
		if uerr := r.notReady(ctx, wf, no.reason, no.msg); uerr != nil {
			return controller.Result{}, uerr
		}
		return controller.Result{RequeueAfter: r.mat.supervisionPeriod}, nil
	}
	if r.contracts == nil {
		return controller.Result{}, nil // gate disabled (materialization-only wiring / tests)
	}

	contract, steps, cerr := r.deriveAndCheck(ctx, wf)
	switch {
	case errors.Is(cerr, errArtifactNotReady):
		// A step image is not pushed yet — requeue AFTER a backoff (not a hot loop; each attempt does
		// registry metadata I/O), leaving status untouched so there is no spurious mismatch (ADR-0098).
		return controller.Result{RequeueAfter: r.contractRequeue}, nil
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

// notReady sets wf Pending with Ready False. It writes only on a change, so a failure that repeats does not
// re-enqueue wf through its own watch.
func (r *WorkflowReconciler) notReady(ctx context.Context, wf *v1.Workflow, reason, msg string) error {
	if c, ok := wf.Status.Conditions.Get(condReady); ok && wf.Status.Phase == v1.PhasePending &&
		c.Status == v1.ConditionFalse && c.Reason == reason && c.Message == msg {
		return nil
	}
	wf.Status.Phase = v1.PhasePending
	wf.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: msg})
	if _, err := r.store.Update(ctx, wf); err != nil && fault.KindOf(err) != fault.Conflict {
		return err
	}
	return nil
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
	// supervisionPeriod requeues a Workflow refused a child it does not own (ADR-0170 Decision 4).
	supervisionPeriod time.Duration
}

// NewMaterializer builds the materializer. supervisionPeriod 0 ⇒ controller.SupervisionPeriod.
func NewMaterializer(s store.Store, r RuntimeResolver, log *slog.Logger, supervisionPeriod time.Duration) *Materializer {
	if log == nil {
		log = slog.Default()
	}
	if supervisionPeriod <= 0 {
		supervisionPeriod = controller.SupervisionPeriod
	}
	return &Materializer{store: s, runtimes: r, log: log.With("component", "workflow.materialize"), supervisionPeriod: supervisionPeriod}
}

// notOwnedError refuses a materialization whose child is controlled by another owner, or a step Function a user
// created (ADR-0170 Decision 4): Ready=False with reason, no write, requeued after the supervision period.
type notOwnedError struct{ reason, msg string }

func (e *notOwnedError) Error() string { return e.msg }

// controlledBy reports whether refs hold a controller ref naming wf's kind and name, any UID.
func controlledBy(refs []v1.OwnerReference, wf *v1.Workflow) bool {
	r, ok := v1.ControllerOf(refs)
	return ok && r.Kind == v1.KindWorkflow && r.Name == wf.Name
}

// Materialize brings the workflow's owned resources to the desired state. It first strips every binding of
// a Function this incarnation controls to a store it did not make, then binds a step only to stores this
// incarnation made (ADR-0178 Decision 2).
func (m *Materializer) Materialize(ctx context.Context, wf *v1.Workflow) error {
	kvs := kvView{s: m.store, ns: wf.Namespace, seen: map[v1.ObjectName]*v1.KVStore{}}
	if err := m.stripBindings(ctx, wf, &kvs); err != nil {
		return err
	}
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
		if err := m.ensureFunction(ctx, wf, fn, &kvs); err != nil {
			return err
		}
	}
	// 2. Owned KVStores: every declared and every step-bound store passes the ownership check before any write.
	stores := make([]*v1.KVStore, len(wf.Spec.KV))
	for i := range wf.Spec.KV {
		stores[i] = buildKVStore(wf, &wf.Spec.KV[i], owner)
		if err := m.checkKVStore(ctx, wf, stores[i]); err != nil {
			return err
		}
	}
	if err := checkStepStores(wf); err != nil {
		return err
	}
	checked := make(map[v1.ObjectName]v1.UID, len(stores))
	for _, st := range stores {
		uid, err := m.ensureKVStore(ctx, wf, st)
		if err != nil {
			return err
		}
		checked[st.Name] = uid
	}
	// 3. Write every owned step's kv bindings, an empty one included (the binding IS the capability).
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Function == nil || st.Function.Image == "" {
			continue
		}
		if err := m.patchFunctionKV(ctx, wf, st, checked); err != nil {
			return err
		}
	}
	return m.pruneFunctions(ctx, wf)
}

// kvView caches one reconcile's KVStore reads; a nil entry is a missing store.
type kvView struct {
	s    store.Store
	ns   v1.NamespaceName
	seen map[v1.ObjectName]*v1.KVStore
}

func (v *kvView) get(ctx context.Context, name v1.ObjectName) (*v1.KVStore, error) {
	if st, ok := v.seen[name]; ok {
		return st, nil
	}
	obj, err := v.s.Get(ctx, v1.KindKVStore.GVK(), v.ns, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		v.seen[name] = nil
		return nil, nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), materializeOp, "get kvstore %q", name)
	}
	st := obj.(*v1.KVStore)
	v.seen[name] = st
	return st, nil
}

// ownedBindings returns the entries of kv whose store carries wf's marker.
func (v *kvView) ownedBindings(ctx context.Context, wf *v1.Workflow, kv []v1.FunctionKV) ([]v1.FunctionKV, error) {
	var out []v1.FunctionKV
	for _, b := range kv {
		st, err := v.get(ctx, b.Store)
		if err != nil {
			return nil, err
		}
		if st != nil && marked(st.OwnerReferences, wf) {
			out = append(out, b)
		}
	}
	return out, nil
}

// stripBindings removes, from every Function this incarnation controls (removed steps included), each kv
// binding to a store without this incarnation's marker, each written at its read resourceVersion.
func (m *Materializer) stripBindings(ctx context.Context, wf *v1.Workflow, kvs *kvView) error {
	res, err := m.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: wf.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "list functions")
	}
	for _, obj := range res.Items {
		fn := obj.(*v1.Function)
		if len(fn.Spec.KV) == 0 || !controlledByUID(fn.OwnerReferences, wf) {
			continue
		}
		keep, err := kvs.ownedBindings(ctx, wf, fn.Spec.KV)
		if err != nil {
			return err
		}
		if len(keep) == len(fn.Spec.KV) {
			continue
		}
		fn.Spec.KV = keep
		if _, err := m.store.Update(ctx, fn); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "strip kv bindings of function %q", fn.Name)
		}
	}
	return nil
}

// checkStepStores refuses a step binding to a store wf does not declare in spec.kv.
func checkStepStores(wf *v1.Workflow) error {
	declared := make(map[v1.ObjectName]bool, len(wf.Spec.KV))
	for i := range wf.Spec.KV {
		declared[wf.Spec.KV[i].Name] = true
	}
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Function == nil || st.Function.Image == "" {
			continue
		}
		for _, b := range st.Function.KV {
			if !declared[b.Store] {
				return kvNotOwned(b.Store, wf)
			}
		}
	}
	return nil
}

func kvNotOwned(name v1.ObjectName, wf *v1.Workflow) *notOwnedError {
	return &notOwnedError{reason: "KVStoreNotOwned", msg: fmt.Sprintf("kvstore %q was not made by this incarnation of workflow %q", name, wf.Name)}
}

// pruneFunctions deletes every Function this Workflow controls (kind, name and UID) that is no image step's
// materialized name, each with its resourceVersion (ADR-0170 Decision 5). KVStores are never pruned.
func (m *Materializer) pruneFunctions(ctx context.Context, wf *v1.Workflow) error {
	keep := make(map[v1.ObjectName]bool, len(wf.Spec.Steps))
	for i := range wf.Spec.Steps {
		if st := &wf.Spec.Steps[i]; st.Function != nil && st.Function.Image != "" {
			keep[materializedName(wf, st.Name)] = true
		}
	}
	res, err := m.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: wf.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "list functions")
	}
	for _, obj := range res.Items {
		fm := obj.GetObjectMeta()
		if keep[fm.Name] || !controlledByUID(fm.OwnerReferences, wf) {
			continue
		}
		if err := m.store.Delete(ctx, v1.KindFunction.GVK(), fm.Namespace, fm.Name, fm.ResourceVersion); err != nil && fault.KindOf(err) != fault.NotFound {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "delete removed step function %q", fm.Name)
		}
	}
	return nil
}

// controlledByUID reports whether refs hold a controller ref naming wf's kind, name and UID.
func controlledByUID(refs []v1.OwnerReference, wf *v1.Workflow) bool {
	r, _ := v1.ControllerOf(refs)
	return controlledBy(refs, wf) && r.UID == wf.UID
}

// materializedName is the owned Function's name: <workflow>-<step>.
func materializedName(wf *v1.Workflow, step v1.ObjectName) v1.ObjectName {
	return v1.StepFunctionName(wf.Name, step)
}

func ownerRef(wf *v1.Workflow) v1.OwnerReference {
	return v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: wf.Namespace, Name: wf.Name},
		UID:                wf.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
}

// kvMarker is the non-controller reference naming the Workflow incarnation that made a KVStore (ADR-0178).
func kvMarker(wf *v1.Workflow) v1.OwnerReference {
	r := ownerRef(wf)
	r.Controller, r.BlockOwnerDeletion = false, false
	return r
}

// KVMarker is kvMarker for the control plane's handover, so the marker has one definition.
func KVMarker(wf *v1.Workflow) v1.OwnerReference { return kvMarker(wf) }

// IsKVMarker reports whether r has the shape of a KVStore marker: a non-controller Workflow ref.
func IsKVMarker(r v1.OwnerReference) bool { return !r.Controller && r.Kind == v1.KindWorkflow }

// marked reports whether refs hold a non-controller ref with wf's kind, name and UID.
func marked(refs []v1.OwnerReference, wf *v1.Workflow) bool {
	for _, r := range refs {
		if IsKVMarker(r) && r.Name == wf.Name && r.UID == wf.UID {
			return true
		}
	}
	return false
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
	// deletion policy (ADR-0094): `delete` adds a cascading controller reference so the store is reclaimed
	// with the workflow; `retain` (default) carries only the marker, so the store outlives it (ADR-0178).
	meta.OwnerReferences = []v1.OwnerReference{kvMarker(wf)}
	if kv.Deletion == v1.DeletionDelete {
		meta.OwnerReferences = append(meta.OwnerReferences, owner)
	}
	return &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: meta,
		Spec:       v1.KVStoreSpec{Tables: tables},
	}
}

// ensureFunction creates the Function or updates it if its spec drifted. It keeps only a Function this
// Workflow's kind and name control (any UID) and writes the ownerRefs it built, so a re-created Workflow's
// steps carry its UID (ADR-0170 Decision 4). It keeps only the kv bindings to stores this incarnation made,
// and none of a Function re-stamped from another UID (ADR-0178 Decision 4).
func (m *Materializer) ensureFunction(ctx context.Context, wf *v1.Workflow, fn *v1.Function, kvs *kvView) error {
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
	if !controlledBy(cur.OwnerReferences, wf) {
		return &notOwnedError{reason: "FunctionNotOwned", msg: fmt.Sprintf("function %q exists and is not owned by workflow %q", fn.Name, wf.Name)}
	}
	var kv []v1.FunctionKV
	if controlledByUID(cur.OwnerReferences, wf) {
		if kv, err = kvs.ownedBindings(ctx, wf, cur.Spec.KV); err != nil {
			return err
		}
	}
	owners, group := fn.OwnerReferences, fn.ResourceGroup
	fn.ObjectMeta = cur.ObjectMeta // preserve UID/RV
	fn.OwnerReferences, fn.ResourceGroup = owners, group
	fn.Spec.KV = kv        // kv is applied in the patch phase
	fn.Status = cur.Status // the Function reconciler owns the status, which tracks a redeploy (ADR-0143)
	if _, err := m.store.Update(ctx, fn); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "update function %q", fn.Name)
	}
	return nil
}

// checkKVStore refuses a declared store that exists without this incarnation's marker (ADR-0178 Decision 2).
func (m *Materializer) checkKVStore(ctx context.Context, wf *v1.Workflow, st *v1.KVStore) error {
	existing, err := m.store.Get(ctx, v1.KindKVStore.GVK(), st.Namespace, st.Name)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "get kvstore %q", st.Name)
	}
	if !marked(existing.GetObjectMeta().OwnerReferences, wf) {
		return kvNotOwned(st.Name, wf)
	}
	return nil
}

// ensureKVStore creates the KVStore, or updates one carrying this incarnation's marker at its read
// resourceVersion, and returns the UID it wrote. Any other store is refused (ADR-0178 Decision 2).
func (m *Materializer) ensureKVStore(ctx context.Context, wf *v1.Workflow, st *v1.KVStore) (v1.UID, error) {
	existing, err := m.store.Get(ctx, v1.KindKVStore.GVK(), st.Namespace, st.Name)
	if fault.KindOf(err) == fault.NotFound {
		out, err := m.store.Create(ctx, st)
		if err != nil {
			return "", fault.Wrapf(err, fault.KindOf(err), materializeOp, "create kvstore %q", st.Name)
		}
		return out.GetObjectMeta().UID, nil
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), materializeOp, "get kvstore %q", st.Name)
	}
	cur := existing.(*v1.KVStore)
	if !marked(cur.OwnerReferences, wf) {
		return "", kvNotOwned(st.Name, wf)
	}
	owners, group := st.OwnerReferences, st.ResourceGroup
	st.ObjectMeta = cur.ObjectMeta // preserve UID/RV
	st.OwnerReferences = owners    // re-derived from the current deletion policy (ADR-0094, #149)
	st.ResourceGroup = group       // a retain store counts as a member of its group, so it follows a moved Workflow (#722)
	st.Status = cur.Status         // the KVStore reconciler owns the status (ADR-0073)
	if _, err := m.store.Update(ctx, st); err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), materializeOp, "update kvstore %q", st.Name)
	}
	return cur.UID, nil
}

// patchFunctionKV writes a step's kv bindings after re-reading each bound store: one whose UID is not the
// one checked, or that lost the marker, is refused with no write.
func (m *Materializer) patchFunctionKV(ctx context.Context, wf *v1.Workflow, st *v1.WorkflowStep, checked map[v1.ObjectName]v1.UID) error {
	for _, b := range st.Function.KV {
		obj, err := m.store.Get(ctx, v1.KindKVStore.GVK(), wf.Namespace, b.Store)
		if fault.KindOf(err) == fault.NotFound {
			return kvNotOwned(b.Store, wf)
		}
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), materializeOp, "re-read kvstore %q", b.Store)
		}
		if meta := obj.GetObjectMeta(); meta.UID != checked[b.Store] || !marked(meta.OwnerReferences, wf) {
			return kvNotOwned(b.Store, wf)
		}
	}
	name := materializedName(wf, st.Name)
	obj, err := m.store.Get(ctx, v1.KindFunction.GVK(), wf.Namespace, name)
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "get function %q for kv patch", name)
	}
	fn := obj.(*v1.Function)
	if len(fn.Spec.KV) == 0 && len(st.Function.KV) == 0 {
		return nil
	}
	fn.Spec.KV = st.Function.KV
	if _, err := m.store.Update(ctx, fn); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "patch kv on function %q", name)
	}
	return nil
}

const checkOp = "workflow.contract-check"

// deriveAndCheck resolves every function step's contract from OCI metadata, type-checks the edges (the
// onFailure handler's against the FailureContext) + when: predicates + root merge, and returns the
// derived workflow contract + per-step statuses (ADR-0098).
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
		if len(st.Params) > 0 { // the engine overlays them on any step, a root included (stepInput)
			diffs := objectIntoVoid(child.Input)
			if len(diffs) == 0 {
				diffs = v1.CheckProps(st.Params, child.Input)
			}
			if len(diffs) > 0 {
				return nil, nil, &mismatchError{reason: "EdgeTypeMismatch", msg: fmt.Sprintf("params of step %q: %s", st.Name, v1.FieldDiffs(diffs))}
			}
		}
		if err := checkStepEdges(st, rs.steps[st.Name], child.Input, contracts); err != nil {
			return nil, nil, err
		}
	}
	// The onFailure handler's producer is the engine's FailureContext, without a params overlay (ADR-0094).
	if hc, ok := contracts[wf.Spec.OnFailure]; ok {
		if diffs := checkEdge(failureContextSchema(), hc.Input, nil); len(diffs) > 0 {
			return nil, nil, &mismatchError{reason: "EdgeTypeMismatch", msg: fmt.Sprintf("FailureContext into onFailure handler %q: %s", wf.Spec.OnFailure, v1.FieldDiffs(diffs))}
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
	//    (ADR-0095 Condition mode): a bad path or type, or a read of a join: any branch that may be skipped
	//    without its guard (ADR-0166), fails here. Only these still fail at run time: builtin pass/wait
	//    expressions (ADR-0096), a nested probe under an optional field, a field read under a null-typed
	//    root, and an unguarded optional field under a guarded required root or field (ADR-0166 Scope).
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

// producerCase is one input a step can receive from its parents.
type producerCase struct {
	schema     json.RawMessage
	onlyBranch v1.ObjectName // join: any: the one branch that ran; "" otherwise
}

// producerSchemas lists the inputs a step's parents can send it, as the engine builds them (flowingInput):
// none for a root or a lone untyped parent; a lone typed parent's output verbatim; for two or more parents,
// typed or not, the composite keyed by parent name — every key required for join: all, and for join: any
// one composite per parent, since adding a branch to the survivors only adds keys (ADR-0166 Decision 3).
func producerSchemas(n *stepNode, contracts map[v1.ObjectName]v1.WorkflowContract) []producerCase {
	switch len(n.dependsOn) {
	case 0:
		return nil
	case 1:
		c, ok := contracts[n.dependsOn[0]]
		if !ok {
			return nil
		}
		return []producerCase{{schema: c.Output}}
	}
	if effectiveJoin(n.join) != v1.JoinAny {
		return []producerCase{{schema: compositeSchema(n.dependsOn)}}
	}
	cases := make([]producerCase, 0, len(n.dependsOn))
	for _, p := range n.dependsOn {
		cases = append(cases, producerCase{schema: compositeSchema([]v1.ObjectName{p}), onlyBranch: p})
	}
	return cases
}

// checkStepEdges type-checks every input a step's parents can send it against its input schema.
func checkStepEdges(st *v1.WorkflowStep, n *stepNode, input json.RawMessage, contracts map[v1.ObjectName]v1.WorkflowContract) error {
	for _, pc := range producerSchemas(n, contracts) {
		diffs := checkEdge(pc.schema, input, paramsKeys(st))
		if len(diffs) == 0 {
			continue
		}
		ran := ""
		if pc.onlyBranch != "" {
			ran = fmt.Sprintf(" when only %q ran (join: any)", pc.onlyBranch)
		}
		return &mismatchError{reason: "EdgeTypeMismatch", msg: fmt.Sprintf("edge into step %q%s: %s", st.Name, ran, v1.FieldDiffs(diffs))}
	}
	return nil
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
