package funcd

import (
	"context"
	"math"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// pacedPlatform builds an in-memory platform with p, on a runtime that starts no process.
func pacedPlatform(t *testing.T, p Pacing) *Platform {
	t.Helper()
	pl, err := New(InMemory(), WithoutLogCompaction(), WithRuntime(&recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}), WithPacing(p))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pl.Shutdown(context.Background()) })
	return pl
}

// reconcilerOf is the reconciler p's controller runs for kind, as its concrete pointer value.
func reconcilerOf(p *Platform, kind v1.Kind) reflect.Value {
	return reflect.ValueOf(p.controller).Elem().FieldByName("reconcilers").MapIndex(reflect.ValueOf(kind.GVK())).Elem()
}

// durationAt reads the duration at the field path below v; the components keep their pacing unexported.
func durationAt(v reflect.Value, path ...string) time.Duration {
	for _, name := range path {
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			v = v.Elem()
		}
		v = v.FieldByName(name)
	}
	return time.Duration(v.Int())
}

func create(t *testing.T, st store.Store, obj v1.Object) {
	t.Helper()
	_, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
}

func routeTo(fn v1.ObjectName) *v1.Route {
	obj, _ := v1.NewObject(v1.KindRoute)
	rt := obj.(*v1.Route)
	rt.Name, rt.Namespace, rt.ResourceGroup = "r", "default", "rg1"
	rt.Spec.Rules = []v1.RouteRule{{Path: "/", Backend: v1.RouteBackend{Function: fn}}}
	return rt
}

// failingReconciler calls record and fails on every reconcile.
type failingReconciler func()

func (f failingReconciler) Reconcile(context.Context, controller.Request) (controller.Result, error) {
	f()
	return controller.Result{}, fault.Unavailablef("test", "always failing")
}

func TestWithPacingRefusesInvalidFields(t *testing.T) {
	cases := []struct {
		name  string
		p     Pacing
		field string
	}{
		{"negative", Pacing{ReclaimInterval: -time.Second}, "Pacing.ReclaimInterval"},
		{"negative zero-ok field", Pacing{DefaultRetryBackoff: -time.Second}, "Pacing.DefaultRetryBackoff"},
		{"retry max below base", Pacing{RetryBackoffMax: time.Millisecond}, "Pacing.RetryBackoffMax"},
		{"boot timeout at the activation hold", Pacing{BootTimeout: 30 * time.Second}, "Pacing.BootTimeout"},
		{"activation hold above the default boot timeout", Pacing{ActivationTimeout: 2 * time.Minute}, "Pacing.BootTimeout"},
		{"settle above the default grace", Pacing{HandOutSettle: time.Minute}, "Pacing.HandOutSettle"},
		{"grace below the default settle", Pacing{DrainGrace: time.Second}, "Pacing.HandOutSettle"},
		{"default retry backoff above 1h", Pacing{DefaultRetryBackoff: 2 * time.Hour}, "Pacing.DefaultRetryBackoff"},
		{"delivery max below the default initial", Pacing{DeliveryBackoffMax: 50 * time.Millisecond}, "Pacing.DeliveryBackoffMax"},
		{"delivery max below a set initial", Pacing{DeliveryBackoffInitial: time.Second, DeliveryBackoffMax: 500 * time.Millisecond}, "Pacing.DeliveryBackoffMax"},
		{"negative upgrade timeout", Pacing{AppUpgradeTimeout: -time.Second}, "Pacing.AppUpgradeTimeout"},
		{"upgrade timeout at the default boot timeout", Pacing{AppUpgradeTimeout: time.Minute}, "Pacing.AppUpgradeTimeout 1m0s must be more than BootTimeout 1m0s"},
		{"upgrade timeout below a set boot timeout", Pacing{BootTimeout: 10 * time.Minute, AppUpgradeTimeout: 6 * time.Minute}, "Pacing.AppUpgradeTimeout 6m0s must be more than BootTimeout 10m0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WithPacing(tc.p)(&config{})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, tc.field)
		})
	}
	c := &config{}
	require.NoError(t, WithPacing(Pacing{DeliveryBackoffInitial: 20 * time.Second})(c))
	require.Equal(t, 20*time.Second, c.pacing.deliveryBackoffMax(), "an unset max follows a larger initial wait")
}

