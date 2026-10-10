//go:build e2e

package funcd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The ADR-0220 fixture's callers: alice may create and update every kind in default, bob may only get.
const (
	aliceToken = "alice-token"
	bobToken   = "bob-token"
)

func aliceAndBob() funcd.Option {
	return funcd.WithCredentials(
		funcd.Credential{Token: aliceToken, Role: "developer", Namespaces: []string{"default"}},
		funcd.Credential{Token: bobToken, Role: "viewer", Namespaces: []string{"default"}},
	)
}

const fnPath = "/apis/funcd.io/v1alpha1/namespaces/default/functions"

// call sends one control-plane request as token and returns the status and the body.
func (e *gcEnv) call(t *testing.T, method, path, token string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(e.ctx, method, e.api+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, out
}

func functionJSON(t *testing.T, name, image, handler string) []byte {
	t.Helper()
	b, err := json.Marshal(&v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: handler, Image: image},
	})
	require.NoError(t, err)
	return b
}

func decodeFunction(t *testing.T, body []byte) *v1.Function {
	t.Helper()
	var fn v1.Function
	require.NoError(t, json.Unmarshal(body, &fn), "%s", body)
	return &fn
}

func detail(t *testing.T, body []byte) string {
	t.Helper()
	var p fault.Problem
	require.NoError(t, json.Unmarshal(body, &p), "%s", body)
	return p.Detail
}

// manifest writes obj as a manifest file and returns its path.
func manifest(t *testing.T, obj v1.Object) string {
	t.Helper()
	b, err := json.Marshal(obj)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, b, 0o600))
	return path
}

// appState waits until no resourceVersion of the App todo, its revisions and its parts changes for a second, and
// returns them and the revision names in order.
func (e *gcEnv) appState(t *testing.T) (map[todoPart]string, []v1.ObjectName) {
	t.Helper()
	objs, err := e.c.List(e.ctx, v1.KindAppRevision, "default")
	require.NoError(t, err)
	parts := append(todoV1Parts(), todoPart{v1.KindApp, "todo"})
	var revs []v1.ObjectName
	for _, o := range objs {
		parts = append(parts, todoPart{v1.KindAppRevision, string(o.GetName())})
		revs = append(revs, o.GetName())
	}
	return e.settled(t, parts), revs
}

// settledList waits until the list of kind in default has not changed for a second, and returns it.
func (e *gcEnv) settledList(t *testing.T, kind v1.Kind) []v1.Object {
	t.Helper()
	var before []v1.Object
	require.Eventually(t, func() bool {
		cur, err := e.c.List(e.ctx, kind, "default")
		settled := err == nil && before != nil && reflect.DeepEqual(cur, before)
		before = cur
		return settled
	}, appWithin, time.Second, "the %s list settles", kind)
	return before
}

// scenario: apply-dry-run-refused
func TestScenarioApplyDryRunRefused(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithBucketQuotaForTest(1))
	bucket := func(name v1.ObjectName) *v1.Bucket {
		return &v1.Bucket{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		}
	}
	e.apply(t, bucket("b1"))
	before := e.settledList(t, v1.KindBucket)
	cli := funcdctl(t, e)
	path := manifest(t, bucket("b2"))

	dry, err := cli("apply", "--dry-run", "-f", path)
	require.Error(t, err, dry)
	require.Contains(t, dry, `admission.bucket-count: namespace "default" already holds the maximum 1 Buckets`)
	require.Contains(t, dry, `document 1 (Bucket "b2")`)
	real, err := cli("apply", "-f", path)
	require.Error(t, err, real)
	require.Equal(t, real, dry, "the dry run fails with the real apply's text")
	after, err := e.c.List(e.ctx, v1.KindBucket, "default")
	require.NoError(t, err)
	require.Equal(t, before, after, "the Bucket list is unchanged")
}

// scenario: app-apply-dry-run
func TestScenarioAppApplyDryRun(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := installTodo(t, e)
	before, revs := e.appState(t)

	a.Spec.Functions[0].Image = e.imageV2(t)
	a.Spec.Routes = a.Spec.Routes[:1]
	out, err := funcdctl(t, e)("apply", "--dry-run", "-f", manifest(t, a))
	require.NoError(t, err, out)
	require.Equal(t, "would apply App/todo\n  revision todo-2\n  update Function/todo-api\n  prune Route/todo-legacy\n", out)
	after, revsAfter := e.appState(t)
	require.Equal(t, before, after, "no object and no AppRevision is written")
	require.Equal(t, []v1.ObjectName{"todo-1"}, revsAfter)
	require.Equal(t, revs, revsAfter)
}

