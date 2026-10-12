//go:build dev

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
	sqlembedengine "github.com/pyvvo/funcd/internal/sql/embedengine"
)

// requireSQLEngine skips a test when this build embeds only the placeholder engine (ADR-0237 Decision 3).
func requireSQLEngine(t *testing.T) {
	t.Helper()
	requireRuntime(t)
	if !sqlembedengine.Bundled() {
		t.Skip("this build has no SQL engine: just build-sql-engine <os> <arch>, then go test -tags dev")
	}
}

// sqlProject is a function binding todo-db as app that adds a row to table notes and returns how many there are.
func sqlProject(t *testing.T) string {
	return devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  sql:\n    - alias: app\n      database: todo-db\n" + permissiveContract,
		"handler.mjs": `export async function handle(ctx) {
  await ctx.sql.execute('app', ['CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY)', 'INSERT INTO notes DEFAULT VALUES']);
  const [row] = await ctx.sql.query('app', 'SELECT count(*) AS n FROM notes');
  return { n: row.n };
}
`,
	})
}

// addNote calls the function once and returns the row count it read.
func addNote(t *testing.T, inst *devInstance) int {
	t.Helper()
	var n int
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, err := http.Post(inst.gatewayURL+"/function/"+inst.functions[0], "application/json", strings.NewReader(`{}`))
		if !assert.NoError(c, err) {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if assert.Equal(c, http.StatusOK, resp.StatusCode, "%s", body) {
			var out struct{ N int }
			assert.NoError(c, json.Unmarshal(body, &out))
			n = out.N
		}
	}, 60*time.Second, 200*time.Millisecond, "the function reaches its database")
	return n
}

func startSQLDev(t *testing.T, dir string, cfg devConfig) (*devInstance, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, "", cfg)
	require.NoError(t, err)
	var stopped bool
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			_ = inst.stop()
		}
	}
	t.Cleanup(stop)
	return inst, stop
}

// scenario: dev-function-binds-database — a function binding todo-db gets the database Ready, owned by it, and its
// context.sql writes and reads work.
func TestScenarioDevFunctionBindsDatabase(t *testing.T) {
	requireSQLEngine(t)
	inst, _ := startSQLDev(t, sqlProject(t), devConfig{cacheDir: t.TempDir()})
	require.Equal(t, 1, addNote(t, inst))
	obj, err := inst.client.Get(context.Background(), v1.KindSQLDatabase, devNamespace, "todo-db")
	require.NoError(t, err)
	db := obj.(*v1.SQLDatabase)
	require.Equal(t, v1.PhaseReady, db.Status.Phase)
	require.Equal(t, v1.ObjectName(inst.functions[0]), db.Spec.Owner)
}

// scenario: dev-data-persists-with-persist — with --persist the rows of one run are there in the next; without it each
// run starts empty.
func TestScenarioDevDataPersistsWithPersist(t *testing.T) {
	requireSQLEngine(t)
	dir, root := sqlProject(t), t.TempDir()
	for i, want := range []int{1, 2} {
		inst, stop := startSQLDev(t, dir, devConfig{persist: true, persistTo: root})
		require.Equal(t, want, addNote(t, inst), "--persist run %d", i+1)
		stop()
	}
	require.DirExists(t, filepath.Join(root, "sql", devNamespace, "todo-db"))
	for i := range 2 {
		inst, stop := startSQLDev(t, dir, devConfig{cacheDir: t.TempDir()})
		require.Equal(t, 1, addNote(t, inst), "run %d without --persist", i+1)
		stop()
	}
}

