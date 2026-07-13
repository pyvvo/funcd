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
	"github.com/green-0-rabbit/funcd/internal/auth"
	kvmemory "github.com/green-0-rabbit/funcd/internal/kvstore/memory"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/workernode/local"
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
