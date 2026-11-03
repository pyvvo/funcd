package local

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/pathx"
)

// Facet is one provider's part of a worker's local API: the routes under /<segment>. Only this package builds one;
// the zero Facet, with no register function, serves nothing.
type Facet struct {
	segment  string
	register func(mux facetMux, caller Ref, logger *slog.Logger)
}

// facetMux is what a facet registers on: the handler's *http.ServeMux, or CheckFacets' recorder.
type facetMux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// WithFacets serves each non-zero facet on every caller's local API.
func WithFacets(facets ...Facet) Option {
	return func(o *options) { o.facets = append(o.facets, facets...) }
}

// CheckFacets returns an Invalid error naming the facet and the pattern or segment for an invalid segment, a pattern
// outside its facet's segment, two facets on one segment, or a facet on invoke, fetch or health.
func CheckFacets(facets ...Facet) error {
	const op = "workernode.local.CheckFacets"
	served := map[string]bool{}
	for _, f := range facets {
		if f.register == nil {
			continue
		}
		switch {
		case f.segment == "" || strings.ContainsAny(f.segment, "/{}"):
			return fault.Invalidf(op, "facet %q: a segment is one path segment, not empty, with no '/', '{' or '}'", f.segment)
		case builtIn(f.segment):
			return fault.Invalidf(op, "facet %q: the local API serves /%s itself", f.segment, f.segment)
		case served[f.segment]:
			return fault.Invalidf(op, "facet %q: two facets serve the segment %q", f.segment, f.segment)
		}
		served[f.segment] = true
		var rec patterns
		f.register(&rec, Ref{}, slog.New(slog.DiscardHandler))
		for _, p := range rec {
			if !pathx.HasPrefix(withoutMethod(p), "/"+f.segment) {
				return fault.Invalidf(op, "facet %q: the pattern %q is not a path under /%s", f.segment, p, f.segment)
			}
		}
	}
	return nil
}

// builtIn reports whether NewHandler serves segment itself (ADR-0310 Decision 3): POST /invoke/{alias}, /fetch/…
// before the mux, and GET /health/dependencies.
func builtIn(segment string) bool {
	switch segment {
	case "invoke", "fetch", "health":
		return true
	}
	return false
}

// withoutMethod is a ServeMux pattern without its optional method, "[HOST]/[PATH]", split as ServeMux splits it.
func withoutMethod(pattern string) string {
	if i := strings.IndexAny(pattern, " \t"); i >= 0 {
		return strings.TrimLeft(pattern[i+1:], " \t")
	}
	return pattern
}

// patterns is the facetMux CheckFacets registers a facet on: it records each pattern and serves nothing.
type patterns []string

func (p *patterns) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	*p = append(*p, pattern)
}
