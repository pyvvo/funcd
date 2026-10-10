//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// requiresWithin bounds each wait of the ADR-0219 scenarios.
const requiresWithin = 15 * time.Second

// requiresPacing scales the fixture's app.upgradeTimeout 2m > runtime.bootTimeout 1m down to 3 s > 1 s.
func requiresPacing() funcd.Option {
	return funcd.WithPacing(funcd.Pacing{AppUpgradeTimeout: 3 * time.Second, BootTimeout: time.Second, ActivationTimeout: 500 * time.Millisecond})
}

// requiresApp is an App of the namespace default with a version and requirements.
func requiresApp(name v1.ObjectName, version string, reqs ...v1.AppRequirement) *v1.App {
	return &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: v1.ResourceGroupName(name)},
		Spec:       v1.AppSpec{Version: version, Requires: reqs},
	}
}

// lakehouseApp is the shared App of ADR-0219's fixture. It declares the Bucket lake where the fixture declares the
// catalog lake: a CatalogService needs the duckdb provider container, which this platform does not run.
func lakehouseApp(version string, reqs ...v1.AppRequirement) *v1.App {
	a := requiresApp("lakehouse", version, reqs...)
	a.Spec.Buckets = []v1.AppBucket{{Name: "lake"}}
	return a
}

// billingApp is the fixture's dependent: version 1.3.0, lakehouse required at rng, the Function invoice, which
// scales to zero, and lake through a ref.
func billingApp(t *testing.T, e *gcEnv, rng string) *v1.App {
	t.Helper()
	a := requiresApp("billing", "1.3.0", v1.AppRequirement{App: "lakehouse", Version: rng})
	a.Spec.Functions = []v1.AppFunction{{Name: "invoice", FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)}}}
	a.Spec.Buckets = []v1.AppBucket{{Ref: "lake"}}
	return a
}

// update applies obj, an App that exists, again on a resourceVersion mismatch: a replace races the App reconciler's
// status writes, which the store refuses after the control plane read the App.
func (e *gcEnv) update(t *testing.T, obj v1.Object) {
	t.Helper()
	for end := time.Now().Add(requiresWithin); ; time.Sleep(20 * time.Millisecond) {
		_, err := e.c.Apply(e.ctx, obj)
		if err == nil || !strings.Contains(err.Error(), "resourceVersion mismatch") || time.Now().After(end) {
			require.NoError(t, err)
			return
		}
	}
}

// waitRolledOut waits until the App name has rev current and is Ready at its generation, and returns it.
func (e *gcEnv) waitRolledOut(t *testing.T, name string, rev v1.ObjectName) *v1.App {
	t.Helper()
	var a *v1.App
	require.Eventually(t, func() bool {
		a = e.app(t, name)
		return a.Status.CurrentRevision == rev && a.Status.Phase == v1.PhaseReady && a.Status.ObservedGeneration == a.Generation
	}, requiresWithin, 20*time.Millisecond, "App/%s serves %s", name, rev)
	return a
}

// waitWaiting waits until the App name is Deploying with Ready=False RequirementNotMet and msg, and returns it.
func (e *gcEnv) waitWaiting(t *testing.T, name, msg string) *v1.App {
	t.Helper()
	a := e.waitApp(t, name, v1.ConditionFalse, "RequirementNotMet", requiresWithin)
	require.Equal(t, v1.PhaseDeploying, a.Status.Phase)
	require.Equal(t, msg, readyCondition(a).Message)
	return a
}

// send sends obj (nil for none) to the API at path with method and returns the status and the problem's detail.
func (e *gcEnv) send(t *testing.T, method, path string, obj v1.Object) (int, string) {
	t.Helper()
	var body io.Reader
	if obj != nil {
		raw, err := json.Marshal(obj)
		require.NoError(t, err)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.api+"/apis/funcd.io/v1alpha1/namespaces/default/"+path, body)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	var p fault.Problem
	if json.Unmarshal(raw, &p) != nil || p.Detail == "" {
		return resp.StatusCode, string(raw)
	}
	return resp.StatusCode, p.Detail
}

