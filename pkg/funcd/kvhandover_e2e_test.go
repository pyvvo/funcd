//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
)

func (e *gcEnv) boundWorkflow(t *testing.T, store v1.WorkflowKVStore) {
	t.Helper()
	writeStep(t, e.src, "reader", `export async function handle(ctx) { return { v: await ctx.kv.getText("keep", "k") }; }`)
	blob, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	img := "oci-layout://" + e.layout + ":reader"
	_, err = artifact.Push(context.Background(), img, filepath.Join(e.src, "reader.mjs"), blob, "nodejs22", "")
	require.NoError(t, err)
	e.apply(t, &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			KV: []v1.WorkflowKVStore{store},
			Steps: []v1.WorkflowStep{{Name: "s", Function: &v1.FunctionStep{
				Image: img,
				KV:    []v1.FunctionKV{{Alias: "keep", Store: store.Name, Table: "t"}},
			}}},
		},
	})
}

func (e *gcEnv) get(t *testing.T, kind v1.Kind, name string) v1.Object {
	t.Helper()
	obj, err := e.c.Get(e.ctx, kind, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj
}

// readKey invokes fn, whose handler returns key k of its "keep" binding, and reports the value it read.
func (e *gcEnv) readKey(t *testing.T, fn string) string {
	t.Helper()
	resp, err := http.Post(e.dp+"/function/"+fn, "application/json", strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out struct {
		V string `json:"v"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
		return ""
	}
	return out.V
}

func (e *gcEnv) workflowReady(t *testing.T) bool {
	t.Helper()
	c, ok := e.get(t, v1.KindWorkflow, "w").(*v1.Workflow).Status.Conditions.Get("Ready")
	return ok && c.Status == v1.ConditionTrue
}

func (e *gcEnv) stepBinds(t *testing.T, fn string) []v1.FunctionKV {
	t.Helper()
	return e.get(t, v1.KindFunction, fn).(*v1.Function).Spec.KV
}

// scenario: recreated-workflow-needs-handover
func TestScenarioRecreatedWorkflowNeedsHandover(t *testing.T) {
	e := startGC(t)
	keep := kvStore("w-keep", v1.DeletionRetain)
	keep.Tables[0].Owner = "s"
	e.boundWorkflow(t, keep)
	e.waitExists(t, v1.KindKVStore, "w-keep")
	require.Eventually(t, func() bool { return e.exists(t, v1.KindFunction, "w-s") && len(e.stepBinds(t, "w-s")) == 1 }, 30*time.Second, 50*time.Millisecond)
	e.putKey(t, "w-keep")
	u1 := e.get(t, v1.KindWorkflow, "w").GetObjectMeta().UID

	e.del(t, v1.KindWorkflow, "w")
	e.waitGone(t, v1.KindFunction, "w-s")
	kept := e.get(t, v1.KindKVStore, "w-keep").(*v1.KVStore)
	e.boundWorkflow(t, keep)
	u2 := e.get(t, v1.KindWorkflow, "w").GetObjectMeta().UID
	require.NotEqual(t, u1, u2)
	require.Eventually(t, func() bool { return readyReason(t, e, v1.KindWorkflow, "w") == "KVStoreNotOwned" }, 30*time.Second, 50*time.Millisecond)
	now := e.get(t, v1.KindKVStore, "w-keep").(*v1.KVStore)
	require.Equal(t, kept.OwnerReferences, now.OwnerReferences, "w-keep's refs are unchanged")
	require.Equal(t, kept.Spec, now.Spec, "w-keep's spec is unchanged")
	e.waitExists(t, v1.KindFunction, "w-s")
	require.Empty(t, e.stepBinds(t, "w-s"), "w-s carries no kv binding")

	require.NoError(t, e.c.HandoverKVStore(e.ctx, "default", "w-keep", "w"))
	refs := e.get(t, v1.KindKVStore, "w-keep").GetObjectMeta().OwnerReferences
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: "default", Name: "w"}, UID: u2}}, refs, "the u2 marker")
	require.Eventually(t, func() bool {
		return len(e.stepBinds(t, "w-s")) == 1 && e.stepBinds(t, "w-s")[0].Store == "w-keep"
	}, 30*time.Second, 100*time.Millisecond, "w's next reconcile materializes the binding, which only a passing check writes")
	require.Eventually(t, func() bool { return e.workflowReady(t) }, 30*time.Second, 100*time.Millisecond, "w is Ready")
	require.Eventually(t, func() bool { return e.readKey(t, "w-s") == "v" }, 30*time.Second, 100*time.Millisecond, "k is readable through w-s")
}
