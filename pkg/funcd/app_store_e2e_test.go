//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The fixture stores' data: each key sits in a table the store declares, since the KV reconciler reclaims an
// undeclared one, and each object under the Bucket's substrate prefix.
const (
	storeKey = "default/todo-store/todos/k"
	cacheKey = "default/todo-cache/entries/k"
	filesKey = "s3/default/todo-files/attachments/a.txt"
	tmpKey   = "s3/default/todo-tmp/a.txt"
)

func (e *gcEnv) putAppKey(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, e.kv.Put(context.Background(), key, []byte("v")))
}

func (e *gcEnv) hasAppKey(t *testing.T, key string) bool {
	t.Helper()
	_, found, err := e.kv.Get(context.Background(), key)
	require.NoError(t, err)
	return found
}

func (e *gcEnv) putObject(t *testing.T, key string) {
	t.Helper()
	require.NoError(t, e.blob.Put(context.Background(), key, []byte("v"), blob.PutOptions{}))
}

func (e *gcEnv) hasObject(t *testing.T, key string) bool {
	t.Helper()
	found, err := e.blob.Exists(context.Background(), key)
	require.NoError(t, err)
	return found
}

// onlyMarked reports whether a's marker is obj's only reference, as on a retain store (ADR-0199 Decision 7).
func onlyMarked(obj v1.Object, a *v1.App) bool {
	marked, controlled := appRefs(obj, a)
	return marked && !controlled && len(obj.GetObjectMeta().OwnerReferences) == 1
}

// pruningChild returns the App's Pruning child of kind and name.
func pruningChild(a *v1.App, kind v1.Kind, name v1.ObjectName) (v1.AppChild, bool) {
	i := slices.IndexFunc(a.Status.Children, func(c v1.AppChild) bool {
		return c.State == v1.AppChildPruning && c.Kind == kind && c.Name == name
	})
	if i < 0 {
		return v1.AppChild{}, false
	}
	return a.Status.Children[i], true
}

// scenario: app-store-deletion-flip
func TestScenarioAppStoreDeletionFlip(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := todoApp(t, e)
	e.apply(t, a)
	live := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.putAppKey(t, cacheKey)

	cache := a.Spec.KV[1]
	a.Spec.KV[1] = v1.AppKVStore{Ref: "todo-cache"}
	_, err := e.c.Apply(e.ctx, a)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "spec.kv[1].ref")
	require.Equal(t, live.Generation, e.app(t, "todo").Generation, "the refused spec is not stored")

	cache.Deletion = v1.DeletionRetain
	a.Spec.KV[1] = cache
	e.apply(t, a)
	require.Eventually(t, func() bool { return onlyMarked(e.object(t, v1.KindKVStore, "todo-cache"), live) }, appWithin, 50*time.Millisecond, "todo-cache keeps only the marker")

	a.Spec.KV[1] = v1.AppKVStore{Ref: "todo-cache"}
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)

	e.del(t, v1.KindApp, "todo")
	e.waitGone(t, v1.KindFunction, "todo-api")
	e.waitGone(t, v1.KindBucket, "todo-tmp")
	require.Never(t, func() bool { return !e.exists(t, v1.KindKVStore, "todo-cache") }, time.Second, 50*time.Millisecond, "todo-cache stays")
	require.True(t, onlyMarked(e.object(t, v1.KindKVStore, "todo-cache"), live))
	require.True(t, e.hasAppKey(t, cacheKey), "todo-cache keeps its keys")
}

