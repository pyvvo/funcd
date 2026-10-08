package controlplane_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// scenario: app-revision-read-only (ADR-0200 Decision 2) — the API serves an AppRevision's get and list only: a
// developer's or an admin's POST, PUT or DELETE answers 405 problem+json with Allow: GET and changes nothing.
func TestScenarioAppRevisionReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	appObj, _ := v1.NewObject(v1.KindApp)
	app := appObj.(*v1.App)
	app.Name, app.Namespace, app.ResourceGroup = "todo", "team-a", "rg1"
	app.Spec.Version = "1.0.0"
	created, err := st.Create(ctx, app)
	require.NoError(t, err)
	revObj, _ := v1.NewObject(v1.KindAppRevision)
	rev := revObj.(*v1.AppRevision)
	rev.Name, rev.Namespace, rev.ResourceGroup = "todo-1", "team-a", "rg1"
	ref := v1.ObjectRef{Kind: v1.KindApp, Namespace: "team-a", Name: "todo"}
	rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: ref, UID: created.GetObjectMeta().UID, Controller: true}}
	rev.Spec = v1.AppRevisionSpec{App: ref, Number: 1, Spec: app.Spec}
	stored, err := st.Create(ctx, rev)
	require.NoError(t, err)

	srv, err := controlplane.NewServer(controlplane.Deps{
		Store:      st,
		Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:   {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			adminToken: {Subject: "ops", Role: auth.RoleAdmin},
		}),
	})
	require.NoError(t, err)

	const base = "/apis/funcd.io/v1alpha1/namespaces/team-a/apprevisions"
	edit := *stored.(*v1.AppRevision)
	edit.Spec.Spec.Version = "9.9.9"
	edited, err := json.Marshal(&edit)
	require.NoError(t, err)
	for _, token := range []string{devToken, adminToken} {
		for _, w := range []struct {
			method, path string
			body         []byte
		}{
			{http.MethodPost, base, edited},
			{http.MethodPut, base + "/todo-1", edited},
			{http.MethodDelete, base + "/todo-1", nil},
		} {
			rec := do(t, srv, w.method, w.path, token, w.body)
			require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s %s: %s", w.method, w.path, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			require.Equal(t, []string{"GET"}, rec.Header().Values("Allow"), "%s %s", w.method, w.path)
		}
		rec := do(t, srv, http.MethodGet, base+"/todo-1", token, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got v1.AppRevision
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, v1.KindAppRevision, got.Kind)
		require.Equal(t, "1.0.0", got.Spec.Spec.Version)
		rec = do(t, srv, http.MethodGet, base, token, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var list []v1.AppRevision
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		require.Len(t, list, 1)
	}

	after, err := st.Get(ctx, v1.KindAppRevision.GVK(), "team-a", "todo-1")
	require.NoError(t, err)
	require.Equal(t, stored.GetObjectMeta().ResourceVersion, after.GetObjectMeta().ResourceVersion, "the AppRevision is unchanged")
	require.Equal(t, "1.0.0", after.(*v1.AppRevision).Spec.Spec.Version)
	list, err := st.List(ctx, v1.KindAppRevision.GVK(), store.ListOptions{Namespace: "team-a"})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "nothing was created")
}
