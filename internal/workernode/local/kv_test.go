package local_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// kvPDP is a test PDP (ADR-0074): kv::read allowed iff readOK (a permitting Policy stand-in); kv::write
// allowed iff the principal IS owner. This drives the local-API wire path through the new auth model.
type kvPDP struct {
	readOK bool
	owner  v1.ObjectName
}

func (p kvPDP) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	switch req.Action {
	case auth.ActionKVRead:
		return auth.Decision{Allowed: p.readOK}, nil
	case auth.ActionKVWrite:
		return auth.Decision{Allowed: req.Identity.Principal != nil && req.Identity.Principal.Name == p.owner}, nil
	default:
		return auth.Decision{Allowed: false}, nil
	}
}

// fakeKVResolver is a static BindingResolver (ADR-0073): it binds caller "fn" via alias "b" → store "s",
// table "t", with the given owner, and denies everything else (default-deny). Writes require the caller
// to be the table owner.
type fakeKVResolver struct{ owner v1.ObjectName }

func (b fakeKVResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (kvsvc.Binding, error) {
	if fn == "fn" && alias == "b" {
		return kvsvc.Binding{Store: "s", Table: "t", Owner: b.owner, MaxValueBytes: 1 << 20, MaxKeyBytes: 1024}, nil
	}
	return kvsvc.Binding{}, fault.Forbiddenf("fakeKVResolver", "no kv binding for %s/%s", fn, alias)
}

// kvHandler builds a worker-node local API handler for caller ns/"fn", backed by a Facade over a fresh
// memory driver + the given BindingResolver.
func kvHandler(t *testing.T, ns v1.NamespaceName, resolver kvsvc.BindingResolver, pdp auth.Authorizer) http.Handler {
	t.Helper()
	f, err := kvsvc.NewFacade(kvsvc.FacadeDeps{KV: kvmemory.New(), Resolver: resolver, Authorizer: pdp})
	require.NoError(t, err)
	return local.NewHandler(local.Ref{Namespace: ns, Function: "fn"}, nil, nil, nil, f, nil, nil)
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

// scenario: kv-binding-resolves (local API) — a bound owner puts a value and reads it back over the local API.
func TestScenarioKVPutGetRoundtrip(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})

	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/count", "42").Code)
	rec := do(t, h, http.MethodGet, "/kv/b/count", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "42", rec.Body.String())

	// a missing key → 404
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/absent", "").Code)
}

// scenario: unbound-access-denied (local API) — an alias with no binding returns 403 (default-deny).
func TestScenarioKVUnboundDenied(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/kv/unbound/k", "").Code)
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/kv/unbound/k", "v").Code)
}

// scenario: owner-write-via-policy (local API) — with a permitting read Policy a non-owner caller may
// get (404 when absent) but is 403 on put/delete: the built-in single-writer forbid (ADR-0074).
func TestScenarioKVNonOwnerReadsButCannotWrite(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "someone-else"}, kvPDP{readOK: true, owner: "someone-else"})
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/k", "").Code, "a permitted non-owner get is allowed (key absent ⇒ 404)")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodPut, "/kv/b/k", "v").Code, "a non-owner put ⇒ 403")
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodDelete, "/kv/b/k", "").Code, "a non-owner delete ⇒ 403")
}

// scenario: cedar-default-deny (local API) — a bound caller with NO permitting read Policy is 403 on get.
func TestScenarioKVReadDefaultDeny(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: false, owner: "fn"})
	require.Equal(t, http.StatusForbidden, do(t, h, http.MethodGet, "/kv/b/k", "").Code, "read denied without a Policy (default-deny)")
}

// scenario: kv-list-prefix — list returns exactly the binding's keys under the prefix, table-stripped.
func TestScenarioKVListPrefix(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	for _, k := range []string{"user/1", "user/2", "session/x"} {
		require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/"+k, "v").Code)
	}
	require.JSONEq(t, `["user/1","user/2"]`, do(t, h, http.MethodGet, "/kv/b?prefix=user/", "").Body.String())
}

// TestIssue100_PathLikeKeysRoundTrip: a key with an empty or dot segment is stored verbatim. The shims
// keep a key's "/" separators raw, so these keys arrive as non-canonical paths ("/kv/b/a//b").
func TestIssue100_PathLikeKeysRoundTrip(t *testing.T) {
	keys := []string{"/lead", "a//b", "a/./b", "a/../b", ".", "..", "a/b", "trail/"}
	for _, tc := range []struct {
		name, route string
		h           http.Handler
	}{
		{"kv", "/kv/b", kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})},
		{"blob", "/blob/b", blobHandler(t, "default", s3TestPDP{readOK: true, writeOK: true}, newBlobMapBucket())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range keys {
				require.Equal(t, http.StatusNoContent, do(t, tc.h, http.MethodPut, tc.route+"/"+k, "v:"+k).Code, "put %q", k)
				rec := do(t, tc.h, http.MethodGet, tc.route+"/"+k, "")
				require.Equal(t, http.StatusOK, rec.Code, "get %q: %s", k, rec.Body.String())
				require.Equal(t, "v:"+k, rec.Body.String(), "get %q", k)
			}
			var listed []string
			require.NoError(t, json.Unmarshal(do(t, tc.h, http.MethodGet, tc.route, "").Body.Bytes(), &listed))
			require.ElementsMatch(t, keys, listed)
			for _, k := range keys {
				require.Equal(t, http.StatusNoContent, do(t, tc.h, http.MethodDelete, tc.route+"/"+k, "").Code, "delete %q", k)
				require.Equal(t, http.StatusNotFound, do(t, tc.h, http.MethodGet, tc.route+"/"+k, "").Code, "get %q after delete", k)
			}
		})
	}
}