// ADR-0200 Decision 10: a zero Pacing.AppUpgradeTimeout is max(5m, twice the effective BootTimeout); a set one is kept.
func TestAppUpgradeTimeoutDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Pacing
		want time.Duration
	}{
		{"zero", Pacing{}, 5 * time.Minute},
		{"short boot timeout", Pacing{BootTimeout: 2 * time.Second, ActivationTimeout: time.Second}, 5 * time.Minute},
		{"long boot timeout", Pacing{BootTimeout: 4 * time.Minute}, 8 * time.Minute},
		{"boot timeout above half the range", Pacing{BootTimeout: math.MaxInt64 - 1}, math.MaxInt64},
		{"set", Pacing{AppUpgradeTimeout: 61 * time.Second}, 61 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &config{}
			require.NoError(t, WithPacing(tc.p)(c))
			require.Equal(t, tc.want, c.pacing.appUpgradeTimeout())
		})
	}
}

// ADR-0200 Decision 10: app.revisionHistory is 1 to 100.
func TestWithAppRevisionHistory(t *testing.T) {
	for _, n := range []int{-1, 0, 101} {
		err := WithAppRevisionHistory(n)(&config{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%d: %v", n, err)
	}
	for _, n := range []int{1, 100} {
		c := &config{}
		require.NoError(t, WithAppRevisionHistory(n)(c))
		require.Equal(t, n, c.appRevisionHistory)
	}
	_, err := New(InMemory(), WithAppRevisionHistory(0))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "New refuses it: %v", err)
}

// scenario: controller-keys-pace-the-control-plane.
func TestScenarioControllerKeysPaceTheControlPlane(t *testing.T) {
	ctx := context.Background()
	t.Run("referentPollInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{ReferentPollInterval: 100 * time.Millisecond})
		create(t, p.cfg.store, routeTo("missing"))
		res, err := p.routeReconciler.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "default", Name: "r"})
		require.NoError(t, err)
		require.Equal(t, 100*time.Millisecond, res.RequeueAfter, "a Route whose backend is missing")

		obj, _ := v1.NewObject(v1.KindWorkflowRun)
		run := obj.(*v1.WorkflowRun)
		run.Name, run.Namespace, run.ResourceGroup, run.Spec.Workflow = "run", "default", "rg1", "missing"
		create(t, p.cfg.store, run)
		res, err = p.workflowSweeper.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run"})
		require.NoError(t, err)
		require.Equal(t, 100*time.Millisecond, res.RequeueAfter, "a WorkflowRun whose Workflow is missing")

		require.Equal(t, 100*time.Millisecond, durationAt(reconcilerOf(p, v1.KindFunction), "referentPoll"))
		require.Equal(t, 100*time.Millisecond, durationAt(reconcilerOf(p, v1.KindCatalogService), "referentPoll"))
		require.Equal(t, 100*time.Millisecond, durationAt(reconcilerOf(p, v1.KindSite), "routeRequeue"))
	})
	t.Run("routeResyncInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{RouteResyncInterval: 200 * time.Millisecond})
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = "backend", "default", "rg1"
		create(t, p.cfg.store, fn)
		create(t, p.cfg.store, routeTo("backend"))
		res, err := p.routeReconciler.Reconcile(ctx, controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "default", Name: "r"})
		require.NoError(t, err)
		require.Equal(t, 200*time.Millisecond, res.RequeueAfter)
	})
	t.Run("retryBackoffMax", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{RetryBackoffMax: 50 * time.Millisecond})
		var mu sync.Mutex
		var at []time.Time
		p.controller.Register(v1.KindConfigMap.GVK(), failingReconciler(func() {
			mu.Lock()
			defer mu.Unlock()
			at = append(at, time.Now())
		}))
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = p.controller.Run(runCtx) }()
		p.controller.Enqueue(controller.Request{GVK: v1.KindConfigMap.GVK(), Namespace: "default", Name: "cm"})
		require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(at) >= 12 }, 3*time.Second, 10*time.Millisecond)
		cancel()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for i := 8; i < 12; i++ {
			require.LessOrEqual(t, at[i].Sub(at[i-1]), 50*time.Millisecond+40*time.Millisecond, "a saturated retry waits at most retryBackoffMax")
		}
	})
}

