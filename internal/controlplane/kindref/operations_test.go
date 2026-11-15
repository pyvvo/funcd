package kindref

import (
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/controlplane"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var update = flag.Bool("update", false, "rewrite testdata/undescribed-operations.txt from the API document")

const operationsList = "testdata/undescribed-operations.txt"

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

// TestFunctionCallInTheSpec holds the call of a Function, which the API reference shows, to ADR-0329 Decision 2: a
// server on the gateway's address, the namespace in the X-Funcd-Namespace header, an optional security, and a control
// plane that neither documents nor routes the call.
func TestFunctionCallInTheSpec(t *testing.T) {
	op := operationOf(t, controlplane.SpecAPI().OpenAPI(), "invokeFunction")
	require.Len(t, op.Servers, 1)
	require.Equal(t, "{gateway}", op.Servers[0].URL)
	require.Contains(t, op.Servers[0].Variables["gateway"].Description, "server.dataPlaneAddr")
	require.True(t, slices.ContainsFunc(op.Parameters, func(p *huma.Param) bool {
		return p.Name == "X-Funcd-Namespace" && p.In == "header"
	}), "the call names its namespace in the X-Funcd-Namespace header")
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

// TestEveryOperationIsDescribed holds every operation to a summary and its answers, but those that
// testdata/undescribed-operations.txt lists; the list only shrinks (ADR-0329 Decision 5). go test -run
// TestEveryOperationIsDescribed -update rewrites it. A described operation's cases each have a problem type or a body,
// a when and a fix, and it lists no default; no description names an ADR (Decision 7).
func TestEveryOperationIsDescribed(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	got := UndescribedOperations(doc)
	if *update {
		require.NoError(t, os.WriteFile(operationsList, []byte(strings.Join(got, "\n")+"\n"), 0o644))
		return
	}
	b, err := os.ReadFile(operationsList)
	require.NoError(t, err)
	listed := slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"), func(s string) bool { return s == "" })
	require.NoError(t, checkOperations(got, listed))
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

// errorCase is one entry of a response's x-funcd-cases, the extension in which controlplane.Describe lists a status's
// cases.
type errorCase struct {
	Type string `json:"type"`
	Body string `json:"body"`
	When string `json:"when"`
	Fix  string `json:"fix"`
}

// casesOf reads a response's x-funcd-cases.
func casesOf(r *huma.Response) ([]errorCase, error) {
	ext, ok := r.Extensions["x-funcd-cases"]
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

// checkOperations fails on an undescribed operation that the list does not hold, and on a listed one that is described
// or gone: the list only shrinks.
func checkOperations(got, listed []string) error {
	var problems []string
	if missing := slices.DeleteFunc(slices.Clone(got), func(id string) bool { return slices.Contains(listed, id) }); len(missing) > 0 {
		problems = append(problems, "operations with no summary or answers, which need controlplane.Describe: "+
			strings.Join(missing, ", "))
	}
	if stale := slices.DeleteFunc(slices.Clone(listed), func(id string) bool { return slices.Contains(got, id) }); len(stale) > 0 {
		problems = append(problems, "listed operations that are described or gone, to remove from "+operationsList+": "+
			strings.Join(stale, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
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
	err := checkOperations(got, nil)
	require.ErrorContains(t, err, "createThing")
	require.ErrorContains(t, err, "listThings")
	require.NoError(t, checkOperations(got, []string{"createThing", "listThings"}), "the list holds them")
	require.ErrorContains(t, checkOperations(got, []string{"createThing", "getThing", "listThings"}), "getThing",
		"a described operation leaves the list")
}

// scenario: no-adr-in-description
func TestScenarioNoADRInDescription(t *testing.T) {
	named := adrNamed(testDoc())
	require.Equal(t, []string{"getThing parameter If-Match: conditional on it (ADR-0210)"}, named)
}
