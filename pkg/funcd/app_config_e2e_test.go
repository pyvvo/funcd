//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// secretWithin is how soon the parts are written once a declared Secret is complete (ADR-0213 app-secret-declared).
const secretWithin = 5 * time.Second

// envSource answers with the two variables todo-api reads.
const envSource = `export async function handle() { return { tz: process.env.TZ ?? "", stripe: process.env.STRIPE_API_KEY ?? "" }; }`

// imageEnv pushes an image of envSource under tag.
func (e *gcEnv) imageEnv(t *testing.T, tag string) string {
	t.Helper()
	writeStep(t, e.src, tag, envSource)
	return pushStepImage(t, e.layout, e.src, tag)
}

// configTodo is ADR-0213's fixture: the App todo plus configMaps todo-settings (TZ: Europe/Paris) and secrets
// todo-stripe-key (key STRIPE_API_KEY); todo-api runs envSource and names both.
func configTodo(t *testing.T, e *gcEnv) *v1.App {
	t.Helper()
	a := todoApp(t, e)
	a.Spec.ConfigMaps = []v1.AppConfigMap{{Name: "todo-settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Paris"}}}}
	a.Spec.Secrets = []v1.AppSecret{{Name: "todo-stripe-key", Description: "Stripe API access for billing", Keys: []string{"STRIPE_API_KEY"}}}
	fn := &a.Spec.Functions[0]
	fn.Image, fn.Config, fn.Secrets = e.imageEnv(t, "env"), []v1.ObjectName{"todo-settings"}, []v1.ObjectName{"todo-stripe-key"}
	return a
}

func (e *gcEnv) stripeKey(t *testing.T, data map[string]string) {
	t.Helper()
	b := make(map[string][]byte, len(data))
	for k, v := range data {
		b[k] = []byte(v)
	}
	applySecret(t, e.c, "todo-stripe-key", b)
}

// apiEnv is what todo-api answers through its Route.
type apiEnv struct {
	TZ     string `json:"tz"`
	Stripe string `json:"stripe"`
}

func (e *gcEnv) apiEnv(t *testing.T) (apiEnv, bool) {
	t.Helper()
	code, body := e.routedBody(t, todoHost, "/api")
	var out apiEnv
	return out, code == http.StatusOK && json.Unmarshal([]byte(body), &out) == nil
}

// waitAPIEnv waits until todo-api answers want.
func (e *gcEnv) waitAPIEnv(t *testing.T, want apiEnv) {
	t.Helper()
	require.Eventually(t, func() bool {
		got, ok := e.apiEnv(t)
		return ok && got == want
	}, appWithin, 50*time.Millisecond, "todo-api answers %+v", want)
}

// settingsName is the stored name of todo-settings with TZ tz.
func settingsName(tz string) string {
	return string(v1.AppConfigMapName("todo-settings", v1.ConfigMapSpec{Data: map[string]string{"TZ": tz}}))
}

// generations reads the generation of each part: a write of its spec moves it, its reconciler's status writes do not.
func (e *gcEnv) generations(t *testing.T, parts []todoPart) map[todoPart]int64 {
	t.Helper()
	out := make(map[todoPart]int64, len(parts))
	for _, p := range parts {
		out[p] = e.object(t, p.kind, p.name).GetObjectMeta().Generation
	}
	return out
}

// stays requires cond every tick for d. It checks in the test's goroutine: require.Never runs each check in a
// goroutine it does not wait for, so a check still reading through e.ctx when the test ends fails on its cancel.
func stays(t *testing.T, cond func() bool, d, tick time.Duration, msg string) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(tick) {
		require.True(t, cond(), msg)
	}
}

// anyPartExists reports whether any of the fixture's parts, or its ConfigMap, is stored.
func (e *gcEnv) anyPartExists(t *testing.T) bool {
	t.Helper()
	if e.exists(t, v1.KindConfigMap, settingsName("Europe/Paris")) {
		return true
	}
	for _, p := range todoParts() {
		if e.exists(t, p.kind, p.name) {
			return true
		}
	}
	return false
}

