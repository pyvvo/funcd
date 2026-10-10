//go:build e2e

package funcd_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
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
	writeStep(t, e.src, "1.0.3", `export async function handle(event) { return event; }`)
	writeStep(t, e.src, "1.2.0", `export async function handle(event) { return event; }`)
	tpl.pushImages(t, tpl.reg)

	secretObj, _ := v1.NewObject(v1.KindSecret)
	secret := secretObj.(*v1.Secret)
	secret.Name, secret.Namespace, secret.ResourceGroup = "todo-stripe-key", "team-a", "todo"
	secret.Spec.Data = map[string][]byte{"STRIPE_API_KEY": []byte("sk_test"), "STRIPE_WEBHOOK_SECRET": []byte("whsec_test")}
	_, err := e.c.Apply(e.ctx, secret)
	require.NoError(t, err)
	return tpl
}

// pushImages pushes the fixture's images, one layout per repo, to reg: the step sources written by newTodoTemplate,
// and a site. Packing is reproducible, so each image has the same digest in every registry.
func (tpl *todoTemplate) pushImages(t *testing.T, reg string) {
	t.Helper()
	tpl.pushTo(t, reg, "todo-api", "1.0.0")
	tpl.pushTo(t, reg, "todo-planner", "1.0.0")
	tpl.pushTo(t, reg, "todo-migrate", "1.2.0")
	tpl.pushTo(t, reg, "todo-stats", "1.0.3")
	site := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.MkdirAll(site, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("<!doctype html><title>todo</title>"), 0o600))
	_, err := artifact.PushSite(context.Background(), "oci-layout://"+reg+"/todo-web:1.0.0", site)
	require.NoError(t, err)
}

// push pushes the step source named version (written by writeStep) as <registry>/<repo>:<version>.
func (tpl *todoTemplate) push(t *testing.T, repo, version string) {
	tpl.pushTo(t, tpl.reg, repo, version)
}

func (tpl *todoTemplate) pushTo(t *testing.T, reg, repo, version string) {
	t.Helper()
	require.Equal(t, "oci-layout://"+reg+"/"+repo+":"+version, pushStepImage(t, filepath.Join(reg, repo), tpl.e.src, version))
}

// appDir and values are the template's directory and one of its values files.
func (tpl *todoTemplate) appDir() string { return filepath.Join(tpl.dir, "app") }
func (tpl *todoTemplate) values(name string) string {
	return filepath.Join(tpl.dir, "values", name+".yaml")
}

