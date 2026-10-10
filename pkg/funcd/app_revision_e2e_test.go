//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// todoV2Marker is what todo-api's second image answers; the fixture's echo image never does.
const todoV2Marker = "todo-v2"

// imageV2 pushes todo-api's second image.
func (e *gcEnv) imageV2(t *testing.T) string {
	t.Helper()
	writeStep(t, e.src, "echo-v2", `export async function handle() { return { image: "`+todoV2Marker+`" }; }`)
	return pushStepImage(t, e.layout, e.src, "echo-v2")
}

// installTodo applies ADR-0200's fixture and waits until todo-1 is current and the App Ready.
func installTodo(t *testing.T, e *gcEnv) *v1.App {
	t.Helper()
	a := todoV1(t, e)
	e.apply(t, a)
	e.waitCurrent(t, "todo-1")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	return a
}

// waitCurrent waits until the App todo's currentRevision is rev.
func (e *gcEnv) waitCurrent(t *testing.T, rev v1.ObjectName) {
	t.Helper()
	require.Eventually(t, func() bool { return e.app(t, "todo").Status.CurrentRevision == rev },
		appWithin, 20*time.Millisecond, "the App todo's currentRevision is %s", rev)
}

func (e *gcEnv) appRevision(t *testing.T, name string) *v1.AppRevision {
	t.Helper()
	return e.object(t, v1.KindAppRevision, name).(*v1.AppRevision)
}

func condition(r *v1.AppRevision, ct v1.ConditionType) v1.Condition {
	c, _ := r.Status.Conditions.Get(ct)
	return c
}

// rvOf is the counter of ev's resourceVersion, its write's place in the store's one timeline.
func rvOf(t *testing.T, ev store.Event) uint64 {
	t.Helper()
	v, err := store.ParseVersion(ev.Object.GetObjectMeta().ResourceVersion)
	require.NoError(t, err)
	return v.N
}

// firstRV is the resourceVersion of the first event of evs that match accepts.
func firstRV(t *testing.T, evs []store.Event, what string, match func(store.Event) bool) uint64 {
	t.Helper()
	i := slices.IndexFunc(evs, match)
	require.GreaterOrEqual(t, i, 0, "no write where %s", what)
	return rvOf(t, evs[i])
}

// buildCmd compiles this repository's cmd/<name> and returns the binary's path.
func buildCmd(t *testing.T, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "./cmd/"+name)
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build cmd/%s: %s", name, out)
	return bin
}

// scenario: app-reapply-same-spec
func TestScenarioAppReapplySameSpec(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	a := installTodo(t, e)
	parts := todoV1Parts()
	before := e.settled(t, parts)
	revs := watchKind(t, st, v1.KindAppRevision)

	e.apply(t, a)
	// The store drops an unchanged write (ADR-0047), so no pass may follow it; a tag alone makes one over the same spec.
	a.Tags = v1.Tags{"reapplied": "true"}
	e.apply(t, a)
	require.Never(t, func() bool { return e.exists(t, v1.KindAppRevision, "todo-2") }, 2*time.Second, 50*time.Millisecond,
		"no AppRevision is stamped")
	require.Empty(t, revs.events(t), "no AppRevision is written")
	require.Equal(t, before, e.versions(t, parts), "no part is written")
	require.Equal(t, v1.ObjectName("todo-1"), e.app(t, "todo").Status.LatestRevision)
}

// scenario: app-upgrade
func TestScenarioAppUpgrade(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	a := installTodo(t, e)
	old := e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Status.ServingRevision
	_, body := e.routedBody(t, todoHost, "/api")
	require.NotContains(t, body, todoV2Marker)
	apps, fns := watchKind(t, st, v1.KindApp), watchKind(t, st, v1.KindFunction)

	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, "2.0.0", got.Status.Version)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.LatestRevision)

	fn := e.object(t, v1.KindFunction, "todo-api").(*v1.Function)
	require.NotEqual(t, old, fn.Status.CurrentRevision, "todo-api has a new Revision")
	require.Equal(t, fn.Status.CurrentRevision, fn.Status.ServingRevision, "todo-api's new Revision serves")
	appEvs, fnEvs := apps.events(t), fns.events(t)
	deploying := 0
	for _, w := range todoWrites(appEvs) {
		if w.Status.LatestRevision != "todo-2" || w.Status.CurrentRevision == "todo-2" {
			continue
		}
		deploying++
		require.Equal(t, v1.ObjectName("todo-1"), w.Status.CurrentRevision)
		require.Equal(t, "1.0.0", w.Status.Version)
		require.Equal(t, v1.PhaseDeploying, w.Status.Phase)
	}
	require.NotZero(t, deploying, "the App is Deploying with todo-1 current once todo-2 is stamped")
	serves := firstRV(t, fnEvs, "todo-api's new Revision serves", func(ev store.Event) bool {
		f, ok := ev.Object.(*v1.Function)
		return ok && f.Name == "todo-api" && f.Status.ServingRevision == fn.Status.CurrentRevision
	})
	switched := firstRV(t, appEvs, "todo-2 is current", func(ev store.Event) bool {
		w, ok := ev.Object.(*v1.App)
		return ok && w.Name == "todo" && w.Status.CurrentRevision == "todo-2"
	})
	require.Greater(t, switched, serves, "todo-2 becomes current only after todo-api's new Revision serves")

	r1, r2 := e.appRevision(t, "todo-1"), e.appRevision(t, "todo-2")
	require.Equal(t, v1.PhaseReady, r2.Status.Phase)
	require.Equal(t, v1.ConditionTrue, condition(r2, "Current").Status)
	require.Equal(t, "2.0.0", r2.Spec.Spec.Version)
	require.Equal(t, v1.PhaseReady, r1.Status.Phase, "a replaced revision stays Ready")
	c := condition(r1, "Current")
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, "Replaced", c.Reason)

	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker, "calls answer from the new image")
}

