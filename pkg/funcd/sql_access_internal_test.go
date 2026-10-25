package funcd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/testkit/fakesql"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/internal/workflow"
)

// fakeEngines is a SQL runtime whose engines are fakesql engines, one per database, started with the password the
// reconciler passes; with ready false no engine is ever Ready.
type fakeEngines struct {
	t       *testing.T
	ready   bool
	mu      sync.Mutex
	engines map[provider.ProviderRef]*fakesql.Engine
}

func newFakeEngines(t *testing.T, ready bool) *fakeEngines {
	return &fakeEngines{t: t, ready: ready, engines: map[provider.ProviderRef]*fakesql.Engine{}}
}

func (f *fakeEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	if !f.ready {
		return provider.ProviderStatus{Reason: "Starting"}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.engines[spec.Ref]
	if !ok {
		e = fakesql.New(f.t, spec.Env["FUNCD_SQL_PASSWORD"])
		f.engines[spec.Ref] = e
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: e.Addr()}, nil
}

func (*fakeEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }
func (*fakeEngines) Reclaim(context.Context, provider.ProviderRef) error  { return nil }
func (*fakeEngines) List(context.Context) ([]provider.ProviderRef, error) { return nil, nil }
func (*fakeEngines) Quarantine() quarantine.Store                         { return noQuarantine() }

func (f *fakeEngines) engine(name string) *fakesql.Engine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.engines[sqlRef(name)]
}

// sqlFunction is a Function of the default namespace that binds each database under its own name as alias "app".
func sqlFunction(name string, databases ...string) *v1.Function {
	fn := &v1.Function{TypeMeta: tmeta(v1.KindFunction)}
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec = v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: "file:///nonexistent.mjs"}
	for _, db := range databases {
		fn.Spec.Bindings.SQL = append(fn.Spec.Bindings.SQL, v1.FunctionSQL{Alias: "app", Database: v1.ObjectName(db)})
	}
	return fn
}

// accessEnv is a platform with a todo-db owned by todo-api, bound by todo-api and todo-report, and its engine Ready.
type accessEnv struct {
	*sqlEnv
	engines *fakeEngines
}

func startAccess(t *testing.T, ready bool, extra ...v1.Object) *accessEnv {
	t.Helper()
	engines := newFakeEngines(t, ready)
	e := &accessEnv{sqlEnv: startSQL(t, engines), engines: engines}
	db := sqlDatabase("todo-db")
	db.Spec.Owner = "todo-api"
	seedObjects(t, e.p.cfg.store, append([]v1.Object{db, sqlFunction("todo-api", "todo-db"), sqlFunction("todo-report", "todo-db")}, extra...)...)
	if ready {
		e.waitReady(t, "todo-db", 1)
	}
	return e
}

// call posts statements (a JSON array) to fn's local API as its shim would, and returns the status and body.
func (e *accessEnv) call(t *testing.T, fn, verb, alias, statements string) (int, string) {
	t.Helper()
	return e.do(t, fn, http.MethodPost, "/sql/"+alias+"/"+verb, `{"statements":`+statements+`}`)
}