// deploy sets the template's version and todo-api image, locks its images at the values/e2e.yaml registry, then runs
// funcdctl app deploy with values/prod.yaml and values/e2e.yaml and the extra args.
func (tpl *todoTemplate) deploy(t *testing.T, version, api string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(tpl.appDir(), "app.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // the test's own copy of the fixture
	require.NoError(t, err)
	data = templateVersion.ReplaceAll(data, []byte("version: "+version))
	data = templateAPI.ReplaceAll(data, []byte("  api: todo-api:"+api))
	require.NoError(t, os.WriteFile(path, data, 0o600))
	out, err := tpl.cli("app", "lock", tpl.appDir(), "-f", tpl.values("e2e"))
	require.NoError(t, err, out)
	return tpl.deployFrom(tpl.appDir(), args...)
}

// deployFrom runs funcdctl app deploy of src, a directory or a template ref, with values/prod.yaml and values/e2e.yaml
// and the extra args.
func (tpl *todoTemplate) deployFrom(src string, args ...string) (string, error) {
	return tpl.cli(append([]string{"app", "deploy", src, "--name", "todo", "-n", "team-a", "-f", tpl.values("prod"),
		"-f", tpl.values("e2e")}, args...)...)
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
		// The slow start (1.5 s) must stay under the boot timeout, so this subtest gives boot 5 s, not failedPacing's 2 s.
		e := startGC(t, funcd.WithPacing(funcd.Pacing{AppUpgradeTimeout: 10 * time.Second, BootTimeout: 5 * time.Second,
			ActivationTimeout: time.Second}), funcd.WithDevAuth(funcd.DevToken, "team-a"))
		tpl := newTodoTemplate(t, e)
		tpl.toTodo2(t)
		writeStep(t, e.src, "1.1.0", `await new Promise((r) => setTimeout(r, 1500));
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

// templateFiles reads every file under dir, keyed by its slash path.
func templateFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p) //nolint:gosec // a file of the test's own template
		files[filepath.ToSlash(rel)] = string(data)
		return err
	}))
	return files
}

// appImages is every image the App's spec names: its Functions', its Sites' and its Workflow steps'.
func appImages(app *v1.App) []string {
	var images []string
	for _, f := range app.Spec.Functions {
		images = append(images, f.Image)
	}
	for _, s := range app.Spec.Sites {
		images = append(images, s.Image)
	}
	for _, w := range app.Spec.Workflows {
		for _, s := range w.Steps {
			if s.Function != nil && s.Function.Image != "" {
				images = append(images, s.Function.Image)
			}
		}
	}
	return images
}

// scenario: app-template-pinned
func TestScenarioAppTemplatePinned(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithDevAuth(funcd.DevToken, "team-a"))
	tpl := newTodoTemplate(t, e)
	apiRef := "oci-layout://" + tpl.reg + "/todo-api:1.0.0"
	pinned, err := artifact.ResolveDigest(e.ctx, apiRef)
	require.NoError(t, err)

	out, err := tpl.cli("app", "lock", tpl.appDir(), "-f", tpl.values("e2e"))
	require.NoError(t, err, out)
	lock, err := template.ReadLock(tpl.appDir())
	require.NoError(t, err)
	require.Equal(t, template.LockedImage{Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: pinned}, lock["api"])

	ref := "oci-layout://" + tpl.reg + "/todo-app:1.2.0"
	out, err = tpl.cli("push", "--template", tpl.appDir(), ref)
	require.NoError(t, err, out)
	require.Regexp(t, `^`+regexp.QuoteMeta(ref)+`@sha256:[0-9a-f]{64}\n$`, out)
	digest := strings.TrimSuffix(strings.TrimPrefix(out, ref+"@"), "\n")
	pulled := filepath.Join(t.TempDir(), "pulled")
	require.NoError(t, artifact.PullTemplate(e.ctx, ref, digest, pulled))
	require.Equal(t, templateFiles(t, tpl.appDir()), templateFiles(t, pulled), "the pulled files equal ./app byte for byte")

	writeStep(t, e.src, "1.0.0", `export async function handle(event) { return { moved: true, event }; }`)
	tpl.push(t, "todo-api", "1.0.0")
	moved, err := artifact.ResolveDigest(e.ctx, apiRef)
	require.NoError(t, err)
	require.NotEqual(t, pinned, moved, "the tag todo-api:1.0.0 moved")

	out, err = tpl.deployFrom(ref)
	require.NoError(t, err, out)
	require.Contains(t, out, "template "+ref+"@"+digest)
	require.NotContains(t, out, "warning", "a ref source makes no moved-tag check")
	obj, _ := tpl.get(t, v1.KindFunction, "todo-api")
	fn := obj.(*v1.Function)
	require.Equal(t, apiRef+"@"+pinned, fn.Spec.Image)
	require.Eventually(t, func() bool {
		obj, _ := tpl.get(t, v1.KindFunction, "todo-api")
		cur := obj.(*v1.Function).Status.CurrentRevision
		if cur == "" {
			return false
		}
		rev, ok := tpl.get(t, v1.KindRevision, v1.ObjectName(cur))
		return ok && rev.(*v1.Revision).Spec.ImageDigest == pinned
	}, appWithin, 50*time.Millisecond, "todo-api's Revision runs the locked digest")

	out, err = tpl.deployFrom(tpl.appDir())
	require.NoError(t, err, out)
	require.Contains(t, out, "no change")
	for _, w := range []string{"warning: images.api", pinned, moved} {
		require.Contains(t, out, w, "only the directory deploy warns")
	}
	obj, _ = tpl.get(t, v1.KindFunction, "todo-api")
	require.Equal(t, apiRef+"@"+pinned, obj.(*v1.Function).Spec.Image)

	out, err = tpl.cli("push", "--template", tpl.appDir(), "oci-layout://"+tpl.reg+"/todo-app:1.3.0")
	require.Error(t, err, out)
	require.Contains(t, out, "1.3.0")
	require.Contains(t, out, "1.2.0")
	bare := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(bare, os.DirFS(tpl.appDir())))
	require.NoError(t, os.Remove(filepath.Join(bare, "app.lock")))
	out, err = tpl.cli("push", "--template", bare, "oci-layout://"+tpl.reg+"/todo-copy:1.2.0")
	require.Error(t, err, out)
	require.Contains(t, out, "app.lock")
	tags, err := artifact.ListTags(e.ctx, "oci-layout://"+tpl.reg+"/todo-app")
	require.NoError(t, err)
	require.Equal(t, []string{"1.2.0"}, tags, "neither refused push writes to the registry")
	require.NoDirExists(t, filepath.Join(tpl.reg, "todo-copy"))
}

// scenario: app-registry-value
func TestScenarioAppRegistryValue(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithDevAuth(funcd.DevToken, "team-a"))
	tpl := newTodoTemplate(t, e)
	origin := shortDataDir(t)
	tpl.pushImages(t, origin)
	require.NoError(t, os.WriteFile(tpl.values("origin"), []byte("registry: oci-layout://"+origin+"\n"), 0o600))
	out, err := tpl.cli("app", "lock", tpl.appDir(), "-f", tpl.values("origin"))
	require.NoError(t, err, out)
	lock, err := template.ReadLock(tpl.appDir())
	require.NoError(t, err)

	out, err = tpl.deployFrom(tpl.appDir())
	require.NoError(t, err, out)
	require.NotContains(t, out, "warning", "reproducible packing gives the same digests in both registries")
	obj, _ := tpl.get(t, v1.KindApp, "todo")
	images := appImages(obj.(*v1.App))
	require.NotEmpty(t, images)
	for _, img := range images {
		var want string
		for _, l := range lock {
			repo, _, _ := strings.Cut(l.Requested, ":")
			if strings.HasPrefix(img, "oci-layout://"+tpl.reg+"/"+repo+":") {
				want = "oci-layout://" + tpl.reg + "/" + repo + ":" + l.Version + "@" + l.Digest
			}
		}
		require.Equal(t, want, img, "every image renders at the registry value with the lock's digest")
	}
	var status v1.AppStatus
	require.Eventually(t, func() bool {
		obj, _ := tpl.get(t, v1.KindApp, "todo")
		status = obj.(*v1.App).Status
		return status.Phase == v1.PhaseReady && status.CurrentRevision == "todo-1"
	}, appWithin, 50*time.Millisecond, "the App becomes Ready: %+v", &status)
}
