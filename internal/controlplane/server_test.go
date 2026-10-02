package controlplane_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

const (
	devToken  = "dev-secret"
	viewToken = "view-secret"
)

// newServer builds the API server backed by an in-memory store + RBAC, with a
// developer (team-a) and a viewer (team-a) credential.
func newServer(t *testing.T) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
	})
	require.NoError(t, err)
	return h
}

// functionBody builds a Function request body in the shape huma's generated request
// schema expects: a nested "TypeMeta" object (huma does not honor the ",inline" tag) +
// metadata. The server stamps TypeMeta from the route kind regardless.
func functionBody(t *testing.T, ns, name, rg string) []byte {
	t.Helper()
	m := map[string]interface{}{
		"TypeMeta": map[string]interface{}{"apiVersion": "funcd.io/v1alpha1", "kind": "Function"},
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "resourceGroup": rg},
		"spec": map[string]interface{}{
			"runtime": "nodejs22",
			"handler": "app.handler",
			"image":   "oci://example/app:v1",
		},
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

func do(t *testing.T, srv http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

const fnBase = "/apis/funcd.io/v1alpha1/namespaces/team-a/functions"

// scenario: unauthenticated-rejected.
func TestScenarioUnauthenticatedRejected(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	rec := do(t, srv, http.MethodGet, fnBase, "", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

	// An unknown token is also 401.
	rec2 := do(t, srv, http.MethodGet, fnBase, "bogus", nil)
	require.Equal(t, http.StatusUnauthorized, rec2.Code)
}

// scenario: token-authenticates-to-identity — a valid token authenticates and is
// authorized (200 in its namespace), proving token → Identity → PDP.
func TestScenarioTokenAuthenticatesToIdentity(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	rec := do(t, srv, http.MethodGet, fnBase, devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, "valid token authenticates + authorizes the list")

	// The same token in a namespace it is not scoped to is authenticated but denied (403).
	rec2 := do(t, srv, http.MethodGet, "/apis/funcd.io/v1alpha1/namespaces/team-b/functions", devToken, nil)
	require.Equal(t, http.StatusForbidden, rec2.Code, "identity is enforced across namespaces")
}

// scenario: admission-rejects-invalid — a body whose namespace ≠ the path namespace is 400.
func TestScenarioAdmissionRejectsInvalid(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	// Replace at path team-a with a body declaring namespace team-b → admission 400.
	// (huma renders handler errors as a problem-shaped application/json body — ADR-0005;
	// the application/problem+json content-type is asserted on the middleware 401 path.)
	body := functionBody(t, "team-b", "echo", "rg1")
	rec := do(t, srv, http.MethodPut, fnBase+"/echo", devToken, body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "does not match", "admission detail names the namespace mismatch")
}

// Issue 61: a create honours the path namespace (ADR-0018 §4 step 2) — a body namespace that differs
// from it, or is missing, is a 400 with nothing stored, and the path namespace is validated as on GET.
func TestIssue61_CreateRejectsPathBodyNamespaceMismatch(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	const nsBase = "/apis/funcd.io/v1alpha1/namespaces/"

	noNamespace := map[string]map[string]interface{}{}
	require.NoError(t, json.Unmarshal(functionBody(t, "team-a", "echo", "rg1"), &noNamespace))
	delete(noNamespace["metadata"], "namespace")
	noNamespaceBody, err := json.Marshal(noNamespace)
	require.NoError(t, err)

	configMapBody, err := json.Marshal(map[string]interface{}{
		"TypeMeta": map[string]interface{}{"apiVersion": "funcd.io/v1alpha1", "kind": "ConfigMap"},
		"metadata": map[string]interface{}{"name": "cfg", "namespace": "team-a", "resourceGroup": "rg1"},
		"spec":     map[string]interface{}{},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name, path string
		body       []byte
		code       int
		detail     string
	}{
		{"function body namespace differs", nsBase + "team-b/functions", functionBody(t, "team-a", "echo", "rg1"), http.StatusBadRequest, "does not match"},
		{"configmap body namespace differs", nsBase + "team-b/configmaps", configMapBody, http.StatusBadRequest, "does not match"},
		{"body namespace missing", fnBase, noNamespaceBody, http.StatusBadRequest, "does not match"},
		{"path namespace invalid", nsBase + "NOT_A_LABEL/functions", functionBody(t, "team-a", "echo", "rg1"), http.StatusUnprocessableEntity, "path.namespace"},
	} {
		rec := do(t, srv, http.MethodPost, tc.path, devToken, tc.body)
		require.Equal(t, tc.code, rec.Code, "%s: %s", tc.name, rec.Body.String())
		require.Contains(t, rec.Body.String(), tc.detail, tc.name)
	}

	for _, kind := range []string{"functions", "configmaps"} {
		rec := do(t, srv, http.MethodGet, nsBase+"team-a/"+kind, devToken, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var items []json.RawMessage
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &items))
		require.Empty(t, items, "nothing persisted in team-a %s", kind)
	}
}

// scenario: crud-roundtrips-through-store — create → get → list → delete round-trips real state.
func TestScenarioCrudRoundtripsThroughStore(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	// create
	rec := do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "echo", "rg1"))
	require.GreaterOrEqual(t, rec.Code, 200)
	require.Less(t, rec.Code, 300, "create succeeds: %s", rec.Body.String())

	// get reflects the stored object
	rec = do(t, srv, http.MethodGet, fnBase+"/echo", devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got v1.Function
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, v1.ObjectName("echo"), got.Name)
	require.NotEmpty(t, got.UID, "store stamped a uid")

	// list contains it
	rec = do(t, srv, http.MethodGet, fnBase, devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var list []v1.Function
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)

	// delete then get is 404
	rec = do(t, srv, http.MethodDelete, fnBase+"/echo", devToken, nil)
	require.GreaterOrEqual(t, rec.Code, 200)
	require.Less(t, rec.Code, 300, "delete succeeds")

	rec = do(t, srv, http.MethodGet, fnBase+"/echo", devToken, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, "deleted object is gone")
}

// An explicit name wins over generateName (ADR-0133): a duplicate name is a 409, never a renamed second object.
func TestIssue165_ExplicitNameWithGenerateNameConflicts(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	withGenerateName := func(name string) []byte {
		var body struct {
			TypeMeta map[string]interface{} `json:"TypeMeta"`
			Metadata map[string]interface{} `json:"metadata"`
			Spec     map[string]interface{} `json:"spec"`
		}
		require.NoError(t, json.Unmarshal(functionBody(t, "team-a", name, "rg1"), &body))
		body.Metadata["generateName"] = "dup-"
		if name == "" {
			delete(body.Metadata, "name")
		}
		b, err := json.Marshal(body)
		require.NoError(t, err)
		return b
	}

	rec := do(t, srv, http.MethodPost, fnBase, devToken, functionBody(t, "team-a", "dup", "rg1"))
	require.Less(t, rec.Code, 300, "first create: %s", rec.Body.String())

	rec = do(t, srv, http.MethodPost, fnBase, devToken, withGenerateName("dup"))
	require.Equal(t, http.StatusConflict, rec.Code, "explicit name + generateName: %s", rec.Body.String())

	rec = do(t, srv, http.MethodPost, fnBase, devToken, withGenerateName(""))
	require.Less(t, rec.Code, 300, "name-less generateName create: %s", rec.Body.String())
	var generated v1.Function
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &generated))
	require.Regexp(t, `^dup-[0-9a-f]{8}$`, string(generated.Name))

	rec = do(t, srv, http.MethodGet, fnBase, devToken, nil)
	var list []v1.Function
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2, "only dup and the generated name")
}