// scenario: runtime-keys-pace-supervision-boot-and-drain — the platform hands each runtime key to the Function
// reconciler, and the supervision period to every reconciler ADR-0142 and ADR-0170 pace with it. The reconciler's use
// of each key is tested in internal/function (TestPacingDepsPaceTheFunctionReconciler, and the switch tests for
// drainGrace and handOutSettle).
func TestScenarioRuntimeKeysPaceSupervisionBootAndDrain(t *testing.T) {
	keys := []struct {
		name  string
		p     Pacing
		field string
		want  time.Duration
	}{
		{"supervisionPeriod", Pacing{SupervisionPeriod: 500 * time.Millisecond}, "supervisionPeriod", 500 * time.Millisecond},
		{"bootTimeout", Pacing{BootTimeout: 2 * time.Second, ActivationTimeout: time.Second}, "bootTimeout", 2 * time.Second},
		{"drainGrace", Pacing{DrainGrace: time.Second, HandOutSettle: 200 * time.Millisecond}, "drainGrace", time.Second},
		{"handOutSettle", Pacing{DrainGrace: time.Second, HandOutSettle: 200 * time.Millisecond}, "handOutSettle", 200 * time.Millisecond},
		{"drainPollInterval", Pacing{DrainPollInterval: 100 * time.Millisecond}, "drainPoll", 100 * time.Millisecond},
	}
	for _, k := range keys {
		t.Run(k.name, func(t *testing.T) {
			p := pacedPlatform(t, k.p)
			require.Equal(t, k.want, durationAt(reconcilerOf(p, v1.KindFunction), k.field))
			if k.name == "supervisionPeriod" {
				require.Equal(t, k.want, durationAt(reconcilerOf(p, v1.KindCatalogService), "period"))
				require.Equal(t, k.want, durationAt(reconcilerOf(p, v1.KindIdentity), "period"))
				require.Equal(t, k.want, durationAt(reconcilerOf(p, v1.KindWorkflow), "mat", "supervisionPeriod"))
				require.Equal(t, k.want, durationAt(reconcilerOf(p, v1.KindFunction), "referentPoll"), "the referent wait stays within the period")
			}
		})
	}
}

// scenario: catalog-keys-pace-the-engine-wait.
func TestScenarioCatalogKeysPaceTheEngineWait(t *testing.T) {
	t.Run("enginePollInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{EnginePollInterval: 200 * time.Millisecond})
		require.Equal(t, 200*time.Millisecond, durationAt(reconcilerOf(p, v1.KindCatalogService), "enginePoll"))
		q := pacedPlatform(t, Pacing{EnginePollInterval: 20 * time.Second})
		require.Equal(t, 10*time.Second, durationAt(reconcilerOf(q, v1.KindCatalogService), "enginePoll"), "bounded by the supervision period")
	})
	t.Run("engineProbeTimeout", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{EngineProbeTimeout: 100 * time.Millisecond})
		require.Equal(t, 100*time.Millisecond, durationAt(reconcilerOf(p, v1.KindCatalogService), "prov", "httpClient", "Timeout"))
	})
}

// scenario: workflow-keys-pace-artifact-wait-and-retry — the platform hands each key to the workflow component; the
// engine's and the reconciler's use of it is tested in internal/workflow (TestDefaultRetryBackoffPacesAStepWithNoBackoff,
// TestArtifactPollIntervalPacesTheContractWait).
func TestScenarioWorkflowKeysPaceArtifactWaitAndRetry(t *testing.T) {
	t.Run("artifactPollInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{ArtifactPollInterval: 200 * time.Millisecond})
		require.Equal(t, 200*time.Millisecond, durationAt(reconcilerOf(p, v1.KindWorkflow), "contractRequeue"))
	})
	t.Run("defaultRetryBackoff", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{DefaultRetryBackoff: 300 * time.Millisecond})
		require.Equal(t, 300*time.Millisecond, durationAt(reflect.ValueOf(p.workflowEngine), "cfg", "DefaultRetryBackoff"))
	})
}

