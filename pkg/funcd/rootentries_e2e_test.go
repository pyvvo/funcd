//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// counterSource counts calls in table counts of the store bound as alias c and answers the count.
const counterSource = `export async function handle(ctx) {
  const n = Number((await ctx.kv.getText('c', 'n')) ?? 0) + 1;
  await ctx.kv.put('c', 'n', String(n));
  return { count: n };
}`

// rootFn is Function name running img, with mutate applied to its spec.
func rootFn(name, img string, mutate func(*v1.FunctionSpec)) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", img
	if mutate != nil {
		mutate(&fn.Spec)
	}
	return fn
}

// countersStore declares KVStore counters with table counts and no owner, which function-store-owner fills in.
func countersStore(s *v1.FunctionSpec, deletion v1.DeletionPolicy) {
	s.KVStores = append(s.KVStores, v1.KVStoreEntry{Name: "counters", Deletion: deletion, KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "counts"}}}})
	s.Bindings.KV = append(s.Bindings.KV, v1.FunctionKV{Alias: "c", Store: "counters", Table: "counts"})
}

// unowned is obj as a user sends it to take it over: no owner and no resourceVersion. With no version the write carries
// no precondition, so the ownership check answers it whatever wrote the object since it was read (ADR-0210, #1158).
func unowned(obj v1.Object) v1.Object {
	obj.GetObjectMeta().OwnerReferences = nil
	obj.GetObjectMeta().ResourceVersion = ""
	return obj
}

func (e *gcEnv) function(t *testing.T, name string) *v1.Function {
	t.Helper()
	return e.object(t, v1.KindFunction, name).(*v1.Function)
}

// waitReason waits until Function name is Ready=False with reason.
func (e *gcEnv) waitReason(t *testing.T, name, reason string) *v1.Function {
	t.Helper()
	var fn *v1.Function
	require.Eventually(t, func() bool {
		fn = e.function(t, name)
		c, ok := fn.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionFalse && c.Reason == reason
	}, 30*time.Second, 50*time.Millisecond, "Function/%s is Ready=False %s", name, reason)
	return fn
}

// answer POSTs an empty event to Function fn and returns its status and body.
func (e *gcEnv) answer(t *testing.T, fn string) (int, string) {
	t.Helper()
	resp, err := http.Post(e.dp+"/function/"+fn, "application/json", strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// scenario: function-owns-its-store
func TestScenarioFunctionOwnsItsStore(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	writeStep(t, e.src, "counter", counterSource)
	e.apply(t, rootFn("counter", pushStepImage(t, e.layout, e.src, "counter"), func(s *v1.FunctionSpec) { countersStore(s, "") }))

	fn := e.function(t, "counter")
	require.Equal(t, v1.ObjectName("counter"), fn.Spec.KVStores[0].Tables[0].Owner, "the stored Function shows owner: counter")
	e.waitExists(t, v1.KindKVStore, "counters")
	ks := e.object(t, v1.KindKVStore, "counters").(*v1.KVStore)
	require.True(t, ownedstore.Marked(ks.OwnerReferences, fn), "KVStore counters carries counter's marker")
	require.Len(t, ks.OwnerReferences, 1, "retain: no controller reference")
	require.Equal(t, []v1.KVTable{{Name: "counts", Owner: "counter"}}, ks.Spec.Tables)

	e.waitServes(t, "counter")
	_, body := e.answer(t, "counter")
	require.JSONEq(t, `{"count":2}`, body, "counter writes counts (the first call was waitServes')")
}

// scenario: function-refs-shared-store
func TestScenarioFunctionRefsSharedStore(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	sessions := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	sessions.Name, sessions.Namespace, sessions.ResourceGroup = "sessions", "default", "rg1"
	sessions.Spec.Tables = []v1.KVTable{{Name: "t"}}
	e.apply(t, sessions)
	before := e.object(t, v1.KindKVStore, "sessions").(*v1.KVStore)

	e.apply(t, rootFn("login", e.image(t), func(s *v1.FunctionSpec) {
		s.KVStores = []v1.KVStoreEntry{{Ref: "sessions"}}
		s.Bindings.KV = []v1.FunctionKV{{Alias: "s", Store: "sessions", Table: "t"}}
	}))
	e.waitServes(t, "login")
	e.del(t, v1.KindFunction, "login")
	e.waitNotFound(t, "login")
	got := e.object(t, v1.KindKVStore, "sessions").(*v1.KVStore)
	require.Equal(t, before.Spec, got.Spec, "sessions stays, unchanged")
	require.Empty(t, got.OwnerReferences)
}

// scenario: binding-needs-root-entry
func TestScenarioBindingNeedsRootEntry(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	body, err := json.Marshal(rootFn("ghostly", e.image(t), func(s *v1.FunctionSpec) {
		s.Bindings.KV = []v1.FunctionKV{{Alias: "g", Store: "ghost", Table: "t"}}
	}))
	require.NoError(t, err)
	code, out := e.call(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/functions", funcd.DevToken, body)
	require.Equal(t, http.StatusBadRequest, code, "%s", out)
	require.Equal(t, `admission.binding-declared: spec.bindings.kv[0]: KVStore "ghost" is not declared in spec.kv`, detail(t, out))
	require.False(t, e.exists(t, v1.KindFunction, "ghostly"))
}

// scenario: store-not-taken-over
func TestScenarioStoreNotTakenOver(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.workflow(t, "keeper", "rg1", []string{"s"}, v1.KVStoreEntry{Name: "counters", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "kept"}}}})
	e.waitExists(t, v1.KindKVStore, "counters")
	before := e.object(t, v1.KindKVStore, "counters").(*v1.KVStore)

	e.apply(t, rootFn("counter", e.image(t), func(s *v1.FunctionSpec) { countersStore(s, "") }))
	fn := e.waitReason(t, "counter", "StoreNotOwned")
	c, _ := fn.Status.Conditions.Get("Ready")
	require.Contains(t, c.Message, `spec.kv[0]: KVStore "counters" exists and is not this Function's`)
	after := e.object(t, v1.KindKVStore, "counters").(*v1.KVStore)
	require.Equal(t, before.Spec, after.Spec, "the store is unchanged")
	require.Equal(t, before.OwnerReferences, after.OwnerReferences)
}

