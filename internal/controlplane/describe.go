package controlplane

import (
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// ErrorCase is one documented failure of an operation (ADR-0329 Decision 1).
type ErrorCase struct {
	Status int    // e.g. http.StatusConflict
	Type   string // the problem type, e.g. "urn:funcd:problem:conflict"; "" when Body names another schema
	Body   string // "" for a problem (FaultError); else the component schema of the body, e.g. "ContractViolation"
	When   string // one sentence, e.g. "The Workflow does not declare the store in spec.kv."
	Fix    string // one sentence, e.g. "Declare the store in the Workflow's spec.kv, then retry."
}

// casesExtension is the response extension that lists a status's cases; kindref reads it.
const casesExtension = "x-funcd-cases"

// The security schemes of the API: either one authenticates a call.
const (
	schemeBearer = "bearer"
	schemeAPIKey = "apiKey"
)

// securitySchemes are the schemes NewAPI registers once, for every described operation to name.
func securitySchemes() map[string]*huma.SecurityScheme {
	return map[string]*huma.SecurityScheme{
		schemeBearer: {Type: "http", Scheme: "bearer",
			Description: "A credential of funcd's config, sent as Authorization: Bearer followed by its token."},
		schemeAPIKey: {Type: "apiKey", In: "header", Name: "X-Api-Key",
			Description: "A credential of funcd's config, sent in the X-Api-Key header."},
	}
}

// caseDoc is one case as the x-funcd-cases extension lists it.
type caseDoc struct {
	Type string `json:"type,omitempty"`
	Body string `json:"body,omitempty"`
	When string `json:"when"`
	Fix  string `json:"fix"`
}

// Describe returns op with its summary, the bearer and apiKey schemes, and one response per status of cases, whose
// description is written from the cases and whose extension "x-funcd-cases" lists them. It never sets op.Errors,
// which would make huma add 422 and 500 and overwrite their descriptions (ADR-0329 Decision 6). A response op already
// holds, such as its success, is kept unless a case names its status.
func Describe(op huma.Operation, summary string, cases ...ErrorCase) huma.Operation {
	op.Summary = summary
	op.Security = []map[string][]string{{schemeBearer: {}}, {schemeAPIKey: {}}}
	responses := maps.Clone(op.Responses)
	if responses == nil {
		responses = map[string]*huma.Response{}
	}
	byStatus := map[int][]ErrorCase{}
	for _, c := range cases {
		byStatus[c.Status] = append(byStatus[c.Status], c)
	}
	for _, status := range slices.Sorted(maps.Keys(byStatus)) {
		responses[strconv.Itoa(status)] = caseResponse(byStatus[status])
	}
	op.Responses = responses
	return op
}

// caseResponse is the response of one status: a problem case's body is FaultError, which huma registers on every
// operation, as application/problem+json; another case's is the schema it names, as application/json.
func caseResponse(cases []ErrorCase) *huma.Response {
	content := map[string]*huma.MediaType{}
	docs := make([]caseDoc, len(cases))
	lines := make([]string, len(cases))
	for i, c := range cases {
		docs[i] = caseDoc{Type: c.Type, Body: c.Body, When: c.When, Fix: c.Fix}
		if c.Body == "" {
			content["application/problem+json"] = &huma.MediaType{Schema: &huma.Schema{Ref: schemaPrefix + faultSchema()}}
			lines[i] = "`" + c.Type + "`: " + c.When + " " + c.Fix
		} else {
			content["application/json"] = &huma.MediaType{Schema: &huma.Schema{Ref: schemaPrefix + c.Body}}
			lines[i] = "`" + c.Body + "`: " + c.When + " " + c.Fix
		}
	}
	description := lines[0]
	if len(lines) > 1 {
		description = "- " + strings.Join(lines, "\n- ")
	}
	return &huma.Response{Description: description, Content: content,
		Extensions: map[string]any{casesExtension: docs}} //nolint:forbidigo // huma.Response.Extensions is a map[string]any
}

// faultSchema is the component name huma gives the error body NewAPI installs.
func faultSchema() string { return huma.DefaultSchemaNamer(reflect.TypeFor[faultError](), "") }

// problem is a case whose body is the problem of kind k, with its status and type from fault.ToProblem.
func problem(k fault.Kind, when, fix string) ErrorCase {
	p := fault.ToProblem(&fault.Error{Kind: k})
	return ErrorCase{Status: p.Status, Type: p.Type, When: when, Fix: fix}
}

// blank is a case of an error that huma raises itself, which no fault kind decides, so its type is about:blank
// (newFaultError).
func blank(status int, when, fix string) ErrorCase {
	return ErrorCase{Status: status, Type: "about:blank", When: when, Fix: fix}
}

// kindCases are the cases a described kind's operations add to their verb's shared ones (ADR-0329 Decision 1). A kind
// whose operations are not described yet has none and keeps huma's responses.
type kindCases struct {
	create, replace, remove []ErrorCase
}

// restOperations are the operations of one kind's REST surface: list and create on its collection, get, replace and
// delete on an item.
type restOperations struct{ list, create, get, replace, remove huma.Operation }

// restOperationsOf returns a kind's operations at base and item, each described with its verb's shared cases and the
// kind's own when cases is set (ADR-0329 Decision 1). The generic route helpers call it, so each kind's instance of
// them carries none of this code.
func restOperationsOf(kind, kinds, base, item string, namespaced bool, cases *kindCases) restOperations {
	tags := []string{kind}
	op := func(id, method, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Tags: tags}
	}
	ops := restOperations{
		list: op("list"+kinds, http.MethodGet, base), create: op("create"+kind, http.MethodPost, base),
		get: op("get"+kind, http.MethodGet, item), replace: op("replace"+kind, http.MethodPut, item),
		remove: op("delete"+kind, http.MethodDelete, item),
	}
	if cases == nil {
		return ops
	}
	v := verbCases{kind: kind, kinds: kinds, namespaced: namespaced}
	list := "List the " + kinds
	if namespaced {
		list += " of a namespace"
	}
	ops.list = Describe(ops.list, list, v.list()...)
	ops.create = Describe(ops.create, "Create "+an(kind), v.create(cases.create)...)
	ops.get = Describe(ops.get, "Read "+an(kind), v.get()...)
	ops.replace = Describe(ops.replace, "Replace "+an(kind), v.replace(cases.replace)...)
	ops.remove = Describe(ops.remove, "Delete "+an(kind), v.remove(cases.remove)...)
	return ops
}

