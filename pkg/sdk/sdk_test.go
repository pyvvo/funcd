package sdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	return newClientVia(t, nil)
}

// newClientVia is newClient with the server's handler wrapped by wrap, when set.
func newClientVia(t *testing.T, wrap func(http.Handler) http.Handler) *sdk.Client {
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
	if wrap != nil {
		h = wrap(h)
	}
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

// scenario: sdk-held-version-conflicts (ADR-0210) — Apply of a copy read before another writer's change, and
// Delete with IfVersion of it, answer fault.Conflict; Apply sends no POST after the 409.
func TestScenarioSDKHeldVersionConflicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var posts atomic.Int32
	c := newClientVia(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
			}
			next.ServeHTTP(w, r)
		})
	})
	_, err := c.Apply(ctx, newFunction("fn1", "h1"))
	require.NoError(t, err)
	held, err := c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, err)
	_, err = c.Apply(ctx, newFunction("fn1", "other"))
	require.NoError(t, err)
	posts.Store(0)

	held.(*v1.Function).Spec.Handler = "mine"
	_, err = c.Apply(ctx, held)
	require.Equal(t, fault.Conflict, fault.KindOf(err), "err: %v", err)
	require.Zero(t, posts.Load(), "a 409 is not a missing object: no POST follows")

	err = c.Delete(ctx, v1.KindFunction, "team-a", "fn1", sdk.IfVersion(held.GetObjectMeta().ResourceVersion))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "err: %v", err)
	cur, err := c.Get(ctx, v1.KindFunction, "team-a", "fn1")
	require.NoError(t, err)
	require.Equal(t, "other", cur.(*v1.Function).Spec.Handler)

	require.NoError(t, c.Delete(ctx, v1.KindFunction, "team-a", "fn1", sdk.IfVersion(cur.GetObjectMeta().ResourceVersion)))
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

func TestForceIsRefusedForAnyKindButResourceGroup(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "true", r.URL.Query().Get("force"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL)
	require.NoError(t, err)
	err = c.Delete(context.Background(), v1.KindFunction, "ns", "a", sdk.Force())
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Zero(t, calls, "refused before a request")
	require.NoError(t, c.Delete(context.Background(), v1.KindResourceGroup, "ns", "team", sdk.Force()))
	require.Equal(t, 1, calls)
}

// A namespace or name that is not a DNS label would add a path segment, a query or a fragment to the request
// URL, so the request would reach another object; the SDK refuses it with fault.Invalid before any request.
func TestIssue698_NameThatIsNotALabelSendsNoRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := map[string]func(*sdk.Client) error{
		"Get name with ?": func(c *sdk.Client) error {
			_, err := c.Get(ctx, v1.KindFunction, "default", "b?anything")
			return err
		},
		"Delete name with #": func(c *sdk.Client) error { return c.Delete(ctx, v1.KindFunction, "default", "a#b") },
		"Delete name with ?force": func(c *sdk.Client) error {
			return c.Delete(ctx, v1.KindFunction, "default", "a?force=true")
		},
		"Delete ResourceGroup name with ?force": func(c *sdk.Client) error {
			return c.Delete(ctx, v1.KindResourceGroup, "default", "rg1?force=true")
		},
		"Delete namespace with /": func(c *sdk.Client) error {
			return c.Delete(ctx, v1.KindFunction, "default/functions/other", "x")
		},
		"List namespace with /": func(c *sdk.Client) error {
			_, err := c.List(ctx, v1.KindFunction, "a/b")
			return err
		},
		"Apply name with #": func(c *sdk.Client) error {
			_, err := c.Apply(ctx, newFunction("a#b", "h"))
			return err
		},
		"HandoverKVStore store with #": func(c *sdk.Client) error { return c.HandoverKVStore(ctx, "default", "s#x", "wf") },
		"Logs function with ?": func(c *sdk.Client) error {
			_, err := c.Logs(ctx, "default", "fn?since=1h", sdk.LogsOptions{})
			return err
		},
		"RunLogs run with /": func(c *sdk.Client) error {
			_, err := c.RunLogs(ctx, "default", "r/x", sdk.LogsOptions{})
			return err
		},
		"DeadLetters namespace with ?": func(c *sdk.Client) error {
			_, err := c.DeadLetters(ctx, "default?x=1")
			return err
		},
		"DeadLetter namespace with /": func(c *sdk.Client) error {
			_, err := c.DeadLetter(ctx, "a/b", "01J")
			return err
		},
		"ReplayDeadLetter namespace with #":   func(c *sdk.Client) error { return c.ReplayDeadLetter(ctx, "a#b", "01J") },
		"DiscardDeadLetter namespace with ..": func(c *sdk.Client) error { return c.DiscardDeadLetter(ctx, "..", "01J") },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			c, err := sdk.New(srv.URL)
			require.NoError(t, err)
			err = call(c)
			require.Zero(t, calls.Load(), "a request was sent; err: %v", err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "err: %v", err)
		})
	}
}

// Against the real control plane: Get(b?anything) must not return b, and Delete(a#zzz) must not delete a.
func TestIssue698_NameThatIsNotALabelLeavesOtherObjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)
	for _, n := range []string{"a", "b"} {
		_, err := c.Apply(ctx, newFunction(n, "h"))
		require.NoError(t, err)
	}
	t.Run("Get", func(t *testing.T) {
		got, err := c.Get(ctx, v1.KindFunction, "team-a", "b?anything")
		require.Equal(t, fault.Invalid, fault.KindOf(err), "Get(b?anything) = %v, %v", got, err)
	})
	t.Run("Delete", func(t *testing.T) {
		err := c.Delete(ctx, v1.KindFunction, "team-a", "a#zzz")
		_, gerr := c.Get(ctx, v1.KindFunction, "team-a", "a")
		require.NoError(t, gerr, "Delete(a#zzz) deleted a; Delete returned %v", err)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "Delete(a#zzz): %v", err)
	})
}

// A dead-letter id is opaque, not a DNS label: it is escaped, so it stays one path segment of the URL.
func TestIssue698_DeadLetterIDStaysOnePathSegment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const base = "/apis/funcd.io/v1alpha1/namespaces/default/deadletters/"
	for id, segment := range map[string]string{
		"a#b":          "a%23b",
		"a?force=true": "a%3Fforce=true",
		"a/b":          "a%2Fb",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			c, err := sdk.New(srv.URL)
			require.NoError(t, err)
			_, err = c.DeadLetter(ctx, "default", id)
			require.NoError(t, err)
			require.NoError(t, c.ReplayDeadLetter(ctx, "default", id))
			require.NoError(t, c.DiscardDeadLetter(ctx, "default", id))
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{
				"GET " + base + segment + "?",
				"POST " + base + segment + "/replay?",
				"DELETE " + base + segment + "?",
			}, seen)
		})
	}
}