// scenario: app-prune-dropped-part
func TestScenarioAppPruneDroppedPart(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := todoApp(t, e)
	e.apply(t, a)
	live := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.putAppKey(t, storeKey)

	a.Spec.Routes, a.Spec.KV = nil, a.Spec.KV[1:]
	e.apply(t, a)
	require.Eventually(t, func() bool { return !e.exists(t, v1.KindRoute, "todo-api") }, appWithin, 20*time.Millisecond, "Route/todo-api is pruned")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Len(t, got.Status.Children, len(todoParts())-2, "no part is left Pruning")
	require.Eventually(t, func() bool { return e.routed(t, todoHost, "/api") == http.StatusNotFound }, gcWithin, 20*time.Millisecond, "nothing routes /api")
	require.Never(t, func() bool { return !e.exists(t, v1.KindKVStore, "todo-store") }, time.Second, 50*time.Millisecond, "todo-store stays")
	require.True(t, onlyMarked(e.object(t, v1.KindKVStore, "todo-store"), live), "todo-store keeps its marker")
	require.True(t, e.hasAppKey(t, storeKey), "todo-store keeps its data")
}

// scenario: app-store-in-use-kept
func TestScenarioAppStoreInUseKept(t *testing.T) {
	t.Parallel()
	e := startGC(t, funcd.WithGCSweepInterval(time.Second))
	a := todoApp(t, e)
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.putAppKey(t, cacheKey)

	a.Spec.KV = a.Spec.KV[:1]
	e.apply(t, a)
	require.Eventually(t, func() bool {
		c, ok := pruningChild(e.app(t, "todo"), v1.KindKVStore, "todo-cache")
		return ok && strings.HasPrefix(c.Reason, "InUse") && strings.Contains(c.Reason, "Function/todo-api")
	}, appWithin, 50*time.Millisecond, "todo-cache is Pruning InUse naming Function/todo-api")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.True(t, e.exists(t, v1.KindKVStore, "todo-cache"))
	require.True(t, e.hasAppKey(t, cacheKey))

	a.Spec.Functions[0].KV = a.Spec.Functions[0].KV[:1]
	e.apply(t, a)
	require.Eventually(t, func() bool {
		return !e.exists(t, v1.KindKVStore, "todo-cache") && !e.hasAppKey(t, cacheKey)
	}, appWithin, 50*time.Millisecond, "todo-cache goes with its keys once unbound")
	require.Eventually(t, func() bool {
		a := e.app(t, "todo")
		_, left := pruningChild(a, v1.KindKVStore, "todo-cache")
		return !left && readyCondition(a).Status == v1.ConditionTrue
	}, appWithin, 50*time.Millisecond, "the App is Ready with nothing Pruning")

	e.apply(t, todoApp(t, e))
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	audit := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "audit", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.FunctionSpec{
			Runtime: "nodejs22",
			Handler: "handle",
			Image:   e.image(t),
			KV:      []v1.FunctionKV{{Alias: "cache", Store: "todo-cache", Table: "entries"}},
		},
	}
	e.apply(t, audit)
	e.putAppKey(t, cacheKey)

	e.del(t, v1.KindApp, "todo")
	e.waitGone(t, v1.KindFunction, "todo-api")
	e.waitGone(t, v1.KindBucket, "todo-tmp")
	require.Never(t, func() bool { return !e.exists(t, v1.KindKVStore, "todo-cache") }, 3*time.Second, 50*time.Millisecond, "todo-cache waits while Function/audit binds it")
	require.True(t, e.hasAppKey(t, cacheKey))

	audit.Spec.KV = nil
	e.apply(t, audit)
	require.Eventually(t, func() bool {
		return !e.exists(t, v1.KindKVStore, "todo-cache") && !e.hasAppKey(t, cacheKey)
	}, appWithin, 50*time.Millisecond, "a later sweep collects todo-cache with its keys")
}

// scenario: app-store-not-taken-over
func TestScenarioAppStoreNotTakenOver(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, todoApp(t, e))
	live := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	before := e.object(t, v1.KindKVStore, "todo-store").(*v1.KVStore)
	e.workflow(t, "w", "rg1", []string{"s"}, kvStore("todo-store", v1.DeletionRetain))

	err := e.c.HandoverKVStore(e.ctx, "default", "todo-store", "w")
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a handover: %v", err)
	require.ErrorContains(t, err, "App/todo")

	_, err = e.c.Apply(e.ctx, &v1.KVStore{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore},
		ObjectMeta: v1.ObjectMeta{Name: "todo-store", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       before.Spec,
	})
	require.Equal(t, fault.Conflict, fault.KindOf(err), "a replace: %v", err)
	require.ErrorContains(t, err, "App/todo")

	after := e.object(t, v1.KindKVStore, "todo-store").(*v1.KVStore)
	require.True(t, onlyMarked(after, live), "todo-store keeps the App's marker")
	require.Equal(t, before.Spec, after.Spec)
}