func savedSQLEngines(t *testing.T, stateDir string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "sql-engines.json"))
	if err != nil {
		return nil
	}
	var out []procreg.Entry
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// scenario: dev-crashed-engine-reaped — a `funcdctl dev --persist` run killed with SIGKILL leaves its engine running;
// the next run reaps it and serves the same data.
func TestScenarioDevCrashedEngineReaped(t *testing.T) {
	if runDevChild(t) {
		return
	}
	requireSQLEngine(t)
	dir, root := sqlProject(t), t.TempDir()
	spec := "persist\n" + root + "\n" + dir
	cfg, _ := crashConfig(spec)
	state, err := devStateDir(cfg, dir)
	require.NoError(t, err)
	reapAtCleanup(t, state)
	t.Cleanup(func() {
		if r, err := procreg.Open(state, "sql-engines"); err == nil {
			_, _ = r.Reap(context.Background(), time.Second)
			_ = r.Close()
		}
	})

	first := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	first.Env = append(os.Environ(), devCrashEnv+"="+spec)
	require.NoError(t, first.Start())
	var engines []procreg.Entry
	require.Eventually(t, func() bool { engines = savedSQLEngines(t, state); return len(engines) == 1 }, 60*time.Second,
		50*time.Millisecond, "the first run starts its engine")
	require.NoError(t, first.Process.Kill())
	_ = first.Wait()
	require.True(t, procreg.Alive(engines[0]), "the killed run left its engine running")

	inst, _ := startSQLDev(t, dir, cfg)
	require.False(t, procreg.Alive(engines[0]), "the next run reaped the old engine")
	require.Equal(t, 1, addNote(t, inst), "the new engine serves the database the old one created")
}

// scenario: dev-engine-unavailable — with the placeholder engine, a function that binds a database fails funcdctl dev
// at start, naming the build recipe.
func TestScenarioDevEngineUnavailable(t *testing.T) {
	if sqlembedengine.Bundled() {
		t.Skip("this build embeds a SQL engine")
	}
	_, err := (&cli{out: io.Discard}).startDev(context.Background(), sqlProject(t), "", devConfig{cacheDir: t.TempDir()})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorContains(t, err, "just build-sql-engine")
}

// scenario: dev-workflow-step-binds-database — step a writes todo-db as a granted writer, step b, its owner, reads
// the row a wrote, and the run succeeds.
func TestScenarioDevWorkflowStepBindsDatabase(t *testing.T) {
	requireSQLEngine(t)
	bind := "bindings:\n  sql:\n    - alias: app\n      database: todo-db\n"
	dir := devProject(t, map[string]string{
		"workflow.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Workflow\n" +
			"metadata:\n  name: notes\n  namespace: default\n" +
			"spec:\n  steps:\n" +
			"    - name: a\n      function:\n        ref: write\n" +
			"    - name: b\n      function:\n        ref: read\n      dependsOn:\n        - a\n",
		"write.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + bind + permissiveContract,
		"write.mjs": `export async function handle(ctx) {
  await ctx.sql.execute('app', ['CREATE TABLE IF NOT EXISTS notes (title TEXT)', ['INSERT INTO notes VALUES (?)', ['milk']]]);
  return {};
}
`,
		"read.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + bind + permissiveContract,
		"read.mjs": `export async function handle(ctx) {
  const rows = await ctx.sql.query('app', 'SELECT title FROM notes');
  if (rows.length !== 1 || rows[0].title !== 'milk') throw new Error('read ' + JSON.stringify(rows));
  return {};
}
`,
	})
	inst := startDevPath(t, filepath.Join(dir, "workflow.yaml"))
	waitFuncsReady(t, inst, "write", "read")
	ctx := context.Background()
	_, err := inst.client.Apply(ctx, &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "run-01", Namespace: devNamespace, ResourceGroup: devResourceGroup},
		Spec:       v1.WorkflowRunSpec{Workflow: "notes", Input: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	var run *v1.WorkflowRun
	require.Eventually(t, func() bool {
		obj, gerr := inst.client.Get(ctx, v1.KindWorkflowRun, devNamespace, "run-01")
		if gerr != nil {
			return false
		}
		run = obj.(*v1.WorkflowRun)
		return run.Status.Phase == "Succeeded" || run.Status.Phase == "Failed"
	}, 60*time.Second, 100*time.Millisecond, "the run ends")
	require.Equal(t, v1.RunPhase("Succeeded"), run.Status.Phase, "%+v", run.Status.Steps)
}

// Two functions whose main files share a directory each run under their own contract: in place they would share the
// directory's one contract dotfile, and the last one written would judge both. The run names the project as `.`, as
// `funcdctl dev .` does.
func TestDevFunctionsSharingAMainDirKeepTheirContracts(t *testing.T) {
	requireRuntime(t)
	contract := func(field string) string {
		return "contract:\n  input:\n    type: object\n    additionalProperties: false\n    properties:\n      " + field +
			":\n        type: string\n  output:\n    type: object\n    additionalProperties: false\n    properties:\n      " + field +
			":\n        type: string\n"
	}
	dir := devProject(t, map[string]string{
		"a.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\nmain: src/a.mjs\n" + contract("x"),
		"b.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\nmain: src/b.mjs\n" + contract("y"),
	})
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o700))
	for name, body := range map[string]string{
		"src/shared.mjs": "export const echo = (d) => d;\n",
		"src/a.mjs":      "import { echo } from './shared.mjs';\nexport function handle(ctx, e) { return echo(e.data); }\n",
		"src/b.mjs":      "import { echo } from './shared.mjs';\nexport function handle(ctx, e) { return echo(e.data); }\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	t.Chdir(dir)
	inst := startDevPath(t, ".")
	for fn, body := range map[string]string{"a": `{"data":{"x":"1"}}`, "b": `{"data":{"y":"2"}}`} {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			resp, err := http.Post(inst.gatewayURL+"/function/"+fn, "application/json", strings.NewReader(body))
			if !assert.NoError(c, err) {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			got, _ := io.ReadAll(resp.Body)
			assert.Equal(c, http.StatusOK, resp.StatusCode, "%s: %s", fn, got)
		}, 30*time.Second, 100*time.Millisecond, "function %s answers under its own contract", fn)
	}
}

