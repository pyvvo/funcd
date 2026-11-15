package kindref

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/controlplane"
)

// section is the part of page under the heading, up to the next heading of the same level or higher.
func section(t *testing.T, page, heading string) string {
	t.Helper()
	start := strings.Index(page, "\n"+heading+"\n")
	require.GreaterOrEqual(t, start, 0, "the page has no %q", heading)
	level := strings.Index(heading, " ")
	rest := page[start+len(heading)+2:]
	for i := 0; i < len(rest); {
		next := strings.Index(rest[i:], "\n#")
		if next < 0 {
			break
		}
		line := rest[i+next+1:]
		if hashes := len(line) - len(strings.TrimLeft(line, "#")); hashes <= level {
			return rest[:i+next]
		}
		i += next + 1
	}
	return rest
}

func operationOf(t *testing.T, doc *huma.OpenAPI, id string) *huma.Operation {
	t.Helper()
	for _, o := range operationsOf(doc) {
		if o.op.OperationID == id {
			return o.op
		}
	}
	t.Fatalf("the document has no operation %s", id)
	return nil
}

// scenario: operation-shows-its-answers
func TestScenarioOperationShowsItsAnswers(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	pages, err := Render(doc)
	require.NoError(t, err)
	kv := string(pages["develop/services/kv/spec.md"])
	require.Less(t, strings.Index(kv, "## Handler API"), strings.Index(kv, "## HTTP API"), "after the Handler API")
	require.Contains(t, kv, "## HTTP API\n\nEach described operation of these kinds,")
	replace := section(t, kv, "### Replace a KVStore")
	require.Contains(t, replace, "| `PUT` | `/apis/funcd.io/v1alpha1/namespaces/{namespace}/kvstores/{name}` | "+
		"`replaceKVStore` | `bearer` or `apiKey` |")
	for _, param := range []string{"| `namespace` | path | string, required, a DNS-1123 label |",
		"| `name` | path | string, required, a DNS-1123 label |", "| `If-Match` | header | string |",
		"| `dryRun` | query | boolean |"} {
		require.Contains(t, replace, param)
	}
	require.Contains(t, replace, "Body: [`KVStore`](#kvstore), required. Answer: [`KVStore`](#kvstore).")
	require.Contains(t, replace, "| 409 | `urn:funcd:problem:conflict` | The KVStore has changed since the "+
		"resourceVersion that If-Match or metadata.resourceVersion names. | Read the KVStore again,")
	op := operationOf(t, doc, "replaceKVStore")
	for code, resp := range op.Responses {
		cases, err := casesOf(resp)
		require.NoError(t, err)
		require.Equal(t, max(len(cases), 1), strings.Count(replace, "\n| "+code+" | "),
			"one row for each case of status %s", code)
	}
	require.Contains(t, kv, "| `bearer` | A credential of funcd's config, sent as Authorization: Bearer")
	require.Contains(t, section(t, kv, "### Hand a kept KVStore over to a Workflow"),
		"Body: [`KVStoreHandover`](#kvstorehandover), required.")
	require.Contains(t, kv, "#### `KVStoreHandover`\n\n| Field | Type and limits | Description |")
}

