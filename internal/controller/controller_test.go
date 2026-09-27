package controller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// fakeReconciler is a real (non-mock) reconciler that records calls, can fail its
// first N attempts, return a fixed Result, and run a hook (e.g. status write-back).
type fakeReconciler struct {
	mu        sync.Mutex
	calls     int
	failUntil int
	result    controller.Result
	hook      func(ctx context.Context, req controller.Request)
}

func (f *fakeReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	hook, fail, res := f.hook, n <= f.failUntil, f.result
	f.mu.Unlock()

	if hook != nil {
		hook(ctx, req)
	}
	if fail {
		return controller.Result{}, errors.New("transient failure")
	}
	return res, nil
}

func (f *fakeReconciler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// createObject creates a minimal valid object of the given kind (name in the
// default namespace, resource group rg1) in the store.
func createObject(t *testing.T, st store.Store, kind v1.Kind, name string) {
	t.Helper()
	obj, ok := v1.NewObject(kind)
	require.True(t, ok)
	meta := obj.GetObjectMeta()
	meta.Name = v1.ObjectName(name)
	meta.Namespace = "default"
	meta.ResourceGroup = "rg1"
	_, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
}

// run starts a controller registered for gvk in a goroutine; the returned func
// cancels it and waits for a clean drain. The object is created first so the watch
// snapshot delivers it (deterministic, no watch-startup race).
func run(t *testing.T, st store.Store, gvk v1.GroupVersionKind, r controller.Reconciler) func() {
	t.Helper()
	c, err := controller.New(controller.Deps{Store: st, Workers: 2})
	require.NoError(t, err)
	c.Register(gvk, r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("controller did not drain after cancel")
		}
	}
}

// scenario: reconcile-on-store-change — a stored object drives Reconcile.
func TestScenarioReconcileOnStoreChange(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fr := &fakeReconciler{}
	createObject(t, st, v1.KindConfigMap, "obj1")
	stop := run(t, st, v1.KindConfigMap.GVK(), fr)
	defer stop()

	require.Eventually(t, func() bool { return fr.count() >= 1 },
		3*time.Second, 10*time.Millisecond, "reconciler invoked on store change")
}

// scenario: reconcile-retry-backoff — failures are retried with backoff, then the
// request is forgotten on success (not requeued forever).
func TestScenarioReconcileRetryBackoff(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fr := &fakeReconciler{failUntil: 2} // fail calls 1 and 2, succeed on 3
	createObject(t, st, v1.KindConfigMap, "retry")
	stop := run(t, st, v1.KindConfigMap.GVK(), fr)
	defer stop()

	require.Eventually(t, func() bool { return fr.count() >= 3 },
		3*time.Second, 10*time.Millisecond, "retried until success")
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 3, fr.count(), "forgotten after success — not requeued forever")
}

// scenario: reconcile-requeue-after — Result{RequeueAfter} re-runs after the delay.
func TestScenarioReconcileRequeueAfter(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fr := &fakeReconciler{result: controller.Result{RequeueAfter: 30 * time.Millisecond}}
	createObject(t, st, v1.KindConfigMap, "requeue")
	stop := run(t, st, v1.KindConfigMap.GVK(), fr)
	defer stop()

	require.Eventually(t, func() bool { return fr.count() >= 2 },
		3*time.Second, 10*time.Millisecond, "re-reconciled after RequeueAfter")
}

// scenario: status-writeback — a reconciler's store.Update persists status.
func TestScenarioStatusWriteback(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	fr := &fakeReconciler{hook: func(ctx context.Context, req controller.Request) {
		obj, err := st.Get(ctx, req.GVK, req.Namespace, req.Name)
		if err != nil {
			return
		}
		so, ok := obj.(interface{ GetStatus() *v1.Status })
		if !ok || so.GetStatus().Phase == v1.PhaseReady {
			return // idempotent: already done
		}
		so.GetStatus().Phase = v1.PhaseReady
		_, _ = st.Update(ctx, obj)
	}}
	createObject(t, st, v1.KindFunction, "status")
	stop := run(t, st, v1.KindFunction.GVK(), fr)
	defer stop()

	require.Eventually(t, func() bool {
		obj, err := st.Get(context.Background(), v1.KindFunction.GVK(), "default", "status")
		if err != nil {
			return false
		}
		so, ok := obj.(interface{ GetStatus() *v1.Status })
		return ok && so.GetStatus().Phase == v1.PhaseReady
	}, 3*time.Second, 10*time.Millisecond, "status written back through the store")
}

// scenario: graceful-shutdown — Run drains workers + watches and returns on cancel.
func TestScenarioGracefulShutdown(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	stop := run(t, st, v1.KindConfigMap.GVK(), &fakeReconciler{}) // run() asserts a clean drain within 3s
	stop()
}
