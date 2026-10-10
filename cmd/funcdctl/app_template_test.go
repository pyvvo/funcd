package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// todoFixture is ADR-0217's to-do template: app/, values/prod.yaml and the App render prints for it.
const todoFixture = "testdata/app-todo"

// cliWithin bounds a deploy or delete wait in these tests, a few deployPoll intervals.
const cliWithin = 20 * time.Second

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// editFile replaces old with new in path, once.
func editFile(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a file of the test's own copy of the fixture
	require.NoError(t, err)
	require.Contains(t, string(data), old)
	writeFile(t, path, strings.Replace(string(data), old, new, 1))
}

// scenario: app-render-matches
func TestScenarioAppRenderMatches(t *testing.T) {
	t.Parallel()
	args := []string{"app", "render", filepath.Join(todoFixture, "app"), "--name", "todo", "-n", "team-a",
		"-f", filepath.Join(todoFixture, "values", "prod.yaml")}
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, args...))
	golden, err := os.ReadFile(filepath.Join(todoFixture, "render.golden.yaml"))
	require.NoError(t, err)
	require.Equal(t, string(golden), out.String())
	require.Contains(t, out.String(), "tick: ${{ event.type }}", "an expression without values, app or images comes out byte for byte")
	require.NotContains(t, out.String(), "@sha256:", "no digest until ADR-0218")

	obj, err := sdk.DecodeManifest(out.Bytes())
	require.NoError(t, err, "the printed App is a manifest funcdctl apply reads")
	app := obj.(*v1.App)
	require.Equal(t, v1.ResourceGroupName("todo"), app.ResourceGroup)
	require.Equal(t, "1.2.0", app.Spec.Version)
	require.True(t, slices.ContainsFunc(app.Spec.Functions, func(f v1.AppFunction) bool {
		return f.Name == "todo-stats" && f.Image == "registry.example/todo-stats:1.0.3"
	}), "analytics.enabled includes todo-stats")

	out.Reset()
	require.NoError(t, execCLI(&out, nil, append(args, "-o", "json")...))
	var asJSON v1.App
	require.NoError(t, json.Unmarshal(out.Bytes(), &asJSON))
	require.Equal(t, app.ObjectMeta, asJSON.ObjectMeta)
	want, err := json.Marshal(app.Spec)
	require.NoError(t, err)
	got, err := json.Marshal(asJSON.Spec)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got), "-o json prints the same App")
}

// scenario: app-render-refuses
func TestScenarioAppRenderRefuses(t *testing.T) {
	t.Parallel()
	type edit struct{ file, old, new string }
	for name, tc := range map[string]struct {
		values string // a values file given after values/prod.yaml
		alone  bool   // values is given without values/prod.yaml
		edit   edit
		want   []string
	}{
		"undeclared value": {values: "minReplica: 2\n", want: []string{"minReplica", "unevaluatedProperties"}},
		"mistyped value":   {values: "minReplicas: \"2\"\n", want: []string{"/minReplicas", "integer"}},
		"required by then": {values: "host: todo.example.com\nbackup:\n  enabled: true\n", alone: true, want: []string{"/backup", "target"}},
		"image": {edit: edit{"app/resources/api.yaml", "image: ${{ images.api }}", "image: registry.example/todo-api:1.0.0"},
			want: []string{"resources/api.yaml:5: functions[0].image"}},
		"digest": {edit: edit{"app/resources/api.yaml", "image: ${{ images.api }}\n", "image: ${{ images.api }}\n    imageDigest: sha256:4f1c\n"},
			want: []string{"resources/api.yaml:6: functions[0].imageDigest"}},
		"unknown key": {edit: edit{"app/resources/api.yaml", "handler: handle\n", "handler: handle\n    minReplica: 1\n"},
			want: []string{"resources/api.yaml: ", "minReplica"}},
		"range": {edit: edit{"app/app.yaml", "stats: todo-stats:1.0.3", "stats: todo-stats:^1.0.0"},
			want: []string{"images.stats", "ADR-0218"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.CopyFS(dir, os.DirFS(todoFixture)))
			args := []string{"app", "render", filepath.Join(dir, "app"), "--name", "todo", "-n", "team-a"}
			if !tc.alone {
				args = append(args, "-f", filepath.Join(dir, "values", "prod.yaml"))
			}
			if tc.values != "" {
				writeFile(t, filepath.Join(dir, "values", "bad.yaml"), tc.values)
				args = append(args, "-f", filepath.Join(dir, "values", "bad.yaml"))
			}
			if tc.edit.file != "" {
				editFile(t, filepath.Join(dir, tc.edit.file), tc.edit.old, tc.edit.new)
			}
			var out bytes.Buffer
			err := execCLI(&out, nil, args...)
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), err.Error())
			for _, w := range tc.want {
				require.ErrorContains(t, err, w)
			}
			require.Empty(t, out.String(), "a refused render prints nothing")
		})
	}
}

