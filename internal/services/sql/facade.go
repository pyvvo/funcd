package sql

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// DefaultCallTimeout bounds one SQL call when sql.callTimeout is unset (ADR-0233 Decision 4).
const DefaultCallTimeout = 30 * time.Second

// engineTimeout is the per-statement error rqlite reports when db_timeout passes.
const engineTimeout = "query timeout"

// FacadeDeps configures the SQL facade, the PEP of every context.sql call (ADR-0233 Decision 4).
type FacadeDeps struct {
	Resolver    Resolver
	Authorizer  auth.Authorizer
	Engines     *Engines
	Master      []byte
	HTTPClient  *http.Client  // nil ⇒ a node client bounded by CallTimeout
	CallTimeout time.Duration // 0 ⇒ DefaultCallTimeout
	// Quota decides each Execute against its database's quota (ADR-0292); nil is quota.None.
	Quota  quota.Checker
	Logger *slog.Logger
}

// Facade resolves a caller's alias, asks Cedar, and calls the database's engine as user funcd. It implements
// local.SQL and health.ReadChecker.
type Facade struct {
	d FacadeDeps
}

var _ local.SQL = (*Facade)(nil)

// NewFacade builds the facade; Resolver, Authorizer, Engines and Master are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	const op = "sql.NewFacade"
	switch {
	case d.Resolver == nil:
		return nil, fault.Invalidf(op, "resolver is required")
	case d.Authorizer == nil:
		return nil, fault.Invalidf(op, "authorizer is required")
	case d.Engines == nil:
		return nil, fault.Invalidf(op, "engine registry is required")
	case len(d.Master) == 0:
		return nil, fault.Invalidf(op, "node master is required")
	}
	if d.CallTimeout <= 0 {
		d.CallTimeout = DefaultCallTimeout
	}
	if d.HTTPClient == nil {
		d.HTTPClient = httpx.NodeClient(d.CallTimeout + time.Second)
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Quota == nil {
		d.Quota = quota.None
	}
	d.Logger = d.Logger.With("component", "services.sql")
	return &Facade{d: d}, nil
}

// Query runs read-only statements on /db/query (ADR-0233): sql::read, and a write is refused by the engine.
func (f *Facade) Query(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]local.QueryResult, error) {
	res, err := f.run(ctx, ns, fn, binding, stmts, "/db/query", url.Values{}, false, auth.ActionSQLRead)
	if err != nil {
		return nil, err
	}
	out := make([]local.QueryResult, len(res))
	for i, r := range res {
		out[i] = local.QueryResult{Columns: nonNil(r.Columns), Values: nonNilRows(r.Values)}
	}
	return out, nil
}

// Execute runs statements in one transaction on /db/execute (ADR-0233): sql::read and sql::write, since RETURNING
// reads; one failed statement applies none. Its database's quota decides it first (ADR-0292).
func (f *Facade) Execute(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage) ([]local.ExecResult, error) {
	res, err := f.run(ctx, ns, fn, binding, stmts, "/db/execute", url.Values{"transaction": {""}}, true, auth.ActionSQLRead, auth.ActionSQLWrite)
	if err != nil {
		return nil, err
	}
	out := make([]local.ExecResult, len(res))
	for i, r := range res {
		out[i] = local.ExecResult{RowsAffected: r.RowsAffected, LastInsertID: r.LastInsertID, Columns: r.Columns, Values: r.Values}
	}
	return out, nil
}

// CheckRead resolves the alias, asks sql::read and checks that the engine is Ready, with no engine call: the
// dependency check of a binding (ADR-0233 Decision 8). A missing engine wraps local.ErrEngineNotReady.
func (f *Facade) CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error {
	db, err := f.authorize(ctx, ns, fn, alias, auth.ActionSQLRead)
	if err != nil {
		return err
	}
	if _, ok := f.d.Engines.Address(ns, db); !ok {
		return fault.Wrapf(local.ErrEngineNotReady, fault.Unavailable, "sql.CheckRead", "the engine of SQLDatabase %q is not Ready", db)
	}
	return nil
}