func (e *accessEnv) do(t *testing.T, fn, method, path, body string) (int, string) {
	t.Helper()
	sock, err := e.p.invokeMgr.SocketFor("default", v1.ObjectName(fn))
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	req, err := http.NewRequestWithContext(e.ctx, method, "http://local"+path, strings.NewReader(body))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

func (e *accessEnv) todo() *fakesql.Engine { return e.engines.engine("todo-db") }

// scenario: bound-function-reads — a bound function's query returns the rows.
func TestScenarioBoundFunctionReads(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	e.todo().Seed("milk", "bread")
	code, body := e.call(t, "todo-api", "query", "app", `[["SELECT id, title FROM todos WHERE done = ?", 0]]`)
	require.Equal(t, http.StatusOK, code, body)
	require.JSONEq(t, `{"results":[{"columns":["id","title"],"values":[[1,"milk"],[2,"bread"]]}]}`, body)
	require.Equal(t, []string{"/db/query?db_timeout=30s"}, e.todo().Paths(), "a query goes to the read-only endpoint")
}

// scenario: owner-writes — the owner's execute writes and returns rowsAffected and the new id.
func TestScenarioOwnerWrites(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	code, body := e.call(t, "todo-api", "execute", "app", `[["INSERT INTO todos(title, done) VALUES(?, ?)", "milk", 0]]`)
	require.Equal(t, http.StatusOK, code, body)
	require.JSONEq(t, `{"results":[{"rowsAffected":1,"lastInsertId":1}]}`, body)
	require.Equal(t, []fakesql.Todo{{ID: 1, Title: "milk"}}, e.todo().Rows())
	require.Equal(t, []string{"/db/execute?db_timeout=30s&transaction="}, e.todo().Paths(), "an execute is one transaction")
}

// scenario: non-writer-write-refused — a bound function that is no writer reads but cannot write.
func TestScenarioNonWriterWriteRefused(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	code, body := e.call(t, "todo-report", "execute", "app", `[["INSERT INTO todos(title, done) VALUES(?, ?)", "milk", 0]]`)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Empty(t, e.todo().Rows())
	require.Empty(t, e.todo().Paths(), "nothing reached the engine")
	code, body = e.call(t, "todo-report", "query", "app", `[["SELECT id, title FROM todos"]]`)
	require.Equal(t, http.StatusOK, code, body)
}

// scenario: writer-role-grants-write — a SQL Data Writer RolesAssignment lets a bound function write.
func TestScenarioWriterRoleGrantsWrite(t *testing.T) {
	t.Parallel()
	grant := &v1.RolesAssignment{TypeMeta: tmeta(v1.KindRolesAssignment), Spec: v1.RolesAssignmentSpec{
		Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: "todo-migrate"},
		Assignments: []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "SQL Data Writer"},
			Scope: &v1.ScopeRef{Kind: v1.ScopeKindSQLDatabase, Name: "todo-db"}}},
	}}
	grant.Name, grant.Namespace, grant.ResourceGroup = "todo-migrate-writes-db", "default", "rg1"
	e := startAccess(t, true, sqlFunction("todo-migrate", "todo-db"), grant)
	code, body := e.call(t, "todo-migrate", "execute", "app", `[["INSERT INTO todos(title, done) VALUES(?, ?)", "milk", 0]]`)
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, e.todo().Rows(), 1)
}

// scenario: unbound-alias-refused — an alias the function does not bind is Forbidden.
func TestScenarioUnboundAliasRefused(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	code, body := e.call(t, "todo-api", "query", "other", `[["SELECT id, title FROM todos"]]`)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Empty(t, e.todo().Paths())
}

// scenario: query-cannot-write — a write sent through query fails Invalid and deletes nothing.
func TestScenarioQueryCannotWrite(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	e.todo().Seed("milk")
	code, body := e.call(t, "todo-api", "query", "app", `[["DELETE FROM todos"]]`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "statement 0")
	require.Len(t, e.todo().Rows(), 1)
}

// scenario: batch-is-all-or-nothing — the second of three statements fails, so none is applied.
func TestScenarioBatchIsAllOrNothing(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	code, body := e.call(t, "todo-api", "execute", "app", `[
		["INSERT INTO todos(title, done) VALUES(?, ?)", "milk", 0],
		["INSERT INTO todos(title, done) VALUES(?, ?)", null, 0],
		["INSERT INTO todos(title, done) VALUES(?, ?)", "eggs", 0]]`)
	require.Equal(t, http.StatusBadRequest, code, body)
	require.Contains(t, body, "statement 1")
	require.Empty(t, e.todo().Rows())
}

// scenario: named-parameters — :done with {"done": 0} returns the same rows as ? with [0].
func TestScenarioNamedParameters(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	e.todo().Seed("milk")
	_, positional := e.call(t, "todo-api", "query", "app", `[["SELECT id, title FROM todos WHERE done = ?", 0]]`)
	code, named := e.call(t, "todo-api", "query", "app", `[["SELECT id, title FROM todos WHERE done = :done", {"done": 0}]]`)
	require.Equal(t, http.StatusOK, code, named)
	require.JSONEq(t, positional, named)
	code, body := e.call(t, "todo-api", "query", "app", `[["SELECT id, title FROM todos WHERE done = ?", [0]]]`)
	require.Equal(t, http.StatusBadRequest, code, "an array parameter is refused: %s", body)
}