// scenario: app-deploy-dry-run
func TestScenarioAppDeployDryRun(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithDevAuth(funcd.DevToken, "team-a"))
	tpl := newTodoTemplate(t, e)
	legacy := filepath.Join(tpl.appDir(), "resources", "legacy.yaml")
	require.NoError(t, os.WriteFile(legacy, []byte(`routes:
  - name: ${{ app.name + "-legacy" }}
    host: ${{ values.host }}
    rules:
      - path: /legacy
        backend:
          function: ${{ app.name + "-api" }}
`), 0o600))
	out, err := tpl.deploy(t, "1.2.0", "1.0.0")
	require.NoError(t, err, out)
	app, _ := tpl.get(t, v1.KindApp, "todo")
	rv := app.GetObjectMeta().ResourceVersion

	require.NoError(t, os.Remove(legacy))
	require.NoError(t, os.WriteFile(filepath.Join(tpl.appDir(), "resources", "hooks.yaml"), []byte(`hooks:
  preApply:
    - function: ${{ app.name + "-migrate" }}
`), 0o600))
	writeStep(t, e.src, "1.1.0", `export async function handle(event) { return { image: "1.1.0", event }; }`)
	tpl.push(t, "todo-api", "1.1.0")
	out, err = tpl.deploy(t, "1.3.0", "1.1.0", "--dry-run")
	require.NoError(t, err, out)
	require.Equal(t, "would apply App/todo\n  revision todo-2\n  update Function/todo-api\n  prune Route/todo-legacy\n"+
		"  hook todo-migrate\n", out)
	app, _ = tpl.get(t, v1.KindApp, "todo")
	require.Equal(t, rv, app.GetObjectMeta().ResourceVersion, "nothing is written")
	_, stamped := tpl.get(t, v1.KindAppRevision, "todo-2")
	require.False(t, stamped, "no AppRevision is stamped")
	_, kept := tpl.get(t, v1.KindRoute, "todo-legacy")
	require.True(t, kept, "nothing is pruned")
}

// scenario: app-dry-run-unchanged
func TestScenarioAppDryRunUnchanged(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	installTodo(t, e)
	before, _ := e.appState(t)
	stored := e.app(t, "todo")
	cli := funcdctl(t, e)
	for _, paused := range []bool{false, true} {
		a := e.app(t, "todo")
		a.Spec.Paused = paused
		out, err := cli("apply", "--dry-run", "-f", manifest(t, a))
		require.NoError(t, err, out)
		require.Equal(t, "would apply App/todo\n  no change\n", out, "paused: %v", paused)
		answer, err := e.c.Apply(e.ctx, a, sdk.DryRun())
		require.NoError(t, err)
		require.Equal(t, &v1.AppPlan{}, answer.(*v1.App).Status.Plan, "no revision, no parts and no hooks")
	}
	after, _ := e.appState(t)
	require.Equal(t, before, after)
	require.False(t, e.app(t, "todo").Spec.Paused, "the App is not paused")
	require.Equal(t, stored.ResourceVersion, e.app(t, "todo").ResourceVersion)
}

// scenario: app-rollback-dry-run
func TestScenarioAppRollbackDryRun(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := installTodo(t, e)
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Eventually(t, func() bool {
		return e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Status.DrainingRevision == ""
	},
		appWithin, 50*time.Millisecond, "todo-api's first Revision is drained")
	before, _ := e.appState(t)

	out, err := funcdctl(t, e)("app", "rollback", "todo", "1", "--dry-run")
	require.NoError(t, err, out)
	require.Equal(t, "would apply App/todo\n  revision todo-3\n  update Function/todo-api\n", out)
	after, revs := e.appState(t)
	require.Equal(t, before, after, "nothing is written")
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2"}, revs)
}

// scenario: dry-run-stores-nothing
func TestScenarioDryRunStoresNothing(t *testing.T) {
	t.Parallel()
	e := startGC(t, aliceAndBob())
	img := e.image(t)
	code, body := e.call(t, http.MethodPost, fnPath+"?dryRun=true", aliceToken, functionJSON(t, "f", img, "handle"))
	require.Equal(t, http.StatusOK, code, "%s", body)
	f := decodeFunction(t, body)
	require.Equal(t, v1.ObjectName("f"), f.Name)
	require.Empty(t, f.UID)
	require.Empty(t, f.ResourceVersion)
	code, _ = e.call(t, http.MethodGet, fnPath+"/f", aliceToken, nil)
	require.Equal(t, http.StatusNotFound, code)

	code, body = e.call(t, http.MethodPost, fnPath, aliceToken, functionJSON(t, "g", img, "handle"))
	require.Equal(t, http.StatusOK, code, "%s", body)
	g := decodeFunction(t, body)
	// The Function reconciler still writes g's status, so the answer is checked only for a dry run between two reads of
	// one resourceVersion.
	resourceVersion := func() string {
		code, body := e.call(t, http.MethodGet, fnPath+"/g", aliceToken, nil)
		require.Equal(t, http.StatusOK, code, "%s", body)
		return decodeFunction(t, body).ResourceVersion
	}
	var stored string
	var answer *v1.Function
	require.Eventually(t, func() bool {
		stored = resourceVersion()
		code, body := e.call(t, http.MethodPut, fnPath+"/g?dryRun=true", aliceToken, functionJSON(t, "g", img, "other"))
		require.Equal(t, http.StatusOK, code, "%s", body)
		answer = decodeFunction(t, body)
		return resourceVersion() == stored
	}, appWithin, 50*time.Millisecond, "a dry run of g lands between two reads of one resourceVersion")
	require.Equal(t, "other", answer.Spec.Handler)
	require.Equal(t, g.UID, answer.UID)
	require.Equal(t, stored, answer.ResourceVersion)
	code, body = e.call(t, http.MethodGet, fnPath+"/g", aliceToken, nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "handle", decodeFunction(t, body).Spec.Handler, "GET still returns the old spec")
}

