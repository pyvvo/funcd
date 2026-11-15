//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/testkit/fakesql"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// sqlEngines is the engine runtime of the SQL matrix (provider.Runtime and provider.Volumes): each database's engine is
// Ready at once, but starting's, which never is. main and full are fake rqlite engines, full holding one row; broken
// and unmeasured answer 503 to every call, slow reports each call past its timeout, and huge answers past the 8 MiB that
// a call reads (ADR-0233 Decision 4).
type sqlEngines struct {
	t     *testing.T
	fixed map[v1.ObjectName]string // the address of each engine that answers every call alike, by database
	mu    sync.Mutex
	addrs map[provider.ProviderRef]string
}

func newSQLEngines(t *testing.T) *sqlEngines {
	t.Helper()
	broken := answering(t, http.StatusServiceUnavailable, "")
	return &sqlEngines{t: t, addrs: map[provider.ProviderRef]string{}, fixed: map[v1.ObjectName]string{
		"broken": broken, "unmeasured": broken,
		"slow": answering(t, http.StatusOK, `{"results":[{"error":"query timeout"}]}`),
		"huge": answering(t, http.StatusOK, strings.Repeat(" ", 8<<20+1)),
	}}
}

// answering is the address of an engine that answers every call with status and body.
func answering(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func (e *sqlEngines) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	if spec.Ref.Name == "starting" {
		return provider.ProviderStatus{Reason: "Starting"}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	addr, ok := e.addrs[spec.Ref]
	if !ok {
		if addr, ok = e.fixed[spec.Ref.Name]; !ok {
			engine := fakesql.New(e.t, spec.Env["FUNCD_SQL_PASSWORD"])
			if spec.Ref.Name == "full" {
				engine.Seed("kept")
			}
			addr = engine.Addr()
		}
		e.addrs[spec.Ref] = addr
	}
	return provider.ProviderStatus{Running: 1, Ready: true, Address: addr}, nil
}

func (*sqlEngines) Teardown(context.Context, provider.ProviderRef) error { return nil }
func (*sqlEngines) Reclaim(context.Context, provider.ProviderRef) error  { return nil }
func (*sqlEngines) List(context.Context) ([]provider.ProviderRef, error) { return nil, nil }
func (*sqlEngines) Quarantine() quarantine.Store {
	return provider.NewDirQuarantine("", v1.KindSQLDatabase, nil, nil)
}

// sqlCauseEnv runs a platform with opts whose default namespace holds the SQL matrix's objects: the databases of
// sqlEngines, each owned by owner, unmeasured and full under a quota that full's one row fills; owner binds each under
// its own name, and reader and denied bind main, which a Policy forbids denied to read. It returns once every engine
// but starting's is Ready.
func sqlCauseEnv(t *testing.T, opts ...funcd.Option) *causeEnv {
	t.Helper()
	p := answersPlatform(t, append([]funcd.Option{funcd.WithSQLProviderRuntime(newSQLEngines(t))}, opts...)...)
	c, base := answersClient(), "http://"+p.Addr()
	databases := []v1.ObjectName{"main", "starting", "broken", "unmeasured", "slow", "huge", "full"}
	owner := function("owner")
	for _, name := range databases {
		db := &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
		db.Spec.Owner = "owner"
		switch name {
		case "unmeasured":
			db.Spec.Quota.MaxBytes = 1 << 20
		case "full":
			db.Spec.Quota.MaxBytes = fakesql.RowBytes
		}
		create(t, c, base, db)
		owner.Spec.SQLDatabases = append(owner.Spec.SQLDatabases, v1.SQLDatabaseEntry{Ref: name})
		owner.Spec.Bindings.SQL = append(owner.Spec.Bindings.SQL, v1.FunctionSQL{Alias: string(name), Database: name})
	}
	create(t, c, base, owner)
	for _, name := range []v1.ObjectName{"reader", "denied"} {
		fn := function(name)
		fn.Spec.SQLDatabases = []v1.SQLDatabaseEntry{{Ref: "main"}}
		fn.Spec.Bindings.SQL = []v1.FunctionSQL{{Alias: "main", Database: "main"}}
		create(t, c, base, fn)
	}
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-sql-reads", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"sql::read", resource);`}})
	for _, name := range databases {
		if name != "starting" {
			require.Eventually(t, func() bool { return databasePhase(c, base, name) == v1.PhaseReady },
				10*time.Second, 10*time.Millisecond, "SQLDatabase %s is Ready", name)
		}
	}
	return &causeEnv{p: p}
}

// databasePhase is the phase of SQLDatabase default/name, "" when it cannot be read.
func databasePhase(c *http.Client, base string, name v1.ObjectName) v1.Phase {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/apis/funcd.io/v1alpha1/namespaces/default/sqldatabases/"+string(name), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var db v1.SQLDatabase
	if json.NewDecoder(resp.Body).Decode(&db) != nil {
		return ""
	}
	return db.Status.Phase
}

// TestSQLCauseMatrix is the SQL service's matrix: each cause that a SQL endpoint declares, provoked by one request.
func TestSQLCauseMatrix(t *testing.T) {
	t.Parallel()
	runCauseMatrix(t, sqlCauseRows(sqlEnvs{
		main:  sqlCauseEnv(t),
		floor: sqlCauseEnv(t, funcd.WithFreeSpaceFloor(func() (int64, error) { return 0, nil }, 1)),
	}))
}

// sqlEnvs are the platforms of the SQL matrix: main, and floor, whose data filesystem is below its free-space floor.
type sqlEnvs struct {
	main, floor *causeEnv
}

// sqlCauseRows is the SQL matrix over envs.
func sqlCauseRows(envs sqlEnvs) []causeRow {
	const query, execute = "POST /sql/{binding}/query", "POST /sql/{binding}/execute"
	const one = `[["SELECT 1"]]`
	call := func(alias, verb, statements string) causeRequest {
		return causeRequest{method: http.MethodPost, path: "/sql/" + alias + "/" + verb, body: `{"statements":` + statements + `}`}
	}
	many := "[" + strings.TrimSuffix(strings.Repeat(`["SELECT 1"],`, local.MaxSQLStatements+1), ",") + "]"
	main, floor := envs.main, envs.floor
	var rows []causeRow
	for _, e := range []struct {
		endpoint, verb string
		refused        string // a statement that the database refuses
	}{
		{query, "query", `[["INSERT INTO todos(title, done) VALUES(?, ?)", "a", 0]]`},
		{execute, "execute", `[["INSERT INTO todos(title, done) VALUES(?, ?)", null, 0]]`},
	} {
		on := func(alias, statements string) causeRequest { return call(alias, e.verb, statements) }
		row := func(cause *fault.KnownCause, caller v1.ObjectName, request causeRequest) causeRow {
			return causeRow{endpoint: e.endpoint, cause: cause, env: main, caller: caller, request: request}
		}
		rows = append(rows,
			row(local.ErrBodyTooLarge, "owner", on("main", `[["SELECT ?", "`+strings.Repeat("x", 1<<20)+`"]]`)),
			row(local.ErrBodyUnreadable, "owner", causeRequest{
				raw: "POST /sql/main/" + e.verb + " HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}),
			row(local.ErrSQLBodyMalformed, "owner", on("main", `"SELECT 1"`)),
			row(local.ErrSQLNoStatements, "owner", on("main", `[]`)),
			row(local.ErrSQLTooManyStatements, "owner", on("main", many)),
			row(local.ErrSQLStatementMalformed, "owner", on("main", `[[]]`)),
			row(local.ErrSQLParameterNotScalar, "owner", on("main", `[["SELECT ?", [1]]]`)),
			row(local.ErrSQLCallerMissing, "ghost", on("main", one)),
			row(local.ErrSQLNoBinding, "owner", on("unbound", one)),
			row(local.ErrSQLReadDenied, "denied", on("main", one)),
			row(local.ErrEngineNotReady, "owner", on("starting", one)),
			row(local.ErrSQLEngineUnavailable, "owner", on("broken", one)),
			row(local.ErrSQLTimeout, "owner", on("slow", one)),
			row(local.ErrSQLAnswerTooLarge, "owner", on("huge", one)),
			row(local.ErrSQLStatementFailed, "owner", on("main", e.refused)),
		)
	}
	return append(rows,
		causeRow{endpoint: execute, cause: local.ErrSQLWriteDenied, env: main, caller: "reader", request: call("main", "execute", one)},
		causeRow{endpoint: execute, cause: quota.ErrFloorReached, env: floor, caller: "owner", request: call("main", "execute", one)},
		causeRow{endpoint: execute, cause: quota.ErrExceeded, env: main, caller: "owner", request: call("full", "execute", one)},
		causeRow{endpoint: execute, cause: quota.ErrUnreadable, env: main, caller: "owner",
			request: call("unmeasured", "execute", one)},
	)
}
