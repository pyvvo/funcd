//go:build dev

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	sqlembedengine "github.com/pyvvo/funcd/internal/sql/embedengine"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// todoToken is the API token the App tests put in TODO_API_TOKEN, and secretYAMLToken the one secret.yaml holds.
const (
	todoToken       = "dev-token"
	secretYAMLToken = "todo-lane-token"
)

// todoBuilds are dev-todo's --build flags at version v (ADR-0253 Scenarios), relative to the todo App's copy: its TS
// builds carry their version in their names, so api and migrate are named by key.
func todoBuilds(v string) []string {
	return []string{"functions/ts", "functions/py", "web=web", "api=functions/ts/api-" + v + ".mjs", "migrate=functions/ts/migrate-" + v + ".mjs"}
}

// todoRequest is a call of the todo App's /api.
type todoRequest struct {
	Op    string `json:"op"`
	Title string `json:"title,omitempty"`
	Token string `json:"token,omitempty"`
}

// todoHook is what a hook of the todo App recorded.
type todoHook struct {
	Event string `json:"event"`
	By    string `json:"by"`
}

// todoListing is the todo App's answer to {"op":"list"}.
type todoListing struct {
	Version   string            `json:"version"`
	Greeting  string            `json:"greeting"`
	Migration *todoHook         `json:"migration"`
	Release   *todoHook         `json:"release"`
	Todos     []json.RawMessage `json:"todos"`
}

// event is h's event, "" when the hook never ran.
func (h *todoHook) event() string {
	if h == nil {
		return ""
	}
	return h.Event
}

// appCopy copies App name of the pinned funcd-functions module into a temp dir, since the module cache is read-only,
// and returns the copy's root.
func appCopy(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.CopyFS(dst, os.DirFS(filepath.Join(langmod.Dir(t, langmod.Functions), "apps", name))))
	return dst
}

// appRun is one funcdctl dev App session of a test.
type appRun struct {
	inst *devInstance
	out  *lockedBuffer
	stop func()
}

// tryStartApp runs funcdctl dev on root/app with cfg, its values, --apply and --build paths taken relative to root.
// cacheDir defaults to a temp dir.
func tryStartApp(t *testing.T, root string, cfg devConfig) (*appRun, error) {
	t.Helper()
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	for i, v := range cfg.values {
		cfg.values[i] = abs(v)
	}
	for i, f := range cfg.apply {
		cfg.apply[i] = abs(f)
	}
	builds := make([]string, len(cfg.builds))
	for i, b := range cfg.builds {
		if key, path, ok := strings.Cut(b, "="); ok {
			builds[i] = key + "=" + abs(path)
		} else {
			builds[i] = abs(b)
		}
	}
	cfg.builds = builds
	if cfg.cacheDir == "" {
		cfg.cacheDir = t.TempDir()
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	inst, err := (&cli{out: out}).startDev(ctx, filepath.Join(root, "app"), cfg)
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	}
	t.Cleanup(stop)
	return &appRun{inst: inst, out: out, stop: stop}, err
}

// startApp is tryStartApp that requires the start to succeed and waits until the App serves /api, or its Site when
// it has no Route.
func startApp(t *testing.T, root string, cfg devConfig) *appRun {
	t.Helper()
	r, err := tryStartApp(t, root, cfg)
	require.NoError(t, err)
	return r
}

// requireApp skips a test of the todo App when node or python is missing.
func requireApp(t *testing.T) {
	t.Helper()
	requireNode(t)
	requirePython(t)
}

// todoCfg is dev-todo's config with extra applied to it.
func todoCfg(extra func(*devConfig)) devConfig {
	cfg := devConfig{builds: todoBuilds("1.0.0")}
	if extra != nil {
		extra(&cfg)
	}
	return cfg
}

// call posts body to the todo App's /api and returns the status and the decoded answer (nil unless 200).
func (r *appRun) call(t *testing.T, body todoRequest) (int, *todoListing) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(r.inst.gatewayURL+"/api", "application/json", strings.NewReader(string(b)))
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out todoListing
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, &out
}

// listing is the todo App's {"op":"list"} answer, the zero listing when /api does not answer 200.
func (r *appRun) listing(t *testing.T) todoListing {
	t.Helper()
	_, body := r.call(t, todoRequest{Op: "list"})
	if body == nil {
		return todoListing{}
	}
	return *body
}

