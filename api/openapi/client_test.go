package openapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
)

// TestSpecConsumableByClient proves the generated OpenAPI spec is consumable
// by a Go HTTP client. The client makes HTTP requests against the huma server
// and round-trips a Function resource, decoding into the canonical v1alpha1
// types — the same shape a generated SDK client would use.
//
// The typed Go client itself is deferred to P-R/F18 (oapi-codegen does not
// support OpenAPI 3.1; P-R will choose the right client-generation path).
func TestSpecConsumableByClient(t *testing.T) {
	h := controlplane.NewStubHandlers()
	r := chi.NewRouter()
	api := controlplane.NewAPI(r, h)

	// Wrap the huma API in an httptest server.
	srv := httptest.NewServer(api.Adapter())
	defer srv.Close()

	// Create a Function via the REST API.
	// Uses map[string]interface{} for the request body because huma's schema
	// expects TypeMeta as a nested $ref, but Go's json:",inline" flattens it.
	fn := map[string]interface{}{
		"TypeMeta": map[string]interface{}{
			"apiVersion": "funcd.io/v1alpha1",
			"kind":       "Function",
		},
		"metadata": map[string]interface{}{
			"name":      "client-fn",
			"namespace": "client-ns",
		},
		"spec": map[string]interface{}{},
	}

	body, err := json.Marshal(fn)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	createReq, err := http.NewRequest(http.MethodPost,
		srv.URL+"/apis/funcd.io/v1alpha1/namespaces/client-ns/functions",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Accept", "application/json")

	createResp, err := srv.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	defer func() {
		if closeErr := createResp.Body.Close(); closeErr != nil {
			t.Errorf("failed to close create response body: %v", closeErr)
		}
	}()

	if createResp.StatusCode != http.StatusOK && createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 200 or 201, got %d", createResp.StatusCode)
	}

	// GET the created function.
	getReq, err := http.NewRequest(http.MethodGet,
		srv.URL+"/apis/funcd.io/v1alpha1/namespaces/client-ns/functions/client-fn",
		nil)
	if err != nil {
		t.Fatalf("failed to create get request: %v", err)
	}
	getReq.Header.Set("Accept", "application/json")

	getResp, err := srv.Client().Do(getReq)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	defer func() {
		if closeErr := getResp.Body.Close(); closeErr != nil {
			t.Errorf("failed to close get response body: %v", closeErr)
		}
	}()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", getResp.StatusCode)
	}

	// Decode the response into the canonical v1alpha1.Function type.
	var result v1.Function
	if err := json.NewDecoder(getResp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result.Name != "client-fn" || result.Namespace != "client-ns" {
		t.Errorf("round-trip mismatch: name=%s ns=%s", result.Name, result.Namespace)
	}
}
