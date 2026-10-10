//go:build e2e

package funcd_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// todoTemplateFixture is ADR-0217's to-do template, the one cmd/funcdctl's render scenarios read.
const todoTemplateFixture = "../../cmd/funcdctl/testdata/app-todo"

var (
	templateVersion = regexp.MustCompile(`(?m)^version: .*$`)
	templateAPI     = regexp.MustCompile(`(?m)^  api: todo-api:.*$`)
)

// todoTemplate is the to-do template installed against e in namespace team-a: a copy of the fixture the test edits,
// values/e2e.yaml naming a short oci-layout registry that holds the to-do images, and the CLI. values/e2e.yaml turns
// analytics off: its CatalogService needs the duckdb provider container, which this platform does not run. The test
// creates Secret todo-stripe-key, which the App declares and never creates (ADR-0213).
type todoTemplate struct {
	e   *gcEnv
	dir string
	reg string
	cli func(args ...string) (string, error)
}

func newTodoTemplate(t *testing.T, e *gcEnv) *todoTemplate {
	t.Helper()
	tpl := &todoTemplate{e: e, dir: t.TempDir(), reg: shortDataDir(t), cli: funcdctl(t, e)}
	require.NoError(t, os.CopyFS(tpl.dir, os.DirFS(todoTemplateFixture)))
	require.NoError(t, os.WriteFile(filepath.Join(tpl.dir, "values", "e2e.yaml"), []byte(
		"registry: oci-layout://"+tpl.reg+"\nhost: todo.e2e.test\nanalytics:\n  enabled: false\n"), 0o600))
	writeStep(t, e.src, "1.0.0", `export async function handle(event) { return event; }`)
	writeStep(t, e.src, "1.2.0", `export async function handle(event) { return event; }`)
	tpl.push(t, "todo-api", "1.0.0")
	tpl.push(t, "todo-planner", "1.0.0")
	tpl.push(t, "todo-migrate", "1.2.0")
	site := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.MkdirAll(site, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("<!doctype html><title>todo</title>"), 0o600))
	_, err := artifact.PushSite(context.Background(), "oci-layout://"+tpl.reg+"/todo-web:1.0.0", site)
	require.NoError(t, err)

	secretObj, _ := v1.NewObject(v1.KindSecret)
	secret := secretObj.(*v1.Secret)
	secret.Name, secret.Namespace, secret.ResourceGroup = "todo-stripe-key", "team-a", "todo"
	secret.Spec.Data = map[string][]byte{"STRIPE_API_KEY": []byte("sk_test"), "STRIPE_WEBHOOK_SECRET": []byte("whsec_test")}
	_, err = e.c.Apply(e.ctx, secret)
	require.NoError(t, err)
	return tpl
}

// push pushes the step source named version (written by writeStep) as <registry>/<repo>:<version>.
func (tpl *todoTemplate) push(t *testing.T, repo, version string) {
	t.Helper()
	require.Equal(t, "oci-layout://"+tpl.reg+"/"+repo+":"+version, pushStepImage(t, filepath.Join(tpl.reg, repo), tpl.e.src, version))
}

