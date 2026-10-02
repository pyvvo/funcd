package controller_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
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

// run starts a controller registered for gvk (then set up by configure) in a goroutine; the returned func
// cancels it and waits for a clean drain. The object is created first so the watch
// snapshot delivers it (deterministic, no watch-startup race).
func run(t *testing.T, st store.Store, gvk v1.GroupVersionKind, r controller.Reconciler, configure ...func(*controller.Controller)) func() {
	t.Helper()
	c, err := controller.New(controller.Deps{Store: st, Workers: 2})
	require.NoError(t, err)
	c.Register(gvk, r)
	for _, fn := range configure {
		fn(c)
	}

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

// scenario: requeue-does-not-multiply (ADR-0142) — a reconciler that always asks to run again after a period,
// with extra watch events for its object, still runs about once per period plus once per event: the queue keeps one
// pending delay per key, so each event does not start another periodic chain.
func TestScenarioRequeueDoesNotMultiply(t *testing.T) {
	t.Parallel()
	const period = 50 * time.Millisecond
	st := store.New(memory.New())
	fr := &fakeReconciler{result: controller.Result{RequeueAfter: period}}
	createObject(t, st, v1.KindConfigMap, "steady")
	stop := run(t, st, v1.KindConfigMap.GVK(), fr)
	defer stop()

	ctx := context.Background()
	for i := range 5 {
		obj, err := st.Get(ctx, v1.KindConfigMap.GVK(), "default", "steady")
		require.NoError(t, err)
		cm := obj.(*v1.ConfigMap)
		cm.Spec.Data = map[string]string{"n": strconv.Itoa(i)}
		_, err = st.Update(ctx, cm)
		require.NoError(t, err)
		time.Sleep(period / 5)
	}

	before := fr.count()
	time.Sleep(20 * period)
	passes := fr.count() - before
	require.GreaterOrEqual(t, passes, 10, "it keeps requeueing every period")
	require.Less(t, passes, 35, "about one pass per period — the extra events did not start more chains")
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

// pausedStore lets a test stop the controller from draining its watches: while hold is locked, each
// watch's forwarder blocks, the store's per-watcher buffer fills, and the store drops the watcher
// (closes its stream) exactly as it does for a watcher that falls behind a burst of writes.
type pausedStore struct {
	store.Store
	hold sync.Mutex
}

func (p *pausedStore) Watch(ctx context.Context, gvk v1.GroupVersionKind, opts store.WatchOptions) (store.Watch, error) {
	w, err := p.Store.Watch(ctx, gvk, opts)
	if err != nil {
		return nil, err
	}
	pw := &pausedWatch{Watch: w, out: make(chan store.Event), done: make(chan struct{})}
	go pw.forward(&p.hold)
	return pw, nil
}

type pausedWatch struct {
	store.Watch
	out  chan store.Event
	done chan struct{}
	once sync.Once
}

func (w *pausedWatch) ResultChan() <-chan store.Event { return w.out }

func (w *pausedWatch) Stop() {
	w.once.Do(func() { close(w.done) })
	w.Watch.Stop()
}

func (w *pausedWatch) forward(hold *sync.Mutex) {
	defer close(w.out)
	for ev := range w.Watch.ResultChan() {
		hold.Lock()
		select {
		case w.out <- ev:
		case <-w.done:
		}
		hold.Unlock()
	}
}

// presence records, per object name, whether a reconcile found the object or saw it as NotFound.
type presence struct {
	mu          sync.Mutex
	found, gone map[string]bool
}

// presenceReconciler returns a reconciler that records in the returned presence what each reconcile reads
// from st.
func presenceReconciler(st store.Store) (*fakeReconciler, *presence) {
	p := &presence{found: map[string]bool{}, gone: map[string]bool{}}
	return &fakeReconciler{hook: func(ctx context.Context, req controller.Request) {
		_, err := st.Get(ctx, req.GVK, req.Namespace, req.Name)
		p.mu.Lock()
		defer p.mu.Unlock()
		if fault.KindOf(err) == fault.NotFound {
			p.gone[string(req.Name)] = true
			return
		}
		p.found[string(req.Name)] = true
	}}, p
}

// missing counts the names not yet reconciled as found, or as NotFound when gone is set.
func (p *presence) missing(gone bool, names ...string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := p.found
	if gone {
		seen = p.gone
	}
	n := 0
	for _, name := range names {
		if !seen[name] {
			n++
		}
	}
	return n
}

// Issue #25: when the store drops the controller's watch (a burst outran its buffer), the controller
// re-watches, so every object written during and after the burst is reconciled, and so is a delete made
// during the burst: it resumes after the last revision it saw, or re-lists when the burst outgrew the
// store's replay ring (1024 events).
func TestIssue25_ReconcilesAfterWatchDrop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		burst int
	}{
		{name: "resumes after the last revision", burst: 100},
		{name: "re-lists when the revision is no longer retained", burst: 1100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			paused := &pausedStore{Store: st}
			fr, seen := presenceReconciler(st)
			createObject(t, st, v1.KindConfigMap, "doomed")
			stop := run(t, paused, v1.KindConfigMap.GVK(), fr)
			defer stop()

			require.Eventually(t, func() bool { return seen.missing(false, "doomed") == 0 },
				3*time.Second, 10*time.Millisecond, "the existing object is reconciled")

			burst := make([]string, tc.burst)
			func() {
				paused.hold.Lock()
				defer paused.hold.Unlock()
				for i := range burst {
					burst[i] = "burst-" + strconv.Itoa(i)
					createObject(t, st, v1.KindConfigMap, burst[i])
				}
				require.NoError(t, st.Delete(context.Background(), v1.KindConfigMap.GVK(), "default", "doomed", ""))
			}()
			createObject(t, st, v1.KindConfigMap, "late")

			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Zero(c, seen.missing(false, burst...), "burst objects never reconciled")
				assert.Zero(c, seen.missing(false, "late"), "object created after the burst never reconciled")
				assert.Zero(c, seen.missing(true, "doomed"), "object deleted during the burst never reconciled as NotFound")
			}, 5*time.Second, 20*time.Millisecond)
		})
	}
}

