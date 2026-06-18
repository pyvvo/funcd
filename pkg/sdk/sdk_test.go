package sdk_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
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