// scenario: function-call-documented
func TestScenarioFunctionCallDocumented(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	pages, err := Render(doc)
	require.NoError(t, err)
	call := section(t, string(pages["develop/workloads/function/spec.md"]), "### Call a Function")
	require.Contains(t, call, "| `POST` | `/function/{name}` | `invokeFunction` | none, `bearer` or `apiKey` |")
	require.Contains(t, call, "It is served at `{gateway}`, funcd's gateway")
	require.Contains(t, call, "| `X-Funcd-Namespace` | header |")
	violation := "[`ContractViolation`](#contractviolation)"
	for _, want := range []struct {
		status int
		body   string // the problem type, or the link to the body's table
		adr    string // the decision behind the answer
		when   string
	}{
		{200, "", "ADR-0329 Decision 2", "The handler's result."},
		{400, "`urn:funcd:problem:invalid`", "#90", "did not arrive within the gateway's read timeout"},
		{401, "`urn:funcd:problem:unauthorized`", "ADR-0113", "the call carries none, or one that funcd does not know"},
		{401, "`urn:funcd:problem:unauthorized`", "ADR-0113", "while server.auth.edge is off"},
		{403, "`urn:funcd:problem:forbidden`", "ADR-0113", "do not allow it to call Functions"},
		{404, "`urn:funcd:problem:not-found`", "ADR-0241", "answers at its own address"},
		{413, "`urn:funcd:problem:payload-too-large`", "ADR-0134", "larger than 1 MiB"},
		{422, violation, "ADR-0303", "does not match the Function's input contract"},
		{429, "`urn:funcd:problem:resource-exhausted`", "ADR-0112", "over the rate that server.limits sets"},
		{500, violation, "ADR-0303", "does not match the Function's output contract"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0112", "server.limits.maxInFlight"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0155", "The worker failed during the call"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0176", "could not read the namespace's settings"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0303", "cannot read the Function's contract"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0290", "The namespace is held for its data"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0290", "The namespace is held while a Restore loads it"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0161", "within invoke.activationTimeout"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0169", "The Function is Failed"},
		{503, "`urn:funcd:problem:unavailable`", "ADR-0028", "funcd is stopping"},
		{504, "`urn:funcd:problem:deadline-exceeded`", "ADR-0151", "within the call's deadline"},
	} {
		row := "\n| " + strconv.Itoa(want.status) + " | " + want.body + " | "
		i := strings.Index(call, row)
		require.GreaterOrEqual(t, i, 0, "%d (%s): no row %q", want.status, want.adr, row)
		require.Contains(t, call[i:], want.when, "%d (%s)", want.status, want.adr)
	}
	require.Contains(t, call, "#### `ContractViolation`\n\n| Field | Type and limits | Description |")
	require.Contains(t, call, "#### `ContractViolation.details[]`")

	op := operationOf(t, doc, "invokeFunction")
	require.Equal(t, "{gateway}", op.Servers[0].URL)
	require.Contains(t, op.Security, map[string][]string{}, "a call's security is optional")
	live := controlplane.NewAPI(chi.NewRouter(), controlplane.NewStubHandlers())
	for _, o := range operationsOf(live.OpenAPI()) {
		require.NotEqual(t, "invokeFunction", o.op.OperationID, "the control plane does not document the call")
	}
	r := chi.NewRouter()
	controlplane.NewAPI(r, controlplane.NewStubHandlers())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/function/echo", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNotFound, rec.Code, "the control plane does not route the call")
}

// TestEveryOperationIsDescribed holds every operation to a summary and its answers (ADR-0329 Decision 5). A described
// operation's cases each have a problem type or a body, a when and a fix, and it lists no default; no description names
// an ADR (Decision 7).
func TestEveryOperationIsDescribed(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	require.Empty(t, UndescribedOperations(doc), "operations with no summary or answers, which need controlplane.Describe")
	require.Empty(t, adrNamed(doc), "a description is written for users and names no ADR")
	for _, o := range operationsOf(doc) {
		if !o.described() {
			continue
		}
		require.Nil(t, o.op.Responses["default"], "%s: a described operation lists every answer, so no default",
			o.op.OperationID)
		for code, resp := range o.op.Responses {
			cases, err := casesOf(resp)
			require.NoError(t, err)
			for _, c := range cases {
				require.True(t, (c.Type == "") != (c.Body == ""), "%s %s: a problem type or a body", o.op.OperationID, code)
				require.NotEmpty(t, c.When, "%s %s: when", o.op.OperationID, code)
				require.NotEmpty(t, c.Fix, "%s %s: what to do", o.op.OperationID, code)
			}
		}
	}
}

var adrName = regexp.MustCompile(`\bADR-\d{4}\b`)

// adrNamed returns each description of an operation, a parameter, a body or a response that names an ADR.
func adrNamed(doc *huma.OpenAPI) []string {
	var out []string
	check := func(where, desc string) {
		if adrName.MatchString(desc) {
			out = append(out, where+": "+desc)
		}
	}
	for _, o := range operationsOf(doc) {
		id := o.op.OperationID
		check(id, o.op.Description)
		for _, p := range o.op.Parameters {
			check(id+" parameter "+p.Name, p.Description)
			if p.Schema != nil {
				check(id+" parameter "+p.Name, p.Schema.Description)
			}
		}
		if b := o.op.RequestBody; b != nil {
			check(id+" body", b.Description)
		}
		for code, resp := range o.op.Responses {
			check(id+" response "+code, resp.Description)
		}
	}
	slices.Sort(out)
	return out
}

// testDoc is a document of one kind's operations: listThings has no summary, createThing only its success and default,
// getThing is described, and the Service operation is an Omitted kind's.
func testDoc() *huma.OpenAPI {
	ok := &huma.Response{Description: "OK"}
	fallback := &huma.Response{Description: "Error"}
	return &huma.OpenAPI{Paths: map[string]*huma.PathItem{
		"/things": {
			Get:  &huma.Operation{OperationID: "listThings", Tags: []string{"Thing"}, Responses: map[string]*huma.Response{"200": ok, "409": ok}},
			Post: &huma.Operation{OperationID: "createThing", Summary: "Create a thing", Tags: []string{"Thing"}, Responses: map[string]*huma.Response{"200": ok, "default": fallback}},
		},
		"/things/{name}": {
			Get: &huma.Operation{OperationID: "getThing", Summary: "Read a thing", Tags: []string{"Thing"},
				Parameters: []*huma.Param{{Name: "If-Match", In: "header", Description: "conditional on it (ADR-0210)"}},
				Responses:  map[string]*huma.Response{"200": ok, "404": {Description: "No thing of that name."}}},
		},
		"/services": {
			Get: &huma.Operation{OperationID: "listServices", Tags: []string{"Service"}, Responses: map[string]*huma.Response{"200": ok}},
		},
	}}
}

// scenario: undocumented-operation-refused
func TestScenarioUndocumentedOperationRefused(t *testing.T) {
	got := UndescribedOperations(testDoc())
	require.Equal(t, []string{"createThing", "listThings"}, got, "the Omitted kind's and the described one are not listed")
}

// scenario: no-adr-in-description
func TestScenarioNoADRInDescription(t *testing.T) {
	named := adrNamed(testDoc())
	require.Equal(t, []string{"getThing parameter If-Match: conditional on it (ADR-0210)"}, named)
}
