package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
)

const appBase = "/apis/funcd.io/v1alpha1/namespaces/team-a/apps"

func appBody(t *testing.T, name v1.ObjectName, mutate func(*v1.AppSpec)) []byte {
	t.Helper()
	app := v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "team-a", ResourceGroup: "rg1"},
		Spec: v1.AppSpec{
			KV: []v1.AppKVStore{{Name: "todo-store", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "todos"}}}}},
			Functions: []v1.AppFunction{
				{Name: "todo-api", FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://todo-api:1"}},
				{Ref: "mailer"},
			},
		},
	}
	if mutate != nil {
		mutate(&app.Spec)
	}
	b, err := json.Marshal(app)
	require.NoError(t, err)
	return b
}

// The App REST endpoints store a valid App and refuse an unknown section (the API schema) or an invalid part (the
// validate admission running App.Validate) before anything is stored (ADR-0199 Decisions 2 and 3).
func TestAppAPIRefusesBeforeStoring(t *testing.T) {
	t.Parallel()
	srv := newServer(t)

	rec := do(t, srv, http.MethodPost, appBase, devToken, appBody(t, "todo", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got v1.App
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, v1.KindApp, got.Kind)
	require.Equal(t, v1.ObjectName("mailer"), got.Spec.Functions[1].Ref)
	rec = do(t, srv, http.MethodPut, appBase+"/todo", devToken, appBody(t, "todo", func(spec *v1.AppSpec) { spec.Version = "2" }))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	unknownSection := bytes.Replace(appBody(t, "todo-bad", nil), []byte(`"spec":{`), []byte(`"spec":{"deployments":[{"name":"web"}],`), 1)
	for _, tc := range []struct {
		want string
		code int
		body []byte
	}{
		{"deployments", http.StatusUnprocessableEntity, unknownSection},
		{"spec.functions[2]", http.StatusBadRequest, appBody(t, "todo-bad", func(spec *v1.AppSpec) {
			spec.Functions = append(spec.Functions, v1.AppFunction{Ref: "mailer"})
		})},
		{"spec.functions[1]", http.StatusBadRequest, appBody(t, "todo-bad", func(spec *v1.AppSpec) {
			spec.Functions[1].Image = "oci-layout://mailer:1"
		})},
	} {
		rec := do(t, srv, http.MethodPost, appBase, devToken, tc.body)
		require.Equal(t, tc.code, rec.Code, "%s: %s", tc.want, rec.Body.String())
		require.Contains(t, rec.Body.String(), tc.want)
		require.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, appBase+"/todo-bad", devToken, nil).Code, "%s: nothing is stored", tc.want)
	}
}

func appMarker(name v1.ObjectName, uid v1.UID) v1.OwnerReference {
	return v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: kvNS, Name: name}, UID: uid}
}

// The API half of scenario app-store-not-taken-over (ADR-0199 Decision 8): while the App that made a KVStore lives,
// a handover and a direct replace of the store answer 409 naming App/todo; once the App is gone, both pass.
func TestKVStoreGuardsCoverALiveApp(t *testing.T) {
	srv, st := newKVServer(t)
	ctx := context.Background()
	obj, err := st.Create(ctx, &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: kvNS, ResourceGroup: "rg1"},
		Spec:       v1.AppSpec{KV: []v1.AppKVStore{{Name: "todo-store", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}}}},
	})
	require.NoError(t, err)
	kvWorkflow(t, st, "todo-store", v1.DeletionRetain)
	kept := keptStore(t, st, "todo-store", appMarker("todo", obj.GetObjectMeta().UID))

	storePath := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/kvstores/todo-store", kvNS)
	body, err := json.Marshal(kept)
	require.NoError(t, err)
	handoverBody, err := json.Marshal(controlplane.KVStoreHandover{Workflow: "w"})
	require.NoError(t, err)

	for name, rec := range map[string]func() int{
		"replace": func() int {
			rec := do(t, srv, http.MethodPut, storePath, "operator-token", body)
			require.Contains(t, rec.Body.String(), "App/todo")
			return rec.Code
		},
		"handover": func() int {
			rec := do(t, srv, http.MethodPost, storePath+"/handover", "operator-token", handoverBody)
			require.Contains(t, rec.Body.String(), "App/todo")
			return rec.Code
		},
	} {
		require.Equal(t, http.StatusConflict, rec(), name)
		require.Equal(t, kept.ResourceVersion, storeRV(t, st, "todo-store"), "%s: the store is unchanged", name)
	}

	require.NoError(t, st.Delete(ctx, v1.KindApp.GVK(), kvNS, "todo", ""))
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPut, storePath, "operator-token", body).Code, "a dead App's marker stays editable")
	require.Equal(t, http.StatusOK, handover(t, srv, "operator-token", "todo-store", "w"))
}