// deployTemplate writes a one-function template of App todo at version, its image at that version too.
func deployTemplate(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.yaml"),
		"name: todo\nversion: "+version+"\nregistry: oci-layout://layout\nimages:\n  api: todo-api:"+version+"\n")
	writeFile(t, filepath.Join(dir, "resources", "api.yaml"),
		"functions:\n  - name: todo-api\n    runtime: nodejs22\n    handler: handle\n    image: ${{ images.api }}\n")
	return dir
}

// renderedSpec is the spec deployTemplate's App renders to at version.
func renderedSpec(t *testing.T, version string) v1.AppSpec {
	t.Helper()
	tpl, err := template.Load(deployTemplate(t, version))
	require.NoError(t, err)
	app, err := template.Render(tpl, template.RenderInput{Namespace: "team-a"})
	require.NoError(t, err)
	return app.Spec
}

// cliRun is a funcdctl command running in the background, as a deploy or a delete waits.
type cliRun struct {
	out  lockedBuffer
	done chan struct{}
	err  error
}

func startCLI(t *testing.T, c *sdk.Client, args ...string) *cliRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &cliRun{done: make(chan struct{})}
	root := newRootCmdWith(&r.out, c)
	root.SetArgs(args)
	go func() {
		defer close(r.done)
		r.err = root.ExecuteContext(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-r.done
	})
	return r
}

// wait returns the command's error once it has returned.
func (r *cliRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(cliWithin):
		require.FailNow(t, "the command did not return", r.out.String())
		return nil
	}
}

// waitOut waits until the command has printed want.
func (r *cliRun) waitOut(t *testing.T, want string) {
	t.Helper()
	require.Eventually(t, func() bool { return strings.Contains(r.out.String(), want) }, cliWithin, 10*time.Millisecond,
		"the command prints %q", want)
}

// waitApplied waits until App todo holds version and returns it.
func waitApplied(t *testing.T, c *sdk.Client, version string) *v1.App {
	t.Helper()
	var app *v1.App
	require.Eventually(t, func() bool {
		obj, err := c.Get(context.Background(), v1.KindApp, "team-a", "todo")
		if err != nil {
			return false
		}
		app = obj.(*v1.App)
		return app.Spec.Version == version
	}, cliWithin, 10*time.Millisecond, "App todo holds version %s", version)
	return app
}

// setAppStatus writes App todo's status straight to the store, as the App reconciler does.
func setAppStatus(t *testing.T, st store.Store, edit func(*v1.AppStatus)) {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindApp.GVK(), "team-a", "todo")
	require.NoError(t, err)
	app := obj.(*v1.App)
	edit(&app.Status)
	_, err = st.Update(context.Background(), app)
	require.NoError(t, err)
}

func readyIs(status v1.ConditionStatus, reason string) v1.Conditions {
	return v1.Conditions{{Type: "Ready", Status: status, Reason: reason}}
}

