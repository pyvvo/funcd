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

// Status is server-owned: an apply never takes it from the client and never clears it.

func newServerOn(t *testing.T, st store.Store) http.Handler {
	t.Helper()
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{Store: st, Authorizer: rbac.New(), Credentials: creds})
	require.NoError(t, err)
	return h
}

// fnManifest is a Function request body; status, when non-nil, is sent as the client's status.
func fnManifest(t *testing.T, image string, status map[string]interface{}) []byte {
	t.Helper()
	m := map[string]interface{}{
		"TypeMeta": map[string]interface{}{"apiVersion": "funcd.io/v1alpha1", "kind": "Function"},
		"metadata": map[string]interface{}{"name": "echo", "namespace": "team-a", "resourceGroup": "rg1"},
		"spec":     map[string]interface{}{"runtime": "nodejs22", "handler": "app.handler", "image": image},
	}
	if status != nil {
		m["status"] = status
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

func apply(t *testing.T, srv http.Handler, method, path string, body []byte) {
	t.Helper()
	rec := do(t, srv, method, path, devToken, body)
	require.Less(t, rec.Code, 300, "%s %s: %s", method, path, rec.Body.String())
}

func storedEcho(t *testing.T, st store.Store) *v1.Function {
	t.Helper()
	obj, err := st.Get(context.Background(), v1.KindFunction.GVK(), "team-a", "echo")
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// reconcile writes a status through the store, as the Function reconciler does, and returns the stored Function.
func reconcile(t *testing.T, st store.Store) *v1.Function {
	t.Helper()
	fn := storedEcho(t, st)
	fn.Status.Phase = v1.PhaseReady
	fn.Status.Replicas = 1
	fn.Status.CurrentRevision = "echo-1"
	fn.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionTrue})
	_, err := st.Update(context.Background(), fn)
	require.NoError(t, err)
	return storedEcho(t, st)
}

func TestApplyOfUnchangedManifestWritesNothing(t *testing.T) {
	st := store.New(memory.New())
	srv := newServerOn(t, st)
	apply(t, srv, http.MethodPost, fnBase, fnManifest(t, "oci://example/app:v1", nil))
	before := reconcile(t, st)

	apply(t, srv, http.MethodPut, fnBase+"/echo", fnManifest(t, "oci://example/app:v1", nil))
	after := storedEcho(t, st)
	require.Equal(t, before.Status, after.Status, "the status the reconciler wrote is kept")
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "nothing changed, so nothing is written (ADR-0047)")
}

func TestApplyOfChangedSpecKeepsStatus(t *testing.T) {
	st := store.New(memory.New())
	srv := newServerOn(t, st)
	apply(t, srv, http.MethodPost, fnBase, fnManifest(t, "oci://example/app:v1", nil))
	before := reconcile(t, st)

	apply(t, srv, http.MethodPut, fnBase+"/echo", fnManifest(t, "oci://example/app:v2", nil))
	after := storedEcho(t, st)
	require.Equal(t, "oci://example/app:v2", after.Spec.Image)
	require.Equal(t, before.Generation+1, after.Generation, "the spec change bumps the generation")
	require.Equal(t, before.Status, after.Status, "the status stays until the reconciler writes a new one")
}

func TestApplyIgnoresClientStatus(t *testing.T) {
	st := store.New(memory.New())
	srv := newServerOn(t, st)
	// huma does not honor ",inline", so the common status fields nest under "Status" (as TypeMeta does).
	claimed := map[string]interface{}{
		"Status":          map[string]interface{}{"phase": "Failed"},
		"currentRevision": "bogus-9",
		"replicas":        7,
	}

	apply(t, srv, http.MethodPost, fnBase, fnManifest(t, "oci://example/app:v1", claimed))
	require.Equal(t, v1.FunctionStatus{}, storedEcho(t, st).Status, "a create takes no status from the client")

	before := reconcile(t, st)
	apply(t, srv, http.MethodPut, fnBase+"/echo", fnManifest(t, "oci://example/app:v1", claimed))
	after := storedEcho(t, st)
	require.Equal(t, before.Status, after.Status, "a replace keeps the stored status, not the client's")
	require.Equal(t, before.ResourceVersion, after.ResourceVersion, "the client's status alone changes nothing")
}
