package local_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// fakeSQL records its calls and answers one fixed result per statement.
type fakeSQL struct {
	calls []string
	err   error
}

func (f *fakeSQL) Query(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]local.QueryResult, error) {
	f.calls = append(f.calls, "query "+string(ns)+"/"+string(fn)+" "+binding)
	if f.err != nil {
		return nil, f.err
	}
	return []local.QueryResult{{Columns: []string{"id"}, Values: [][]json.RawMessage{{json.RawMessage("1")}}}}, nil
}

func (f *fakeSQL) Execute(_ context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]local.ExecResult, error) {
	f.calls = append(f.calls, "execute "+string(ns)+"/"+string(fn)+" "+binding)
	return []local.ExecResult{{RowsAffected: int64(len(stmts)), LastInsertID: 7}}, nil
}

func sqlHandler(s local.SQL) http.Handler {
	opts := []local.Option{}
	if s != nil {
		opts = append(opts, local.WithSQL(s))
	}
	return local.NewHandler(local.Ref{Namespace: "default", Function: "todo-api"}, nil, nil, nil, nil, nil, nil, nil, opts...)
}

func TestSQLRoutes(t *testing.T) {
	s := &fakeSQL{}
	h := sqlHandler(s)
	rec := do(t, h, http.MethodPost, "/sql/app/query", `{"statements":[["SELECT id FROM todos"]]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"results":[{"columns":["id"],"values":[[1]]}]}`, rec.Body.String())
	rec = do(t, h, http.MethodPost, "/sql/app/execute", `{"statements":[["DELETE FROM a"],["DELETE FROM b"]]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"results":[{"rowsAffected":2,"lastInsertId":7}]}`, rec.Body.String())
	require.Equal(t, []string{"query default/todo-api app", "execute default/todo-api app"}, s.calls, "the caller is the sandbox's, never the request's")

	s.err = fault.Forbiddenf("test", "no binding")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPost, "/sql/other/query", `{"statements":[["SELECT 1"]]}`).Code)
	require.Equal(t, http.StatusNotFound, do(t, sqlHandler(nil), http.MethodPost, "/sql/app/query", `{"statements":[["SELECT 1"]]}`).Code,
		"no SQL port, no routes")
}

func TestSQLRequestBounds(t *testing.T) {
	s := &fakeSQL{}
	h := sqlHandler(s)
	many := `{"statements":[` + strings.TrimSuffix(strings.Repeat(`["SELECT 1"],`, local.MaxSQLStatements+1), ",") + `]}`
	require.Equal(t, http.StatusRequestEntityTooLarge, do(t, h, http.MethodPost, "/sql/app/execute", many).Code)
	big := `{"statements":[["SELECT ?", "` + strings.Repeat("a", 1<<20) + `"]]}`
	require.Equal(t, http.StatusRequestEntityTooLarge, do(t, h, http.MethodPost, "/sql/app/execute", big).Code)
	for _, bad := range []string{`not json`, `{"statements":[]}`, `{}`} {
		require.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/sql/app/query", bad).Code, bad)
	}
	require.Empty(t, s.calls, "a refused body never reaches the facade")
}