// ADR-0217 Decision 8: deploy applies the rendered App, follows the revision that holds it and prints each change of
// its parts and of the App's Ready condition until that revision is current.
func TestCLIAppDeployFollowsRevision(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	run := startCLI(t, c, "app", "deploy", deployTemplate(t, "1.0.0"), "-n", "team-a")
	app := waitApplied(t, c, "1.0.0")
	require.Equal(t, v1.ResourceGroupName("todo"), app.ResourceGroup, "the group defaults to the App's name")
	require.Equal(t, "oci-layout://layout/todo-api:1.0.0", app.Spec.Functions[0].Image)

	seedAppRevision(t, st, "todo", app.UID, 1, app.Spec, v1.PhaseDeploying)
	setAppStatus(t, st, func(s *v1.AppStatus) {
		s.LatestRevision = "todo-1"
		s.Children = []v1.AppChild{{Kind: v1.KindFunction, Name: "todo-api", State: v1.AppChildPending, Reason: "Progressing"}}
		s.Conditions = readyIs(v1.ConditionFalse, "Progressing")
	})
	run.waitOut(t, "Function/todo-api Pending Progressing")
	setAppStatus(t, st, func(s *v1.AppStatus) {
		s.CurrentRevision = "todo-1"
		s.Children[0].State, s.Children[0].Reason = v1.AppChildReady, ""
		s.Conditions = readyIs(v1.ConditionTrue, "")
	})
	require.NoError(t, run.wait(t))
	require.Equal(t, "todo-1\nFunction/todo-api Pending Progressing\nApp/todo Ready=False Progressing\n"+
		"Function/todo-api Ready\nApp/todo Ready=True\n", run.out.String())
}

// ADR-0217 Decision 8: --no-wait returns once applied; a deploy whose spec both the App and its revision n0 hold
// prints no change, applies nothing and exits 0 once that revision is current.
func TestCLIAppDeployNoChange(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	dir := deployTemplate(t, "1.0.0")
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	require.Empty(t, out.String())
	app := waitApplied(t, c, "1.0.0")
	seedAppRevision(t, st, "todo", app.UID, 1, app.Spec, v1.PhaseReady)
	setAppStatus(t, st, func(s *v1.AppStatus) { s.LatestRevision, s.CurrentRevision = "todo-1", "todo-1" })
	rv := resourceVersion(t, c, v1.KindApp, "todo")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	require.Equal(t, "no change\n", out.String())
	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "deploy", dir, "-n", "team-a"))
	require.Equal(t, "no change\ntodo-1\n", out.String())
	require.Equal(t, rv, resourceVersion(t, c, v1.KindApp, "todo"), "nothing is applied")
}

// signalTransport closes seen on the first request whose path holds part, so a test acts only after the command
// has read what it decides on.
type signalTransport struct {
	part string
	once sync.Once
	seen chan struct{}
}

func (s *signalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, s.part) {
		s.once.Do(func() { close(s.seen) })
	}
	return http.DefaultTransport.RoundTrip(r)
}

// ADR-0217 Decision 8: a stored spec equal to the rendered one that no revision holds yet is not applied again and
// not reported as no change: deploy waits for its stamp.
func TestCLIAppDeployWaitsForStamp(t *testing.T) {
	t.Parallel()
	c, url, st := newTestServer(t)
	dir := deployTemplate(t, "1.0.0")
	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	app := waitApplied(t, c, "1.0.0")
	seedAppRevision(t, st, "todo", app.UID, 1, renderedSpec(t, "0.9.0"), v1.PhaseReady)
	setAppStatus(t, st, func(s *v1.AppStatus) { s.LatestRevision, s.CurrentRevision = "todo-1", "todo-1" })

	revisionsRead := &signalTransport{part: "apprevisions", seen: make(chan struct{})}
	sc, err := sdk.New(url, sdk.WithToken(devToken), sdk.WithHTTPClient(&http.Client{Transport: revisionsRead}))
	require.NoError(t, err)
	run := startCLI(t, sc, "app", "deploy", dir, "-n", "team-a")
	select {
	case <-revisionsRead.seen:
	case <-time.After(cliWithin):
		require.FailNow(t, "deploy never reads the AppRevisions")
	}
	seedAppRevision(t, st, "todo", app.UID, 2, app.Spec, v1.PhaseReady)
	setAppStatus(t, st, func(s *v1.AppStatus) {
		s.LatestRevision, s.CurrentRevision = "todo-2", "todo-2"
		s.Conditions = readyIs(v1.ConditionTrue, "")
	})
	require.NoError(t, run.wait(t))
	require.Equal(t, "todo-2\nApp/todo Ready=True\n", run.out.String(), "no change only once revision n0 holds the spec")
	require.Equal(t, app.Generation, waitApplied(t, c, "1.0.0").Generation, "nothing is applied")
}