// scenario: user-cannot-replace-owned-store
func TestScenarioUserCannotReplaceOwnedStore(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, rootFn("counter", e.image(t), func(s *v1.FunctionSpec) {
		countersStore(s, "")
		s.Buckets = []v1.BucketEntry{{Name: "files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw"}}}}}
	}))
	e.waitExists(t, v1.KindKVStore, "counters")
	e.waitExists(t, v1.KindBucket, "files")
	for _, kind := range []v1.Kind{v1.KindKVStore, v1.KindBucket} {
		obj := e.object(t, kind, map[v1.Kind]string{v1.KindKVStore: "counters", v1.KindBucket: "files"}[kind])
		_, err := e.c.Apply(context.Background(), unowned(obj))
		require.Equal(t, fault.Conflict, fault.KindOf(err), "%s: %v", kind, err)
		require.ErrorContains(t, err, "is managed by Function/counter, which made it")
	}
}

// Issue #1158: the owner rewrites its store between the user's read and the user's apply, as a reconciler's write did;
// the apply is still refused for ownership, not for a stale resourceVersion.
func TestIssue1158_OwnedStoreRefusedAfterTheOwnerWritesIt(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, rootFn("counter", e.image(t), func(s *v1.FunctionSpec) { countersStore(s, "") }))
	e.waitExists(t, v1.KindKVStore, "counters")
	read := e.object(t, v1.KindKVStore, "counters")
	e.apply(t, rootFn("counter", e.image(t), func(s *v1.FunctionSpec) {
		countersStore(s, "")
		s.KVStores[0].Tables = append(s.KVStores[0].Tables, v1.KVTable{Name: "totals"})
	}))
	require.Eventually(t, func() bool {
		return e.object(t, v1.KindKVStore, "counters").GetObjectMeta().ResourceVersion != read.GetObjectMeta().ResourceVersion
	}, 30*time.Second, 20*time.Millisecond, "the owner rewrites its store")

	_, err := e.c.Apply(context.Background(), unowned(read))
	require.Equal(t, fault.Conflict, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "is managed by Function/counter, which made it")
}