// scenario: dry-run-store-refusals
func TestScenarioDryRunStoreRefusals(t *testing.T) {
	t.Parallel()
	e := startGC(t, aliceAndBob())
	img := e.image(t)
	code, body := e.call(t, http.MethodPost, fnPath, aliceToken, functionJSON(t, "g", img, "handle"))
	require.Equal(t, http.StatusOK, code, "%s", body)
	code, body = e.call(t, http.MethodPost, fnPath+"?dryRun=true", aliceToken, functionJSON(t, "g", img, "handle"))
	require.Equal(t, http.StatusConflict, code, "%s", body)
	require.Equal(t, `store.Create: Function "g" already exists`, detail(t, body))
	code, body = e.call(t, http.MethodPut, fnPath+"/h?dryRun=true", aliceToken, functionJSON(t, "h", img, "handle"))
	require.Equal(t, http.StatusNotFound, code, "%s", body)
	realCode, _ := e.call(t, http.MethodPut, fnPath+"/h", aliceToken, functionJSON(t, "h", img, "handle"))
	require.Equal(t, realCode, code, "as the real write")
}

// scenario: dry-run-forbidden
func TestScenarioDryRunForbidden(t *testing.T) {
	t.Parallel()
	e := startGC(t, aliceAndBob())
	img := e.image(t)
	code, body := e.call(t, http.MethodPost, fnPath, aliceToken, functionJSON(t, "g", img, "handle"))
	require.Equal(t, http.StatusOK, code, "%s", body)
	app, err := json.Marshal(todoApp(t, e))
	require.NoError(t, err)
	for _, w := range []struct{ method, path string }{
		{http.MethodPost, fnPath},
		{http.MethodPut, fnPath + "/g"},
	} {
		code, body := e.call(t, w.method, w.path+"?dryRun=true", bobToken, functionJSON(t, "g", img, "other"))
		require.Equal(t, http.StatusForbidden, code, "%s %s: %s", w.method, w.path, body)
	}
	code, body = e.call(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/apps?dryRun=true", bobToken, app)
	require.Equal(t, http.StatusForbidden, code, "%s", body)
	require.NotContains(t, string(body), `"plan"`, "no plan")
}

// scenario: dry-run-unsupported
func TestScenarioDryRunUnsupported(t *testing.T) {
	t.Parallel()
	e := startGC(t, aliceAndBob())
	code, spec := e.call(t, http.MethodGet, "/openapi.json", aliceToken, nil)
	require.Equal(t, http.StatusOK, code)
	older := strings.ReplaceAll(string(spec), `"name":"dryRun"`, `"name":"unrelated"`)
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		if r.URL.Path != "/openapi.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, older)
	}))
	t.Cleanup(srv.Close)
	bucket := &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "b", Namespace: "default", ResourceGroup: "rg1"},
	}
	out, err := exec.Command(buildCmd(t, "funcdctl"), "--server", srv.URL, "apply", "--dry-run", "-f", manifest(t, bucket)).CombinedOutput()
	require.Error(t, err, "%s", out)
	require.Contains(t, string(out), "sdk.Apply: the server does not support dryRun")
	require.Zero(t, writes.Load(), "no write is sent")

	img := e.image(t)
	code, body := e.call(t, http.MethodPost, fnPath, aliceToken, functionJSON(t, "g", img, "handle"))
	require.Equal(t, http.StatusOK, code, "%s", body)
	code, body = e.call(t, http.MethodDelete, fnPath+"/g?dryRun=true", aliceToken, nil)
	require.Equal(t, http.StatusUnprocessableEntity, code, "%s", body)
	require.Contains(t, string(body), "unknown query parameter")
	code, _ = e.call(t, http.MethodGet, fnPath+"/g", aliceToken, nil)
	require.Equal(t, http.StatusOK, code, "g remains")
}