// unauthorized is every described control-plane operation's 401, which the authentication middleware writes.
func unauthorized() ErrorCase {
	return problem(fault.Unauthorized, "The request carries no bearer token or API key, or one that funcd does not know.",
		"Send a credential of funcd's config as a bearer token in the Authorization header, or in X-Api-Key.")
}

// forbidden is the 403 of a namespaced operation whose credential's role or namespaces do not allow what, such as
// "create a KVStore in the namespace".
func forbidden(what string) ErrorCase {
	return problem(fault.Forbidden, "The credential's role or namespaces do not allow it to "+what+".",
		"Use a credential whose role allows it, such as a developer of the namespace or an admin.")
}

// badPath is the 422 of a path parameter that breaks its schema.
func badPath(params string) ErrorCase {
	return blank(http.StatusUnprocessableEntity, "The "+params+" in the path is not a DNS-1123 label.",
		"Use at most 63 lowercase letters, digits and hyphens, starting and ending with a letter or a digit.")
}

// badQuery is a write's 422 for a query parameter it does not take, so a dryRun never reaches an operation that
// would store the write.
func badQuery() ErrorCase {
	return blank(http.StatusUnprocessableEntity, "The query holds a parameter that the operation does not take.",
		"Send only the parameters that the operation lists.")
}

// badDryRun is the 422 of a dryRun that is no boolean.
func badDryRun() ErrorCase {
	return blank(http.StatusUnprocessableEntity, "The dryRun parameter is neither true nor false.",
		"Send dryRun=true, or leave it out.")
}

// bodyCases are the answers of an operation whose body huma reads: none or not JSON, over its 1 MiB cap, in a format
// it does not decode, or against the schema.
func bodyCases() []ErrorCase {
	return []ErrorCase{
		blank(http.StatusBadRequest, "The body is missing or is not JSON.", "Send a JSON body."),
		problem(fault.PayloadTooLarge, "The body is larger than 1 MiB.", "Send a smaller body."),
		blank(http.StatusUnsupportedMediaType, "The Content-Type of the body is not JSON.",
			"Send the body as application/json."),
		blank(http.StatusUnprocessableEntity,
			"The body breaks the schema: a field that it does not know, of the wrong type or past its limits.",
			"Correct the field at the location that the detail gives, such as body.spec.tables."),
	}
}

