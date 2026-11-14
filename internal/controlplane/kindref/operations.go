package kindref

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/reference"
)

// TagFeatures places the operations whose tag is not a kind: Hold and PlatformBackup on "disaster-recovery",
// DeadLetter on "services/events" (ADR-0329 Decision 3).
//
//nolint:gochecknoglobals // an immutable lookup table, the ADR's contract
var TagFeatures = map[string]string{
	"Hold":           "disaster-recovery",
	"PlatformBackup": "disaster-recovery",
	"DeadLetter":     "services/events",
}

// casesExtension is the response extension in which controlplane.Describe lists a status's cases.
const casesExtension = "x-funcd-cases"

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

// errorCase is one entry of a response's x-funcd-cases.
type errorCase struct {
	Type string `json:"type"`
	Body string `json:"body"`
	When string `json:"when"`
	Fix  string `json:"fix"`
}

// casesOf reads a response's x-funcd-cases, as Describe holds it or as a parsed document does.
func casesOf(r *huma.Response) ([]errorCase, error) {
	ext, ok := r.Extensions[casesExtension]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(ext)
	if err != nil {
		return nil, err
	}
	var cases []errorCase
	return cases, json.Unmarshal(raw, &cases)
}

// httpSection writes f's HTTP API section (ADR-0329 Decision 3): each described operation with its method, path,
// operation ID and authentication, its parameters, its body and answer linked to their tables, and every status it
// answers with its problem type, when and what to do.
func (r *renderer) httpSection(d document, f Feature) error {
	ops := featureOperations(d.ops, f)
	if len(ops) == 0 {
		return nil
	}
	r.lines = append(r.lines, "", "## HTTP API", "", "Each operation of these kinds, with its parameters, its body "+
		"and its answer, and every status that it answers. A refusal's body is a problem+json document whose type "+
		"the status table gives, unless the table names another body.")
	var schemes []string
	for _, o := range ops {
		for _, req := range o.op.Security {
			for _, name := range slices.Sorted(maps.Keys(req)) {
				if !slices.Contains(schemes, name) {
					schemes = append(schemes, name)
				}
			}
		}
	}
	if len(schemes) > 0 {
		r.lines = append(r.lines, "", "| Authentication | How a call sends it |", "| --- | --- |")
		for _, name := range schemes {
			desc := ""
			if s := d.schemes[name]; s != nil {
				desc = s.Description
			}
			r.lines = append(r.lines, "| `"+name+"` | "+reference.Cell(desc)+" |")
		}
	}
	for _, o := range ops {
		if err := r.operation(o, f); err != nil {
			return err
		}
	}
	return nil
}

