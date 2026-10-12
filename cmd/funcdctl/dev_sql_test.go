//go:build dev

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
