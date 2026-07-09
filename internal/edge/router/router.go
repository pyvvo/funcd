// Package router is the edge request matcher (ADR-0110, F79): it compiles the Ready Route set
// into a host/path/method table and resolves a public data-plane request to a target
// (namespace, function). It RESOLVES, it does not proxy — the data-plane handler then runs the
// existing activator hop, so scale-to-zero is preserved. It is an internal component (interface
// + one table impl); no external driver is anticipated (the external gateway is the
// gateway.Gateway port's V2 concern, not this invoke-path matcher).
package router

import (
	"context"
	"sort"
	"strings"
	"sync"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Router holds the compiled, replace-all Route table and resolves a request to a Match.
// Concurrency-safe.
type Router interface {
	// Program replaces the live table with the compiled Ready-route entries (mirrors the
	// gateway's replace-all ProgramRoutes).
	Program(ctx context.Context, entries []Entry) error
	// Resolve returns the matched target for (host, path, method), or ok=false ⇒ the caller 404s
	// (no activator wake). The matched entry carries its namespace, so no namespace input is needed.
	Resolve(host, path, method string) (Match, bool)
	// Hosts returns the distinct non-empty hosts currently programmed (F74 reads this for SNI).
	Hosts() []string
}

// Entry is one Route's compiled contribution: its host + its rules + its auth stance (F77).
type Entry struct {
	Namespace v1.NamespaceName
	Host      string      // exact; "" matches any host
	Auth      v1.AuthMode // the Route's edge auth stance (ADR-0113); "" ⇒ inherit the namespace default
	Rules     []CompiledRule
}

// CompiledRule is one path rule ready to match.
type CompiledRule struct {
	Path     string          // the rule prefix, e.g. "/orders"
	Exact    bool            // pathType == Exact
	Methods  map[string]bool // nil/empty ⇒ all methods
	Function v1.ObjectName
}

// Match is the resolved target. StripPrefix is the matched rule's prefix the handler strips from
// the request path (empty for an Exact rule) to get the function-relative remainder.
type Match struct {
	Namespace   v1.NamespaceName
	Function    v1.ObjectName
	StripPrefix string
	Auth        v1.AuthMode // the matched Route's auth stance (ADR-0113); "" ⇒ namespace default
}

// compiled is one flattened matcher row (host + rule), sorted longest-path-first.
type compiled struct {
	host      string
	path      string
	exact     bool
	methods   map[string]bool
	namespace v1.NamespaceName
	function  v1.ObjectName
	auth      v1.AuthMode
}

// table is the in-process Router driver.
type table struct {
	mu   sync.RWMutex
	rows []compiled
}

// New returns an empty edge Router.
func New() Router { return &table{} }

func (t *table) Program(_ context.Context, entries []Entry) error {
	rows := make([]compiled, 0, len(entries))
	for _, e := range entries {
		for _, r := range e.Rules {
			rows = append(rows, compiled{
				host:      e.Host,
				path:      r.Path,
				exact:     r.Exact,
				methods:   r.Methods,
				namespace: e.Namespace,
				function:  r.Function,
				auth:      e.Auth,
			})
		}
	}
	// Longest-path-first so a more specific prefix wins; a host-qualified row (non-empty host)
	// beats a wildcard-host row of equal path length (host is the tenant discriminator).
	sort.SliceStable(rows, func(i, j int) bool {
		if len(rows[i].path) != len(rows[j].path) {
			return len(rows[i].path) > len(rows[j].path)
		}
		return rows[i].host != "" && rows[j].host == ""
	})
	t.mu.Lock()
	t.rows = rows
	t.mu.Unlock()
	return nil
}

func (t *table) Resolve(host, path, method string) (Match, bool) {
	t.mu.RLock()
	rows := t.rows
	t.mu.RUnlock()
	for i := range rows {
		row := &rows[i]
		if row.host != "" && row.host != host {
			continue
		}
		if len(row.methods) > 0 && !row.methods[method] {
			continue
		}
		if !matchPath(path, row.path, row.exact) {
			continue
		}
		strip := ""
		if !row.exact {
			strip = row.path
		}
		return Match{Namespace: row.namespace, Function: row.function, StripPrefix: strip, Auth: row.auth}, true
	}
	return Match{}, false
}

func (t *table) Hosts() []string {
	t.mu.RLock()
	rows := t.rows
	t.mu.RUnlock()
	seen := map[string]bool{}
	out := []string{}
	for i := range rows {
		if h := rows[i].host; h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// matchPath is exact equality (Exact) or segment-aware prefix ("/x" matches "/x" and "/x/y",
// not "/x-2"), mirroring the gateway embedded matcher.
func matchPath(path, prefix string, exact bool) bool {
	if exact {
		return path == prefix
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