// scenario: store-deletion
func TestScenarioStoreDeletion(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	e.apply(t, rootFn("counter", e.image(t), func(s *v1.FunctionSpec) {
		countersStore(s, v1.DeletionDelete)
		s.KVStores = append(s.KVStores, v1.KVStoreEntry{Name: "history", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}})
	}))
	e.waitExists(t, v1.KindKVStore, "counters", "history")
	e.del(t, v1.KindFunction, "counter")
	e.waitGone(t, v1.KindKVStore, "counters")
	require.True(t, e.exists(t, v1.KindKVStore, "history"), "a retain store stays")
}

// scenario: recreated-owner
func TestScenarioRecreatedOwner(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	img := e.image(t)
	e.apply(t, rootFn("counter", img, func(s *v1.FunctionSpec) { countersStore(s, "") }))
	e.waitExists(t, v1.KindKVStore, "counters")
	e.del(t, v1.KindFunction, "counter")
	require.Eventually(t, func() bool { return !e.exists(t, v1.KindFunction, "counter") }, 30*time.Second, 50*time.Millisecond)

	e.apply(t, rootFn("counter", img, func(s *v1.FunctionSpec) { countersStore(s, "") }))
	e.waitReason(t, "counter", "StoreNotOwned")

	e.apply(t, rootFn("counter", img, func(s *v1.FunctionSpec) {
		countersStore(s, "")
		s.KVStores[0] = v1.KVStoreEntry{Ref: "counters"}
	}))
	e.waitServes(t, "counter")
}

// scenario: function-config-and-secrets
func TestScenarioFunctionConfigAndSecrets(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	settings := v1.ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Paris"}}
	stored := v1.AppConfigMapName("app-settings", settings)
	e.apply(t, rootFn("billing", e.imageEnv(t, "env"), func(s *v1.FunctionSpec) {
		s.ConfigMaps = []v1.ConfigMapEntry{{Name: "app-settings", ConfigMapSpec: settings}}
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"STRIPE_KEY"}}}
		s.Bindings.Config = []v1.ObjectName{"app-settings"}
		s.Bindings.Secret = []v1.ObjectName{"api-keys"}
	}))
	require.Equal(t, []v1.ObjectName{stored}, e.function(t, "billing").Spec.Bindings.Config, "the binding is repointed")

	e.waitReason(t, "billing", "SecretNotFound")
	require.False(t, e.exists(t, v1.KindConfigMap, string(stored)), "no ConfigMap before the Secrets are complete")
	applySecret(t, e.c, "api-keys", map[string][]byte{"OTHER": []byte("x")})
	fn := e.waitReason(t, "billing", "SecretKeyMissing")
	c, _ := fn.Status.Conditions.Get("Ready")
	require.NotContains(t, c.Message, "x", "no value enters a status")
	applySecret(t, e.c, "api-keys", map[string][]byte{"STRIPE_KEY": []byte("sk_test")})
	e.waitServes(t, "billing")
	require.True(t, e.exists(t, v1.KindConfigMap, string(stored)), "ConfigMap app-settings-<hash> exists")

	// ADR-0248 Decision 5 (ADR-0213 Decision 5): new data is a new stored name; once it serves, the old one is pruned.
	berlin := v1.ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Berlin"}}
	next := v1.AppConfigMapName("app-settings", berlin)
	fn = e.function(t, "billing")
	fn.ResourceVersion, fn.Status = "", v1.FunctionStatus{}
	fn.Spec.ConfigMaps[0].ConfigMapSpec = berlin
	fn.Spec.Bindings.Config = []v1.ObjectName{"app-settings"}
	e.apply(t, fn)
	require.Equal(t, []v1.ObjectName{next}, e.function(t, "billing").Spec.Bindings.Config, "the binding follows the data")
	require.Eventually(t, func() bool {
		_, body := e.answer(t, "billing")
		return strings.Contains(body, "Europe/Berlin")
	}, 30*time.Second, 100*time.Millisecond, "billing serves the new data")
	require.Eventually(t, func() bool { return !e.exists(t, v1.KindConfigMap, string(stored)) }, 30*time.Second, 50*time.Millisecond,
		"the old app-settings-<hash> is pruned")
	require.True(t, e.exists(t, v1.KindConfigMap, string(next)))
}

