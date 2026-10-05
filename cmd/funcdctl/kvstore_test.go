package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

func TestKVStoreHandoverCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
		}),
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)

	obj, err := st.Create(ctx, &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: "team-a", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{{Name: "s", Function: &v1.FunctionStep{Image: "oci:s"}}},
			KV:    []v1.WorkflowKVStore{{Name: "w-keep", Tables: []v1.KVTable{{Name: "t"}}}},
		},
	})
	require.NoError(t, err)
	wf := obj.(*v1.Workflow)
	_, err = st.Create(ctx, &v1.KVStore{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: "w-keep", Namespace: "team-a", ResourceGroup: "rg1", OwnerReferences: []v1.OwnerReference{{
			ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: "team-a", Name: "w"}, UID: "u1",
		}}},
		Spec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}},
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "kvstore", "handover", "w-keep", "w", "-n", "team-a"))
	require.Equal(t, "kvstore/w-keep handed over to workflow/w\n", out.String())
	got, err := st.Get(ctx, v1.KindKVStore.GVK(), "team-a", "w-keep")
	require.NoError(t, err)
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: "team-a", Name: "w"}, UID: wf.UID}}, got.GetObjectMeta().OwnerReferences)
}