// scenario: app-requires-waits
func TestScenarioAppRequiresWaits(t *testing.T) {
	t.Parallel()
	e := startGC(t, requiresPacing())
	e.apply(t, billingApp(t, e, "^2.0.0"))
	const msg = "App/lakehouse does not exist; billing needs ^2.0.0"
	e.waitWaiting(t, "billing", msg)
	waiting := func() bool {
		b, r := e.app(t, "billing"), e.appRevision(t, "billing-1")
		c := condition(r, "Applied")
		return b.Status.Phase == v1.PhaseDeploying && readyCondition(b).Message == msg && r.Status.Phase == v1.PhaseDeploying &&
			r.Status.StartedAt == nil && c.Status == v1.ConditionFalse && c.Reason == "RequirementNotMet" && c.Message == msg &&
			!e.exists(t, v1.KindFunction, "invoice")
	}
	stays(t, waiting, 4*time.Second, 100*time.Millisecond, "billing-1 waits past app.upgradeTimeout, writing no part")

	applied := time.Now()
	e.apply(t, lakehouseApp("2.1.0"))
	b := e.waitRolledOut(t, "billing", "billing-1")
	r := e.appRevision(t, "billing-1")
	require.NotNil(t, r.Status.StartedAt)
	require.False(t, time.Time(*r.Status.StartedAt).Before(applied.Truncate(time.Millisecond)), "startedAt is set once lakehouse is Ready")
	require.True(t, e.exists(t, v1.KindFunction, "invoice"))
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "2.1.0", Met: true}}, b.Status.Requires)
	require.Eventually(t, func() bool {
		return slices.Equal(e.app(t, "lakehouse").Status.RequiredBy, []v1.ObjectName{"billing"})
	}, requiresWithin, 20*time.Millisecond, "lakehouse's status.requiredBy is billing")
}

// scenario: app-requires-version
func TestScenarioAppRequiresVersion(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	e := startGC(t, requiresPacing(), funcd.WithStore(st))
	e.apply(t, lakehouseApp("1.9.0"))
	e.waitRolledOut(t, "lakehouse", "lakehouse-1")
	e.apply(t, billingApp(t, e, "^2.0.0"))
	b := e.waitWaiting(t, "billing", "App/lakehouse is 1.9.0; billing needs ^2.0.0")
	require.Equal(t, []v1.AppRequirementState{{App: "lakehouse", Version: "1.9.0"}}, b.Status.Requires)
	apps, revs := watchKind(t, st, v1.KindApp), watchKind(t, st, v1.KindAppRevision)

	e.update(t, lakehouseApp("2.1.0"))
	e.waitRolledOut(t, "billing", "billing-1")
	served := firstRV(t, apps.events(t), "lakehouse 2.1.0 is current and Ready", func(ev store.Event) bool {
		a, ok := ev.Object.(*v1.App)
		return ok && a.Name == "lakehouse" && a.Status.Version == "2.1.0" && a.Status.Phase == v1.PhaseReady
	})
	started := firstRV(t, revs.events(t), "billing-1 gets startedAt", func(ev store.Event) bool {
		r, ok := ev.Object.(*v1.AppRevision)
		return ok && r.Name == "billing-1" && r.Status.StartedAt != nil
	})
	require.Greater(t, started, served, "billing rolls out only after lakehouse 2.1.0 is current and Ready")
}

// scenario: app-requires-any-version
func TestScenarioAppRequiresAnyVersion(t *testing.T) {
	t.Parallel()
	e := startGC(t, requiresPacing())
	e.apply(t, billingApp(t, e, ""))
	e.waitWaiting(t, "billing", "App/lakehouse does not exist; billing needs any version")
	e.apply(t, lakehouseApp(""))
	e.waitRolledOut(t, "billing", "billing-1")

	e.update(t, lakehouseApp("3.0.0"))
	e.update(t, lakehouseApp("beta"))
	require.Equal(t, "beta", e.app(t, "lakehouse").Spec.Version)
	code, body := e.send(t, http.MethodDelete, "apps/lakehouse", nil)
	require.Equal(t, http.StatusConflict, code, body)
	require.Contains(t, body, `app "lakehouse" is required by billing; remove the requirement first`)
	require.True(t, e.exists(t, v1.KindApp, "lakehouse"))
}

