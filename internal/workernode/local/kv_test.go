package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/kvstore"
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
// to be the table owner. maxValueBytes is the store's value cap (0 ⇒ 1 MiB).
type fakeKVResolver struct {
	owner         v1.ObjectName
	maxValueBytes int64
}

func (b fakeKVResolver) Resolve(_ context.Context, _ v1.NamespaceName, fn v1.ObjectName, alias string) (kvsvc.Binding, error) {
	if fn == "fn" && alias == "b" {
		maxValue := b.maxValueBytes
		if maxValue == 0 {
			maxValue = 1 << 20
		}
		return kvsvc.Binding{Store: "s", Table: "t", Owner: b.owner, MaxValueBytes: maxValue, MaxKeyBytes: 1024}, nil
	}
	return kvsvc.Binding{}, fault.Forbiddenf("fakeKVResolver", "no kv binding for %s/%s", fn, alias)
}

// kvHandler builds a worker-node local API handler for caller ns/"fn", backed by a Facade over a fresh
// memory driver + the given BindingResolver.
func kvHandler(t *testing.T, ns v1.NamespaceName, resolver kvsvc.BindingResolver, pdp auth.Authorizer) http.Handler {
	t.Helper()
	f, err := kvsvc.NewFacade(kvsvc.FacadeDeps{KV: kvmemory.New(), Resolver: resolver, Authorizer: pdp})
	require.NoError(t, err)
	return local.NewHandler(local.Ref{Namespace: ns, Function: "fn"}, nil, nil, nil, nil, nil, local.WithFacets(local.KVFacet(f)))
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

// TestIssue169_DeclaredValueCapIsServable — a maxValueBytes a KVStore passes Validate with is a cap the
// platform serves: a put of exactly that many bytes over the local API reaches the facade and succeeds.
func TestIssue169_DeclaredValueCapIsServable(t *testing.T) {
	served := 0
	for _, capBytes := range []int64{1 << 20, 1<<20 + 1, 4 << 20} {
		ks := &v1.KVStore{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
			ObjectMeta: v1.ObjectMeta{Name: "s", Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.KVStoreSpec{Quota: v1.KVStoreQuota{MaxValueBytes: capBytes}, Tables: []v1.KVTable{{Name: "t", Owner: "fn"}}},
		}
		if ks.Validate() != nil {
			continue
		}
		h := kvHandler(t, "default", fakeKVResolver{owner: "fn", maxValueBytes: ks.Spec.EffectiveMaxValueBytes()}, kvPDP{readOK: true, owner: "fn"})
		rec := do(t, h, http.MethodPut, "/kv/b/k", strings.Repeat("x", int(capBytes)))
		require.Equalf(t, http.StatusNoContent, rec.Code, "store cap %d bytes passed Validate, so a put of that size must be served: %s", capBytes, rec.Body.String())
		served++
	}
	require.Positive(t, served, "at least one cap must be accepted and served")
}

// requireProblem asserts rec is an RFC 9457 problem of the given status and type whose detail contains detail.
func requireProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, typ, detail string) {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, typ, p.Type)
	require.Contains(t, p.Detail, detail)
}

const payloadTooLarge = "urn:funcd:problem:payload-too-large"

// scenario: kv-value-over-store-cap — a context.kv.put of a value one byte over the store's maxValueBytes
// answers 413 naming the cap (ADR-0148).
func TestScenarioKVValueOverStoreCap(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn", maxValueBytes: 100}, kvPDP{readOK: true, owner: "fn"})
	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/k", strings.Repeat("x", 100)).Code)
	requireProblem(t, do(t, h, http.MethodPut, "/kv/b/k", strings.Repeat("x", 101)), http.StatusRequestEntityTooLarge, payloadTooLarge, "(100 bytes)")
}

// scenario: kv-key-over-store-cap — a put of a key one byte over the store's maxKeyBytes (1024) answers 413.
func TestScenarioKVKeyOverStoreCap(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	require.Equal(t, http.StatusNoContent, do(t, h, http.MethodPut, "/kv/b/"+strings.Repeat("k", 1024), "v").Code)
	requireProblem(t, do(t, h, http.MethodPut, "/kv/b/"+strings.Repeat("k", 1025), "v"), http.StatusRequestEntityTooLarge, payloadTooLarge, "(1024 bytes)")
}