// eventually waits until cond holds on the running App.
func (r *appRun) eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	require.Eventually(t, cond, 90*time.Second, 50*time.Millisecond, "%s; dev printed:\n%s", what, r.out)
}

// serves waits until /api answers.
func (r *appRun) serves(t *testing.T) {
	t.Helper()
	r.eventually(t, func() bool { _, b := r.call(t, todoRequest{Op: "list"}); return b != nil }, "/api answers")
}

// revision is the number of the App's latest revision.
func (r *appRun) revision(t *testing.T, name v1.ObjectName) int64 {
	t.Helper()
	app, err := storedApp(context.Background(), r.inst.client, devNamespace, name)
	require.NoError(t, err)
	require.NotNil(t, app)
	n, err := revisionNumber(app)
	require.NoError(t, err)
	return n
}

// settled waits until the App's latest revision is current and returns its number.
func (r *appRun) settled(t *testing.T, name v1.ObjectName) int64 {
	t.Helper()
	r.eventually(t, func() bool {
		app, err := storedApp(context.Background(), r.inst.client, devNamespace, name)
		return err == nil && app != nil && app.Status.LatestRevision != "" && app.Status.CurrentRevision == app.Status.LatestRevision
	}, "the App's latest revision is current")
	return r.revision(t, name)
}

// get reads url with host as its Host header, and returns the status and body.
func get(t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// replaceIn replaces the one occurrence of old in the file at path with new.
func replaceIn(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(b), old), "%s holds %q once", path, old)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o600))
}

// scenario: app-dev-runs-template — dev-todo serves /api from the local builds, the Site serves web/, and the banner
// names the App, its current revision, the Route's path and the Site's URL.
func TestScenarioAppDevRunsTemplate(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	r := startApp(t, root, todoCfg(nil))
	r.serves(t)
	status, _ := r.call(t, todoRequest{Op: "add", Title: "milk", Token: todoToken})
	require.Equal(t, http.StatusOK, status, "the App's Secret holds TODO_API_TOKEN")
	index, err := os.ReadFile(filepath.Join(root, "web", "index.html"))
	require.NoError(t, err)
	r.eventually(t, func() bool {
		code, body := get(t, r.inst.gatewayURL+"/site/todo-web/", "")
		return code == http.StatusOK && body == string(index)
	}, "the Site serves web/")
	r.settled(t, "todo")
	require.Contains(t, r.out.String(), "App todo deployed in ")

	var banner strings.Builder
	require.NoError(t, (&cli{out: &banner}).printBanner(r.inst))
	for _, want := range []string{"APP", "todo", "revision todo-", r.inst.gatewayURL + "/api", r.inst.gatewayURL + "/site/todo-web"} {
		require.Contains(t, banner.String(), want)
	}
	require.NotContains(t, banner.String(), "/function/", "the POST list is folder mode's")
}

// scenario: app-dev-reload — a save of a build, of a template file or of the values file stamps one new revision and
// the App serves the change.
func TestScenarioAppDevReload(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	r := startApp(t, root, todoCfg(func(c *devConfig) { c.values = []string{"values/greeting.yaml"} }))
	r.serves(t)
	n := r.settled(t, "todo")
	for _, tc := range []struct {
		what   string
		save   func()
		served func(todoListing) string
		want   string
	}{
		{"a build", func() {
			replaceIn(t, filepath.Join(root, "functions/ts/api-1.0.0.mjs"), `var version = "1.0.0";`, `var version = "1.0.0-saved";`)
		}, func(b todoListing) string { return b.Version }, "1.0.0-saved"},
		{"a template file", func() {
			replaceIn(t, filepath.Join(root, "app/app.yaml"), "default: hello from the todo App", "default: hello from app.yaml")
			require.NoError(t, os.WriteFile(filepath.Join(root, "values/greeting.yaml"), nil, 0o600))
		}, func(b todoListing) string { return b.Greeting }, "hello from app.yaml"},
		{"the values file", func() {
			require.NoError(t, os.WriteFile(filepath.Join(root, "values/greeting.yaml"), []byte("greeting: hello from values\n"), 0o600))
		}, func(b todoListing) string { return b.Greeting }, "hello from values"},
	} {
		tc.save()
		r.eventually(t, func() bool {
			return tc.served(r.listing(t)) == tc.want
		}, "the App serves "+tc.what+"'s change")
		next := r.settled(t, "todo")
		require.Equal(t, n+1, next, "a save of %s stamps one revision", tc.what)
		n = next
	}
}

