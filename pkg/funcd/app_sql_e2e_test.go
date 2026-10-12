//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/testkit/fakesql"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// strictEngines is a SQL runtime whose engines are strict fakesql engines, one per database; a database in notReady
// never gets a Ready engine.
type strictEngines struct {
	t        *testing.T
	mu       sync.Mutex
	engines  map[v1.ObjectName]*fakesql.Engine
	notReady map[v1.ObjectName]bool
}

func newStrictEngines(t *testing.T) *strictEngines {
	return &strictEngines{t: t, engines: map[v1.ObjectName]*fakesql.Engine{}, notReady: map[v1.ObjectName]bool{}}
}

func (s *strictEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notReady[spec.Ref.Name] {
		return provider.ProviderStatus{Reason: "Starting"}, nil
	}
	e, ok := s.engines[spec.Ref.Name]
	if !ok {
		e = fakesql.NewStrict(s.t, spec.Env["FUNCD_SQL_PASSWORD"])
		s.engines[spec.Ref.Name] = e
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: e.Addr()}, nil
}

func (*strictEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }
func (*strictEngines) Reclaim(context.Context, provider.ProviderRef) error  { return nil }
func (*strictEngines) List(context.Context) ([]provider.ProviderRef, error) { return nil, nil }

func (s *strictEngines) engine(t *testing.T, name v1.ObjectName) *fakesql.Engine {
	t.Helper()
	var e *fakesql.Engine
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		e = s.engines[name]
		return e != nil
	}, appWithin, 20*time.Millisecond, "the engine of %s starts", name)
	return e
}

// migration is one numbered migration the hook ships.
type migration struct{ version, sql string }

// sqlMigrateImage pushes a migration hook: it reads _migrations and applies, in one all-or-nothing execute, each
// version it ships that is not there yet with its _migrations row (ADR-0236 Decision 5).
func (e *gcEnv) sqlMigrateImage(t *testing.T, name string, ms ...migration) string {
	t.Helper()
	pairs := make([][2]string, len(ms))
	for i, m := range ms {
		pairs[i] = [2]string{m.version, m.sql}
	}
	list, err := json.Marshal(pairs)
	require.NoError(t, err)
	writeStep(t, e.src, name, fmt.Sprintf(`const migrations = %s;
export async function handle(ctx) {
  await ctx.sql.execute('app', 'CREATE TABLE IF NOT EXISTS _migrations (version TEXT PRIMARY KEY)');
  const done = new Set((await ctx.sql.query('app', 'SELECT version FROM _migrations')).map((r) => r.version));
  const stmts = [];
  for (const [v, s] of migrations) {
    if (!done.has(v)) stmts.push(s, ['INSERT INTO _migrations (version) VALUES (?)', [v]]);
  }
  if (stmts.length > 0) await ctx.sql.execute('app', stmts);
  return { applied: stmts.length / 2 };
}`, list))
	return pushStepImage(t, e.layout, e.src, name)
}

// sqlAPIImage pushes todo-api: it reads the todos the migration created.
func (e *gcEnv) sqlAPIImage(t *testing.T) string {
	t.Helper()
	writeStep(t, e.src, "sql-api", `export async function handle(ctx) {
  return { todos: await ctx.sql.query('app', 'SELECT id, title FROM todos') };
}`)
	return pushStepImage(t, e.layout, e.src, "sql-api")
}

func createTodos() migration {
	return migration{"0001", "CREATE TABLE todos (id INTEGER PRIMARY KEY, title TEXT NOT NULL, done INTEGER NOT NULL)"}
}

// sqlApp is ADR-0236's fixture: App sqltodo with todo-db owned by todo-api, todo-api and the migration hook
// todo-migrate binding it, the hook a preApply hook.
func sqlApp(version, api, migrate string) *v1.App {
	bind := []v1.FunctionSQL{{Alias: "app", Database: "todo-db"}}
	return &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "sqltodo", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.AppSpec{
			Version: version,
			SQL:     []v1.AppSQLDatabase{{Name: "todo-db", SQLDatabaseSpec: v1.SQLDatabaseSpec{Owner: "todo-api"}}},
			Functions: []v1.AppFunction{
				{Name: "todo-api", FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: api, SQL: bind,
					Scaling: v1.Scaling{MinReplicas: 1}}},
				{Name: "todo-migrate", FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: migrate, SQL: bind}},
			},
			Hooks: &v1.AppHooks{PreApply: []v1.AppHook{{Function: "todo-migrate"}}},
		},
	}
}

