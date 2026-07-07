package local_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/workernode/local"
)

type fakeResolver struct {
	target    local.Ref
	timeout   time.Duration
	err       error
	gotCaller local.Ref
	gotAlias  string
}

func (f *fakeResolver) Resolve(_ context.Context, caller local.Ref, alias string) (local.Ref, time.Duration, error) {
	f.gotCaller, f.gotAlias = caller, alias
	return f.target, f.timeout, f.err
}

type fakeInvoker struct {
	out []byte
	err error
}

func (f fakeInvoker) Invoke(_ context.Context, _ local.Ref, _ []byte, _ time.Duration) ([]byte, error) {
	return f.out, f.err
}

// fakeAuthz records the link::invoke request the handler builds and returns a fixed decision — it
// stands in for the cedar PDP (ADR-0075) so the handler's gate is tested in isolation.
type fakeAuthz struct {
	allowed bool
	err     error
	gotReq  auth.Request
}

func (f *fakeAuthz) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	f.gotReq = req
	if f.err != nil {
		return auth.Decision{}, f.err
	}
	return auth.Decision{Allowed: f.allowed, Reason: "fake"}, nil
}

func post(t *testing.T, h http.Handler, alias, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/invoke/"+alias, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// scenario: invoke error taxonomy — a parametrized table mapping each condition to the HTTP status
// the local API surfaces. Daemon faults (403/404/503) via the fault bridge; the target shim's
// 422/500 propagated verbatim (ADR-0064).
func TestScenarioInvokeErrorTaxonomy(t *testing.T) {
	caller := local.Ref{Namespace: "team-a", Function: "a"}
	for _, tc := range []struct {
		name       string
		resolveErr error
		invokeOut  []byte
		invokeErr  error
		wantStatus int
	}{
		{"success → 200", nil, []byte(`{"ok":true}`), nil, http.StatusOK},
		{"no link → 403", fault.Forbiddenf("op", "no link"), nil, nil, http.StatusForbidden},
		{"unknown target → 404", fault.NotFoundf("op", "no target"), nil, nil, http.StatusNotFound},
		{"input mismatch → 422 propagated", nil, nil, &local.UpstreamError{Status: 422, Body: []byte("bad input")}, http.StatusUnprocessableEntity},
		{"output mismatch → 500 propagated", nil, nil, &local.UpstreamError{Status: 500, Body: []byte("bad output")}, http.StatusInternalServerError},
		{"cold-wake timeout → 503", nil, nil, fault.Unavailablef("op", "never ready"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &fakeResolver{target: local.Ref{Namespace: "team-a", Function: "b"}, err: tc.resolveErr}
			h := local.NewHandler(caller, res, fakeInvoker{out: tc.invokeOut, err: tc.invokeErr}, nil, nil, nil)
			rec := post(t, h, "payments", `{}`)
			require.Equal(t, tc.wantStatus, rec.Code)
		})
	}
}

// scenario: caller-identity-from-connection — the caller is the fixed sandbox Ref; a request body
// cannot name a different caller.
func TestScenarioCallerIdentityFromConnection(t *testing.T) {
	caller := local.Ref{Namespace: "team-a", Function: "a"}
	res := &fakeResolver{target: local.Ref{Namespace: "team-a", Function: "b"}}
	h := local.NewHandler(caller, res, fakeInvoker{out: []byte(`{}`)}, nil, nil, nil)

	post(t, h, "payments", `{"caller":"evil/other","sneaky":true}`)
	require.Equal(t, caller, res.gotCaller, "caller is the fixed sandbox Ref, never the request body")
	require.Equal(t, "payments", res.gotAlias)
}

// the broker logs every invoke — an allowed call at Info (caller→target), a denial at Warn.
func TestInvokeIsLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	caller := local.Ref{Namespace: "team-a", Function: "a"}

	h := local.NewHandler(caller, &fakeResolver{target: local.Ref{Namespace: "team-a", Function: "b"}},
		fakeInvoker{out: []byte(`{}`)}, nil, nil, logger)
	post(t, h, "payments", `{}`)
	got := buf.String()
	require.Contains(t, got, "fn-to-fn invoke")
	require.Contains(t, got, "caller=team-a/a")
	require.Contains(t, got, "target=team-a/b")
	require.Contains(t, got, "alias=payments")

	buf.Reset()
	hDeny := local.NewHandler(caller, &fakeResolver{err: fault.Forbiddenf("op", "no link")}, fakeInvoker{}, nil, nil, logger)
	post(t, hDeny, "ghost", `{}`)
	require.Contains(t, buf.String(), "fn-to-fn invoke denied")
	require.Contains(t, buf.String(), "level=WARN")
}