// scenario: app-dev-hooks-opt-in — dev-todo calls no hook at start nor on a reload; under --hooks the install calls
// preApply then postApply, and each new revision calls them again.
func TestScenarioAppDevHooksOptIn(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	off := appCopy(t, "todo")
	r := startApp(t, off, todoCfg(nil))
	r.serves(t)
	replaceIn(t, filepath.Join(off, "functions/ts/api-1.0.0.mjs"), `var version = "1.0.0";`, `var version = "1.0.0-off";`)
	r.eventually(t, func() bool { return r.listing(t).Version == "1.0.0-off" }, "the reload serves")
	body := r.listing(t)
	require.Nil(t, body.Migration, "no preApply hook ran")
	require.Nil(t, body.Release, "no postApply hook ran")
	r.stop()

	on := appCopy(t, "todo")
	r = startApp(t, on, todoCfg(func(c *devConfig) { c.hooks = true }))
	r.eventually(t, func() bool {
		b := r.listing(t)
		return b.Migration.event() == "install" && b.Release.event() == "install"
	}, "the install calls preApply, then postApply")
	r.settled(t, "todo")
	replaceIn(t, filepath.Join(on, "app/app.yaml"), "default: hello from the todo App", "default: hello again")
	r.eventually(t, func() bool {
		b := r.listing(t)
		return b.Greeting == "hello again" && b.Migration.event() == "upgrade" && b.Release.event() == "upgrade"
	}, "the new revision calls the hooks again")
}

// upgradeTo110 runs dev-todo --hooks --persist at 1.0.0 with two todos, stops it, and swaps the template for 1.1.0.
func upgradeTo110(t *testing.T, root, state string) {
	t.Helper()
	r := startApp(t, root, todoCfg(func(c *devConfig) { c.hooks, c.persist, c.persistTo = true, true, state }))
	r.serves(t)
	for _, title := range []string{"milk", "passport"} {
		status, _ := r.call(t, todoRequest{Op: "add", Title: title, Token: todoToken})
		require.Equal(t, http.StatusOK, status)
	}
	r.settled(t, "todo")
	r.stop()
	b, err := os.ReadFile(filepath.Join(root, "app-1.1.0.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "app/app.yaml"), b, 0o600))
}

// scenario: app-dev-failed-revision — at 1.1.0 the preApply hook gets 403 for want of a grant: dev prints the reason
// naming the part, 1.0.0 keeps serving and dev keeps watching; once the grant is applied, app retry rolls 1.1.0 out.
func TestScenarioAppDevFailedRevision(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root, state := appCopy(t, "todo"), t.TempDir()
	upgradeTo110(t, root, state)
	r := startApp(t, root, devConfig{builds: todoBuilds("1.1.0"), hooks: true, persist: true, persistTo: state})
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "did not become current") }, "dev reports the failed revision")
	out := r.out.String()
	require.Contains(t, out, "save a fix, or run funcdctl app retry")
	require.Contains(t, out, "todo-migrate")
	require.Contains(t, out, "403")
	require.Equal(t, "1.0.0", r.listing(t).Version, "1.0.0 keeps serving")

	grants, err := os.ReadFile(filepath.Join(root, "grants.yaml"))
	require.NoError(t, err)
	grant := filepath.Join(t.TempDir(), "grant.yaml")
	require.NoError(t, os.WriteFile(grant, []byte(strings.SplitN(string(grants), "\n---\n", 2)[0]), 0o600))
	require.NoError(t, execCLI(io.Discard, r.inst.client, "apply", "-f", grant))
	var retried strings.Builder
	require.NoError(t, execCLI(&retried, r.inst.client, "app", "retry", "todo"))
	require.Equal(t, "retrying the failed hook of todo\n", retried.String())
	r.eventually(t, func() bool {
		b := r.listing(t)
		return b.Version == "1.1.0" && b.Migration != nil && b.Migration.By == "todo-migrate 1.1.0"
	}, "the retry rolls 1.1.0 out")
}

