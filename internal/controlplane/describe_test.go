package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// Describe sets the summary, the two schemes and one response per status, written from its cases, keeps the success
// the operation holds, and leaves op.Errors unset, so huma adds no 422 or 500 of its own (ADR-0329 Decision 6).
func TestDescribeWritesTheResponsesOnly(t *testing.T) {
	op := controlplane.Describe(huma.Operation{OperationID: "getThing",
		Responses: map[string]*huma.Response{"200": {Description: "The thing."}}}, "Read a thing",
		controlplane.ErrorCase{Status: http.StatusConflict, Type: "urn:funcd:problem:conflict", When: "A.", Fix: "B."},
		controlplane.ErrorCase{Status: http.StatusConflict, Type: "urn:funcd:problem:conflict", When: "C.", Fix: "D."},
		controlplane.ErrorCase{Status: http.StatusUnprocessableEntity, Body: "ContractViolation", When: "E.", Fix: "F."})
	require.Equal(t, "Read a thing", op.Summary)
	require.Nil(t, op.Errors)
	require.Equal(t, []map[string][]string{{"bearer": {}}, {"apiKey": {}}}, op.Security)
	require.Equal(t, "The thing.", op.Responses["200"].Description)
	conflict := op.Responses["409"]
	require.Equal(t, "- `urn:funcd:problem:conflict`: A. B.\n- `urn:funcd:problem:conflict`: C. D.", conflict.Description)
	require.Equal(t, "#/components/schemas/FaultError", conflict.Content["application/problem+json"].Schema.Ref)
	cases, err := json.Marshal(conflict.Extensions["x-funcd-cases"])
	require.NoError(t, err)
	require.JSONEq(t, `[{"type": "urn:funcd:problem:conflict", "when": "A.", "fix": "B."},
		{"type": "urn:funcd:problem:conflict", "when": "C.", "fix": "D."}]`, string(cases))
	body := op.Responses["422"]
	require.Equal(t, "`ContractViolation`: E. F.", body.Description)
	require.Equal(t, "#/components/schemas/ContractViolation", body.Content["application/json"].Schema.Ref)
}

// A described kind's operations list every answer and no default; a kind not described yet keeps huma's.
func TestOnlyDescribedKindsLoseTheDefault(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	replace := doc.Paths["/apis/funcd.io/v1alpha1/namespaces/{namespace}/kvstores/{name}"].Put
	require.Nil(t, replace.Responses["default"])
	require.Contains(t, replace.Responses, "409")
	require.NotContains(t, replace.Responses, "500", "huma adds no 500 to a described operation")
	other := doc.Paths["/apis/funcd.io/v1alpha1/namespaces/{namespace}/routes/{name}"].Put
	require.Empty(t, other.Summary)
	require.NotNil(t, other.Responses["default"])
}

// The control plane's /docs reads the document it serves, which holds the cases of replaceKVStore.
func TestDocsShowTheCasesOfReplaceKVStore(t *testing.T) {
	r := chi.NewRouter()
	controlplane.NewAPI(r, controlplane.NewStubHandlers())
	docs := httptest.NewRecorder()
	r.ServeHTTP(docs, httptest.NewRequest(http.MethodGet, "/docs", nil))
	require.Equal(t, http.StatusOK, docs.Code)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Responses   map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	replace := doc.Paths["/apis/funcd.io/v1alpha1/namespaces/{namespace}/kvstores/{name}"]["put"]
	require.Equal(t, "replaceKVStore", replace.OperationID)
	require.Contains(t, replace.Responses["409"].Description, "The KVStore has changed since the resourceVersion")
	require.NotContains(t, doc.Paths, "/function/{name}", "the control plane serves no call of a Function")
}

// OperationOf names a request by the operations the server registered, so a route that Deps leaves out names none,
// and the call of a Function, which only the gateway serves, names none either (ADR-0329 Decision 4).
func TestOperationOfNamesTheServersOwnOperations(t *testing.T) {
	h, err := controlplane.NewServer(controlplane.Deps{Store: store.New(memory.New()), Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{})})
	require.NoError(t, err)
	for target, want := range map[string]string{
		"PUT /apis/funcd.io/v1alpha1/namespaces/default/kvstores/s":           "replaceKVStore",
		"POST /apis/funcd.io/v1alpha1/namespaces/default/kvstores/s/handover": "handoverKVStore",
		"GET /apis/funcd.io/v1alpha1/namespaces":                              "listNamespaces",
		"GET /apis/funcd.io/v1alpha1/namespaces/default/functions/f/logs":     "",
		"PATCH /apis/funcd.io/v1alpha1/namespaces/default/kvstores/s":         "",
		"POST /function/echo": "",
		"GET /docs":           "",
	} {
		method, path, _ := strings.Cut(target, " ")
		require.Equal(t, want, controlplane.OperationOf(h, httptest.NewRequest(method, path, nil)), target)
	}
	require.Empty(t, controlplane.OperationOf(http.NotFoundHandler(),
		httptest.NewRequest(http.MethodGet, "/apis/funcd.io/v1alpha1/namespaces", nil)), "a handler NewServer did not build")
}