// deploy sets the template's version and todo-api image, then runs funcdctl app deploy with values/prod.yaml and
// values/e2e.yaml.
func (tpl *todoTemplate) deploy(t *testing.T, version, api string) (string, error) {
	t.Helper()
	path := filepath.Join(tpl.dir, "app", "app.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // the test's own copy of the fixture
	require.NoError(t, err)
	data = templateVersion.ReplaceAll(data, []byte("version: "+version))
	data = templateAPI.ReplaceAll(data, []byte("  api: todo-api:"+api))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return tpl.cli("app", "deploy", filepath.Join(tpl.dir, "app"), "--name", "todo", "-n", "team-a",
		"-f", filepath.Join(tpl.dir, "values", "prod.yaml"), "-f", filepath.Join(tpl.dir, "values", "e2e.yaml"))
}

// toTodo2 deploys versions 1.2.0 and 1.2.1, so todo-2 is current.
func (tpl *todoTemplate) toTodo2(t *testing.T) {
	t.Helper()
	out, err := tpl.deploy(t, "1.2.0", "1.0.0")
	require.NoError(t, err, out)
	require.True(t, strings.HasPrefix(out, "todo-1\n"), out)
	out, err = tpl.deploy(t, "1.2.1", "1.0.0")
	require.NoError(t, err, out)
	require.True(t, strings.HasPrefix(out, "todo-2\n"), out)
}

func (tpl *todoTemplate) get(t *testing.T, kind v1.Kind, name v1.ObjectName) (v1.Object, bool) {
	t.Helper()
	obj, err := tpl.e.c.Get(tpl.e.ctx, kind, "team-a", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj, true
}

// scenario: app-deploy-waits
func TestScenarioAppDeployWaits(t *testing.T) {
	t.Parallel()
	t.Run("current", func(t *testing.T) {
		t.Parallel()
		e := startGC(t, failedPacing(), funcd.WithDevAuth(funcd.DevToken, "team-a"))
		tpl := newTodoTemplate(t, e)
		tpl.toTodo2(t)
		writeStep(t, e.src, "1.1.0", `await new Promise((r) => setTimeout(r, 3000));
export async function handle(event) { return { image: "1.1.0", event }; }`)
		tpl.push(t, "todo-api", "1.1.0")

		out, err := tpl.deploy(t, "1.3.0", "1.1.0")
		require.NoError(t, err, out)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		require.Equal(t, "todo-3", lines[0], out)
		pending := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Function/todo-api Pending Progressing") })
		require.Positive(t, pending, out)
		require.Contains(t, lines[pending+1:], "Function/todo-api Ready", out)
		obj, _ := tpl.get(t, v1.KindApp, "todo")
		app := obj.(*v1.App)
		require.Equal(t, v1.ObjectName("todo-3"), app.Status.CurrentRevision)
		require.Equal(t, "1.3.0", app.Status.Version)

		out, err = tpl.deploy(t, "1.3.0", "1.1.0")
		require.NoError(t, err, out)
		require.True(t, strings.HasPrefix(out, "no change\n"), out)
		_, stamped := tpl.get(t, v1.KindAppRevision, "todo-4")
		require.False(t, stamped, "the same deploy stamps nothing")
	})

	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		e := startGC(t, failedPacing(), funcd.WithDevAuth(funcd.DevToken, "team-a"))
		tpl := newTodoTemplate(t, e)
		tpl.toTodo2(t)
		writeStep(t, e.src, "1.1.0", `await new Promise(() => setInterval(() => {}, 1000));
export async function handle() { return {}; }`)
		tpl.push(t, "todo-api", "1.1.0")

		out, err := tpl.deploy(t, "1.3.0", "1.1.0")
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "deploy fails: %v\n%s", err, out)
		require.Equal(t, 1, exit.ExitCode(), out)
		require.True(t, strings.HasPrefix(out, "todo-3\n"), out)
		require.Contains(t, out, "AppRevision todo-3 failed: ChildNotReady: Function/todo-api", out)
		obj, _ := tpl.get(t, v1.KindAppRevision, "todo-3")
		require.Equal(t, v1.PhaseFailed, obj.(*v1.AppRevision).Status.Phase)
	})
}

// scenario: app-delete-reports
func TestScenarioAppDeleteReports(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithDevAuth(funcd.DevToken, "team-a"))
	tpl := newTodoTemplate(t, e)
	out, err := tpl.deploy(t, "1.2.0", "1.0.0")
	require.NoError(t, err, out)
	_, ok := tpl.get(t, v1.KindFunction, "todo-plan-due")
	require.True(t, ok, "the step Function todo-plan-due exists")

	out, err = tpl.cli("app", "delete", "todo", "-n", "team-a")
	require.NoError(t, err, out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, want := range []string{
		"deleted Function/todo-api", "deleted Function/todo-plan-due", "deleted Workflow/todo-plan", "deleted Site/todo-web",
		"deleted Route/todo-api", "deleted Route/todo-web", "deleted EventSource/todo-plan-timer",
		"deleted Sensor/todo-plan-schedule", "deleted AppRevision/todo-1", "deleted Revision/todo-api-1",
	} {
		require.Contains(t, lines, want, out)
	}
	require.True(t, slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "deleted ConfigMap/todo-settings-") }), out)
	require.Equal(t, []string{"kept KVStore/todo-store", "kept Bucket/todo-files"}, lines[len(lines)-2:], out)
	_, ok = tpl.get(t, v1.KindFunction, "todo-plan-due")
	require.False(t, ok, "todo-plan-due is gone")
	revs, err := e.c.List(e.ctx, v1.KindRevision, "team-a")
	require.NoError(t, err)
	require.Empty(t, revs, "every Revision of the App's Functions is gone")
	for _, s := range []struct {
		kind v1.Kind
		name v1.ObjectName
	}{{v1.KindKVStore, "todo-store"}, {v1.KindBucket, "todo-files"}} {
		_, ok := tpl.get(t, s.kind, s.name)
		require.True(t, ok, "%s/%s is kept", s.kind, s.name)
	}
}
