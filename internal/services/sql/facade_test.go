package sql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	sqlsvc "github.com/pyvvo/funcd/internal/services/sql"
	"github.com/pyvvo/funcd/internal/testkit/fakesql"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// staticResolver binds alias "app" to todo-db.
type staticResolver struct{}

func (staticResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (v1.ObjectName, error) {
	if alias != "app" {
		return "", fault.Forbiddenf("test", "%s has no SQL binding %q", fn, alias)
	}
	return "todo-db", nil
}

// recordingPDP allows every action not in deny and records each one asked.
type recordingPDP struct {
	asked []auth.Action
	deny  map[auth.Action]bool
}

func (p *recordingPDP) Authorize(_ context.Context, r auth.Request) (auth.Decision, error) {
	p.asked = append(p.asked, r.Action)
	if r.Resource.Type != v1.KindSQLDatabase || r.Resource.Name != "todo-db" || r.Identity.Principal.Name != "todo-api" {
		return auth.Decision{}, fault.Internalf("test", "unexpected request %+v", r)
	}
	return auth.Decision{Allowed: !p.deny[r.Action], Reason: "test"}, nil
}

func facade(t *testing.T, pdp auth.Authorizer, addr string, logs *bytes.Buffer, timeout time.Duration) *sqlsvc.Facade {
	t.Helper()
	engines := sqlsvc.NewEngines()
	if addr != "" {
		engines.Set("default", "todo-db", addr)
	}
	f, err := sqlsvc.NewFacade(sqlsvc.FacadeDeps{Resolver: staticResolver{}, Authorizer: pdp, Engines: engines, Master: []byte(masterKey),
		CallTimeout: timeout, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	require.NoError(t, err)
	return f
}

func stmts(t *testing.T, raw string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

func TestFacadeQueryAndExecute(t *testing.T) {
	engine := fakesql.New(t, sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db"))
	pdp := &recordingPDP{}
	f := facade(t, pdp, engine.Addr(), &bytes.Buffer{}, 0)
	ctx := context.Background()

	ex, err := f.Execute(ctx, "default", "todo-api", "app", stmts(t, `[["INSERT INTO todos(title, done) VALUES(:t, :d)", {"t": "milk", "d": 0}]]`))
	require.NoError(t, err)
	require.Equal(t, []local.ExecResult{{RowsAffected: 1, LastInsertID: 1}}, ex)
	require.Equal(t, []auth.Action{auth.ActionSQLRead, auth.ActionSQLWrite}, pdp.asked, "execute asks read and write")

	pdp.asked = nil
	q, err := f.Query(ctx, "default", "todo-api", "app", stmts(t, `[["SELECT id, title FROM todos"]]`))
	require.NoError(t, err)
	require.Equal(t, []string{"id", "title"}, q[0].Columns)
	require.Len(t, q[0].Values, 1)
	require.Equal(t, []auth.Action{auth.ActionSQLRead}, pdp.asked, "query asks read only")

	ret, err := f.Execute(ctx, "default", "todo-api", "app", stmts(t, `[["UPDATE todos SET done = 1 RETURNING id, title"]]`))
	require.NoError(t, err)
	require.Equal(t, []string{"id", "title"}, ret[0].Columns, "RETURNING rows come back")
	require.Equal(t, []string{"/db/execute?db_timeout=30s&transaction=", "/db/query?db_timeout=30s", "/db/execute?db_timeout=30s&transaction="}, engine.Paths())
}

func TestFacadeRefusals(t *testing.T) {
	engine := fakesql.New(t, sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db"))
	ctx := context.Background()
	run := func(f *sqlsvc.Facade, alias, raw string) error {
		_, err := f.Execute(ctx, "default", "todo-api", alias, stmts(t, raw))
		return err
	}
	ok := facade(t, &recordingPDP{}, engine.Addr(), &bytes.Buffer{}, 0)
	for _, bad := range []string{`["SELECT 1"]`, `[[]]`, `[[1]]`, `[["SELECT ?", [1]]]`, `[["SELECT ?", 1, {"a": 1}]]`, `[["SELECT :a", {"a": [1]}]]`} {
		require.Equal(t, fault.Invalid, fault.KindOf(run(ok, "app", bad)), bad)
	}
	require.Empty(t, engine.Paths(), "a malformed statement never reaches the engine")
	require.Equal(t, fault.Forbidden, fault.KindOf(run(ok, "other", `[["DELETE FROM todos"]]`)), "an unknown alias")
	denied := facade(t, &recordingPDP{deny: map[auth.Action]bool{auth.ActionSQLWrite: true}}, engine.Addr(), &bytes.Buffer{}, 0)
	require.Equal(t, fault.Forbidden, fault.KindOf(run(denied, "app", `[["DELETE FROM todos"]]`)))
	noEngine := facade(t, &recordingPDP{}, "", &bytes.Buffer{}, 0)
	err := run(noEngine, "app", `[["DELETE FROM todos"]]`)
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	require.ErrorIs(t, err, local.ErrEngineNotReady)
	require.ErrorIs(t, noEngine.CheckRead(ctx, "default", "todo-api", "app"), local.ErrEngineNotReady)
	require.NoError(t, ok.CheckRead(ctx, "default", "todo-api", "app"))
}

func TestFacadeEngineFailures(t *testing.T) {
	ctx := context.Background()
	query := func(f *sqlsvc.Facade) error {
		_, err := f.Query(ctx, "default", "todo-api", "app", stmts(t, `[["SELECT id, title FROM todos"]]`))
		return err
	}
	var logs bytes.Buffer
	wrongPassword := fakesql.New(t, "not the derived password")
	err := query(facade(t, &recordingPDP{}, wrongPassword.Addr(), &logs, 0))
	require.Equal(t, fault.Internal, fault.KindOf(err), "a 401 is funcd's fault")
	pw := sqlsvc.EnginePassword([]byte(masterKey), "default", "todo-db")
	require.NotContains(t, err.Error(), pw)
	require.NotContains(t, logs.String(), pw)
	require.Contains(t, logs.String(), "sql engine refused funcd")

	closedURL := "http://127.0.0.1:1" // nothing listens there: a closed server's port could be bound again
	addr := strings.TrimPrefix(closedURL, "http://")
	var refusedLogs bytes.Buffer
	refused := query(facade(t, &recordingPDP{}, addr, &refusedLogs, 0))
	require.Equal(t, fault.Unavailable, fault.KindOf(refused), "a refused connection")
	require.NotContains(t, refused.Error(), addr, "the engine's address never reaches the caller")
	require.NotContains(t, refusedLogs.String(), addr)

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	slowAddr := strings.TrimPrefix(slow.URL, "http://")
	late := query(facade(t, &recordingPDP{}, slowAddr, &bytes.Buffer{}, 100*time.Millisecond))
	require.Equal(t, fault.Unavailable, fault.KindOf(late), "a call past sql.callTimeout")
	require.NotContains(t, late.Error(), slowAddr)

	timedOut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"error":"query timeout"}]}`))
	}))
	t.Cleanup(timedOut.Close)
	require.Equal(t, fault.Unavailable, fault.KindOf(query(facade(t, &recordingPDP{}, strings.TrimPrefix(timedOut.URL, "http://"), &bytes.Buffer{}, 0))),
		"rqlite's db_timeout")

	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"columns":["x"],"values":[["`))
		_, _ = w.Write(bytes.Repeat([]byte("a"), 9<<20))
		_, _ = w.Write([]byte(`"]]}]}`))
	}))
	t.Cleanup(huge.Close)
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(query(facade(t, &recordingPDP{}, strings.TrimPrefix(huge.URL, "http://"), &bytes.Buffer{}, 0))))
}
