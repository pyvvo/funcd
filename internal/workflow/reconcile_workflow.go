package workflow

import (
	"context"
	"log/slog"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/store"
)

const materializeOp = "workflow.materialize"

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
		if st.Image == "" { // only owned (image) steps are materialized
			continue
		}
		rt, err := m.runtimes.Runtime(ctx, st.Image)
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
		if st.Image == "" || len(st.KV) == 0 {
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
	return v1.ObjectName(string(wf.Name) + "-" + string(step))
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
			Runtime:  rt,
			Artifact: v1.ArtifactRef{URI: st.Image},
			Scaling:  v1.Scaling{MinReplicas: pool.MinReplicas},
			Blob:     st.Blob,
			Secrets:  st.Secrets,
			Config:   st.Config,
			Catalogs: st.Catalogs,
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
	if st.Pooling != nil {
		return *st.Pooling
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
	return &v1.KVStore{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{
			Name: kv.Name, Namespace: wf.Namespace, ResourceGroup: wf.ResourceGroup,
			OwnerReferences: []v1.OwnerReference{owner},
		},
		Spec: v1.KVStoreSpec{Tables: tables},
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
	st.ObjectMeta = cur.ObjectMeta
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
	fn.Spec.KV = st.KV
	if _, err := m.store.Update(ctx, fn); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "patch kv on function %q", name)
	}
	return nil
}