// ADR-0217 Decision 8: an applied spec that the latest revision n0 holds while the stored App holds another is
// applied, and deploy follows n0, since the stamp then stamps nothing.
func TestCLIAppDeployFollowsLatest(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "deploy", deployTemplate(t, "2.0.0"), "-n", "team-a", "--no-wait"))
	app := waitApplied(t, c, "2.0.0")
	seedAppRevision(t, st, "todo", app.UID, 1, renderedSpec(t, "0.9.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo", app.UID, 2, renderedSpec(t, "1.0.0"), v1.PhaseDeploying)
	setAppStatus(t, st, func(s *v1.AppStatus) { s.LatestRevision, s.CurrentRevision = "todo-2", "todo-1" })

	run := startCLI(t, c, "app", "deploy", deployTemplate(t, "1.0.0"), "-n", "team-a")
	run.waitOut(t, "todo-2\n")
	require.Equal(t, app.Generation+1, waitApplied(t, c, "1.0.0").Generation, "the rendered spec is applied")
	setAppStatus(t, st, func(s *v1.AppStatus) { s.CurrentRevision = "todo-2" })
	require.NoError(t, run.wait(t))
	require.Equal(t, "todo-2\n", run.out.String())
}

// ADR-0217 Decision 8: deploy exits 1 when its revision fails, when the App's spec changes before a revision holds the
// applied one, or when the App is deleted.
func TestCLIAppDeployFails(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		act  func(t *testing.T, c *sdk.Client, st store.Store, app *v1.App)
		kind fault.Kind
		want []string
	}{
		"revision failed": {
			act: func(t *testing.T, _ *sdk.Client, st store.Store, app *v1.App) {
				r := seedAppRevision(t, st, "todo", app.UID, 1, app.Spec, v1.PhaseFailed)
				r.Status.Conditions = v1.Conditions{
					{Type: "ChildrenReady", Status: v1.ConditionFalse, Reason: "ChildNotReady", Message: "Function/todo-api: Pending: Progressing"},
					{Type: "Current", Status: v1.ConditionFalse, Reason: "ChildNotReady"},
				}
				_, err := st.Update(context.Background(), r)
				require.NoError(t, err)
				setAppStatus(t, st, func(s *v1.AppStatus) { s.LatestRevision = "todo-1" })
			},
			kind: fault.Conflict,
			want: []string{"todo-1", "ChildNotReady", "Function/todo-api"},
		},
		"spec changed": {
			act: func(t *testing.T, c *sdk.Client, _ store.Store, app *v1.App) {
				app.Spec.Version = "9.9.9"
				_, err := c.Apply(context.Background(), app)
				require.NoError(t, err)
			},
			kind: fault.Conflict,
			want: []string{"spec changed"},
		},
		"app deleted": {
			act: func(t *testing.T, c *sdk.Client, _ store.Store, _ *v1.App) {
				require.NoError(t, c.Delete(context.Background(), v1.KindApp, "team-a", "todo"))
			},
			kind: fault.NotFound,
			want: []string{"App todo was deleted"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _, st := newTestServer(t)
			run := startCLI(t, c, "app", "deploy", deployTemplate(t, "1.0.0"), "-n", "team-a")
			tc.act(t, c, st, waitApplied(t, c, "1.0.0"))
			err := run.wait(t)
			require.Error(t, err)
			require.Equal(t, tc.kind, fault.KindOf(err), err.Error())
			for _, w := range tc.want {
				require.ErrorContains(t, err, w)
			}
		})
	}
}