// scenario: app-secret-declared
func TestScenarioAppSecretDeclared(t *testing.T) {
	t.Run("held until the Secret is complete", func(t *testing.T) {
		e := startGC(t)
		a := configTodo(t, e)
		e.apply(t, a)
		got := e.waitApp(t, "todo", v1.ConditionFalse, "SecretNotFound", appWithin)
		require.Equal(t, v1.PhaseDeploying, got.Status.Phase)
		require.Contains(t, readyCondition(got).Message, "Secret/todo-stripe-key")
		require.Never(t, func() bool { return e.anyPartExists(t) }, time.Second, 50*time.Millisecond, "no part is written")

		e.stripeKey(t, map[string]string{"OTHER": "x"})
		got = e.waitApp(t, "todo", v1.ConditionFalse, "SecretKeyMissing", appWithin)
		require.Contains(t, readyCondition(got).Message, "Secret/todo-stripe-key")
		require.Contains(t, readyCondition(got).Message, "STRIPE_API_KEY")
		require.False(t, e.anyPartExists(t))

		e.stripeKey(t, map[string]string{"STRIPE_API_KEY": "sk-test-1"})
		require.Eventually(t, func() bool { return e.exists(t, v1.KindFunction, "todo-api") }, secretWithin, 20*time.Millisecond,
			"the parts are written within 5 s")
		e.waitCurrent(t, "todo-1")
		e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
		e.waitAPIEnv(t, apiEnv{TZ: "Europe/Paris", Stripe: "sk-test-1"})

		parts := append(todoParts(), todoPart{v1.KindConfigMap, settingsName("Europe/Paris")})
		before := e.settled(t, parts)
		e.stripeKey(t, map[string]string{"STRIPE_API_KEY": "sk-test-2"})
		require.Never(t, func() bool { return !maps.Equal(before, e.versions(t, parts)) }, 2*time.Second, 100*time.Millisecond,
			"a value changed outside the App writes no object")
		a.Spec.Functions[0].Image = e.imageEnv(t, "env-restart")
		e.apply(t, a)
		e.waitCurrent(t, "todo-2")
		e.waitAPIEnv(t, apiEnv{TZ: "Europe/Paris", Stripe: "sk-test-2"})
	})
	t.Run("provided after the deadline", func(t *testing.T) {
		e := startGC(t, failedPacing())
		a := configTodo(t, e)
		e.apply(t, a)
		one := e.waitRevisionPhase(t, "todo-1", v1.PhaseFailed, 2*appWithin)
		c := condition(one, "ChildrenReady")
		require.Equal(t, "ChildNotReady", c.Reason)
		require.Contains(t, c.Message, "Secret/todo-stripe-key")
		got := e.waitApp(t, "todo", v1.ConditionFalse, "SecretNotFound", appWithin)
		require.Equal(t, v1.PhaseFailed, got.Status.Phase)

		e.stripeKey(t, map[string]string{"STRIPE_API_KEY": "sk-test-1"})
		require.Eventually(t, func() bool { return e.exists(t, v1.KindFunction, "todo-api") }, secretWithin, 20*time.Millisecond,
			"the parts are written within 5 s")
		e.waitAPIEnv(t, apiEnv{TZ: "Europe/Paris", Stripe: "sk-test-1"})
		got = e.waitApp(t, "todo", v1.ConditionFalse, "ChildNotReady", appWithin)
		require.Equal(t, v1.PhaseFailed, got.Status.Phase)
		require.Contains(t, readyCondition(got).Message, "Secret/todo-stripe-key")
		require.Equal(t, v1.PhaseFailed, e.appRevision(t, "todo-1").Status.Phase)

		a.Spec.Version = "2.0.0"
		e.apply(t, a)
		e.waitCurrent(t, "todo-2")
		require.Equal(t, v1.PhaseReady, e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin).Status.Phase)
	})
}

