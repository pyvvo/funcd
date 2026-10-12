// Package fakesql is a test stand-in for a rqlite engine (ADR-0233): one table todos(id, title, done), basic auth as
// user funcd, /db/query on a read-only connection and /db/execute?transaction all or nothing, in rqlite's array format.
// It understands only the statements the SQL tests send; any other is a syntax error, as an unknown table would be.
package fakesql

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// Todo is one row of the fake's todos table.
type Todo struct {
	ID    int64
	Title string
	Done  int64
}

// Engine is a running fake engine.
type Engine struct {
	srv      *httptest.Server
	password string

	mu    sync.Mutex
	rows  []Todo
	next  int64
	paths []string
}

// New starts a fake engine accepting user funcd with password; it stops at the end of t.
func New(t *testing.T, password string) *Engine {
	t.Helper()
	e := &Engine{password: password, next: 1}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)
	return e
}

// Addr is the engine's host:port, as the Engines registry records it.
func (e *Engine) Addr() string { return strings.TrimPrefix(e.srv.URL, "http://") }

// Rows returns a copy of the table.
func (e *Engine) Rows() []Todo {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Todo(nil), e.rows...)
}

// Seed appends rows to the table.
func (e *Engine) Seed(titles ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, title := range titles {
		e.rows = append(e.rows, Todo{ID: e.next, Title: title})
		e.next++
	}
}

// Paths returns the request paths with their query, in order.
func (e *Engine) Paths() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.paths...)
}

type result struct {
	Columns      []string            `json:"columns,omitempty"`
	Values       [][]json.RawMessage `json:"values,omitempty"`
	RowsAffected int64               `json:"rows_affected,omitempty"`
	LastInsertID int64               `json:"last_insert_id,omitempty"`
	Error        string              `json:"error,omitempty"`
}

func (e *Engine) serve(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "funcd" || pass != e.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var stmts [][]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&stmts); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paths = append(e.paths, r.URL.RequestURI())
	readOnly := r.URL.Path == "/db/query"
	_, tx := r.URL.Query()["transaction"]
	rows, next := append([]Todo(nil), e.rows...), e.next
	var out []result
	for _, s := range stmts {
		res := run(&rows, &next, s, readOnly)
		out = append(out, res)
		if res.Error != "" && tx {
			break
		}
	}
	failed := false
	for _, res := range out {
		failed = failed || res.Error != ""
	}
	if !readOnly && (!failed || !tx) {
		e.rows, e.next = rows, next
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]result{"results": out})
}

var (
	selectTodos = regexp.MustCompile(`^SELECT id, title FROM todos(?: WHERE done = (\?|:\w+))?$`)
	insertTodo  = regexp.MustCompile(`^INSERT INTO todos\(title, done\) VALUES\((\?|:\w+), (\?|:\w+)\)$`)
	updateDone  = regexp.MustCompile(`^UPDATE todos SET done = 1( RETURNING id, title)?$`)
	deleteTodos = regexp.MustCompile(`^DELETE FROM todos$`)
)

func run(rows *[]Todo, next *int64, stmt []json.RawMessage, readOnly bool) result {
	var text string
	_ = json.Unmarshal(stmt[0], &text)
	param := params(stmt[1:])
	writes := !strings.HasPrefix(text, "SELECT")
	if writes && readOnly {
		return result{Error: "attempt to change database via query operation"}
	}
	switch {
	case selectTodos.MatchString(text):
		m := selectTodos.FindStringSubmatch(text)
		res := result{Columns: []string{"id", "title"}, Values: [][]json.RawMessage{}}
		for _, t := range *rows {
			if m[1] != "" && fmt.Sprint(t.Done) != param(m[1], 0) {
				continue
			}
			res.Values = append(res.Values, row(t))
		}
		return res
	case insertTodo.MatchString(text):
		m := insertTodo.FindStringSubmatch(text)
		title := param(m[1], 0)
		if title == "null" {
			return result{Error: "NOT NULL constraint failed: todos.title"}
		}
		var done int64
		_, _ = fmt.Sscan(param(m[2], 1), &done)
		*rows = append(*rows, Todo{ID: *next, Title: strings.Trim(title, `"`), Done: done})
		*next++
		return result{RowsAffected: 1, LastInsertID: *next - 1}
	case updateDone.MatchString(text):
		res := result{}
		for i := range *rows {
			(*rows)[i].Done = 1
			res.RowsAffected++
		}
		if updateDone.FindStringSubmatch(text)[1] != "" {
			res = result{Columns: []string{"id", "title"}, Values: [][]json.RawMessage{}}
			for _, t := range *rows {
				res.Values = append(res.Values, row(t))
			}
		}
		return res
	case deleteTodos.MatchString(text):
		n := int64(len(*rows))
		*rows = nil
		return result{RowsAffected: n}
	}
	return result{Error: fmt.Sprintf("near %q: syntax error", text)}
}

// params resolves a placeholder: "?" takes the i-th positional parameter, ":name" the named one; values are raw JSON.
func params(ps []json.RawMessage) func(placeholder string, i int) string {
	named := map[string]json.RawMessage{}
	if len(ps) == 1 && strings.HasPrefix(strings.TrimSpace(string(ps[0])), "{") {
		_ = json.Unmarshal(ps[0], &named)
	}
	return func(placeholder string, i int) string {
		if strings.HasPrefix(placeholder, ":") {
			return string(named[placeholder[1:]])
		}
		if i < len(ps) {
			return string(ps[i])
		}
		return "null"
	}
}

func row(t Todo) []json.RawMessage {
	title, _ := json.Marshal(t.Title)
	return []json.RawMessage{json.RawMessage(fmt.Sprint(t.ID)), title}
}
