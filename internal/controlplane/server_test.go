package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// functionBody builds a Function request body. The server stamps TypeMeta from the route kind regardless.
func functionBody(t *testing.T, ns, name, rg string) []byte {
	t.Helper()
	m := map[string]interface{}{
		"apiVersion": "funcd.io/v1alpha1",
		"kind":       "Function",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns, "resourceGroup": rg},
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

	noNamespace := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(functionBody(t, "team-a", "echo", "rg1"), &noNamespace))
	delete(noNamespace["metadata"].(map[string]interface{}), "namespace")
	noNamespaceBody, err := json.Marshal(noNamespace)
	require.NoError(t, err)

	configMapBody, err := json.Marshal(map[string]interface{}{
		"apiVersion": "funcd.io/v1alpha1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cfg", "namespace": "team-a", "resourceGroup": "rg1"},
		"spec":       map[string]interface{}{},
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
			APIVersion string                 `json:"apiVersion"`
			Kind       string                 `json:"kind"`
			Metadata   map[string]interface{} `json:"metadata"`
			Spec       map[string]interface{} `json:"spec"`
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

// TestIssue166_WireShapeMatchesSpec: the request schema accepts the flat shape the server writes
// (TypeMeta, Status and OwnerReference's ObjectRef are `,inline`), so a GET body PUTs back unchanged,
// and a handler error is served as application/problem+json.
func TestIssue166_WireShapeMatchesSpec(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	srv := newServerOn(t, st)

	fn := v1.Function{
		TypeMeta: v1.TypeMeta{APIVersion: "funcd.io/v1alpha1", Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{
			Name: "echo", Namespace: "team-a", ResourceGroup: "rg1",
			OwnerReferences: []v1.OwnerReference{{
				ObjectRef: v1.ObjectRef{Kind: v1.KindFunction, Name: "parent"},
				UID:       "u-1",
			}},
		},
		Spec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "app.handler", Image: "oci://example/app:v1"},
	}
	body, err := json.Marshal(fn)
	require.NoError(t, err)
	rec := do(t, srv, http.MethodPost, fnBase, devToken, body)
	require.Less(t, rec.Code, 300, "create with the flat stdlib body: %s", rec.Body.String())

	// The create ignores the client's owner references (#328); a reconciler sets them through the store.
	stored := storedEcho(t, st)
	stored.OwnerReferences = fn.OwnerReferences
	_, err = st.Update(context.Background(), stored)
	require.NoError(t, err)

	rec = do(t, srv, http.MethodGet, fnBase+"/echo", devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got v1.Function
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, fn.OwnerReferences[0].ObjectRef, got.OwnerReferences[0].ObjectRef, "owner kind/name survive")

	rec = do(t, srv, http.MethodPut, fnBase+"/echo", devToken, rec.Body.Bytes())
	require.Less(t, rec.Code, 300, "PUT of the exact GET body: %s", rec.Body.String())

	r := httptest.NewRequest(http.MethodGet, fnBase+"/missing", nil)
	r.Header.Set("Authorization", "Bearer "+devToken)
	r.Header.Set("Accept", "application/problem+json")
	miss := httptest.NewRecorder()
	srv.ServeHTTP(miss, r)
	require.Equal(t, http.StatusNotFound, miss.Code)
	require.Equal(t, "application/problem+json", miss.Header().Get("Content-Type"))
	rec = do(t, srv, http.MethodGet, fnBase+"/missing", devToken, nil)
	require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
}

// TestIssue166_ErrorsShareOneProblemShape: the router's 404 and 405 and huma's 422 are problem+json with
// the same members as a handler's fault, and the 405 names every method of the path in one Allow header.
func TestIssue166_ErrorsShareOneProblemShape(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	invalid, err := json.Marshal(map[string]interface{}{
		"apiVersion": "funcd.io/v1alpha1",
		"kind":       "Function",
		"metadata":   map[string]interface{}{"name": "echo", "namespace": "team-a", "resourceGroup": "rg1"},
	})
	require.NoError(t, err)
	for _, c := range []struct {
		what, method, path string
		body               []byte
		status             int
		detail             string
	}{
		{"handler fault", http.MethodGet, fnBase + "/missing", nil, http.StatusNotFound, "missing"},
		{"unknown route", http.MethodGet, "/apis/funcd.io/v1alpha1/namespaces/team-a/nosuchkinds/x", nil, http.StatusNotFound, "nosuchkinds"},
		{"method not allowed", http.MethodPatch, fnBase + "/echo", nil, http.StatusMethodNotAllowed, "PATCH"},
		{"schema-invalid body", http.MethodPost, fnBase, invalid, http.StatusUnprocessableEntity, "spec"},
	} {
		rec := do(t, srv, c.method, c.path, devToken, c.body)
		require.Equal(t, c.status, rec.Code, "%s: %s", c.what, rec.Body.String())
		require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"), c.what)
		var p map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), c.what)
		require.ElementsMatch(t, []string{"type", "title", "status", "detail"}, keysOf(p), "%s: %s", c.what, rec.Body.String())
		require.NotEmpty(t, p["type"], c.what)
		require.InDelta(t, c.status, p["status"], 0, c.what)
		require.Contains(t, p["detail"], c.detail, c.what)
	}

	rec := do(t, srv, http.MethodPatch, fnBase+"/echo", devToken, nil)
	require.Equal(t, []string{"GET, PUT, DELETE"}, rec.Header().Values("Allow"))
}

// TestIssue166_SpecHasNoDeadOrDanglingSchemas: a type only ever embedded `,inline` leaves no component
// behind, and every $ref in the served spec still resolves.
func TestIssue166_SpecHasNoDeadOrDanglingSchemas(t *testing.T) {
	t.Parallel()
	rec := do(t, newServer(t), http.MethodGet, "/openapi.json", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
	require.NotContains(t, spec.Components.Schemas, "TypeMeta")
	require.Contains(t, spec.Components.Schemas, "ObjectRef", "still referenced by its own fields")

	var doc interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	for _, ref := range refsOf(doc) {
		name, ok := strings.CutPrefix(ref, "#/components/schemas/")
		require.True(t, ok, ref)
		require.Contains(t, spec.Components.Schemas, name, "dangling $ref %s", ref)
	}
}

func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func refsOf(v interface{}) []string {
	var refs []string
	switch n := v.(type) {
	case map[string]interface{}:
		for k, c := range n {
			if s, ok := c.(string); ok && k == "$ref" {
				refs = append(refs, s)
				continue
			}
			refs = append(refs, refsOf(c)...)
		}
	case []interface{}:
		for _, c := range n {
			refs = append(refs, refsOf(c)...)
		}
	}
	return refs
}
