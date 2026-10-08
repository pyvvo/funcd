//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: app-install
func TestScenarioAppInstall(t *testing.T) {
	e := startGC(t)
	e.apply(t, todoApp(t, e))
	a := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, a.Status.Phase)
	parts := todoParts()
	require.Len(t, a.Status.Children, len(parts))
	for i, c := range a.Status.Children {
		require.Equal(t, parts[i].kind, c.Kind)
		require.Equal(t, v1.ObjectName(parts[i].name), c.Name)
		require.Equal(t, v1.AppChildReady, c.State, "%s/%s", c.Kind, c.Name)
	}
	for _, p := range parts {
		obj := e.object(t, p.kind, p.name)
		require.Equal(t, v1.NamespaceName("default"), obj.GetObjectMeta().Namespace)
		require.Equal(t, v1.ResourceGroupName("rg1"), obj.GetObjectMeta().ResourceGroup, "%s/%s", p.kind, p.name)
		marked, controlled := appRefs(obj, a)
		require.Equal(t, p.store(), marked, "%s/%s carries the marker", p.kind, p.name)
		require.Equal(t, !p.retained(), controlled, "%s/%s carries the controller reference", p.kind, p.name)
	}
	step, ok := v1.ControllerOf(e.object(t, v1.KindFunction, "todo-plan-due").GetObjectMeta().OwnerReferences)
	require.True(t, ok)
	require.Equal(t, v1.KindWorkflow, step.Kind)
	require.Equal(t, v1.ObjectName("todo-plan"), step.Name)
	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/api"))
}

// requireNothingStored checks that neither the App todo nor any of its parts exists.
func requireNothingStored(t *testing.T, e *gcEnv) {
	t.Helper()
	require.False(t, e.exists(t, v1.KindApp, "todo"), "App/todo is not stored")
	for _, p := range todoParts() {
		require.False(t, e.exists(t, p.kind, p.name), "%s/%s is not stored", p.kind, p.name)
	}
}