// scenario: app-dev-apply-files — with --apply grants.yaml the 1.1.0 hook rewrites table todos and 1.1.0 rolls out with
// no 403; a document removed from grants.yaml is deleted.
func TestScenarioAppDevApplyFiles(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root, state := appCopy(t, "todo"), t.TempDir()
	upgradeTo110(t, root, state)
	r := startApp(t, root, devConfig{builds: todoBuilds("1.1.0"), hooks: true, persist: true, persistTo: state,
		apply: []string{"grants.yaml"}})
	r.eventually(t, func() bool {
		b := r.listing(t)
		return b.Version == "1.1.0" && b.Migration != nil && b.Migration.By == "todo-migrate 1.1.0"
	}, "1.1.0 rolls out")
	require.NotContains(t, r.out.String(), "did not become current")
	grant, err := r.inst.client.Get(context.Background(), v1.KindRolesAssignment, devNamespace, "todo-intake-writes-db")
	require.NoError(t, err)
	require.Equal(t, devManagedBy, grant.GetObjectMeta().Tags[devManagedTag], "an --apply document carries dev's tag")

	grants := filepath.Join(root, "grants.yaml")
	b, err := os.ReadFile(grants)
	require.NoError(t, err)
	last := strings.LastIndex(string(b), "\n---\n")
	require.NoError(t, os.WriteFile(grants, b[:last+1], 0o600))
	r.eventually(t, func() bool {
		_, err := r.inst.client.Get(context.Background(), v1.KindRolesAssignment, devNamespace, "todo-intake-writes-db")
		return fault.KindOf(err) == fault.NotFound
	}, "dev deletes the document removed from grants.yaml")
	_, err = r.inst.client.Get(context.Background(), v1.KindRolesAssignment, devNamespace, "todo-migrate-writes-todos")
	require.NoError(t, err, "the documents still in grants.yaml stay")
}

// scenario: app-dev-apply-secret — with --apply secret.yaml and no TODO_API_TOKEN, the App uses that file's Secret and
// dev asks for no variable.
func TestScenarioAppDevApplySecret(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", "")
	require.NoError(t, os.Unsetenv("TODO_API_TOKEN"))
	root := appCopy(t, "todo")
	r := startApp(t, root, todoCfg(func(c *devConfig) { c.apply = []string{"secret.yaml"} }))
	r.serves(t)
	status, _ := r.call(t, todoRequest{Op: "add", Title: "milk", Token: secretYAMLToken})
	require.Equal(t, http.StatusOK, status, "the App reads the Secret of secret.yaml")
}

