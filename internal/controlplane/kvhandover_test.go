package controlplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow"
)

const kvNS v1.NamespaceName = "team-kv"

type grant struct {
	verb auth.Verb
	kind v1.Kind
}

// pairAuthorizer allows each subject exactly its listed (verb, kind) pairs.
type pairAuthorizer map[string][]grant

func (a pairAuthorizer) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	for _, g := range a[req.Identity.Subject] {
		if g.verb == req.Verb && g.kind == req.Kind {
			return auth.Decision{Allowed: true}, nil
		}
	}
	return auth.Decision{Reason: "not granted"}, nil
}

func newKVServer(t *testing.T) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	return kvServerOn(t, st), st
}

// kvServerOn is newKVServer over st.
func kvServerOn(t *testing.T, st store.Store) http.Handler {
	t.Helper()
	tokens := map[string]auth.Identity{}
	for _, sub := range []string{"operator", "wfwriter", "kvupdater"} {
		tokens[sub+"-token"] = auth.Identity{Subject: sub, Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{kvNS}}
	}
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st,
		Authorizer: pairAuthorizer{
			"operator": {
				{auth.VerbGet, v1.KindKVStore}, {auth.VerbUpdate, v1.KindKVStore}, {auth.VerbDelete, v1.KindKVStore},
				{auth.VerbGet, v1.KindWorkflow},
			},
			"wfwriter":  {{auth.VerbGet, v1.KindWorkflow}, {auth.VerbUpdate, v1.KindWorkflow}, {auth.VerbCreate, v1.KindWorkflow}},
			"kvupdater": {{auth.VerbGet, v1.KindWorkflow}, {auth.VerbUpdate, v1.KindKVStore}},
		},
		Credentials: middleware.NewStaticCredentials(tokens),
	})
	require.NoError(t, err)
	return h
}

func kvWorkflow(t *testing.T, st store.Store, store v1.ObjectName, policy v1.DeletionPolicy) *v1.Workflow {
	t.Helper()
	out, err := st.Create(context.Background(), &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: kvNS, ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{{Name: "s", Function: &v1.FunctionStep{Image: "oci:s"}}},
			KV:    []v1.WorkflowKVStore{{Name: store, Deletion: policy, Tables: []v1.KVTable{{Name: "t"}}}},
		},
	})
	require.NoError(t, err)
	return out.(*v1.Workflow)
}

func markerOf(wfName v1.ObjectName, uid v1.UID) v1.OwnerReference {
	return v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: kvNS, Name: wfName}, UID: uid}
}

func keptStore(t *testing.T, st store.Store, name v1.ObjectName, refs ...v1.OwnerReference) *v1.KVStore {
	t.Helper()
	out, err := st.Create(context.Background(), &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: kvNS, ResourceGroup: "rg1", OwnerReferences: refs},
		Spec:       v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}},
	})
	require.NoError(t, err)
	return out.(*v1.KVStore)
}

func handover(t *testing.T, srv http.Handler, token string, store, wf v1.ObjectName) int {
	t.Helper()
	body, err := json.Marshal(controlplane.KVStoreHandover{Workflow: wf})
	require.NoError(t, err)
	return do(t, srv, http.MethodPost, fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/kvstores/%s/handover", kvNS, store), token, body).Code
}

func storeRV(t *testing.T, st store.Store, name v1.ObjectName) string {
	t.Helper()
	o, err := st.Get(context.Background(), v1.KindKVStore.GVK(), kvNS, name)
	require.NoError(t, err)
	return o.GetObjectMeta().ResourceVersion
}

// scenario: handover-needs-kvstore-update-and-delete
func TestScenarioHandoverNeedsKvstoreUpdateAndDelete(t *testing.T) {
	for _, policy := range []v1.DeletionPolicy{v1.DeletionRetain, v1.DeletionDelete} {
		srv, st := newKVServer(t)
		wf := kvWorkflow(t, st, "w-keep", policy)
		kept := keptStore(t, st, "w-keep", markerOf("w", "u1"))

		for _, token := range []string{"wfwriter-token", "kvupdater-token"} {
			require.Equal(t, http.StatusForbidden, handover(t, srv, token, "w-keep", "w"), "%s %s", policy, token)
			require.Equal(t, kept.ResourceVersion, storeRV(t, st, "w-keep"), "the store is unchanged")
		}

		live, err := st.Get(context.Background(), v1.KindKVStore.GVK(), kvNS, "w-keep")
		require.NoError(t, err)
		live.GetObjectMeta().OwnerReferences = []v1.OwnerReference{markerOf("w", wf.UID)}
		live, err = st.Update(context.Background(), live)
		require.NoError(t, err)
		require.Equal(t, http.StatusConflict, handover(t, srv, "operator-token", "w-keep", "w"), "the marker names a live Workflow")
		require.Equal(t, live.GetObjectMeta().ResourceVersion, storeRV(t, st, "w-keep"))

		live.GetObjectMeta().OwnerReferences = []v1.OwnerReference{markerOf("w", "u1")}
		_, err = st.Update(context.Background(), live)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, handover(t, srv, "operator-token", "w-keep", "w"))
		got, err := st.Get(context.Background(), v1.KindKVStore.GVK(), kvNS, "w-keep")
		require.NoError(t, err)
		require.Equal(t, []v1.OwnerReference{markerOf("w", wf.UID)}, got.GetObjectMeta().OwnerReferences)
		require.Equal(t, kept.Spec, got.(*v1.KVStore).Spec, "the spec is untouched")
	}
}