// scenario: app-admission-refuses
func TestScenarioAppAdmissionRefuses(t *testing.T) {
	e := startGC(t, funcd.WithBucketQuotaForTest(1))
	for name, tc := range map[string]struct {
		edit func(*v1.App)
		want []string
	}{
		"a repeated function": {
			edit: func(a *v1.App) { a.Spec.Functions = append(a.Spec.Functions, a.Spec.Functions[0]) },
			want: []string{"spec.functions[1]"},
		},
		"a bad table name": {
			edit: func(a *v1.App) { a.Spec.KV[0].Tables[0].Name = "Bad_Name" },
			want: []string{"spec.kv[0].tables[0].name"},
		},
		"a ref with an image": {
			edit: func(a *v1.App) {
				a.Spec.Functions[0] = v1.AppFunction{Ref: "todo-api", FunctionSpec: v1.FunctionSpec{Image: a.Spec.Functions[0].Image}}
			},
			want: []string{"spec.functions[0]"},
		},
		"two new Buckets one below the quota": {
			edit: func(*v1.App) {},
			want: []string{"spec.buckets[", "bucket-count"},
		},
	} {
		a := todoApp(t, e)
		tc.edit(a)
		_, err := e.c.Apply(e.ctx, a)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", name, err)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
		requireNothingStored(t, e)
	}

	_, err := sdk.DecodeManifest([]byte(`apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: default
spec:
  deployments:
    - name: web
`))
	require.ErrorContains(t, err, "deployments", "funcdctl's strict decoding refuses an unknown section")
	body := `{"apiVersion":"funcd.io/v1alpha1","kind":"App","metadata":{"name":"todo","namespace":"default","resourceGroup":"rg1"},"spec":{"deployments":[{"name":"web"}]}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.api+"/apis/funcd.io/v1alpha1/namespaces/default/apps", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	msg, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Less(t, resp.StatusCode, http.StatusInternalServerError)
	require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, "the API schema refuses an unknown section: %s", msg)
	require.Contains(t, string(msg), "deployments")
	requireNothingStored(t, e)
}

// scenario: app-shared-writer-refused
func TestScenarioAppSharedWriterRefused(t *testing.T) {
	e := startGC(t)
	site, _ := pushSiteBundle(t, e.layout, "web", map[string]string{"index.html": "<!doctype html><title>todo</title>"})
	for name, tc := range map[string]struct {
		edit func(*v1.App)
		want []string
	}{
		"a Site writing a declared Bucket": {
			edit: func(a *v1.App) {
				a.Spec.Sites = []v1.AppSite{{Name: "todo-web", SiteSpec: v1.SiteSpec{
					Image:   site,
					Bucket:  v1.SiteBucket{Name: "todo-files"},
					Prefix:  "web",
					Ingress: v1.SiteIngress{Host: "web." + todoHost, Public: true},
				}}}
			},
			want: []string{"spec.sites[0].bucket.name", "spec.buckets[0]"},
		},
		"a Function named like a step Function": {
			edit: func(a *v1.App) {
				fn := a.Spec.Functions[0]
				fn.Name, fn.KV, fn.Blob = "todo-plan-due", nil, nil
				a.Spec.Functions = append(a.Spec.Functions, fn)
			},
			want: []string{"spec.workflows[0].steps[0].name", "spec.functions[1]"},
		},
	} {
		a := todoApp(t, e)
		tc.edit(a)
		_, err := e.c.Apply(e.ctx, a)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", name, err)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
		requireNothingStored(t, e)
		require.False(t, e.exists(t, v1.KindSite, "todo-web"), name)
	}
}

// scenario: app-child-not-owned
func TestScenarioAppChildNotOwned(t *testing.T) {
	e := startGC(t)
	e.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "todo-api", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)},
	})
	before := e.object(t, v1.KindFunction, "todo-api").GetObjectMeta()
	e.apply(t, todoApp(t, e))
	a := e.waitApp(t, "todo", v1.ConditionFalse, "ChildNotOwned", appWithin)
	require.Contains(t, readyCondition(a).Message, "Function/todo-api")
	require.Never(t, func() bool {
		for _, p := range todoParts() {
			if p != (todoPart{v1.KindFunction, "todo-api"}) && e.exists(t, p.kind, p.name) {
				return true
			}
		}
		return false
	}, time.Second, 50*time.Millisecond, "the App writes no part")
	after := e.object(t, v1.KindFunction, "todo-api").GetObjectMeta()
	require.Empty(t, after.OwnerReferences, "the App does not take Function/todo-api")
	require.Equal(t, before.Generation, after.Generation, "the App does not write Function/todo-api")
}

// scenario: app-spec-change-applies
func TestScenarioAppSpecChangeApplies(t *testing.T) {
	e := startGC(t)
	a := todoApp(t, e)
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	parts := todoParts()
	others := parts[:len(parts)-1]
	var before map[todoPart]string
	require.Eventually(t, func() bool {
		cur := e.versions(t, others)
		settled := maps.Equal(cur, before)
		before = cur
		return settled
	}, appWithin, time.Second, "the parts settle")
	require.Equal(t, http.StatusNotFound, e.routed(t, todoHost, "/v2"))

	a.Spec.Routes[0].Rules[0].Path = "/v2"
	e.apply(t, a)
	require.Eventually(t, func() bool { return e.routed(t, todoHost, "/v2") == http.StatusOK }, 5*time.Second, 20*time.Millisecond, "the Route serves /v2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, before, e.versions(t, others), "no other part is written")
}

// scenario: app-ref-waits
func TestScenarioAppRefWaits(t *testing.T) {
	e := startGC(t)
	a := todoApp(t, e)
	a.Spec.Functions = append(a.Spec.Functions, v1.AppFunction{Ref: "mailer"})
	e.apply(t, a)
	got := e.waitApp(t, "todo", v1.ConditionFalse, "RefNotFound", appWithin)
	require.Contains(t, readyCondition(got).Message, "Function/mailer")

	e.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "mailer", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)},
	})
	e.waitServes(t, "mailer")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	before := e.object(t, v1.KindFunction, "mailer").(*v1.Function)

	e.del(t, v1.KindApp, "todo")
	e.waitGone(t, v1.KindFunction, "todo-api")
	require.Never(t, func() bool { return !e.exists(t, v1.KindFunction, "mailer") }, time.Second, 50*time.Millisecond)
	after := e.object(t, v1.KindFunction, "mailer").(*v1.Function)
	require.Empty(t, after.OwnerReferences)
	require.Equal(t, before.Generation, after.Generation)
	require.Equal(t, before.Spec, after.Spec)
	require.Equal(t, http.StatusOK, e.invoke(t, "mailer"))
}