// scenario: app-dev-refused-apply — a fragment whose Workflow step binds a store nothing declares, saved into
// resources/, is refused: dev prints the refusal naming the part, and the last App keeps serving.
func TestScenarioAppDevRefusedApply(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	r := startApp(t, root, todoCfg(nil))
	r.serves(t)
	n := r.settled(t, "todo")
	const replan = `workflows:
  - name: ${{ app.name + "-replan" }}
    steps:
      - name: rank
        function:
          image: ${{ images.rank }}
          bindings:
            kv:
              - alias: todos
                store: todo-nowhere
                table: todos
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "app/resources/replan.yaml"), []byte(replan), 0o600))
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "reload failed") }, "dev prints the refusal")
	require.Contains(t, r.out.String(), "Workflow/todo-replan")
	require.NotEmpty(t, r.listing(t).Version, "the last App keeps serving")
	require.Equal(t, n, r.revision(t, "todo"), "the refused App stamps no revision")
}

// scenario: app-dev-persist — a --persist restart with the same builds stamps no revision, calls no hook and keeps the
// App's data.
func TestScenarioAppDevPersist(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root, state := appCopy(t, "todo"), t.TempDir()
	cfg := func() devConfig {
		return todoCfg(func(c *devConfig) { c.hooks, c.persist, c.persistTo = true, true, state })
	}
	r := startApp(t, root, cfg())
	r.serves(t)
	status, _ := r.call(t, todoRequest{Op: "add", Title: "kept", Token: todoToken})
	require.Equal(t, http.StatusOK, status)
	n := r.settled(t, "todo")
	r.stop()

	r = startApp(t, root, cfg())
	r.serves(t)
	require.Contains(t, r.out.String(), "App todo deployed in ")
	require.Equal(t, n, r.revision(t, "todo"), "the restart stamps no revision")
	body := r.listing(t)
	require.Equal(t, "install", body.Migration.event(), "no hook ran again")
	require.Len(t, body.Todos, 1, "the App's data is kept")
}

// scenario: app-dev-site-only — the site-config App, a Site and no Function, serves its generated config.json.
func TestScenarioAppDevSiteOnly(t *testing.T) {
	t.Parallel()
	root := appCopy(t, "site-config")
	r := startApp(t, root, devConfig{values: []string{"values/staging.yaml"}, builds: []string{"web=web"}})
	const host = "site-config.staging.test"
	var body string
	r.eventually(t, func() bool {
		var code int
		code, body = get(t, r.inst.gatewayURL+"/config.json", host)
		return code == http.StatusOK
	}, "the Site serves config.json")
	var conf map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &conf))
	require.Equal(t, "https://api.staging.example", conf["API_URL"])
	require.Equal(t, "false", conf["NEW_DASHBOARD"])
}

// scenario: app-dev-refusals — dev stops before the platform starts, naming what to fix.
func TestScenarioAppDevRefusals(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	writeProject(t, root, map[string]string{
		"sql-values.yaml": "sql: true\n",
		"other-ns.yaml":   "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: x\n  namespace: other\nspec:\n  data:\n    a: b\n",
		"an-app.yaml":     "apiVersion: funcd.io/v1alpha1\nkind: App\nmetadata:\n  name: other\nspec:\n  version: 1.0.0\n",
	})
	without := func(drop string) []string {
		var out []string
		for _, b := range todoBuilds("1.0.0") {
			if b != drop {
				out = append(out, b)
			}
		}
		return out
	}
	cases := []struct {
		name    string
		cfg     devConfig
		noToken bool
		want    string
	}{
		{name: "no TODO_API_TOKEN", cfg: todoCfg(nil), noToken: true, want: "set the environment variable TODO_API_TOKEN"},
		{name: "no api= entry", cfg: devConfig{builds: without("api=functions/ts/api-1.0.0.mjs")}, want: "no build of images.api"},
		{name: "no web=web", cfg: devConfig{builds: without("web=web")}, want: "is a Site's image"},
		{name: "requires", cfg: todoCfg(func(c *devConfig) { c.values = []string{"values/shared.yaml"} }),
			want: "does not run an App that requires another"},
		{name: "another namespace", cfg: todoCfg(func(c *devConfig) { c.apply = []string{"other-ns.yaml"} }), want: "namespace"},
		{name: "an App document", cfg: todoCfg(func(c *devConfig) { c.apply = []string{"an-app.yaml"} }), want: "template directory"},
	}
	if !sqlembedengine.Bundled() {
		cases = append(cases, struct {
			name    string
			cfg     devConfig
			noToken bool
			want    string
		}{name: "sql without the engine", cfg: todoCfg(func(c *devConfig) { c.values = []string{"sql-values.yaml"} }), want: "has no SQL engine"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.noToken {
				t.Setenv("TODO_API_TOKEN", "")
				require.NoError(t, os.Unsetenv("TODO_API_TOKEN"))
			}
			r, err := tryStartApp(t, root, tc.cfg)
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, r.inst, "dev stops before the platform starts")
		})
	}
	t.Run("App flags on a folder", func(t *testing.T) {
		_, err := (&cli{out: io.Discard}).startDev(context.Background(), filepath.Join(root, "functions/ts"),
			devConfig{hooks: true, cacheDir: t.TempDir()})
		require.ErrorContains(t, err, "run an App template")
	})
}

// scenario: dev-folder-unchanged — a folder of resource files runs as ADR-0247 says, and still stops on an App document.
func TestScenarioDevFolderUnchanged(t *testing.T) {
	requireNode(t)
	t.Parallel()
	inst := runDev(t, devProject(t, nodeFunction("counter", "", "export function handle() { return { ok: true } }\n")))
	waitReady(t, inst)
	require.Nil(t, inst.app, "a folder runs in folder mode")
	eventuallyServes(t, inst.gatewayURL+"/function/counter", "{}", http.StatusOK, `"ok":true`)
	_, err := tryStartDev(t, devProject(t, map[string]string{
		"app.yaml": "apiVersion: funcd.io/v1alpha1\nkind: App\nmetadata:\n  name: todo\nspec:\n  version: 1.0.0\n",
	}))
	require.ErrorContains(t, err, "runs an App from its template directory")
}

// The build lookup: a key=path file or bundle directory, the first --build folder that holds the key, and a Site
// named by key=dir only.
func TestAppDevBuildLookup(t *testing.T) {
	t.Parallel()
	root := appCopy(t, "todo")
	s := &devAppSession{cfg: devConfig{builds: []string{
		filepath.Join(root, "functions/ts"), "api=" + filepath.Join(root, "functions/ts/api-1.1.0.mjs"), "web=" + filepath.Join(root, "web"),
	}}}
	api := &devImage{part: partFunction, runtime: "nodejs22"}
	require.NoError(t, s.resolveImage("test", "api", api))
	require.Equal(t, "api-1.1.0.mjs", api.build.entry, "key=path names the build")
	collect := &devImage{part: partStep}
	require.NoError(t, s.resolveImage("test", "collect", collect))
	require.Equal(t, "collect.mjs", collect.build.entry, "a --build folder holds <key>.mjs")
	web := &devImage{part: partSite}
	require.NoError(t, s.resolveImage("test", "web", web))
	require.Equal(t, filepath.Join(root, "web"), web.site)
	require.ErrorContains(t, s.resolveImage("test", "migrate", &devImage{part: partFunction, runtime: "nodejs22"}), "no build of images.migrate")

	bare := &devAppSession{cfg: devConfig{builds: []string{root}}}
	require.ErrorContains(t, bare.resolveImage("test", "web", &devImage{part: partSite}), "is a Site's image")
	_, err := buildAt("test", "api", filepath.Join(root, "functions/ts/nope.mjs"))
	require.Error(t, err)
	_, err = buildAt("test", "site", filepath.Join(root, "web"))
	require.Error(t, err, "a directory without a contract is no bundle")
}

// The stand-in render: it learns which images the App's parts use and what each serves, without a build.
func TestAppDevStandInRender(t *testing.T) {
	t.Parallel()
	root := appCopy(t, "todo")
	s := &devAppSession{dir: filepath.Join(root, "app"), stage: t.TempDir()}
	tpl, err := s.loadTemplate("test")
	require.NoError(t, err)
	tpl.Registry = "oci-layout://" + t.TempDir()
	used, lock, app, err := probeImages(tpl, template.RenderInput{Namespace: devNamespace})
	require.NoError(t, err)
	require.Equal(t, partFunction, used["api"].part)
	require.Equal(t, partSite, used["web"].part)
	require.Equal(t, partStep, used["collect"].part)
	require.Equal(t, "1.0.0", lock["api"].Version)
	require.Equal(t, "todo-api", appKeypairName(app))

	site := appCopy(t, "site-config")
	ss := &devAppSession{dir: filepath.Join(site, "app"), stage: t.TempDir()}
	stpl, err := ss.loadTemplate("test")
	require.NoError(t, err)
	stpl.Registry = "oci-layout://" + t.TempDir()
	_, _, sapp, err := probeImages(stpl, template.RenderInput{Namespace: devNamespace,
		Values: []json.RawMessage{json.RawMessage(`{"host":"h.test","api":{"url":"https://a.test"}}`)}})
	require.NoError(t, err)
	require.Equal(t, devAppKeypairName, appKeypairName(sapp), "an App with no function part")
}

// Decision 3: dev skips dot-entries and its persist dir in the template directory, and refuses any other entry.
func TestAppDevTemplateEntries(t *testing.T) {
	t.Parallel()
	root := appCopy(t, "todo")
	dir := filepath.Join(root, "app")
	persist := filepath.Join(dir, "state")
	writeProject(t, dir, map[string]string{".editorconfig": "root = true\n", "state/process/x": "", "app.lock": "garbage"})
	s := &devAppSession{dir: dir, stage: t.TempDir(), persist: persist}
	_, err := s.loadTemplate("test")
	require.NoError(t, err, "dot-entries, the persist dir and app.lock are skipped")
	writeProject(t, dir, map[string]string{"notes.txt": "x"})
	_, err = s.loadTemplate("test")
	require.ErrorContains(t, err, "notes.txt: a template holds only app.yaml, app.lock and resources/*.yaml")
}
