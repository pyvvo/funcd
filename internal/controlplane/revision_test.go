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

// scenario: revision-writes-refused (ADR-0172) — the API serves a Revision's get and list only: a developer's or an
// admin's POST, PUT or DELETE answers 405 problem+json with Allow: GET and changes nothing.
func TestScenarioRevisionWritesRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	fnObj, _ := v1.NewObject(v1.KindFunction)
	fn := fnObj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "greeter", "team-a", "rg1"
	created, err := st.Create(ctx, fn)
	require.NoError(t, err)
	revObj, _ := v1.NewObject(v1.KindRevision)
	rev := revObj.(*v1.Revision)
	rev.Name, rev.Namespace, rev.ResourceGroup = "greeter-1", "team-a", "rg1"
	ref := v1.ObjectRef{Kind: v1.KindFunction, Namespace: "team-a", Name: "greeter"}
	rev.OwnerReferences = []v1.OwnerReference{{ObjectRef: ref, UID: created.GetObjectMeta().UID, Controller: true}}
	rev.Spec = v1.RevisionSpec{Function: ref, Number: 1, Image: "oci-layout://greeter:v1", ImageDigest: "sha256:A"}
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

	const base = "/apis/funcd.io/v1alpha1/namespaces/team-a/revisions"
	edit := *stored.(*v1.Revision)
	edit.Spec.ImageDigest = "sha256:E"
	edited, err := json.Marshal(&edit)
	require.NoError(t, err)
	for _, token := range []string{devToken, adminToken} {
		for _, w := range []struct {
			method, path string
			body         []byte
		}{
			{http.MethodPost, base, edited},
			{http.MethodPut, base + "/greeter-1", edited},
			{http.MethodDelete, base + "/greeter-1", nil},
		} {
			rec := do(t, srv, w.method, w.path, token, w.body)
			require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s %s: %s", w.method, w.path, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			require.Equal(t, []string{"GET"}, rec.Header().Values("Allow"), "%s %s", w.method, w.path)
		}
		require.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, base+"/greeter-1", token, nil).Code)
		require.Equal(t, http.StatusOK, do(t, srv, http.MethodGet, base, token, nil).Code)
	}

	after, err := st.Get(ctx, v1.KindRevision.GVK(), "team-a", "greeter-1")
	require.NoError(t, err)
	require.Equal(t, stored.GetObjectMeta().ResourceVersion, after.GetObjectMeta().ResourceVersion, "the Revision is unchanged")
	require.Equal(t, "sha256:A", after.(*v1.Revision).Spec.ImageDigest)
	list, err := st.List(ctx, v1.KindRevision.GVK(), store.ListOptions{Namespace: "team-a"})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "nothing was created")
}