// postApp sends body to the API as a create of an App and returns the status and the answer.
func (e *gcEnv) postApp(t *testing.T, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.api+"/apis/funcd.io/v1alpha1/namespaces/default/apps", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	msg, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode, string(msg)
}

// scenario: app-secret-undeclared-refused
func TestScenarioAppSecretUndeclaredRefused(t *testing.T) {
	e := startGC(t)
	for name, tc := range map[string]struct {
		edit func(*v1.App)
		want []string
	}{
		"todo-api names todo-billing": {
			edit: func(a *v1.App) { a.Spec.Functions[0].Secrets = append(a.Spec.Functions[0].Secrets, "todo-billing") },
			want: []string{"spec.functions[0].secrets[1]", "Function/todo-api", `Secret "todo-billing"`},
		},
		"the step due names todo-billing": {
			edit: func(a *v1.App) { a.Spec.Workflows[0].Steps[0].Function.Secrets = []v1.ObjectName{"todo-billing"} },
			want: []string{"spec.workflows[0].steps[0].function.secrets[0]", "Workflow/todo-plan", `Secret "todo-billing"`},
		},
		"a secrets entry without keys": {
			edit: func(a *v1.App) { a.Spec.Secrets[0].Keys = nil },
			want: []string{"spec.secrets[0].keys"},
		},
		"the key STRIPE-KEY": {
			edit: func(a *v1.App) { a.Spec.Secrets[0].Keys = []string{"STRIPE-KEY"} },
			want: []string{"spec.secrets[0].keys[0]"},
		},
	} {
		a := configTodo(t, e)
		tc.edit(a)
		_, err := e.c.Apply(e.ctx, a)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", name, err)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
		requireNothingStored(t, e)
	}

	b, err := json.Marshal(configTodo(t, e))
	require.NoError(t, err)
	code, msg := e.postApp(t, string(b))
	require.Less(t, code, http.StatusMultipleChoices, "the fixture as JSON is accepted: %s", msg)
	e.del(t, v1.KindApp, "todo")
	e.waitGone(t, v1.KindApp, "todo")
	withData := strings.Replace(string(b), `"keys":["STRIPE_API_KEY"]`, `"keys":["STRIPE_API_KEY"],"data":{"STRIPE_API_KEY":"c2s="}`, 1)
	require.NotEqual(t, string(b), withData)
	code, msg = e.postApp(t, withData)
	require.Equal(t, http.StatusUnprocessableEntity, code, "a secrets entry carrying data is refused: %s", msg)
	require.Contains(t, msg, "data")
	requireNothingStored(t, e)

	_, err = sdk.DecodeManifest([]byte(`apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: default
spec:
  secrets:
    - name: todo-stripe-key
      keys:
        - STRIPE_API_KEY
      data:
        STRIPE_API_KEY: c2s=
`))
	require.ErrorContains(t, err, "data", "funcdctl's strict decoding refuses data in a secrets entry")
}