// scenario: policy-revokes-invoke (ADR-0075) — a declared link resolves (the Resolver allows it), but
// the PDP denies link::invoke (a forbid Policy revoking the declared link) ⇒ the handler returns
// Forbidden and never forwards to the Invoker. The invoke principal is connection-scoped (the fixed
// caller Ref) and the resource is the resolved target Function — never the request body.
func TestScenarioPolicyRevokesInvoke(t *testing.T) {
	caller := local.Ref{Namespace: "team-a", Function: "a"}
	target := local.Ref{Namespace: "team-a", Function: "pricing"}
	res := &fakeResolver{target: target}

	t.Run("deny → Forbidden, not forwarded", func(t *testing.T) {
		az := &fakeAuthz{allowed: false}
		var forwarded bool
		inv := fakeInvoker{out: []byte(`{}`)}
		h := local.NewHandler(caller, res, recordingInvoker{inner: inv, hit: &forwarded}, az, nil, nil)
		rec := post(t, h, "pricing", `{"caller":"evil/other"}`)
		require.Equal(t, http.StatusForbidden, rec.Code, "a forbid Policy revokes a declared invoke")
		require.False(t, forwarded, "a denied invoke never reaches the Invoker")
		// principal is connection-scoped (the fixed Ref), resource is the resolved target.
		require.Equal(t, auth.ActionLinkInvoke, az.gotReq.Action)
		require.Equal(t, &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "a"}, az.gotReq.Identity.Principal)
		require.Equal(t, &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "pricing"}, az.gotReq.Resource)
	})

	t.Run("permit → forwarded (declared link invokes)", func(t *testing.T) {
		az := &fakeAuthz{allowed: true}
		var forwarded bool
		h := local.NewHandler(caller, res, recordingInvoker{inner: fakeInvoker{out: []byte(`{"ok":1}`)}, hit: &forwarded}, az, nil, nil)
		rec := post(t, h, "pricing", `{}`)
		require.Equal(t, http.StatusOK, rec.Code, "a permitted declared link forwards")
		require.True(t, forwarded, "an allowed invoke reaches the Invoker")
	})
}

// recordingInvoker flags whether Invoke was called (to prove a denial short-circuits before forwarding).
type recordingInvoker struct {
	inner fakeInvoker
	hit   *bool
}

func (r recordingInvoker) Invoke(ctx context.Context, t local.Ref, in []byte, d time.Duration) ([]byte, error) {
	*r.hit = true
	return r.inner.Invoke(ctx, t, in, d)
}

type fakeStore struct{ obj v1.Object }

func (f fakeStore) Get(_ context.Context, _ v1.GroupVersionKind, _ v1.NamespaceName, _ v1.ObjectName) (v1.Object, error) {
	return f.obj, nil
}

func callerFn(links ...v1.FunctionLink) *v1.Function {
	f := &v1.Function{Spec: v1.FunctionSpec{Links: links}}
	f.Name, f.Namespace = "a", "team-a"
	return f
}

// resolver — link-as-grant: a matching link resolves the target (default timeout when unset); no
// matching link is default-deny (fault.Forbidden).
func TestResolver(t *testing.T) {
	r := local.NewResolver(fakeStore{obj: callerFn(v1.FunctionLink{Alias: "payments", Target: "checkout"})})
	caller := local.Ref{Namespace: "team-a", Function: "a"}

	target, timeout, err := r.Resolve(context.Background(), caller, "payments")
	require.NoError(t, err)
	require.Equal(t, local.Ref{Namespace: "team-a", Function: "checkout"}, target)
	require.Equal(t, 30*time.Second, timeout, "unset link timeout ⇒ platform default")

	_, _, err = r.Resolve(context.Background(), caller, "ghost")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "no link is no grant (default-deny)")
}

// invoker — forwards to the in-process data-plane and propagates its status: 2xx → output bytes;
// non-2xx → *UpstreamError carrying the exact status+body (no re-validation).
func TestInvokerPropagates(t *testing.T) {
	dp := func(status int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/function/b", r.URL.Path, "forwards to /function/<target>")
			require.Equal(t, "team-a", r.Header.Get("X-Funcd-Namespace"))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
	}
	target := local.Ref{Namespace: "team-a", Function: "b"}

	out, err := local.NewInvoker(dp(http.StatusOK, `{"ok":1}`)).Invoke(context.Background(), target, []byte(`{}`), time.Second)
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":1}`, string(out))

	_, err = local.NewInvoker(dp(http.StatusUnprocessableEntity, "bad")).Invoke(context.Background(), target, []byte(`{}`), time.Second)
	var ue *local.UpstreamError
	require.ErrorAs(t, err, &ue)
	require.Equal(t, http.StatusUnprocessableEntity, ue.Status, "shim 422 propagated verbatim")
}