// migrateWritesDB is the RolesAssignment beside the App: todo-migrate writes todo-db, which todo-api owns.
func migrateWritesDB() *v1.RolesAssignment {
	return &v1.RolesAssignment{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindRolesAssignment.GVK().APIVersion(), Kind: v1.KindRolesAssignment},
		ObjectMeta: v1.ObjectMeta{Name: "todo-migrate-writes-db", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{Assignments: []v1.AssignmentEntry{{
			Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "todo-migrate"},
			RoleRef:   v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "SQL Data Writer"},
			Scope:     &v1.ScopeRef{Kind: v1.ScopeKindSQLDatabase, Name: "todo-db"},
		}}},
	}
}

func (e *gcEnv) waitSQLApp(t *testing.T, rev v1.ObjectName) {
	t.Helper()
	require.Eventually(t, func() bool { return e.app(t, "sqltodo").Status.CurrentRevision == rev },
		appWithin, 20*time.Millisecond, "the App sqltodo's currentRevision is %s", rev)
	e.waitApp(t, "sqltodo", v1.ConditionTrue, "", appWithin)
}

// installSQL installs the fixture with the 0001 migration and waits until sqltodo-1 is current.
func installSQL(t *testing.T, e *gcEnv) *v1.App {
	t.Helper()
	e.apply(t, migrateWritesDB())
	a := sqlApp("1.0.0", e.sqlAPIImage(t), e.sqlMigrateImage(t, "migrate-1", createTodos()))
	e.apply(t, a)
	e.waitSQLApp(t, "sqltodo-1")
	return a
}

// scenario: app-installs-database — the App's sql entry makes SQLDatabase todo-db, owned by the App, with its owner.
func TestScenarioAppInstallsDatabase(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	installSQL(t, e)
	db := e.object(t, v1.KindSQLDatabase, "todo-db").(*v1.SQLDatabase)
	require.Equal(t, v1.ObjectName("todo-api"), db.Spec.Owner)
	marked, controlled := appRefs(db, e.app(t, "sqltodo"))
	require.True(t, marked, "the App marks its database")
	require.False(t, controlled, "deletion retain: no controller reference")
}

// scenario: migration-runs-before-serving — the hook runs once todo-db is Ready and before todo-api serves, and
// todo-api's first SELECT on the new table succeeds.
func TestScenarioMigrationRunsBeforeServing(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	installSQL(t, e)
	db := engines.engine(t, "todo-db")
	require.Equal(t, []string{"0001"}, db.Migrations())
	log := db.Log()
	recorded := indexOf(log, "INSERT INTO _migrations (version) VALUES (?)")
	require.Less(t, recorded, len(log), "the hook recorded 0001")
	require.NotContains(t, log[:recorded], "SELECT id, title FROM todos",
		"nothing read todos before the migration")
	require.Equal(t, http.StatusOK, e.invoke(t, "todo-api"), "todo-api reads the new table")
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return len(xs)
}

// scenario: upgrade-runs-pending-migrations — version 2 ships 0001 and 0002, and the hook applies only 0002.
func TestScenarioUpgradeRunsPendingMigrations(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	a := installSQL(t, e)
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[1].Image = e.sqlMigrateImage(t, "migrate-2", createTodos(), migration{"0002", "ALTER TABLE todos ADD COLUMN note TEXT"})
	e.apply(t, a)
	e.waitSQLApp(t, "sqltodo-2")
	db := engines.engine(t, "todo-db")
	require.Equal(t, []string{"0001", "0002"}, db.Migrations())
	require.Len(t, db.DDL(), 3, "_migrations, todos once, the new column once")
}

// scenario: database-not-ready-fails-rollout — a hook whose database never turns Ready fails the rollout at
// app.upgradeTimeout, HookFailed naming the database and its Ready reason.
func TestScenarioDatabaseNotReadyFailsRollout(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	engines.notReady["todo-db"] = true
	e := startGC(t, funcd.WithSQLProviderRuntime(engines), funcd.WithPacing(funcd.Pacing{
		AppUpgradeTimeout: 3 * time.Second, BootTimeout: 2 * time.Second, ActivationTimeout: time.Second,
	}))
	e.apply(t, migrateWritesDB())
	e.apply(t, sqlApp("1.0.0", e.sqlAPIImage(t), e.sqlMigrateImage(t, "migrate-1", createTodos())))
	require.Eventually(t, func() bool {
		if !e.exists(t, v1.KindAppRevision, "sqltodo-1") {
			return false
		}
		rev := e.appRevision(t, "sqltodo-1")
		c := condition(rev, "Applied")
		return rev.Status.Phase == v1.PhaseFailed && c.Reason == "HookFailed" && c.Message == "SQLDatabase/todo-db: Starting"
	}, appWithin, 50*time.Millisecond, "the rollout fails HookFailed, naming todo-db and its Ready reason")
}