// ADR-0217 Decision 8 and ADR-0212 Decision 9: deploy copies the stored spec.paused into the applied spec, so a deploy
// that changes a paused App's spec keeps it paused, one with the same spec applies nothing, and a resume during the
// wait is no spec change.
func TestCLIAppDeployKeepsPaused(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "deploy", deployTemplate(t, "1.0.0"), "-n", "team-a", "--no-wait"))
	waitApplied(t, c, "1.0.0")
	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "pause", "todo", "-n", "team-a"))

	dir := deployTemplate(t, "2.0.0")
	run := startCLI(t, c, "app", "deploy", dir, "-n", "team-a")
	app := waitApplied(t, c, "2.0.0")
	want := renderedSpec(t, "2.0.0")
	want.Paused = true
	require.Equal(t, want, app.Spec, "a deploy never resumes an App")

	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	require.Equal(t, app.ResourceVersion, resourceVersion(t, c, v1.KindApp, "todo"), "the same spec is not applied")

	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "resume", "todo", "-n", "team-a"))
	seedAppRevision(t, st, "todo", app.UID, 1, app.Spec.WithoutPause(), v1.PhaseReady)
	setAppStatus(t, st, func(s *v1.AppStatus) { s.LatestRevision, s.CurrentRevision = "todo-1", "todo-1" })
	require.NoError(t, run.wait(t))
	require.Equal(t, "todo-1\n", run.out.String())
}

// ADR-0217 Decision 8: a --resource-group naming another group than the stored App's is refused, naming both.
func TestCLIAppDeployRefusesOtherGroup(t *testing.T) {
	t.Parallel()
	c, _, _ := newTestServer(t)
	dir := deployTemplate(t, "1.0.0")
	require.NoError(t, execCLI(&bytes.Buffer{}, c, "app", "deploy", dir, "-n", "team-a", "--no-wait"))
	var out bytes.Buffer
	err := execCLI(&out, c, "app", "deploy", dir, "-n", "team-a", "--resource-group", "other")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "other")
	require.ErrorContains(t, err, "todo")
	require.Empty(t, out.String())
}

// seedChild writes an object of kind owned by owner straight to the store, as a reconciler stamps it: controlled, or
// with the owner's marker only (a retain store).
func seedChild(t *testing.T, st store.Store, kind v1.Kind, name v1.ObjectName, owner v1.Object, controller bool) v1.Object {
	t.Helper()
	obj, ok := v1.NewObject(kind)
	require.True(t, ok)
	if wf, ok := obj.(*v1.Workflow); ok {
		wf.Spec.Steps = []v1.WorkflowStep{{Name: "due", Function: &v1.FunctionStep{Image: "oci-layout://layout/todo-planner:1.0.0"}}}
	}
	m := owner.GetObjectMeta()
	*obj.GetObjectMeta() = v1.ObjectMeta{Name: name, Namespace: "team-a", ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{{
		ObjectRef: v1.ObjectRef{Kind: owner.GroupVersionKind().Kind, Namespace: "team-a", Name: m.Name},
		UID:       m.UID, Controller: controller, BlockOwnerDeletion: controller,
	}}}
	created, err := st.Create(context.Background(), obj)
	require.NoError(t, err)
	return created
}

func storeDelete(t *testing.T, st store.Store, kind v1.Kind, names ...v1.ObjectName) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, st.Delete(context.Background(), kind.GVK(), "team-a", n, ""))
	}
}