// Issue #302: after the store drops the controller's watch during a burst of deletes, every delete still
// reaches its reconcile as NotFound, and so do the Requests a MapFunc derived from the deleted object. A
// Deleted event carries the revision of the delete, so the re-watch resumes after the last delete seen even
// when the objects were last written long before; a re-list after a gap longer than the replay ring
// enqueues what each vanished object last drove.
func TestIssue302_ReconcilesDeletesAfterWatchDrop(t *testing.T) {
	t.Parallel()
	const doomed, filler = 200, 1100
	for _, tc := range []struct {
		name        string
		fillerFirst bool
	}{
		{name: "resumes after the last delete seen", fillerFirst: true},
		{name: "re-lists after a gap longer than the ring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			paused := &pausedStore{Store: st}
			fr, seen := presenceReconciler(st)
			gvk := v1.KindConfigMap.GVK()
			var names, dependents []string
			for i := range doomed {
				name := "doomed-" + strconv.Itoa(i)
				createObject(t, st, v1.KindConfigMap, name)
				names, dependents = append(names, name), append(dependents, name+"-dependent")
			}
			stop := run(t, paused, gvk, fr, func(c *controller.Controller) {
				c.Watches(gvk, func(_ context.Context, obj v1.Object) []controller.Request {
					return []controller.Request{{GVK: gvk, Namespace: obj.GetNamespace(), Name: obj.GetName() + "-dependent"}}
				})
			})
			defer stop()

			require.Eventually(t, func() bool { return seen.missing(false, names...)+seen.missing(true, dependents...) == 0 },
				3*time.Second, 10*time.Millisecond, "the existing objects and their dependents are reconciled")
			seen.mu.Lock()
			clear(seen.gone)
			seen.mu.Unlock()

			fill := func() {
				for i := range filler {
					createObject(t, st, v1.KindSecret, "filler-"+strconv.Itoa(i))
				}
			}
			if tc.fillerFirst {
				fill()
			}
			func() {
				paused.hold.Lock()
				defer paused.hold.Unlock()
				for _, name := range names {
					require.NoError(t, st.Delete(context.Background(), gvk, "default", v1.ObjectName(name), ""))
				}
				if !tc.fillerFirst {
					fill()
				}
			}()

			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Zero(c, seen.missing(true, names...), "deleted objects never reconciled as NotFound")
				assert.Zero(c, seen.missing(true, dependents...), "mapped Requests of deleted objects never reconciled")
			}, 5*time.Second, 20*time.Millisecond)
		})
	}
}

// Watches: a change of a watched kind that has no Reconciler of its own enqueues the Requests its MapFunc
// returns for another kind.
func TestWatchesEnqueuesMappedRequests(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	c, err := controller.New(controller.Deps{Store: st})
	require.NoError(t, err)
	var mu sync.Mutex
	got := map[controller.Request]bool{}
	c.Register(v1.KindSecret.GVK(), &fakeReconciler{hook: func(_ context.Context, req controller.Request) {
		mu.Lock()
		defer mu.Unlock()
		got[req] = true
	}})
	c.Watches(v1.KindConfigMap.GVK(), func(_ context.Context, obj v1.Object) []controller.Request {
		meta := obj.GetObjectMeta()
		return []controller.Request{{GVK: v1.KindSecret.GVK(), Namespace: meta.Namespace, Name: meta.Name + "-dependent"}}
	})
	createObject(t, st, v1.KindConfigMap, "cm")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()

	want := controller.Request{GVK: v1.KindSecret.GVK(), Namespace: "default", Name: "cm-dependent"}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return got[want]
	}, 3*time.Second, 10*time.Millisecond, "the ConfigMap change reconciles the mapped Secret request")
}