// scenario: failed-migration-stops-rollout — a migration that fails stops the rollout and records nothing.
func TestScenarioFailedMigrationStopsRollout(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	a := installSQL(t, e)
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[1].Image = e.sqlMigrateImage(t, "migrate-bad", createTodos(), migration{"0002", "THIS IS NOT SQL"})
	e.apply(t, a)
	require.Eventually(t, func() bool {
		if !e.exists(t, v1.KindAppRevision, "sqltodo-2") {
			return false
		}
		rev := e.appRevision(t, "sqltodo-2")
		return rev.Status.Phase == v1.PhaseFailed && condition(rev, "Applied").Reason == "HookFailed"
	}, appWithin, 50*time.Millisecond, "the revision does not roll out")
	require.Equal(t, v1.ObjectName("sqltodo-1"), e.app(t, "sqltodo").Status.CurrentRevision)
	require.Equal(t, []string{"0001"}, engines.engine(t, "todo-db").Migrations(), "the failed batch recorded nothing")
}

// scenario: deletion-policy-retain — a deleted App leaves a retained database; with deletion: delete it goes.
func TestScenarioDeletionPolicyRetain(t *testing.T) {
	t.Parallel()
	for _, policy := range []v1.DeletionPolicy{v1.DeletionRetain, v1.DeletionDelete} {
		t.Run(string(policy), func(t *testing.T) {
			t.Parallel()
			engines := newStrictEngines(t)
			e := startGC(t, funcd.WithSQLProviderRuntime(engines))
			e.apply(t, migrateWritesDB())
			a := sqlApp("1.0.0", e.sqlAPIImage(t), e.sqlMigrateImage(t, "migrate-1", createTodos()))
			a.Spec.SQL[0].Deletion = policy
			e.apply(t, a)
			e.waitSQLApp(t, "sqltodo-1")
			e.del(t, v1.KindApp, "sqltodo")
			e.waitGone(t, v1.KindFunction, "todo-api", "todo-migrate")
			if policy == v1.DeletionDelete {
				e.waitGone(t, v1.KindSQLDatabase, "todo-db")
				return
			}
			stays(t, func() bool { return e.exists(t, v1.KindSQLDatabase, "todo-db") }, 2*time.Second, 100*time.Millisecond,
				"a retained database outlives its App")
		})
	}
}

// scenario: pruning-keeps-bound-database — a deletion: delete database the App drops is kept while a Function outside
// the App binds it, and pruned once none does.
func TestScenarioPruningKeepsBoundDatabase(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	e.apply(t, migrateWritesDB())
	api, migrate := e.sqlAPIImage(t), e.sqlMigrateImage(t, "migrate-1", createTodos())
	a := sqlApp("1.0.0", api, migrate)
	a.Spec.SQL = append(a.Spec.SQL, v1.AppSQLDatabase{Name: "scratch-db", Deletion: v1.DeletionDelete})
	e.apply(t, a)
	e.waitSQLApp(t, "sqltodo-1")
	reporter := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "reporter", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: api, SQL: []v1.FunctionSQL{{Alias: "app", Database: "scratch-db"}}}}
	e.apply(t, reporter)
	a = sqlApp("2.0.0", api, migrate)
	e.apply(t, a)
	e.waitSQLApp(t, "sqltodo-2")
	stays(t, func() bool { return e.exists(t, v1.KindSQLDatabase, "scratch-db") }, 2*time.Second, 100*time.Millisecond,
		"a bound database is never pruned")
	e.del(t, v1.KindFunction, "reporter")
	e.waitGone(t, v1.KindSQLDatabase, "scratch-db")
}

// scenario: ref-entry — an App entry {ref: shared-db} neither creates nor deletes the database.
func TestScenarioSQLRefEntry(t *testing.T) {
	t.Parallel()
	engines := newStrictEngines(t)
	e := startGC(t, funcd.WithSQLProviderRuntime(engines))
	shared := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase},
		ObjectMeta: v1.ObjectMeta{Name: "shared-db", Namespace: "default", ResourceGroup: "rg1"}}
	e.apply(t, shared)
	e.apply(t, migrateWritesDB())
	a := sqlApp("1.0.0", e.sqlAPIImage(t), e.sqlMigrateImage(t, "migrate-1", createTodos()))
	a.Spec.SQL = append(a.Spec.SQL, v1.AppSQLDatabase{Ref: "shared-db"})
	e.apply(t, a)
	e.waitSQLApp(t, "sqltodo-1")
	marked, controlled := appRefs(e.object(t, v1.KindSQLDatabase, "shared-db"), e.app(t, "sqltodo"))
	require.False(t, marked || controlled, "a ref entry is not the App's")
	e.del(t, v1.KindApp, "sqltodo")
	e.waitGone(t, v1.KindFunction, "todo-api")
	require.True(t, e.exists(t, v1.KindSQLDatabase, "shared-db"))
}
