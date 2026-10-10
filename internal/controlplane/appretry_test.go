package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
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
)

type fakeRetrier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeRetrier) Retry(_ context.Context, ns v1.NamespaceName, app v1.ObjectName) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, string(ns)+"/"+string(app))
	return f.err
}

func newRetryServer(t *testing.T, r *fakeRetrier) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken:  {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		viewToken: {Subject: "obs", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: store.New(memory.New()), Authorizer: rbac.New(), Credentials: creds, AppRetrier: r,
	})
	require.NoError(t, err)
	return h
}

// ADR-0214 Decision 7: POST …/apps/{name}/retry authorizes an update of the App, calls the retrier, and maps its
// Conflict to a 409 problem; a viewer, another namespace or an unknown query parameter is refused before the call.
func TestAppRetryRoute(t *testing.T) {
	r := &fakeRetrier{}
	srv := newRetryServer(t, r)
	rec := do(t, srv, http.MethodPost, appBase+"/todo/retry", devToken, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, []string{"team-a/todo"}, r.calls)

	r.err = fault.Conflictf("app.Retry", "app todo has no failed hook")
	rec = do(t, srv, http.MethodPost, appBase+"/todo/retry", devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var p fault.Problem
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	require.Contains(t, p.Detail, "app todo has no failed hook")

	for _, tc := range []struct {
		path, token string
		code        int
	}{
		{appBase + "/todo/retry", viewToken, http.StatusForbidden},
		{"/apis/funcd.io/v1alpha1/namespaces/team-b/apps/todo/retry", devToken, http.StatusForbidden},
		{appBase + "/todo/retry", "", http.StatusUnauthorized},
		{appBase + "/todo/retry?dryRun=true", devToken, http.StatusUnprocessableEntity},
	} {
		require.Equal(t, tc.code, do(t, srv, http.MethodPost, tc.path, tc.token, nil).Code, tc.path)
	}
	require.Len(t, r.calls, 2, "a refused request calls no retry")
}

func TestAppRetryRouteAbsentWhenUnset(t *testing.T) {
	require.Equal(t, http.StatusNotFound, do(t, newServer(t), http.MethodPost, appBase+"/todo/retry", devToken, nil).Code)
}
