package controlplane_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// storeOf builds a Bucket or a SQLDatabase of kvNS carrying refs.
func storeOf(kind v1.Kind, name v1.ObjectName, refs ...v1.OwnerReference) v1.Object {
	meta := v1.ObjectMeta{Name: name, Namespace: kvNS, ResourceGroup: "rg1", OwnerReferences: refs}
	if kind == v1.KindBucket {
		return &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}, ObjectMeta: meta}
	}
	return &v1.SQLDatabase{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSQLDatabase.GVK().APIVersion(), Kind: v1.KindSQLDatabase}, ObjectMeta: meta}
}

// ADR-0248 Decision 3: the replace guard covers Buckets and SQLDatabases as it covers KVStores, so no user apply
// replaces a store a live owner made; a marker naming a deleted maker stays editable.
func TestReplaceGuardCoversBucketsAndSQLDatabases(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st,
		Authorizer: pairAuthorizer{"operator": {
			{auth.VerbGet, v1.KindBucket}, {auth.VerbUpdate, v1.KindBucket},
			{auth.VerbGet, v1.KindSQLDatabase}, {auth.VerbUpdate, v1.KindSQLDatabase},
		}},
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			"operator-token": {Subject: "operator", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{kvNS}},
		}),
	})
	require.NoError(t, err)
	wf := kvWorkflow(t, st, "cache", v1.DeletionRetain)
	for kind, plural := range map[v1.Kind]string{v1.KindBucket: "buckets", v1.KindSQLDatabase: "sqldatabases"} {
		for name, ref := range map[v1.ObjectName]v1.OwnerReference{"made": markerOf(wf.Name, wf.UID), "orphan": markerOf("ghost", "gone")} {
			_, err := st.Create(context.Background(), storeOf(kind, name, ref))
			require.NoError(t, err)
		}
		path := "/apis/funcd.io/v1alpha1/namespaces/" + string(kvNS) + "/" + plural + "/"
		rec := do(t, srv, http.MethodPut, path+"made", "operator-token", encode(t, storeOf(kind, "made")))
		require.Equal(t, http.StatusConflict, rec.Code, "%s: %s", kind, rec.Body.String())
		require.Contains(t, problem(t, rec.Body.Bytes()).Detail, "is managed by Workflow/w")
		rec = do(t, srv, http.MethodPut, path+"orphan", "operator-token", encode(t, storeOf(kind, "orphan")))
		require.Equal(t, http.StatusOK, rec.Code, "%s: a dead maker's store stays editable: %s", kind, rec.Body.String())
	}
}