// operation writes one operation, then a table for each body schema the page does not show yet.
func (r *renderer) operation(o operation, f Feature) error {
	op := o.op
	r.lines = append(r.lines, "", "### "+op.Summary)
	if op.Description != "" {
		r.lines = append(r.lines, "", reference.Cell(op.Description))
	}
	r.lines = append(r.lines, "", "| Method | Path | Operation ID | Authentication |", "| --- | --- | --- | --- |",
		"| `"+o.method+"` | `"+o.path+"` | `"+op.OperationID+"` | "+authentication(op.Security)+" |")
	for _, s := range op.Servers {
		line := "It is served at `" + s.URL + "`, " + lowerFirst(s.Description)
		for _, name := range slices.Sorted(maps.Keys(s.Variables)) {
			line += " `{" + name + "}` is " + lowerFirst(s.Variables[name].Description)
		}
		r.lines = append(r.lines, "", reference.Cell(line))
	}
	var nested []child
	if params := parameters(op.Parameters); len(params) > 0 {
		r.lines = append(r.lines, "", "| Parameter | In | Type and limits | Description |", "| --- | --- | --- | --- |")
		for _, p := range params {
			_, s := r.resolve(p.Schema)
			kind := []string{scalar(s)}
			if p.Required {
				kind = append(kind, "required")
			}
			kind = append(kind, limits(s)...)
			r.lines = append(r.lines, "| `"+p.Name+"` | "+p.In+" | "+reference.Cell(strings.Join(kind, ", "))+" | "+
				reference.Cell(p.Description)+" |")
		}
	}
	var parts []string
	if b := op.RequestBody; b != nil {
		part := "Body: " + r.schemaLink(mediaSchema(b.Content), f, &nested)
		if b.Required {
			part += ", required"
		}
		parts = append(parts, part+".")
	}
	codes := slices.SortedFunc(maps.Keys(op.Responses), func(a, b string) int {
		x, _ := strconv.Atoi(a)
		y, _ := strconv.Atoi(b)
		return x - y
	})
	for _, code := range codes {
		if status, _ := strconv.Atoi(code); status >= 200 && status <= 299 {
			answer := "none"
			if s := mediaSchema(op.Responses[code].Content); s != nil {
				answer = r.schemaLink(s, f, &nested)
			}
			parts = append(parts, "Answer: "+answer+".")
			break
		}
	}
	if len(parts) > 0 {
		r.lines = append(r.lines, "", strings.Join(parts, " "))
	}
	r.lines = append(r.lines, "", "| Status | Problem type | When | What to do |", "| --- | --- | --- | --- |")
	for _, code := range codes {
		resp := op.Responses[code]
		cases, err := casesOf(resp)
		if err != nil {
			return err
		}
		if len(cases) == 0 {
			r.lines = append(r.lines, "| "+code+" |  | "+reference.Cell(resp.Description)+" |  |")
			continue
		}
		for _, c := range cases {
			kind := "`" + c.Type + "`"
			if c.Body != "" {
				kind = r.schemaLink(&huma.Schema{Ref: "#/components/schemas/" + c.Body}, f, &nested)
			}
			r.lines = append(r.lines, "| "+code+" | "+kind+" | "+reference.Cell(c.When)+" | "+reference.Cell(c.Fix)+" |")
		}
	}
	for _, c := range nested {
		r.section("`"+c.path+"`", 4, c.schema, c.path, c.owner)
	}
	return nil
}

// schemaLink names the schema s in a sentence: a link to its table, which nested queues when the page does not show
// it yet; an array of such a link; or a JSON type.
func (r *renderer) schemaLink(s *huma.Schema, f Feature, nested *[]child) string {
	if s == nil {
		return "none"
	}
	name, target := r.resolve(s)
	switch {
	case name != "" && slices.Contains(f.Kinds, v1.Kind(name)):
		return "[`" + name + "`](#" + slug(name) + ")"
	case name != "" && len(target.Properties) > 0:
		return r.link(name, name, name, target, nested)
	case target.Type == huma.TypeArray && target.Items != nil:
		return "array of " + r.schemaLink(target.Items, f, nested)
	case target.Type == "":
		return "any JSON value"
	}
	return scalar(target)
}

// mediaSchema is the schema of a body's JSON media type, else of its only one.
func mediaSchema(content map[string]*huma.MediaType) *huma.Schema {
	for _, ct := range []string{"application/json", "application/problem+json"} {
		if m := content[ct]; m != nil {
			return m.Schema
		}
	}
	for _, m := range content {
		return m.Schema
	}
	return nil
}

// parameters orders an operation's parameters by where they go: the path, the headers, then the query.
func parameters(params []*huma.Param) []*huma.Param {
	order := []string{"path", "header", "query", "cookie"}
	return slices.SortedStableFunc(slices.Values(params), func(a, b *huma.Param) int {
		return slices.Index(order, a.In) - slices.Index(order, b.In)
	})
}

// authentication lists the ways an operation's security accepts a call: its schemes, or none when a requirement is
// empty.
func authentication(security []map[string][]string) string {
	var ways []string
	for _, req := range security {
		if len(req) == 0 {
			ways = append(ways, "none")
			continue
		}
		names := slices.Sorted(maps.Keys(req))
		for i, n := range names {
			names[i] = "`" + n + "`"
		}
		ways = append(ways, strings.Join(names, " and "))
	}
	if len(ways) == 0 {
		return "none"
	}
	if len(ways) == 1 {
		return ways[0]
	}
	return strings.Join(ways[:len(ways)-1], ", ") + " or " + ways[len(ways)-1]
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