// ADR-0217 Decision 9: delete notes the App's tree and its own stores, deletes the App, prints each object of the tree
// as it goes (a grandchild Revision and one made after the delete included) and what it still waits on, and ends
// with the stores left.
func TestCLIAppDeleteReports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, url, st := newTestServer(t)
	app := applyTodo(t, c, v1.AppSpec{
		KV:      []v1.AppKVStore{{Name: "todo-store"}, {Name: "todo-cache", Deletion: v1.DeletionDelete}, {Ref: "shared"}},
		Buckets: []v1.AppBucket{{Name: "todo-files"}, {Name: "todo-tmp", Deletion: v1.DeletionDelete}},
	})
	seedAppRevision(t, st, "todo", app.UID, 1, app.Spec, v1.PhaseReady)
	wf := seedChild(t, st, v1.KindWorkflow, "todo-plan", app, true)
	fn := seedChild(t, st, v1.KindFunction, "todo-plan-due", wf, true)
	seedChild(t, st, v1.KindRevision, "todo-plan-due-1", fn, true)
	seedChild(t, st, v1.KindKVStore, "todo-cache", app, true)
	seedChild(t, st, v1.KindBucket, "todo-tmp", app, true)
	seedChild(t, st, v1.KindKVStore, "todo-store", app, false)
	seedChild(t, st, v1.KindBucket, "todo-files", app, false)
	other := &v1.App{TypeMeta: v1.TypeMeta{Kind: v1.KindApp}, ObjectMeta: v1.ObjectMeta{Name: "other", UID: "uid-of-other"}}
	seedChild(t, st, v1.KindFunction, "other-api", other, true)
	shared, _ := v1.NewObject(v1.KindKVStore)
	shared.GetObjectMeta().Name, shared.GetObjectMeta().Namespace, shared.GetObjectMeta().ResourceGroup = "shared", "team-a", "rg1"
	_, err := st.Create(ctx, shared)
	require.NoError(t, err)

	viewer, err := sdk.New(url, sdk.WithToken(viewerToken))
	require.NoError(t, err)
	var out bytes.Buffer
	err = execCLI(&out, viewer, "app", "delete", "todo", "-n", "team-a")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a refused delete is surfaced: %v", err)
	require.Empty(t, out.String())
	require.Equal(t, fault.NotFound, fault.KindOf(execCLI(&out, c, "app", "delete", "nope", "-n", "team-a")))

	run := startCLI(t, c, "app", "delete", "todo", "-n", "team-a")
	require.Eventually(t, func() bool {
		_, err := c.Get(ctx, v1.KindApp, "team-a", "todo")
		return fault.KindOf(err) == fault.NotFound
	}, cliWithin, 10*time.Millisecond, "the App is deleted")
	seedChild(t, st, v1.KindRevision, "todo-plan-due-2", fn, true)
	storeDelete(t, st, v1.KindAppRevision, "todo-1")
	storeDelete(t, st, v1.KindWorkflow, "todo-plan")
	storeDelete(t, st, v1.KindKVStore, "todo-cache")
	storeDelete(t, st, v1.KindBucket, "todo-tmp")
	run.waitOut(t, "waiting Revision/todo-plan-due-2\n")
	storeDelete(t, st, v1.KindFunction, "todo-plan-due")
	storeDelete(t, st, v1.KindRevision, "todo-plan-due-1", "todo-plan-due-2")
	require.NoError(t, run.wait(t))

	got := run.out.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	for _, want := range []string{
		"deleted AppRevision/todo-1", "deleted Workflow/todo-plan", "deleted KVStore/todo-cache", "deleted Bucket/todo-tmp",
		"waiting Function/todo-plan-due", "waiting Revision/todo-plan-due-1", "waiting Revision/todo-plan-due-2",
		"deleted Function/todo-plan-due", "deleted Revision/todo-plan-due-1", "deleted Revision/todo-plan-due-2",
	} {
		require.Equal(t, 1, strings.Count(got, want+"\n"), "%q once in:\n%s", want, got)
	}
	require.Equal(t, []string{"kept KVStore/todo-store", "kept Bucket/todo-files"}, lines[len(lines)-2:], got)
	require.NotContains(t, got, "shared", "a ref store is not the App's")
	require.NotContains(t, got, "other-api", "an object of another App is not in the tree")
}

// ADR-0217 Decision 9: --no-wait returns once the App is deleted.
func TestCLIAppDeleteNoWait(t *testing.T) {
	t.Parallel()
	c, _, _ := newTestServer(t)
	applyTodo(t, c, todoSpec("1.0.0"))
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "delete", "todo", "-n", "team-a", "--no-wait"))
	require.Empty(t, out.String())
	_, err := c.Get(context.Background(), v1.KindApp, "team-a", "todo")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// ADR-0217 Decision 9: the tree's kinds are those gc.Pairs reaches from App, Secret excluded.
func TestAppTreeKinds(t *testing.T) {
	t.Parallel()
	reach := map[v1.Kind]bool{v1.KindApp: true}
	for grew := true; grew; {
		grew = false
		for _, p := range gc.Pairs() {
			if reach[p.Owner] && !reach[p.Child] {
				reach[p.Child], grew = true, true
			}
		}
	}
	delete(reach, v1.KindApp)
	delete(reach, v1.KindSecret)
	require.ElementsMatch(t, slices.Collect(maps.Keys(reach)), treeKinds())
	require.Contains(t, treeKinds(), v1.KindRevision, "a Function's Revisions are the App's grandchildren")
	require.NotContains(t, treeKinds(), v1.KindSecret)
}