// scenario: engine-not-ready-unavailable — with no Ready engine a query is 503 and the dependency check names it.
func TestScenarioEngineNotReadyUnavailable(t *testing.T) {
	t.Parallel()
	e := startAccess(t, false)
	code, body := e.call(t, "todo-api", "query", "app", `[["SELECT id, title FROM todos"]]`)
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	code, body = e.do(t, "todo-api", http.MethodGet, "/health/dependencies", "")
	require.Equal(t, http.StatusServiceUnavailable, code, body)
	var report local.DependencyReport
	require.NoError(t, json.Unmarshal([]byte(body), &report))
	require.Equal(t, local.DependencyReport{Kind: "sql", Binding: "app", Reason: "EngineNotReady",
		Message: report.Message}, report)
}

type nodeRuntime struct{}

func (nodeRuntime) Runtime(context.Context, string) (v1.RuntimeName, error) { return "nodejs22", nil }

// scenario: workflow-step-binds-database — an image step's Function binds the database and reads it.
func TestScenarioWorkflowStepBindsDatabase(t *testing.T) {
	t.Parallel()
	wf := &v1.Workflow{TypeMeta: tmeta(v1.KindWorkflow), Spec: v1.WorkflowSpec{Steps: []v1.WorkflowStep{{Name: "report",
		Function: &v1.FunctionStep{Image: "file:///nonexistent.mjs", Bindings: v1.StepBindings{SQL: []v1.FunctionSQL{{Alias: "app", Database: "todo-db"}}}}}}}}
	wf.Name, wf.Namespace, wf.ResourceGroup = "nightly", "default", "rg1"
	e := startAccess(t, true, wf)
	e.todo().Seed("milk")
	// The platform's materializer reads a step image's runtime from its registry; this one is told it.
	m := workflow.NewMaterializer(e.p.cfg.store, nodeRuntime{}, nil, time.Hour)
	stored, err := e.p.cfg.store.Get(e.ctx, v1.KindWorkflow.GVK(), "default", "nightly")
	require.NoError(t, err)
	require.NoError(t, m.Materialize(e.ctx, stored.(*v1.Workflow)))
	step := string(v1.StepFunctionName("nightly", "report"))
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindFunction.GVK(), "default", v1.ObjectName(step))
	require.NoError(t, err)
	require.Equal(t, []v1.FunctionSQL{{Alias: "app", Database: "todo-db"}}, obj.(*v1.Function).Spec.Bindings.SQL, "the step's Function carries the binding")
	code, body := e.call(t, step, "query", "app", `[["SELECT id, title FROM todos"]]`)
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "milk")
}

// scenario: delete-refused-while-bound — an API delete of a bound database is 409 naming the Function.
func TestScenarioDeleteRefusedWhileBound(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true)
	err := e.c.Delete(e.ctx, v1.KindSQLDatabase, "default", "todo-db")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "409: %v", err)
	require.Contains(t, err.Error(), "todo-api")
}

func forbidPolicy(name, cedar string) *v1.Policy {
	p := &v1.Policy{TypeMeta: tmeta(v1.KindPolicy), Spec: v1.PolicySpec{Cedar: cedar}}
	p.Name, p.Namespace, p.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return p
}

// scenario: operator-policy-revokes — a Policy forbidding sql::write stops the owner's execute.
func TestScenarioOperatorPolicyRevokes(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true, forbidPolicy("no-writes",
		`forbid(principal, action == Action::"sql::write", resource == SQLDatabase::"default/todo-db");`))
	code, body := e.call(t, "todo-api", "execute", "app", `[["INSERT INTO todos(title, done) VALUES(?, ?)", "milk", 0]]`)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Empty(t, e.todo().Rows())
}

// scenario: read-forbid-blocks-execute — forbidding the owner's sql::read stops an execute with RETURNING.
func TestScenarioReadForbidBlocksExecute(t *testing.T) {
	t.Parallel()
	e := startAccess(t, true, forbidPolicy("no-reads",
		`forbid(principal == Function::"default/todo-api", action == Action::"sql::read", resource == SQLDatabase::"default/todo-db");`))
	e.todo().Seed("milk")
	code, body := e.call(t, "todo-api", "execute", "app", `[["UPDATE todos SET done = 1 RETURNING id, title"]]`)
	require.Equal(t, http.StatusForbidden, code, body)
	require.Equal(t, []fakesql.Todo{{ID: 1, Title: "milk"}}, e.todo().Rows(), "nothing changes")
}