// scenario: pooled-functions-own-config
func TestScenarioPooledFunctionsOwnConfig(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	e := startGC(t, funcd.WithPoolShim(node, langmod.PoolShim(t)))
	img := e.imageEnv(t, "env")
	for name, tz := range map[string]string{"paris": "Europe/Paris", "tokyo": "Asia/Tokyo"} {
		e.apply(t, rootFn(name, img, func(s *v1.FunctionSpec) {
			s.Pooling.Worker = "shared"
			s.ConfigMaps = []v1.ConfigMapEntry{{Name: "app-settings", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"TZ": tz}}}}
			s.Bindings.Config = []v1.ObjectName{"app-settings"}
		}))
	}
	e.waitServes(t, "paris", "tokyo")
	for name, tz := range map[string]string{"paris": "Europe/Paris", "tokyo": "Asia/Tokyo"} {
		_, body := e.answer(t, name)
		require.JSONEq(t, `{"tz":"`+tz+`","stripe":"","envStripe":""}`, body, "%s reads its own values", name)
	}
	require.NotEqual(t, e.function(t, "paris").Status.Pool, e.function(t, "tokyo").Status.Pool, "they land in different pools")
}

// scenario: workflow-steps-bind
func TestScenarioWorkflowStepsBind(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	shared := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	shared.Name, shared.Namespace, shared.ResourceGroup = "shared", "default", "rg1"
	shared.Spec.Tables = []v1.KVTable{{Name: "t"}}
	e.apply(t, shared)
	before := e.object(t, v1.KindKVStore, "shared").(*v1.KVStore)

	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "etl", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			KVStores: []v1.KVStoreEntry{{Name: "etl-kv", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "rows", Owner: "load"}}}}, {Ref: "shared"}},
			Buckets:  []v1.BucketEntry{{Name: "etl-files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: "load"}}}}},
			Steps: []v1.WorkflowStep{{Name: "load", Function: &v1.FunctionStep{Image: e.image(t), Bindings: v1.StepBindings{
				KV:   []v1.FunctionKV{{Alias: "rows", Store: "etl-kv", Table: "rows"}, {Alias: "shared", Store: "shared", Table: "t"}},
				Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "etl-files", Prefix: "raw"}},
			}}}},
		},
	}
	e.apply(t, wf)
	e.waitServes(t, "etl-load")
	stored := e.object(t, v1.KindWorkflow, "etl")
	kv := e.object(t, v1.KindKVStore, "etl-kv").(*v1.KVStore)
	require.True(t, ownedstore.Marked(kv.OwnerReferences, stored))
	require.Equal(t, v1.ObjectName("etl-load"), kv.Spec.Tables[0].Owner, "a table owner names a step")
	b := e.object(t, v1.KindBucket, "etl-files").(*v1.Bucket)
	require.True(t, ownedstore.Marked(b.OwnerReferences, stored))
	require.Equal(t, []v1.BucketPrefix{{Name: "raw", Owner: "etl-load"}}, b.Spec.Prefixes, "a prefix owner names a step")
	step := e.function(t, "etl-load")
	require.Equal(t, wf.Spec.Steps[0].Function.Bindings.KV, step.Spec.Bindings.KV, "the step keeps its binding to the ref store")
	require.Equal(t, wf.Spec.Steps[0].Function.Bindings.Blob, step.Spec.Bindings.Blob)
	after := e.object(t, v1.KindKVStore, "shared").(*v1.KVStore)
	require.Equal(t, before.Spec, after.Spec, "a ref store is never written")
	require.Empty(t, after.OwnerReferences)
}

// scenario: app-parts-bind-app-stores
func TestScenarioAppPartsBindAppStores(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := todoApp(t, e)
	a.Spec.Workflows[0].Steps[0].Function.Bindings.KV = []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}}
	e.apply(t, a)
	e.waitServes(t, "todo-plan-due")
	require.Equal(t, a.Spec.Workflows[0].Steps[0].Function.Bindings.KV, e.function(t, "todo-plan-due").Spec.Bindings.KV,
		"the Workflow part's step binds the App's store")

	own := todoApp(t, e)
	own.Name, own.ResourceGroup = "todo-own", "todo-own"
	own.Spec.Functions[0].KVStores = []v1.KVStoreEntry{{Name: "private"}}
	body, err := json.Marshal(own)
	require.NoError(t, err)
	code, out := e.call(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/apps", funcd.DevToken, body)
	require.Equal(t, http.StatusBadRequest, code, "%s", out)
	require.Contains(t, detail(t, out), "spec.functions[0].kv: an App's part declares nothing at its root")
}