// scenario: eventing-keys-pace-recheck-and-delivery-retry.
func TestScenarioEventingKeysPaceRecheckAndDeliveryRetry(t *testing.T) {
	t.Run("bucketRecheckInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{BucketRecheckInterval: 200 * time.Millisecond})
		obj, _ := v1.NewObject(v1.KindEventSource)
		es := obj.(*v1.EventSource)
		es.Name, es.Namespace, es.ResourceGroup = "drops", "default", "rg1"
		es.Spec.Blob = &v1.BlobSource{Bucket: "missing", Events: []v1.BlobEvent{{Name: "arrived"}}}
		create(t, p.cfg.store, es)
		res, err := p.eventing.Reconcile(context.Background(), controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "default", Name: "drops"})
		require.NoError(t, err)
		require.Equal(t, 200*time.Millisecond, res.RequeueAfter, "a blob source whose Bucket is missing")
	})
	t.Run("deliveryBackoffInitial", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{DeliveryBackoffInitial: 50 * time.Millisecond, DeliveryBackoffMax: 100 * time.Millisecond})
		require.Equal(t, 50*time.Millisecond, durationAt(reflect.ValueOf(p.sensorReconciler), "retry", "base"))
	})
	t.Run("deliveryBackoffMax", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{DeliveryBackoffInitial: 50 * time.Millisecond, DeliveryBackoffMax: 100 * time.Millisecond})
		require.Equal(t, 100*time.Millisecond, durationAt(reflect.ValueOf(p.sensorReconciler), "retry", "max"))
		q := pacedPlatform(t, Pacing{DeliveryBackoffInitial: 20 * time.Second})
		require.Equal(t, 20*time.Second, durationAt(reflect.ValueOf(q.sensorReconciler), "retry", "max"), "an unset max follows a larger initial wait")
	})
}

// scenario: invoke-keys-pace-wake-and-reclaim.
func TestScenarioInvokeKeysPaceWakeAndReclaim(t *testing.T) {
	t.Run("activationTimeout", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{ActivationTimeout: 500 * time.Millisecond})
		require.Equal(t, 500*time.Millisecond, durationAt(reflect.ValueOf(p.activator), "activationTimeout"))
	})
	t.Run("reclaimInterval", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{ReclaimInterval: 200 * time.Millisecond})
		require.Equal(t, 200*time.Millisecond, durationAt(reflect.ValueOf(p.activator), "reclaimInterval"))
	})
}

// scenario: server-keys-pace-shutdown-and-egress-sync.
func TestScenarioServerKeysPaceShutdownAndEgressSync(t *testing.T) {
	t.Run("shutdownTimeout", func(t *testing.T) {
		p := pacedPlatform(t, Pacing{ShutdownTimeout: time.Second})
		require.Equal(t, time.Second, p.drainTimeout)
		require.Equal(t, shutdownTimeout, pacedPlatform(t, Pacing{}).drainTimeout)
	})
	t.Run("workerSyncInterval", func(t *testing.T) {
		st := store.New(memory.New())
		rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
		p := &Platform{cfg: &config{store: st, runtime: rt, pacing: Pacing{WorkerSyncInterval: 200 * time.Millisecond}}, egressWorkers: egress.NewMemoryWorkerIndex()}
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = "fn", "default", "rg1"
		create(t, st, fn)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go p.syncEgressWorkers(ctx)
		time.Sleep(50 * time.Millisecond)
		ip := netip.MustParseAddr("10.63.0.7")
		id := runtime.NewInstanceID("default", "fn", "", 0)
		rt.mu.Lock()
		rt.insts[id] = runtime.Instance{ID: id, Namespace: "default", Name: "fn", OwnerKind: v1.KindFunction, State: runtime.StateRunning, IP: ip.String()}
		rt.mu.Unlock()
		start := time.Now()
		require.Eventually(t, func() bool { _, ok := p.egressWorkers.Lookup(ip); return ok }, time.Second, 5*time.Millisecond)
		require.Less(t, time.Since(start), 200*time.Millisecond+100*time.Millisecond)
	})
}