// scenario: app-requires-upgrade-refused
func TestScenarioAppRequiresUpgradeRefused(t *testing.T) {
	t.Parallel()
	t.Run("a started dependent", func(t *testing.T) {
		t.Parallel()
		e := startGC(t, requiresPacing())
		cli := funcdctl(t, e)
		e.apply(t, lakehouseApp("1.9.0"))
		e.waitRolledOut(t, "lakehouse", "lakehouse-1")
		e.update(t, lakehouseApp("2.1.0"))
		e.waitRolledOut(t, "lakehouse", "lakehouse-2")
		e.apply(t, billingApp(t, e, "^2.0.0"))
		e.waitRolledOut(t, "billing", "billing-1")
		e.apply(t, requiresApp("audit", "1.0.0", v1.AppRequirement{App: "lakehouse", Version: "^3.0.0"}))
		e.waitWaiting(t, "audit", "App/lakehouse is 2.1.0; audit needs ^3.0.0")
		require.Eventually(t, func() bool {
			return slices.Equal(e.app(t, "lakehouse").Status.RequiredBy, []v1.ObjectName{"audit", "billing"})
		}, requiresWithin, 20*time.Millisecond, "lakehouse's status.requiredBy is audit and billing")
		before := e.app(t, "lakehouse")

		_, err := e.c.Apply(e.ctx, lakehouseApp("3.0.0"))
		require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, `app "lakehouse" version "3.0.0" is outside the range its dependents require: billing (^2.0.0)`)
		require.NotContains(t, err.Error(), "audit")
		out, err := cli("app", "rollback", "lakehouse", "1")
		require.Error(t, err, out)
		require.Contains(t, out, `app "lakehouse" version "1.9.0" is outside the range its dependents require: billing (^2.0.0)`)
		after := e.app(t, "lakehouse")
		require.Equal(t, before.Generation, after.Generation, "nothing is stored")
		require.Equal(t, "2.1.0", after.Spec.Version)

		e.update(t, lakehouseApp("2.2.0"))
		require.Equal(t, "2.2.0", e.app(t, "lakehouse").Spec.Version)
	})
	t.Run("a dependent waiting on a lakehouse not Ready", func(t *testing.T) {
		t.Parallel()
		e := startGC(t, requiresPacing())
		stuck := func(version string) *v1.App {
			a := lakehouseApp(version)
			a.Spec.Buckets = append(a.Spec.Buckets, v1.AppBucket{Ref: "missing"})
			return a
		}
		e.apply(t, stuck("2.1.0"))
		e.apply(t, billingApp(t, e, "^2.0.0"))
		e.waitWaiting(t, "billing", "App/lakehouse is not Ready (Deploying); billing needs ^2.0.0")
		e.update(t, stuck("3.0.0"))
		require.Equal(t, "3.0.0", e.app(t, "lakehouse").Spec.Version)
	})
}

// scenario: app-requires-delete-refused
func TestScenarioAppRequiresDeleteRefused(t *testing.T) {
	t.Parallel()
	e := startGC(t, requiresPacing())
	e.apply(t, lakehouseApp("2.1.0"))
	e.apply(t, billingApp(t, e, "^2.0.0"))
	code, body := e.send(t, http.MethodDelete, "apps/lakehouse", nil)
	require.Equal(t, http.StatusConflict, code, body)
	require.Contains(t, body, `app "lakehouse" is required by billing; remove the requirement first`)
	require.True(t, e.exists(t, v1.KindApp, "lakehouse"), "nothing is deleted")

	e.del(t, v1.KindApp, "billing")
	e.del(t, v1.KindApp, "lakehouse")
	e.waitGone(t, v1.KindApp, "lakehouse")
}

// scenario: app-requires-cycle-refused
func TestScenarioAppRequiresCycleRefused(t *testing.T) {
	t.Parallel()
	e := startGC(t, requiresPacing())
	e.apply(t, lakehouseApp("2.1.0"))
	e.apply(t, billingApp(t, e, "^2.0.0"))
	before := e.app(t, "lakehouse")
	for _, tc := range []struct {
		requires v1.ObjectName
		cycle    string
	}{
		{"billing", "lakehouse → billing → lakehouse"},
		{"lakehouse", "lakehouse → lakehouse"},
	} {
		code, body := e.send(t, http.MethodPut, "apps/lakehouse", lakehouseApp("2.1.0", v1.AppRequirement{App: tc.requires}))
		require.Equal(t, http.StatusBadRequest, code, body)
		require.Contains(t, body, "spec.requires would create a requirement cycle ("+tc.cycle+")")
	}
	after := e.app(t, "lakehouse")
	require.Equal(t, before.Generation, after.Generation, "nothing is stored")
	require.Empty(t, after.Spec.Requires)
}