// scenario: app-prune-after-current
func TestScenarioAppPruneAfterCurrent(t *testing.T) {
	st := store.New(memory.New())
	e := startGC(t, funcd.WithStore(st))
	a := installTodo(t, e)
	require.Equal(t, http.StatusOK, e.routed(t, todoHost, "/legacy"))
	apps, routes := watchKind(t, st, v1.KindApp), watchKind(t, st, v1.KindRoute)

	a.Spec.Routes = a.Spec.Routes[:1]
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	require.Eventually(t, func() bool { return !e.exists(t, v1.KindRoute, "todo-legacy") }, 5*time.Second, 20*time.Millisecond,
		"Route/todo-legacy is gone within 5 s after todo-2 is current")

	appEvs, routeEvs := apps.events(t), routes.events(t)
	deleted := firstRV(t, routeEvs, "Route/todo-legacy is deleted", func(ev store.Event) bool {
		return ev.Type == store.Deleted && ev.Object.GetObjectMeta().Name == "todo-legacy"
	})
	pruning := v1.AppChild{Kind: v1.KindRoute, Name: "todo-legacy", State: v1.AppChildPruning, Reason: "NotCurrent"}
	held := 0
	for _, ev := range appEvs {
		w, ok := ev.Object.(*v1.App)
		if !ok || w.Name != "todo" || w.Status.LatestRevision != "todo-2" || w.Status.CurrentRevision == "todo-2" {
			continue
		}
		held++
		require.Equal(t, v1.PhaseDeploying, w.Status.Phase)
		require.Contains(t, w.Status.Children, pruning)
		require.Less(t, rvOf(t, ev), deleted, "Route/todo-legacy outlives every write of the App while todo-2 is Deploying")
	}
	require.NotZero(t, held, "the App wrote its status while todo-2 was Deploying")
	require.Eventually(t, func() bool { return e.routed(t, todoHost, "/legacy") == http.StatusNotFound }, gcWithin, 20*time.Millisecond,
		"nothing routes /legacy")
}

// scenario: app-revision-read-only
func TestScenarioAppRevisionReadOnly(t *testing.T) {
	e := startGC(t)
	installTodo(t, e)
	before := e.appRevision(t, "todo-1")
	body, err := json.Marshal(before)
	require.NoError(t, err)
	coll := e.api + "/apis/funcd.io/v1alpha1/namespaces/default/apprevisions"
	for _, w := range []struct{ method, url string }{
		{http.MethodPost, coll},
		{http.MethodPut, coll + "/todo-1"},
		{http.MethodDelete, coll + "/todo-1"},
	} {
		req, err := http.NewRequestWithContext(context.Background(), w.method, w.url, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+funcd.DevToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "%s %s", w.method, w.url)
		require.Equal(t, []string{"GET"}, resp.Header.Values("Allow"), "%s %s", w.method, w.url)
	}

	const readOnly = "AppRevision is read-only: the App reconciler writes it"
	manifest := filepath.Join(t.TempDir(), "todo-1.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(`apiVersion: funcd.io/v1alpha1
kind: AppRevision
metadata:
  name: todo-1
  namespace: default
  resourceGroup: rg1
spec:
  app:
    kind: App
    name: todo
  number: 1
  spec:
    version: 9.9.9
`), 0o600))
	cli := buildCmd(t, "funcdctl")
	for _, args := range [][]string{{"apply", "-f", manifest}, {"delete", "apprevision", "todo-1"}} {
		out, err := exec.Command(cli, append([]string{"--server", e.api, "--token", funcd.DevToken}, args...)...).CombinedOutput()
		require.Error(t, err, "funcdctl %v: %s", args, out)
		require.Contains(t, string(out), readOnly, "funcdctl %v", args)
	}
	after := e.appRevision(t, "todo-1")
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "nothing changes")
	require.Equal(t, "1.0.0", after.Spec.Spec.Version)
}

// scenario: app-upgrade-timeout-config
func TestScenarioAppUpgradeTimeoutConfig(t *testing.T) {
	_, err := funcd.New(funcd.InMemory(), funcd.WithPacing(funcd.Pacing{AppUpgradeTimeout: time.Minute}))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "Pacing.AppUpgradeTimeout 1m0s must be more than BootTimeout 1m0s")

	dir := shortDataDir(t)
	cfg := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(cfg, fmt.Appendf(nil, "storage:\n  mode: memory\n  dataDir: %q\napp:\n  upgradeTimeout: 1m\n",
		filepath.Join(dir, "data")), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, buildCmd(t, "funcd"), "--config", cfg).CombinedOutput()
	require.NoError(t, ctx.Err(), "funcd exits by itself: %s", out)
	require.Error(t, err, "funcd does not start: %s", out)
	require.Contains(t, string(out), "app.upgradeTimeout")
	require.Contains(t, string(out), "runtime.bootTimeout")
	require.NotContains(t, string(out), "funcd starting")
}