// badIfMatch is the 400 of an If-Match header that is not one strong entity-tag.
func badIfMatch() ErrorCase {
	return problem(fault.Invalid, "The If-Match header is not one strong entity-tag, such as \"42\", or *.",
		"Send one If-Match line with the quoted resourceVersion, or *.")
}

// verbCases are the cases each verb of a described kind shares with every other kind's (ADR-0329 Decision 1), worded
// for a namespaced kind or a cluster-scoped one.
type verbCases struct {
	kind, kinds string
	namespaced  bool
}

// in is where the kind's objects are, after a verb.
func (v verbCases) in() string {
	if v.namespaced {
		return " in the namespace"
	}
	return ""
}

// path names the parameters of an item's path.
func (v verbCases) path() string {
	if v.namespaced {
		return "namespace or the name"
	}
	return "name"
}

func (v verbCases) notFound() ErrorCase {
	if v.namespaced {
		return problem(fault.NotFound, "No "+v.kind+" of that name exists in the namespace.",
			"Check the name and the namespace.")
	}
	return problem(fault.NotFound, "No "+v.kind+" of that name exists.", "Check the name.")
}

// ruleBroken is a write's 400 for an object that breaks a rule the schema cannot hold.
func ruleBroken() ErrorCase {
	return problem(fault.Invalid, "A field breaks a rule that the schema cannot hold, which the detail names.",
		"Correct the field that the detail names.")
}

func (v verbCases) stale() ErrorCase {
	return problem(fault.Conflict,
		"The "+v.kind+" has changed since the resourceVersion that If-Match or metadata.resourceVersion names.",
		"Read the "+v.kind+" again, apply the change to it, and send it with its new resourceVersion.")
}

// forbidden is the 403 of a credential whose role, or for a namespaced kind its namespaces, do not allow what.
func (v verbCases) forbidden(what string) ErrorCase {
	if v.namespaced {
		return forbidden(what + " in the namespace")
	}
	return problem(fault.Forbidden, "The credential's role does not allow it to "+what+".", "Use an admin's credential.")
}

func (v verbCases) list() []ErrorCase {
	cases := []ErrorCase{unauthorized(), v.forbidden("list " + v.kinds)}
	if v.namespaced {
		cases = append(cases, badPath("namespace"))
	}
	return cases
}

func (v verbCases) get() []ErrorCase {
	return []ErrorCase{unauthorized(), v.forbidden("read " + an(v.kind)), v.notFound(), badPath(v.path())}
}

func (v verbCases) create(own []ErrorCase) []ErrorCase {
	cases := []ErrorCase{unauthorized(), v.forbidden("create " + an(v.kind)), badQuery(), badDryRun(), ruleBroken(),
		problem(fault.Conflict, capital(an(v.kind))+" of that name already exists"+v.in()+".",
			"Choose another name, or replace the existing "+v.kind+".")}
	if v.namespaced {
		cases = append(cases, badPath("namespace"), problem(fault.Invalid,
			"The body's metadata.namespace is not the namespace of the path.",
			"Send the object to the path of its own namespace."))
	}
	return slices.Concat(cases, bodyCases(), own)
}

func (v verbCases) replace(own []ErrorCase) []ErrorCase {
	meta := "metadata.name"
	if v.namespaced {
		meta = "metadata.namespace or metadata.name"
	}
	return slices.Concat([]ErrorCase{unauthorized(), v.forbidden("replace " + an(v.kind)), v.notFound(),
		badPath(v.path()), badQuery(), badDryRun(), badIfMatch(), ruleBroken(), v.stale(),
		problem(fault.Invalid, "The body's "+meta+" is not the path's, or If-Match and metadata.resourceVersion "+
			"name two versions.", "Send the object to its own path, with at most one version.")},
		bodyCases(), own)
}

func (v verbCases) remove(own []ErrorCase) []ErrorCase {
	return slices.Concat([]ErrorCase{unauthorized(), v.forbidden("delete " + an(v.kind)), v.notFound(),
		badPath(v.path()), badQuery(), badIfMatch(), v.stale()}, own)
}

// an is kind with its indefinite article.
func an(kind string) string {
	if strings.ContainsRune("AEIOU", rune(kind[0])) {
		return "an " + kind
	}
	return "a " + kind
}

// capital is s with its first letter in upper case.
func capital(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