// The steps of a workflow whose main files share a directory each run under their own contract too.
func TestDevWorkflowStepsSharingAMainDirKeepTheirContracts(t *testing.T) {
	requireRuntime(t)
	contract := func(in, out string) string {
		return "contract:\n  input:\n    type: object\n    additionalProperties: false\n    properties:\n      " + in +
			":\n        type: string\n  output:\n    type: object\n    additionalProperties: false\n    properties:\n      " + out +
			":\n        type: string\n"
	}
	dir := devProject(t, map[string]string{
		"workflow.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Workflow\nmetadata:\n  name: pair\n  namespace: default\n" +
			"spec:\n  steps:\n    - name: a\n      function:\n        ref: a\n    - name: b\n      function:\n        ref: b\n" +
			"      dependsOn:\n        - a\n",
		"a.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\nmain: src/a.mjs\n" + contract("x", "y"),
		"b.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\nmain: src/b.mjs\n" + contract("y", "z"),
	})
	require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "a.mjs"), []byte("export function handle(ctx, e) { return { y: e.data.x }; }\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "b.mjs"), []byte("export function handle(ctx, e) { return { z: e.data.y }; }\n"), 0o600))
	t.Chdir(dir)
	inst := startDevPath(t, "workflow.yaml")
	waitFuncsReady(t, inst, "a", "b")
	ctx := context.Background()
	_, err := inst.client.Apply(ctx, &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "run-01", Namespace: devNamespace, ResourceGroup: devResourceGroup},
		Spec:       v1.WorkflowRunSpec{Workflow: "pair", Input: json.RawMessage(`{"x":"1"}`)},
	})
	require.NoError(t, err)
	var run *v1.WorkflowRun
	require.Eventually(t, func() bool {
		obj, gerr := inst.client.Get(ctx, v1.KindWorkflowRun, devNamespace, "run-01")
		if gerr != nil {
			return false
		}
		run = obj.(*v1.WorkflowRun)
		return run.Status.Phase == "Succeeded" || run.Status.Phase == "Failed"
	}, 60*time.Second, 100*time.Millisecond, "the run ends")
	require.Equal(t, v1.RunPhase("Succeeded"), run.Status.Phase, "%+v", run.Status.Steps)
}

