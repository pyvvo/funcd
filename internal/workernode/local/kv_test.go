package local_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	kvmemory "github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/workernode/local"
)

// fakeBinder is a static Binder: it grants "fn" the binding "b" → store "s" at the given mode, and
// denies everything else (default-deny).
type fakeBinder struct{ mode v1.KVMode }

func (b fakeBinder) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, binding string) (kvsvc.Binding, error) {
	if fn == "fn" && binding == "b" {
		return kvsvc.Binding{Store: "s", Mode: b.mode, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024}, nil
	}
	return kvsvc.Binding{}, fault.Forbiddenf("fakeBinder", "no grant for %s/%s", fn, binding)
}

// kvHandler builds a worker-node local API handler for caller ns/"fn", backed by a Facade over a fresh
// memory driver + the given Binder.
func kvHandler(t *testing.T, ns v1.NamespaceName, binder kvsvc.Binder) http.Handler {
	t.Helper()
	f, err := kvsvc.NewFacade(kvsvc.FacadeDeps{KV: kvmemory.New(), Binder: binder})
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

// scenario: kv-put-get-roundtrip — a granted (rw) function puts a value and reads it back over the local API.
func TestScenarioKVPutGetRoundtrip(t *testing.T) {
	h := kvHandler(t, "default", fakeBinder{mode: v1.KVModeRW})

	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/count", "42").Code)
	rec := do(t, h, http.MethodGet, "/kv/b/count", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "42", rec.Body.String())

	// a missing key → 404
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/absent", "").Code)
}

// scenario: ungranted-access-denied (local API) — a binding with no Grant returns 403 (default-deny).
func TestScenarioKVUngrantedDenied(t *testing.T) {
	h := kvHandler(t, "default", fakeBinder{mode: v1.KVModeRW})
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/kv/ungranted/k", "").Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/kv/ungranted/k", "v").Code)
}

// scenario: reader-grant-allows-get-not-put (local API) — an ro grant: get 404 (allowed, empty), put 403.
func TestScenarioKVReaderGrantAllowsGetNotPut(t *testing.T) {
	h := kvHandler(t, "default", fakeBinder{mode: v1.KVModeRO})
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/k", "").Code, "ro get is allowed (key absent ⇒ 404)")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/kv/b/k", "v").Code, "ro put ⇒ 403")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodDelete, "/kv/b/k", "").Code, "ro delete ⇒ 403")
}

// scenario: kv-list-prefix — list returns exactly the binding's keys under the prefix, store-stripped.
func TestScenarioKVListPrefix(t *testing.T) {
	h := kvHandler(t, "default", fakeBinder{mode: v1.KVModeRW})
	for _, k := range []string{"user/1", "user/2", "session/x"} {
		require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/"+k, "v").Code)
	}
	require.JSONEq(t, `["user/1","user/2"]`, do(t, h, http.MethodGet, "/kv/b?prefix=user/", "").Body.String())
}
