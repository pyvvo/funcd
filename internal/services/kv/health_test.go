package kv_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/health"
	"github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// noEngine is a KV engine that fails the test on any call.
type noEngine struct{ t *testing.T }

func (e noEngine) Get(context.Context, string) ([]byte, bool, error) {
	e.t.Error("CheckRead must not call the engine")
	return nil, false, nil
}

func (e noEngine) Put(context.Context, string, []byte) error {
	e.t.Error("CheckRead must not call the engine")
	return nil
}

func (e noEngine) Delete(context.Context, string) error {
	e.t.Error("CheckRead must not call the engine")
	return nil
}

func (e noEngine) List(context.Context, string) ([]string, error) {
	e.t.Error("CheckRead must not call the engine")
	return nil, nil
}

// CheckRead resolves the alias and asks kv::read on its table, as Get does, with no engine call (ADR-0215 Decision 3).
func TestFacadeCheckRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := fakeResolver{m: map[string]kv.Binding{bkey("todo-api", "store"): {Store: "todo-store", Table: "todos"}}}
	pdp := &pdpStub{readGrants: map[string]bool{rgKey("default", "todo-api", "todo-store"): true}}
	f, err := kv.NewFacade(kv.FacadeDeps{KV: noEngine{t}, Resolver: b, Authorizer: pdp})
	require.NoError(t, err)

	require.NoError(t, f.CheckRead(ctx, "default", "todo-api", "store"))
	require.Equal(t, auth.ActionKVRead, pdp.last.Action)
	require.Equal(t, fault.Forbidden, fault.KindOf(f.CheckRead(ctx, "default", "todo-api", "audit")), "an unbound alias")

	pdp.readGrants = map[string]bool{}
	require.Equal(t, fault.Forbidden, fault.KindOf(f.CheckRead(ctx, "default", "todo-api", "store")), "no permitting Policy")
}

// probe is a storage probe whose answer a test sets.
type probe struct{ err atomic.Pointer[error] }

func (p *probe) fail(err error) { p.err.Store(&err) }
func (p *probe) pass()          { p.err.Store(nil) }
func (p *probe) run(context.Context) error {
	if e := p.err.Load(); e != nil {
		return *e
	}
	return nil
}

// probeOnce runs one probe of every target of p, as one interval of the running prober does.
func probeOnce(p *health.Prober) {
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	cancel()
}

// ADR-0215 Decision 7 (health-storage-down, the KVStore half): a KVStore is Ready while the KV probe passes and
// Degraded, Ready=False StorageUnreachable, while it fails, at its generation, its counts unchanged; a pass whose
// result did not change writes nothing and no pass requeues.
func TestKVStoreStatusFollowsTheKVProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkKVStore("todo-store", v1.KVTable{Name: "todos", Owner: "todo-api"}))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkFunctionWithKV("todo-api", v1.FunctionKV{Alias: "store", Store: "todo-store", Table: "todos"}))
	require.NoError(t, err)
	kvProbe := &probe{}
	prober, err := health.NewProber(nil, time.Hour, time.Second, map[health.Target]health.Probe{health.TargetKV: kvProbe.run}, nil)
	require.NoError(t, err)
	probeOnce(prober)
	r, err := kv.NewReconciler(kv.ReconcilerDeps{Store: st, Health: prober})
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "todo-store"}
	pass := func() *v1.KVStore {
		t.Helper()
		res, rerr := r.Reconcile(ctx, req)
		require.NoError(t, rerr)
		require.Zero(t, res.RequeueAfter, "the prober enqueues the stores on a flip; a pass never requeues")
		obj, gerr := st.Get(ctx, v1.KindKVStore.GVK(), "default", "todo-store")
		require.NoError(t, gerr)
		return obj.(*v1.KVStore)
	}
	requireReady := func(ks *v1.KVStore, status v1.ConditionStatus, reason, msg string) {
		t.Helper()
		c, ok := ks.Status.Conditions.Get("Ready")
		require.True(t, ok)
		require.Equal(t, status, c.Status)
		require.Equal(t, reason, c.Reason)
		require.Equal(t, msg, c.Message)
		require.Equal(t, ks.Generation, c.ObservedGeneration)
		require.Equal(t, ks.Generation, ks.Status.ObservedGeneration)
		require.Equal(t, 1, ks.Status.Tables)
		require.Equal(t, 1, ks.Status.Bindings)
	}

	ks := pass()
	require.Equal(t, v1.PhaseReady, ks.Status.Phase)
	requireReady(ks, v1.ConditionTrue, "", "")
	require.Equal(t, ks.ResourceVersion, pass().ResourceVersion, "an unchanged result writes no status")

	kvProbe.fail(errors.New("badger: the engine is closed"))
	probeOnce(prober)
	ks = pass()
	require.Equal(t, v1.PhaseDegraded, ks.Status.Phase)
	requireReady(ks, v1.ConditionFalse, "StorageUnreachable", "badger: the engine is closed")
	require.Equal(t, ks.ResourceVersion, pass().ResourceVersion, "still failing: no write")

	kvProbe.pass()
	probeOnce(prober)
	ks = pass()
	require.Equal(t, v1.PhaseReady, ks.Status.Phase)
	requireReady(ks, v1.ConditionTrue, "", "")
}