// The change feed works under funcdctl dev: an EventSource and a Sensor applied to the session see a row change, and
// the session's feed takes a free port, so a busy daemon default (7075) does not stop it.
func TestDevSQLChangeFeedReachesASensor(t *testing.T) {
	requireSQLEngine(t)
	if l, err := net.Listen("tcp", "127.0.0.1:7075"); err == nil {
		t.Cleanup(func() { _ = l.Close() })
	}
	bind := "bindings:\n  sql:\n    - alias: app\n      database: feed-db\n"
	dir := devProject(t, map[string]string{
		"items.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + bind + permissiveContract,
		"items.mjs": `export async function handle(ctx, e) {
  await ctx.sql.execute('app', ['CREATE TABLE IF NOT EXISTS items (name TEXT)', 'CREATE TABLE IF NOT EXISTS seen (name TEXT)']);
  if (e.data.name) await ctx.sql.execute('app', 'INSERT INTO items VALUES (?)', [e.data.name]);
  return { seen: (await ctx.sql.query('app', 'SELECT name FROM seen')).map((r) => r.name) };
}
`,
		"seen.funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + bind + permissiveContract,
		"seen.mjs": `export async function handle(ctx, e) {
  await ctx.sql.execute('app', 'INSERT INTO seen VALUES (?)', [e.data.after.name]);
  return {};
}
`,
	})
	inst, _ := startSQLDev(t, dir, devConfig{cacheDir: t.TempDir()})
	waitFuncsReady(t, inst, "items", "seen")
	post := func(body string) []string {
		var out struct{ Seen []string }
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			resp, err := http.Post(inst.gatewayURL+"/function/items", "application/json", strings.NewReader(body))
			if !assert.NoError(c, err) {
				return
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)
			if assert.Equal(c, http.StatusOK, resp.StatusCode, "%s", raw) {
				assert.NoError(c, json.Unmarshal(raw, &out))
			}
		}, 30*time.Second, 200*time.Millisecond)
		return out.Seen
	}
	post(`{"data":{}}`)
	ctx := context.Background()
	es := &v1.EventSource{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		ObjectMeta: v1.ObjectMeta{Name: "items-changes", Namespace: devNamespace, ResourceGroup: devResourceGroup},
		Spec:       v1.EventSourceSpec{SQL: &v1.SQLSource{Database: "feed-db", Events: []v1.SQLEvent{{Name: "added", Table: "items", On: []v1.SQLChangeType{v1.SQLInsert}}}}},
	}
	sensor := &v1.Sensor{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: "items-seen", Namespace: devNamespace, ResourceGroup: devResourceGroup},
		Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "added", Source: "items-changes", Event: "added"}},
			Do: []v1.Action{{Name: "record", On: "added", Function: "seen"}},
		},
	}
	for _, obj := range []v1.Object{es, sensor} {
		_, err := inst.client.Apply(ctx, obj)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		obj, err := inst.client.Get(ctx, v1.KindEventSource, devNamespace, "items-changes")
		return err == nil && obj.(*v1.EventSource).Status.Phase == v1.PhaseReady
	}, 30*time.Second, 100*time.Millisecond, "the EventSource watches feed-db")
	post(`{"data":{"name":"milk"}}`)
	require.Eventually(t, func() bool {
		return slices.Equal(post(`{"data":{}}`), []string{"milk"})
	}, 30*time.Second, 200*time.Millisecond, "the Sensor recorded the new row")
}

// A SQL binding added to a manifest while funcdctl dev runs is served after the hot reload, as a KV binding is.
func TestDevSQLBindingAddedWhileRunning(t *testing.T) {
	requireSQLEngine(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return { n: 0 }; }\n",
	})
	inst, _ := startSQLDev(t, dir, devConfig{cacheDir: t.TempDir()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "handler.mjs"), []byte(`export async function handle(ctx) {
  await ctx.sql.execute('app', ['CREATE TABLE IF NOT EXISTS t (x)', 'INSERT INTO t VALUES (1)']);
  const [row] = await ctx.sql.query('app', 'SELECT count(*) AS n FROM t');
  return { n: row.n };
}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte("runtime: nodejs22\nhandler: handle\n"+
		"bindings:\n  sql:\n    - alias: app\n      database: late-db\n"+permissiveContract), 0o600))
	require.GreaterOrEqual(t, addNote(t, inst), 1, "the reloaded function reaches its new database")
}
