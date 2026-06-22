package local_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	kvmemory "github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/workernode/local"
)

// fakeAuthz is a PDP stub: allow=true permits, allow=false denies (→ the Facade returns Forbidden → 403).
type fakeAuthz struct{ allow bool }

func (f fakeAuthz) Authorize(_ context.Context, _ auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: f.allow, Reason: "test"}, nil
}

// kvHandler builds a worker-node local API handler for caller ns/fn, backed by a Facade over kvDriver +
// authz. (res/inv are nil — the KV tests never hit /invoke.)
func kvHandler(t *testing.T, ns v1.NamespaceName, kvDriver kvsvc.FacadeDeps, authz auth.Authorizer) http.Handler {
	t.Helper()
	kvDriver.Authorizer = authz
	f, err := kvsvc.NewFacade(kvDriver)
	require.NoError(t, err)
	return local.NewHandler(local.Ref{Namespace: ns, Function: "fn"}, nil, nil, f, nil)
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, http.NoBody)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// scenario: kv-put-get-roundtrip — a function puts a value and reads it back over the local API.
func TestScenarioKVPutGetRoundtrip(t *testing.T) {
	h := kvHandler(t, "default", kvsvc.FacadeDeps{KV: kvmemory.New()}, fakeAuthz{allow: true})

	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/count", "42").Code)
	rec := do(t, h, http.MethodGet, "/kv/b/count", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "42", rec.Body.String())

	// a missing key → 404
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/absent", "").Code)
}

// scenario: kv-tenancy-isolation — two functions in different namespaces both write key "k" to binding
// "b"; each reads only its own value (the Facade's <namespace>/<binding>/ tenant prefix).
func TestScenarioKVTenancyIsolation(t *testing.T) {
	driver := kvmemory.New() // shared engine; isolation is by tenant prefix, not by instance
	ha := kvHandler(t, "team-a", kvsvc.FacadeDeps{KV: driver}, fakeAuthz{allow: true})
	hb := kvHandler(t, "team-b", kvsvc.FacadeDeps{KV: driver}, fakeAuthz{allow: true})

	require.Equal(t, http.StatusNoContent, do(t, ha, http.MethodPut, "/kv/b/k", "a-value").Code)
	require.Equal(t, http.StatusNoContent, do(t, hb, http.MethodPut, "/kv/b/k", "b-value").Code)

	require.Equal(t, "a-value", do(t, ha, http.MethodGet, "/kv/b/k", "").Body.String())
	require.Equal(t, "b-value", do(t, hb, http.MethodGet, "/kv/b/k", "").Body.String(), "each namespace sees only its own value")

	// each lists only its own key
	require.JSONEq(t, `["k"]`, do(t, ha, http.MethodGet, "/kv/b", "").Body.String())
	require.JSONEq(t, `["k"]`, do(t, hb, http.MethodGet, "/kv/b", "").Body.String())
}

// scenario: kv-authz-denied — a PDP denial returns 403 (RFC 9457), the Facade having refused.
func TestScenarioKVAuthzDenied(t *testing.T) {
	h := kvHandler(t, "default", kvsvc.FacadeDeps{KV: kvmemory.New()}, fakeAuthz{allow: false})
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/kv/b/k", "").Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/kv/b/k", "v").Code)
}

// scenario: kv-list-prefix — list returns exactly the binding's keys under the prefix, tenant-stripped.
func TestScenarioKVListPrefix(t *testing.T) {
	h := kvHandler(t, "default", kvsvc.FacadeDeps{KV: kvmemory.New()}, fakeAuthz{allow: true})
	for _, k := range []string{"user/1", "user/2", "session/x"} {
		require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/"+k, "v").Code)
	}
	require.JSONEq(t, `["user/1","user/2"]`, do(t, h, http.MethodGet, "/kv/b?prefix=user/", "").Body.String())
}