func (f *Facade) run(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string, stmts []json.RawMessage,
	path string, query url.Values, writes bool, actions ...auth.Action) ([]engineResult, error) {
	const op = "sql.call"
	if err := validateStatements(stmts); err != nil {
		return nil, err
	}
	db, err := f.authorize(ctx, ns, fn, binding, actions...)
	if err != nil {
		return nil, err
	}
	addr, ok := f.d.Engines.Address(ns, db)
	if !ok {
		return nil, fault.Wrapf(local.ErrEngineNotReady, fault.Unavailable, op, "the engine of SQLDatabase %q is not Ready", db)
	}
	settle := quota.Settle(func(bool) {})
	if writes {
		settle, err = f.d.Quota.Admit(ctx, quota.Request{
			Subject: quota.Subject{Kind: v1.KindSQLDatabase, Namespace: ns, Name: db},
			Bytes:   quota.Unknown,
			Deletes: deletesOnly(stmts),
		})
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, f.d.CallTimeout)
	defer cancel()
	query.Set("db_timeout", f.d.CallTimeout.String())
	res, err := call(ctx, f.d.HTTPClient, addr, EnginePassword(f.d.Master, ns, db), path, query, stmts)
	settle(err == nil)
	if err != nil {
		if fault.KindOf(err) == fault.Internal {
			f.d.Logger.ErrorContext(ctx, "sql engine refused funcd", "database", string(ns)+"/"+string(db), "err", err)
		}
		return nil, err
	}
	for i, r := range res {
		switch {
		case r.Error == engineTimeout:
			return nil, fault.Unavailablef(op, "statement %d ran past %s", i, f.d.CallTimeout)
		case r.Error != "":
			return nil, fault.Invalidf(op, "statement %d: %s", i, r.Error)
		}
	}
	if len(res) != len(stmts) {
		return nil, fault.Internalf(op, "the engine answered %d results for %d statements", len(res), len(stmts))
	}
	return res, nil
}

// usageSQL is a database's pages in use (ADR-0292 Decision 2): its size less its free pages.
const usageSQL = "SELECT (page_count - freelist_count) * page_size FROM pragma_page_count(), pragma_freelist_count(), pragma_page_size()"

// Usage reads the bytes of db's pages in use through its engine, as funcd: what its quota measures.
func (f *Facade) Usage(ctx context.Context, ns v1.NamespaceName, db v1.ObjectName) (int64, error) {
	const op = "sql.Usage"
	addr, ok := f.d.Engines.Address(ns, db)
	if !ok {
		return 0, fault.Wrapf(local.ErrEngineNotReady, fault.Unavailable, op, "the engine of SQLDatabase %q is not Ready", db)
	}
	ctx, cancel := context.WithTimeout(ctx, f.d.CallTimeout)
	defer cancel()
	stmt, _ := json.Marshal([]string{usageSQL})
	res, err := call(ctx, f.d.HTTPClient, addr, EnginePassword(f.d.Master, ns, db), "/db/query", url.Values{}, []json.RawMessage{stmt})
	if err != nil {
		return 0, err
	}
	var n int64
	if len(res) != 1 || res[0].Error != "" || len(res[0].Values) != 1 || len(res[0].Values[0]) != 1 ||
		json.Unmarshal(res[0].Values[0][0], &n) != nil {
		return 0, fault.Internalf(op, "SQLDatabase %q answered no page count", db)
	}
	return n, nil
}

// authorize resolves the alias, then asks Cedar every action on the database; any deny is Forbidden.
func (f *Facade) authorize(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string, actions ...auth.Action) (v1.ObjectName, error) {
	const op = "sql.authorize"
	db, err := f.d.Resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return "", err
	}
	for _, a := range actions {
		dec, aerr := f.d.Authorizer.Authorize(ctx, auth.Request{
			Identity: auth.Identity{Subject: string(ns) + "/" + string(fn), Principal: &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}},
			Action:   a,
			Resource: &auth.EntityRef{Type: v1.KindSQLDatabase, Namespace: ns, Name: db},
		})
		if aerr != nil {
			return "", fault.Wrapf(aerr, fault.KindOf(aerr), op, "authorize %s on SQLDatabase %q", a, db)
		}
		if !dec.Allowed {
			return "", fault.Forbiddenf(op, "function %q is not authorized to %s SQLDatabase %q: %s", fn, a, db, dec.Reason)
		}
	}
	return db, nil
}

// validateStatements refuses a statement that is not ["SQL", params...] with scalar params, or ["SQL", {named
// scalars}] (ADR-0233 Decision 3), before any engine call.
func validateStatements(stmts []json.RawMessage) error {
	const op = "sql.statement"
	for i, raw := range stmts {
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
			return fault.Invalidf(op, "statement %d is not [\"SQL\", params...]", i)
		}
		var text string
		if err := json.Unmarshal(parts[0], &text); err != nil || text == "" {
			return fault.Invalidf(op, "statement %d does not start with its SQL text", i)
		}
		params := parts[1:]
		if len(params) == 1 && bytes.HasPrefix(bytes.TrimSpace(params[0]), []byte("{")) {
			var named map[string]json.RawMessage
			if err := json.Unmarshal(params[0], &named); err != nil {
				return fault.Invalidf(op, "statement %d: named parameters are not an object", i)
			}
			for name, v := range named {
				if !scalar(v) {
					return fault.Invalidf(op, "statement %d: parameter %q is an array or an object", i, name)
				}
			}
			continue
		}
		for j, p := range params {
			if !scalar(p) {
				return fault.Invalidf(op, "statement %d: parameter %d is an array or an object", i, j+1)
			}
		}
	}
	return nil
}

func scalar(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) > 0 && t[0] != '[' && t[0] != '{'
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilRows(v [][]json.RawMessage) [][]json.RawMessage {
	if v == nil {
		return [][]json.RawMessage{}
	}
	return v
}