func TestHandoverKVStoreRefusesMissingOrUndeclared(t *testing.T) {
	srv, st := newKVServer(t)
	kvWorkflow(t, st, "w-keep", v1.DeletionRetain)
	keptStore(t, st, "w-keep")
	keptStore(t, st, "other")
	require.Equal(t, http.StatusNotFound, handover(t, srv, "operator-token", "missing", "w"))
	require.Equal(t, http.StatusNotFound, handover(t, srv, "operator-token", "w-keep", "nope"))
	require.Equal(t, http.StatusConflict, handover(t, srv, "operator-token", "other", "w"), "w does not declare it")
}

func TestKVStoreUpdateRefusedWhileItsWorkflowLives(t *testing.T) {
	srv, st := newKVServer(t)
	wf := kvWorkflow(t, st, "w-kv", v1.DeletionRetain)
	kept := keptStore(t, st, "w-kv", markerOf("w", wf.UID))
	body, err := json.Marshal(kept)
	require.NoError(t, err)
	path := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/kvstores/w-kv", kvNS)
	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodPut, path, "operator-token", body).Code)
	require.Equal(t, kept.ResourceVersion, storeRV(t, st, "w-kv"))

	require.NoError(t, st.Delete(context.Background(), v1.KindWorkflow.GVK(), kvNS, "w", ""))
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPut, path, "operator-token", body).Code, "a dead-UID marker stays editable")
}

// noPurge is a gc.BucketPurger that purges nothing: these tests collect no Bucket.
type noPurge struct{}

func (noPurge) Purge(context.Context, v1.NamespaceName, v1.ObjectName) error { return nil }

// scenario: migration-record-guarded
func TestScenarioMigrationRecordGuarded(t *testing.T) {
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	col, err := gc.New(gc.Deps{Store: st, Purger: noPurge{}})
	require.NoError(t, err)
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(), Collector: col,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{"admin-token": {Subject: "admin", Role: auth.RoleAdmin}}),
	})
	require.NoError(t, err)
	ctx := context.Background()
	rec := &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: workflow.KVMigrationRecord, Namespace: workflow.KVMigrationNamespace, ResourceGroup: "funcd-system"},
		Spec:       v1.ConfigMapSpec{Data: map[string]string{"k": "v"}},
	}
	body, err := json.Marshal(rec)
	require.NoError(t, err)
	base := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/configmaps", workflow.KVMigrationNamespace)

	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodPost, base, "admin-token", body).Code)
	_, err = st.Get(ctx, v1.KindConfigMap.GVK(), workflow.KVMigrationNamespace, workflow.KVMigrationRecord)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no record exists")

	require.NoError(t, workflow.MarkKVStoresOnce(ctx, st, nil))
	before, err := st.Get(ctx, v1.KindConfigMap.GVK(), workflow.KVMigrationNamespace, workflow.KVMigrationRecord)
	require.NoError(t, err)
	item := base + "/" + string(workflow.KVMigrationRecord)
	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodPut, item, "admin-token", body).Code)
	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodDelete, item, "admin-token", nil).Code)

	groups := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/resourcegroups", workflow.KVMigrationNamespace)
	require.Equal(t, http.StatusOK, do(t, srv, http.MethodPost, groups, "admin-token", groupBody(t, workflow.KVMigrationNamespace, "funcd-system")).Code)
	require.Equal(t, http.StatusConflict, do(t, srv, http.MethodDelete, groups+"/funcd-system?force=true", "admin-token", nil).Code,
		"a forced delete of the record's group leaves the record")
	after, err := st.Get(ctx, v1.KindConfigMap.GVK(), workflow.KVMigrationNamespace, workflow.KVMigrationRecord)
	require.NoError(t, err)
	require.Equal(t, before.GetObjectMeta().ResourceVersion, after.GetObjectMeta().ResourceVersion, "the record is unchanged")
}

// A caller the PDP denies gets 403 on every verb on the migration record, as on any other name; only an
// authorized caller meets the ADR-0180 guard's 409.
func TestIssue719_MigrationRecordGuardRunsAfterAuthorization(t *testing.T) {
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close() })
	srv, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			"dev-token":        {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			"view-token":       {Subject: "view", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
			"sys-viewer-token": {Subject: "sysview", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{workflow.KVMigrationNamespace}},
		}),
	})
	require.NoError(t, err)
	require.NoError(t, workflow.MarkKVStoresOnce(context.Background(), st, nil))
	body, err := json.Marshal(&v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: workflow.KVMigrationRecord, Namespace: workflow.KVMigrationNamespace, ResourceGroup: "x"},
		Spec:       v1.ConfigMapSpec{Data: map[string]string{"a": "b"}},
	})
	require.NoError(t, err)
	base := fmt.Sprintf("/apis/funcd.io/v1alpha1/namespaces/%s/configmaps", workflow.KVMigrationNamespace)
	item := base + "/" + string(workflow.KVMigrationRecord)

	for _, token := range []string{"dev-token", "view-token", "sys-viewer-token"} {
		for _, req := range []struct {
			method, path string
			body         []byte
		}{
			{http.MethodPost, base, body},
			{http.MethodPut, item, body},
			{http.MethodDelete, item, nil},
		} {
			t.Run(token+"/"+req.method, func(t *testing.T) {
				rec := do(t, srv, req.method, req.path, token, req.body)
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			})
		}
	}
}
