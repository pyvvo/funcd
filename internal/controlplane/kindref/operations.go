package kindref

import (
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/reference"
)

// TagFeatures places the operations whose tag is not a kind: Hold and PlatformBackup on "disaster-recovery",
// DeadLetter on "services/events" (ADR-0329 Decision 3), on that feature's Spec tab and in its section of the API
// reference (ADR-0331 Decision 3.2).
//
//nolint:gochecknoglobals // an immutable lookup table, the ADR's contract
var TagFeatures = map[string]string{
	"Hold":           "disaster-recovery",
	"PlatformBackup": "disaster-recovery",
	"DeadLetter":     "services/events",
}

// operation is one operation of the document, at its method and path.
type operation struct {
	method, path string
	op           *huma.Operation
}

// operationsOf returns every operation of doc, by path and then by method: GET, POST, PUT, PATCH and DELETE.
func operationsOf(doc *huma.OpenAPI) []operation {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	var out []operation
	for _, path := range slices.Sorted(maps.Keys(doc.Paths)) {
		item := doc.Paths[path]
		for i, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				out = append(out, operation{method: methods[i], path: path, op: op})
			}
		}
	}
	return out
}

// tag is the operation's first tag: its kind, or a name TagFeatures places.
func (o operation) tag() string {
	if len(o.op.Tags) == 0 {
		return ""
	}
	return o.op.Tags[0]
}

// described reports whether the operation has a summary and a response besides its success and default.
func (o operation) described() bool {
	if o.op.Summary == "" {
		return false
	}
	for code := range o.op.Responses {
		if status, err := strconv.Atoi(code); err == nil && (status < 200 || status > 299) {
			return true
		}
	}
	return false
}

// UndescribedOperations returns, sorted, the ID of every operation whose tag is not an Omitted kind and that has no
// summary, or no response but its success and default.
func UndescribedOperations(doc *huma.OpenAPI) []string {
	var out []string
	for _, o := range operationsOf(doc) {
		if !slices.Contains(Omitted, v1.Kind(o.tag())) && !o.described() {
			out = append(out, o.op.OperationID)
		}
	}
	slices.Sort(out)
	return out
}

// featureOperations returns f's described operations: those of its kinds, in their order, then those of the tags
// TagFeatures places on it.
func featureOperations(ops []operation, f Feature) []operation {
	rank := func(o operation) int {
		if i := slices.Index(f.Kinds, v1.Kind(o.tag())); i >= 0 {
			return i
		}
		if TagFeatures[o.tag()] == f.Path {
			return len(f.Kinds)
		}
		return -1
	}
	var out []operation
	for _, o := range ops {
		if rank(o) >= 0 && o.described() {
			out = append(out, o)
		}
	}
	slices.SortStableFunc(out, func(a, b operation) int { return rank(a) - rank(b) })
	return out
}

// httpSection writes f's HTTP API section (ADR-0331 Decision 3.6): a row for each described operation of its kinds and
// of the tags that TagFeatures places on it, with its method and path, and its summary linking to its page of the API
// reference, relative to the Spec page's file, as the guide links pages. up leads from that file's folder to develop/.
func httpSection(d document, f Feature, up string) []string {
	ops := featureOperations(d.ops, f)
	if len(ops) == 0 {
		return nil
	}
	lines := []string{"", "## HTTP API", "", "The table lists each described operation of these kinds, with its method " +
		"and path. The operation's page in the API reference gives its parameters, its body, its authentication and " +
		"every answer, with when it happens and what to do.", "", "| Method | Path | Operation |", "| --- | --- | --- |"}
	for _, o := range ops {
		lines = append(lines, "| `"+o.method+"` | `"+o.path+"` | ["+reference.Cell(o.op.Summary)+"]("+up+"../api/"+
			d.paths[o.op.OperationID]+".md) |")
	}
	return lines
}