// scenario: app-config-change-rolls
func TestScenarioAppConfigChangeRolls(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	applyConfigMap(t, e.c, "shared", map[string]string{"SHARED": "one"})
	e.stripeKey(t, map[string]string{"STRIPE_API_KEY": "sk-test-1"})
	a := configTodo(t, e)
	a.Spec.Functions[0].Config = append(a.Spec.Functions[0].Config, "shared")
	e.apply(t, a)
	e.waitCurrent(t, "todo-1")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.waitAPIEnv(t, apiEnv{TZ: "Europe/Paris", Stripe: "sk-test-1"})
	paris, utc := settingsName("Europe/Paris"), settingsName("UTC")
	rev1 := e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Status.CurrentRevision

	cms, fns := watchKind(t, st, v1.KindConfigMap), watchKind(t, st, v1.KindFunction)
	a.Spec.ConfigMaps[0].Data["TZ"] = "UTC"
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	fn := e.object(t, v1.KindFunction, "todo-api").(*v1.Function)
	require.Equal(t, []v1.ObjectName{v1.ObjectName(utc), "shared"}, fn.Spec.Config)
	created := firstRV(t, cms.events(t), "ConfigMap/"+utc+" is created", func(ev store.Event) bool {
		return ev.Type == store.Added && ev.Object.GetObjectMeta().Name == v1.ObjectName(utc)
	})
	written := firstRV(t, fns.events(t), "Function/todo-api names it", func(ev store.Event) bool {
		f, ok := ev.Object.(*v1.Function)
		return ok && f.Name == "todo-api" && len(f.Spec.Config) > 0 && f.Spec.Config[0] == v1.ObjectName(utc)
	})
	require.Less(t, created, written, "the ConfigMap is created before todo-api is written")
	e.waitAPIEnv(t, apiEnv{TZ: "UTC", Stripe: "sk-test-1"})
	require.NotEqual(t, rev1, e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Status.CurrentRevision, "todo-api switched to a new Revision")
	e.waitGone(t, v1.KindConfigMap, paris)
	require.Equal(t, []v1.ObjectName{"todo-settings", "shared"}, e.app(t, "todo").Spec.Functions[0].Config, "the App keeps the declared names")

	out, err := funcdctl(t, e)("app", "rollback", "todo", "1")
	require.NoError(t, err, out)
	e.waitCurrent(t, "todo-3")
	require.True(t, e.exists(t, v1.KindConfigMap, paris), "todo-3 creates the previous ConfigMap again")
	e.waitAPIEnv(t, apiEnv{TZ: "Europe/Paris", Stripe: "sk-test-1"})
	e.waitGone(t, v1.KindConfigMap, utc)

	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	parts := append(todoParts(), todoPart{v1.KindConfigMap, paris})
	before := e.generations(t, parts)
	applyConfigMap(t, e.c, "shared", map[string]string{"SHARED": "two"})
	stays(t, func() bool { return maps.Equal(before, e.generations(t, parts)) }, 2*time.Second, 100*time.Millisecond,
		"a ConfigMap the App does not define rolls nothing: no part's spec is written")
	require.Equal(t, []v1.ObjectName{v1.ObjectName(paris), "shared"}, e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Spec.Config)
	require.Empty(t, e.object(t, v1.KindConfigMap, "shared").GetObjectMeta().OwnerReferences)
	require.Equal(t, v1.ObjectName("todo-3"), e.app(t, "todo").Status.LatestRevision)
}

// scenario: app-pre-f116-app
func TestScenarioAppPreF116App(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	applySecret(t, e.c, "todo-billing", map[string][]byte{"BILLING_KEY": []byte("bk-test")})
	older := todoApp(t, e)
	older.Spec.Version = "1.0.0"
	older.Spec.Functions[0].Secrets = []v1.ObjectName{"todo-billing"}
	_, err := st.Create(e.ctx, older)
	require.NoError(t, err, "an App stored before ADR-0213, written without the admission")
	e.waitCurrent(t, "todo-1")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)

	a := e.app(t, "todo")
	a.Spec.Version = "2.0.0"
	_, err = e.c.Apply(e.ctx, a)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `spec.functions[0].secrets[0]: Function/todo-api names Secret "todo-billing", which spec.secrets does not declare`)
	require.Equal(t, "1.0.0", e.app(t, "todo").Spec.Version, "nothing is stored")

	a = e.app(t, "todo")
	a.Spec.Version = "2.0.0"
	a.Spec.Secrets = []v1.AppSecret{{Name: "todo-billing", Keys: []string{"BILLING_KEY"}}}
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)

	out, err := funcdctl(t, e)("app", "rollback", "todo", "1")
	require.Error(t, err, out)
	require.Contains(t, out, "todo-billing")
	require.Never(t, func() bool { return e.exists(t, v1.KindAppRevision, "todo-3") }, time.Second, 50*time.Millisecond,
		"the refused rollback stamps nothing")
}