// scenario: app-ref-kept-store
func TestScenarioAppRefKeptStore(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	writeStep(t, e.src, "reader", `export async function handle(ctx) { return { v: await ctx.kv.getText("store", "k") }; }`)
	a := todoApp(t, e)
	a.Spec.Functions[0].Image = pushStepImage(t, e.layout, e.src, "reader")
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.putAppKey(t, storeKey)
	e.putObject(t, filesKey)

	e.del(t, v1.KindApp, "todo")
	e.waitGone(t, v1.KindFunction, "todo-api", "todo-plan-due")
	e.waitGone(t, v1.KindWorkflow, "todo-plan")
	e.waitGone(t, v1.KindRoute, "todo-api")
	e.waitGone(t, v1.KindKVStore, "todo-cache")
	e.waitGone(t, v1.KindBucket, "todo-tmp")
	store := e.object(t, v1.KindKVStore, "todo-store").(*v1.KVStore)
	files := e.object(t, v1.KindBucket, "todo-files").(*v1.Bucket)

	a.Spec.KV[0] = v1.AppKVStore{Ref: "todo-store"}
	a.Spec.Buckets[0] = v1.AppBucket{Ref: "todo-files"}
	e.apply(t, a)
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Eventually(t, func() bool { return e.readKey(t, "todo-api") == "v" }, appWithin, 100*time.Millisecond, "todo-api reads the kept key")

	nowStore := e.object(t, v1.KindKVStore, "todo-store").(*v1.KVStore)
	require.Equal(t, store.OwnerReferences, nowStore.OwnerReferences)
	require.Equal(t, store.Generation, nowStore.Generation)
	require.Equal(t, store.Spec, nowStore.Spec)
	nowFiles := e.object(t, v1.KindBucket, "todo-files").(*v1.Bucket)
	require.Equal(t, files.OwnerReferences, nowFiles.OwnerReferences)
	require.Equal(t, files.ResourceVersion, nowFiles.ResourceVersion)
	require.True(t, e.hasObject(t, filesKey), "todo-files keeps its objects")
}

// scenario: app-delete-collects-tree
func TestScenarioAppDeleteCollectsTree(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, todoApp(t, e))
	live := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	e.waitExists(t, v1.KindFunction, "todo-plan-due")
	e.putAppKey(t, storeKey)
	e.putAppKey(t, cacheKey)
	e.putObject(t, filesKey)
	e.putObject(t, tmpKey)

	e.del(t, v1.KindApp, "todo")
	gone := []todoPart{
		{v1.KindFunction, "todo-api"}, {v1.KindRoute, "todo-api"}, {v1.KindWorkflow, "todo-plan"},
		{v1.KindFunction, "todo-plan-due"}, {v1.KindKVStore, "todo-cache"}, {v1.KindBucket, "todo-tmp"},
	}
	require.Eventually(t, func() bool {
		for _, p := range gone {
			if e.exists(t, p.kind, p.name) {
				return false
			}
		}
		return !e.hasAppKey(t, cacheKey) && !e.hasObject(t, tmpKey)
	}, gcWithin, 20*time.Millisecond, "the tree goes, with todo-cache's keys and todo-tmp's objects")
	for _, p := range []todoPart{{v1.KindKVStore, "todo-store"}, {v1.KindBucket, "todo-files"}} {
		require.True(t, onlyMarked(e.object(t, p.kind, p.name), live), "%s/%s stays with its marker", p.kind, p.name)
	}
	require.True(t, e.hasAppKey(t, storeKey), "todo-store keeps its data")
	require.True(t, e.hasObject(t, filesKey), "todo-files keeps its objects")
}
