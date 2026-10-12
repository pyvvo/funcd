package local

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The SQL request bounds (ADR-0233 Decision 3).
const (
	maxSQLBytes      = 1 << 20
	MaxSQLStatements = 500
)

// DependencySQL is the dependency-report kind of a spec.sql binding, and ReasonEngineNotReady its reason while the
// database's engine is not Ready (ADR-0233 Decision 8).
const (
	DependencySQL        = "sql"
	ReasonEngineNotReady = "EngineNotReady"
)

// ErrEngineNotReady marks a SQL call or check whose database has no Ready engine.
var ErrEngineNotReady = errors.New("the database's engine is not ready")

// SQL is the function-facing SQL port the local API routes to; the services/sql.Facade satisfies it. As for KV, the
// caller's namespace and function come from the sandbox's fixed Ref, never from the request (ADR-0233).
type SQL interface {
	Query(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]QueryResult, error)
	Execute(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]ExecResult, error)
}

// QueryResult is one query statement's rows in rqlite's array format, which keeps duplicate column names.
type QueryResult struct {
	Columns []string            `json:"columns"`
	Values  [][]json.RawMessage `json:"values"`
}

// ExecResult is one execute statement's outcome; a RETURNING statement also carries its rows.
type ExecResult struct {
	RowsAffected int64               `json:"rowsAffected"`
	LastInsertID int64               `json:"lastInsertId"`
	Columns      []string            `json:"columns,omitempty"`
	Values       [][]json.RawMessage `json:"values,omitempty"`
}

// queryResponse and execResponse are the bodies of a successful query and execute.
type queryResponse struct {
	Results []QueryResult `json:"results"`
}

type execResponse struct {
	Results []ExecResult `json:"results"`
}

// sqlRequest is the body of POST /sql/{binding}/query and /execute.
type sqlRequest struct {
	Statements []json.RawMessage `json:"statements"`
}

// registerSQL adds POST /sql/{binding}/query and POST /sql/{binding}/execute, routed to s with the sandbox's fixed
// namespace and function. Errors are RFC 9457: an unknown alias or a denial 403, a failed statement 400, an engine
// that is not Ready 503, an oversized body or answer 413.
func registerSQL(mux *http.ServeMux, caller Ref, s SQL, logger *slog.Logger) {
	ns, fn := caller.Namespace, caller.Function
	mux.HandleFunc("POST /sql/{binding}/query", func(w http.ResponseWriter, r *http.Request) {
		stmts, ok := readStatements(w, r, "workernode.local.sql.query")
		if !ok {
			return
		}
		res, err := s.Query(r.Context(), ns, fn, r.PathValue("binding"), stmts)
		writeSQL(w, logger, caller, "query", r.PathValue("binding"), queryResponse{res}, err)
	})
	mux.HandleFunc("POST /sql/{binding}/execute", func(w http.ResponseWriter, r *http.Request) {
		stmts, ok := readStatements(w, r, "workernode.local.sql.execute")
		if !ok {
			return
		}
		res, err := s.Execute(r.Context(), ns, fn, r.PathValue("binding"), stmts)
		writeSQL(w, logger, caller, "execute", r.PathValue("binding"), execResponse{res}, err)
	})
}

// readStatements reads and bounds a SQL request body; on a failure it has written the problem and returns false.
func readStatements(w http.ResponseWriter, r *http.Request, op string) ([]json.RawMessage, bool) {
	body, err := readBody(w, r, op, maxSQLBytes)
	if err == nil {
		var req sqlRequest
		if uerr := json.Unmarshal(body, &req); uerr != nil {
			err = fault.Invalidf(op, "the body is not {\"statements\": [...]}: %v", uerr)
		} else if len(req.Statements) == 0 {
			err = fault.Invalidf(op, "no statements")
		} else if len(req.Statements) > MaxSQLStatements {
			err = fault.PayloadTooLargef(op, "%d statements exceed the limit of %d", len(req.Statements), MaxSQLStatements)
		} else {
			return req.Statements, true
		}
	}
	fault.WriteProblem(w, err)
	return nil, false
}

// writeSQL writes a SQL call's results, or its problem, logged without the statements.
func writeSQL[T queryResponse | execResponse](w http.ResponseWriter, logger *slog.Logger, caller Ref, verb, binding string, out T, err error) {
	if err != nil {
		logger.Warn("sql "+verb+" denied/failed", "caller", caller.String(), "binding", binding, "err", err.Error())
		fault.WriteProblem(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