// scenario: kv-key-over-hard-limit — a get, put or delete of a key one byte over v1.MaxKeyBytesLimit answers
// 413, never 404, 400 or 500: the put trips the store cap, get and delete the storable-key limit.
func TestScenarioKVKeyOverHardLimit(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	path := "/kv/b/" + strings.Repeat("k", v1.MaxKeyBytesLimit+1)
	for _, tc := range []struct{ method, body, limit string }{
		{http.MethodGet, "", "(64000 bytes)"},
		{http.MethodPut, "v", "(1024 bytes)"},
		{http.MethodDelete, "", "(64000 bytes)"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			requireProblem(t, do(t, h, tc.method, path, tc.body), http.StatusRequestEntityTooLarge, payloadTooLarge, tc.limit)
		})
	}
}

// scenario: local-api-invalid-stays-400 — a context.kv.put body that breaks off mid-read for a non-size reason
// is malformed input: 400 of type invalid, not 413 (ADR-0148).
func TestScenarioLocalAPIInvalidStays400(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	body := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(io.ErrUnexpectedEOF))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/kv/b/k", body))
	requireProblem(t, rec, http.StatusBadRequest, "urn:funcd:problem:invalid", "read request body")
}

// A body that cannot be read answers 400 with a fixed text.
func TestUnreadableBodyAnswersAFixedText(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	body := iotest.ErrReader(errors.New("read unix data/run/api.sock->@: read: connection reset by peer"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/kv/b/k", body))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, "workernode.local.kv.put: could not read request body", p.Detail)
}

// failingKV fails each read.
type failingKV struct {
	local.KV
	err error
}

func (f failingKV) GetEntry(context.Context, v1.NamespaceName, v1.ObjectName, string, string) (kvstore.Entry, bool, error) {
	return kvstore.Entry{}, false, f.err
}

// The local API's sites log what they are handed before they answer, so a 500's cause stays in the log there too.
func TestLocalAPIFaultCauseStaysInTheLog(t *testing.T) {
	const cause = "open data/kv/000001.vlog: permission denied"
	var logs bytes.Buffer
	h := local.NewHandler(local.Ref{Namespace: "default", Function: "fn"}, nil, nil, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)),
		local.WithFacets(local.KVFacet(failingKV{err: fault.Wrapf(errors.New(cause), fault.Internal, "kvbadger.Get", "read the key")})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kv/b/k", http.NoBody))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Equal(t, "kvbadger.Get: read the key", p.Detail)
	require.Equal(t, 1, strings.Count(logs.String(), cause), logs.String())
}

// doH is do with request headers, given as name, value pairs.
func doH(t *testing.T, h http.Handler, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

const preconditionFailed = "urn:funcd:problem:precondition-failed"

// scenario: claim-race-one-winner (local API) — If-None-Match: * creates the key once: 201 with its version as a strong
// ETag, then 412 with that ETag, and a GET answers the same ETag.
func TestScenarioKVPutIfAbsentOverLocalAPI(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	rec := doH(t, h, http.MethodPut, "/kv/b/claim", "a", "If-None-Match", "*")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	tag := rec.Header().Get("ETag")
	require.Regexp(t, `^"[0-9a-f]{32}"$`, tag, "a strong ETag of the key's version, an opaque hash")
	rec = doH(t, h, http.MethodPut, "/kv/b/claim", "b", "If-None-Match", "*")
	requireProblem(t, rec, http.StatusPreconditionFailed, preconditionFailed, "is at version")
	require.Equal(t, tag, rec.Header().Get("ETag"), "the loser learns the holder's version")
	rec = do(t, h, http.MethodGet, "/kv/b/claim", "")
	require.Equal(t, "a", rec.Body.String())
	require.Equal(t, tag, rec.Header().Get("ETag"))
}

// scenario: compare-and-set-names-a-version (local API) — If-Match: "<version>" replaces only the version it names: 204 with the
// new ETag; a stale version is 412 with the current ETag, an absent key 412 without one.
func TestScenarioKVCompareAndSetOverLocalAPI(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	first := doH(t, h, http.MethodPut, "/kv/b/k", "v1", "If-None-Match", "*").Header().Get("ETag")
	rec := doH(t, h, http.MethodPut, "/kv/b/k", "v2", "If-Match", first)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	second := rec.Header().Get("ETag")
	require.NotEqual(t, first, second)
	rec = doH(t, h, http.MethodPut, "/kv/b/k", "v3", "If-Match", first)
	requireProblem(t, rec, http.StatusPreconditionFailed, preconditionFailed, "is at version")
	require.Equal(t, second, rec.Header().Get("ETag"))
	rec = doH(t, h, http.MethodPut, "/kv/b/absent", "x", "If-Match", first)
	requireProblem(t, rec, http.StatusPreconditionFailed, preconditionFailed, "does not exist")
	require.Empty(t, rec.Header().Get("ETag"))
	requireProblem(t, doH(t, h, http.MethodPut, "/kv/b/k", "x", "If-Match", `"abc"`), http.StatusPreconditionFailed,
		preconditionFailed, "is at version")
	require.Equal(t, "v2", do(t, h, http.MethodGet, "/kv/b/k", "").Body.String())
}

// scenario: delete-if-releases-only-its-own (local API) — a DELETE with If-Match deletes only the version it names.
func TestScenarioKVDeleteIfOverLocalAPI(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	tag := doH(t, h, http.MethodPut, "/kv/b/lease", "me", "If-None-Match", "*").Header().Get("ETag")
	doH(t, h, http.MethodPut, "/kv/b/other", "x", "If-None-Match", "*")
	other := do(t, h, http.MethodGet, "/kv/b/other", "").Header().Get("ETag")
	requireProblem(t, doH(t, h, http.MethodDelete, "/kv/b/lease", "", "If-Match", other), http.StatusPreconditionFailed, preconditionFailed, "is at version")
	require.Equal(t, http.StatusNoContent, doH(t, h, http.MethodDelete, "/kv/b/lease", "", "If-Match", tag).Code)
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/lease", "").Code)
	requireProblem(t, doH(t, h, http.MethodDelete, "/kv/b/lease", "", "If-Match", tag), http.StatusPreconditionFailed, preconditionFailed, "does not exist")
}

// scenario: ttl-expires-a-key (local API) — ?ttl=1 makes the key expire: the GET finds it, then answers 404, and a put-if-absent
// claims it again.
func TestScenarioKVTTLOverLocalAPI(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	require.Equal(t, http.StatusCreated, doH(t, h, http.MethodPut, "/kv/b/lease?ttl=1", "me", "If-None-Match", "*").Code)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/kv/b/lease", "").Code)
	require.Eventually(t, func() bool { return do(t, h, http.MethodGet, "/kv/b/lease", "").Code == http.StatusNotFound },
		4*time.Second, 20*time.Millisecond)
	require.JSONEq(t, `[]`, do(t, h, http.MethodGet, "/kv/b", "").Body.String())
	require.Equal(t, http.StatusCreated, doH(t, h, http.MethodPut, "/kv/b/lease?ttl=1", "you", "If-None-Match", "*").Code)
}

// scenario: lost-condition-fails-only-its-request (local API refusals)
// The conditions the local API refuses as 400 before any write, and the owner rule a conditional write keeps.
func TestKVConditionalRefusals(t *testing.T) {
	h := kvHandler(t, "default", fakeKVResolver{owner: "fn"}, kvPDP{readOK: true, owner: "fn"})
	for _, tc := range []struct {
		name, method, path string
		hdr                []string
	}{
		{"if-none-match tag", http.MethodPut, "/kv/b/k", []string{"If-None-Match", `"1"`}},
		{"if-match star", http.MethodPut, "/kv/b/k", []string{"If-Match", "*"}},
		{"weak tag", http.MethodPut, "/kv/b/k", []string{"If-Match", `W/"1"`}},
		{"unquoted", http.MethodPut, "/kv/b/k", []string{"If-Match", "1"}},
		{"empty tag", http.MethodPut, "/kv/b/k", []string{"If-Match", `""`}},
		{"a space in the tag", http.MethodPut, "/kv/b/k", []string{"If-Match", `"a b"`}},
		{"two lines", http.MethodPut, "/kv/b/k", []string{"If-Match", `"1"`, "If-Match", `"2"`}},
		{"both", http.MethodPut, "/kv/b/k", []string{"If-None-Match", "*", "If-Match", `"1"`}},
		{"ttl 0", http.MethodPut, "/kv/b/k?ttl=0", nil},
		{"ttl text", http.MethodPut, "/kv/b/k?ttl=1m", nil},
		{"delete if-none-match", http.MethodDelete, "/kv/b/k", []string{"If-None-Match", "*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireProblem(t, doH(t, h, tc.method, tc.path, "v", tc.hdr...), http.StatusBadRequest, "urn:funcd:problem:invalid", "")
		})
	}
	require.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/kv/b/k", "").Code, "a refused write stores nothing")
	other := kvHandler(t, "default", fakeKVResolver{owner: "someone-else"}, kvPDP{readOK: true, owner: "someone-else"})
	require.Equal(t, http.StatusForbidden, doH(t, other, http.MethodPut, "/kv/b/k", "v", "If-None-Match", "*").Code)
	require.Equal(t, http.StatusForbidden, doH(t, other, http.MethodDelete, "/kv/b/k", "", "If-Match", `"1"`).Code)
}
