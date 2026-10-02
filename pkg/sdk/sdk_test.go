package sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const devToken = "dev-secret"

// newClient mounts the REAL store-backed control-plane (authn + RBAC) on httptest
// and returns an SDK client pointed at it with a developer token (no mocks).
func newClient(t *testing.T) *sdk.Client {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)
	return c
}

func newFunction(name, handler string) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name = v1.ObjectName(name)
	fn.Namespace = "team-a"
	fn.ResourceGroup = "rg1"
	fn.Spec.Handler = handler
	return fn
}

// scenario: sdk-applies-and-gets (covers first apply = create, second = replace).
func TestScenarioSDKAppliesAndGets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)

	created, err := c.Apply(ctx, newFunction("fn1", "h1")) // create (PUT 404 → POST)
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("fn1"), created.GetName())

	got, err := c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("fn1"), got.GetName())
	require.Equal(t, "h1", got.(*v1.Function).Spec.Handler)

	_, err = c.Apply(ctx, newFunction("fn1", "h2")) // replace (PUT 200)
	require.NoError(t, err)
	got2, err := c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, err)
	require.Equal(t, "h2", got2.(*v1.Function).Spec.Handler, "second apply replaced the object")
}

// scenario: sdk-lists.
func TestScenarioSDKLists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	_, err := c.Apply(ctx, newFunction("a", "h"))
	require.NoError(t, err)
	_, err = c.Apply(ctx, newFunction("b", "h"))
	require.NoError(t, err)

	list, err := c.List(ctx, v1.KindFunction, "team-a")
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.ElementsMatch(t, []v1.ObjectName{"a", "b"}, []v1.ObjectName{list[0].GetName(), list[1].GetName()})
}

// scenario: sdk-deletes.
func TestScenarioSDKDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	_, err := c.Apply(ctx, newFunction("fn1", "h"))
	require.NoError(t, err)
	require.NoError(t, c.Delete(ctx, v1.KindFunction, "team-a", "fn1"))
	_, err = c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
}

// scenario: sdk-maps-error-to-fault.
func TestScenarioSDKMapsErrorToFault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	_, err := c.Get(ctx, v1.KindFunction, "team-a", "missing")
	require.Error(t, err)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "problem+json mapped back to fault kind")
}

// A huma 422 names the bad field in errors[]; the SDK error must carry it, not only "validation failed".
func TestIssue139_ValidationErrorNamesField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	cases := map[string]struct {
		mutate   func(*v1.Function)
		location string
	}{
		"replicas": {
			mutate:   func(fn *v1.Function) { fn.Spec.Replicas = -1 },
			location: "body.spec.replicas",
		},
		"minReplicas": {
			mutate:   func(fn *v1.Function) { fn.Spec.Scaling.MinReplicas = -1 },
			location: "body.spec.scaling.minReplicas",
		},
	}
	for name, tc := range cases {
		fn := newFunction("bad-"+strings.ToLower(name), "h")
		tc.mutate(fn)
		_, err := c.Apply(ctx, fn)
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.Contains(t, err.Error(), tc.location, name)
		require.Contains(t, err.Error(), "expected number >= 0", name)
	}
}

// A 301/302/303 makes Go resend a PUT/DELETE as a body-less GET; the SDK must surface that as an
// error, never report the GET's 200 as a stored or deleted object. 307/308 keep the method and work.
func TestIssue136_WritesRefuseMethodChangingRedirect(t *testing.T) {
	t.Parallel()
	stored, err := json.Marshal(newFunction("fn1", "old"))
	require.NoError(t, err)
	for _, tc := range []struct {
		code    int
		follows bool
	}{
		{code: http.StatusMovedPermanently},
		{code: http.StatusFound},
		{code: http.StatusSeeOther},
		{code: http.StatusTemporaryRedirect, follows: true},
		{code: http.StatusPermanentRedirect, follows: true},
	} {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.URL.Path)
				mu.Unlock()
				if r.URL.Path != "/final" {
					http.Redirect(w, r, "/final", tc.code)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(stored)
			}))
			t.Cleanup(srv.Close)
			c, err := sdk.New(srv.URL)
			require.NoError(t, err)

			_, applyErr := c.Apply(ctx, newFunction("fn1", "new"))
			deleteErr := c.Delete(ctx, v1.KindFunction, "team-a", "fn1")
			mu.Lock()
			defer mu.Unlock()
			if tc.follows {
				require.NoError(t, applyErr)
				require.NoError(t, deleteErr)
				require.Subset(t, seen, []string{"PUT /final", "DELETE /final"})
				return
			}
			require.Error(t, applyErr, "Apply reported the redirected GET as the stored object; seen %v", seen)
			require.Error(t, deleteErr, "Delete reported the redirected GET as a delete; seen %v", seen)
			require.False(t, slices.Contains(seen, "GET /final"), "a write was resent as GET: %v", seen)
		})
	}
}

// A 413 or 429 maps back to its own fault kind (the inverse of api/fault's Kind-to-status table),
// never to Internal. The control plane itself answers 413 for a body over its 1 MiB limit.
func TestIssue323_TooLargeAndThrottledMapToTheirKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("control plane body limit", func(t *testing.T) {
		t.Parallel()
		obj, _ := v1.NewObject(v1.KindConfigMap)
		cm := obj.(*v1.ConfigMap)
		cm.Name = "big"
		cm.Namespace = "team-a"
		cm.ResourceGroup = "rg1"
		cm.Spec.Data = map[string]string{"BIG": strings.Repeat("x", 2<<20)}
		_, err := newClient(t).Apply(ctx, cm)
		require.Equal(t, fault.PayloadTooLarge, fault.KindOf(err), "err: %v", err)
	})
	for _, k := range []fault.Kind{fault.PayloadTooLarge, fault.ResourceExhausted} {
		t.Run(string(k), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fault.WriteProblem(w, &fault.Error{Kind: k, Op: "test", Msg: "refused"})
			}))
			t.Cleanup(srv.Close)
			c, err := sdk.New(srv.URL)
			require.NoError(t, err)
			_, err = c.Get(ctx, v1.KindFunction, "team-a", "fn1")
			require.Equal(t, k, fault.KindOf(err), "err: %v", err)
		})
	}
}
