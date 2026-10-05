//go:build e2e

package funcd_test

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/store"
	bstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const gcWithin = 5 * time.Second

type gcEnv struct {
	ctx    context.Context
	c      *sdk.Client
	dp     string
	kv     kvstore.KV
	blob   blob.Bucket
	layout string
	src    string
	stop   func()
}

func startGC(t *testing.T, opts ...funcd.Option) *gcEnv {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err, "node serves the step Functions")
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	kv := kvmemory.New()
	base := []funcd.Option{
		funcd.InMemory(), funcd.WithBlob(bucket), funcd.WithKVStore(kv),
		funcd.WithRuntimeShim(node, langmod.NodeShim(t)), funcd.WithArtifactStore(t.TempDir()),
		funcd.WithWorkflow("", 0, time.Hour, 1, 0), funcd.WithGCSweepInterval(time.Hour),
	}
	p, err := funcd.New(append(base, opts...)...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("platform Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	e := &gcEnv{ctx: ctx, c: c, dp: "http://" + p.DataPlaneAddr(), kv: kv, blob: bucket, layout: t.TempDir(), src: t.TempDir(), stop: stop}
	writeStep(t, e.src, "echo", `export async function handle(event) { return event; }`)
	return e
}

func (e *gcEnv) image(t *testing.T) string { return pushStepImage(t, e.layout, e.src, "echo") }

func (e *gcEnv) workflow(t *testing.T, name, group string, steps []string, kv ...v1.WorkflowKVStore) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: v1.ResourceGroupName(group)},
		Spec:       v1.WorkflowSpec{KV: kv},
	}
	img := e.image(t)
	for _, s := range steps {
		wf.Spec.Steps = append(wf.Spec.Steps, v1.WorkflowStep{Name: v1.ObjectName(s), Function: &v1.FunctionStep{Image: img}})
	}
	_, err := e.c.Apply(e.ctx, wf)
	require.NoError(t, err)
}

func (e *gcEnv) apply(t *testing.T, obj v1.Object) {
	t.Helper()
	_, err := e.c.Apply(e.ctx, obj)
	require.NoError(t, err)
}

func (e *gcEnv) exists(t *testing.T, kind v1.Kind, name string) bool {
	t.Helper()
	_, err := e.c.Get(context.Background(), kind, "default", v1.ObjectName(name))
	return fault.KindOf(err) != fault.NotFound
}

func (e *gcEnv) waitExists(t *testing.T, kind v1.Kind, names ...string) {
	t.Helper()
	for _, n := range names {
		require.Eventually(t, func() bool { return e.exists(t, kind, n) }, 30*time.Second, 50*time.Millisecond, "%s/%s appears", kind, n)
	}
}

func (e *gcEnv) waitGone(t *testing.T, kind v1.Kind, names ...string) {
	t.Helper()
	for _, n := range names {
		require.Eventually(t, func() bool { return !e.exists(t, kind, n) }, gcWithin, 20*time.Millisecond, "%s/%s is collected", kind, n)
	}
}

func (e *gcEnv) del(t *testing.T, kind v1.Kind, name string, opts ...sdk.DeleteOption) {
	t.Helper()
	require.NoError(t, e.c.Delete(e.ctx, kind, "default", v1.ObjectName(name), opts...))
}

func (e *gcEnv) invoke(t *testing.T, fn string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.dp+"/function/"+fn, strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	return e.do(t, req)
}

func (e *gcEnv) do(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (e *gcEnv) waitServes(t *testing.T, fns ...string) {
	t.Helper()
	e.waitExists(t, v1.KindFunction, fns...)
	for _, fn := range fns {
		require.Eventually(t, func() bool { return e.invoke(t, fn) == http.StatusOK }, 30*time.Second, 100*time.Millisecond, "%s answers", fn)
	}
}

func (e *gcEnv) waitNotFound(t *testing.T, fn string) {
	t.Helper()
	require.Eventually(t, func() bool { return e.invoke(t, fn) == http.StatusNotFound }, gcWithin, 20*time.Millisecond, "%s answers 404", fn)
}

func kvKey(store string) string { return "default/" + store + "/t/k" }

func (e *gcEnv) putKey(t *testing.T, store string) {
	t.Helper()
	require.NoError(t, e.kv.Put(context.Background(), kvKey(store), []byte("v")))
}

func (e *gcEnv) hasKey(t *testing.T, store string) bool {
	t.Helper()
	_, found, err := e.kv.Get(context.Background(), kvKey(store))
	require.NoError(t, err)
	return found
}

func kvStore(name string, deletion v1.DeletionPolicy) v1.WorkflowKVStore {
	return v1.WorkflowKVStore{Name: v1.ObjectName(name), Deletion: deletion, Tables: []v1.KVTable{{Name: "t"}}}
}

func readyReason(t *testing.T, e *gcEnv, kind v1.Kind, name string) string {
	t.Helper()
	obj, err := e.c.Get(e.ctx, kind, "default", v1.ObjectName(name))
	require.NoError(t, err)
	var conds v1.Conditions
	switch o := obj.(type) {
	case *v1.Workflow:
		conds = o.Status.Conditions
	case *v1.Identity:
		conds = o.Status.Conditions
	}
	c, _ := conds.Get("Ready")
	return c.Reason
}

// scenario: workflow-delete-collects-steps-and-stores
func TestScenarioWorkflowDeleteCollectsStepsAndStores(t *testing.T) {
	e := startGC(t)
	e.workflow(t, "gcwf", "rg1", []string{"s1", "s2"}, kvStore("gcwf-state", v1.DeletionDelete))
	e.waitServes(t, "gcwf-s1", "gcwf-s2")
	e.waitExists(t, v1.KindKVStore, "gcwf-state")
	e.putKey(t, "gcwf-state")
	e.del(t, v1.KindWorkflow, "gcwf")
	e.waitGone(t, v1.KindFunction, "gcwf-s1", "gcwf-s2")
	e.waitGone(t, v1.KindKVStore, "gcwf-state")
	e.waitNotFound(t, "gcwf-s1")
	require.Eventually(t, func() bool { return !e.hasKey(t, "gcwf-state") }, gcWithin, 20*time.Millisecond, "the key is reclaimed")
}

// scenario: workflow-delete-keeps-retain-store
func TestScenarioWorkflowDeleteKeepsRetainStore(t *testing.T) {
	e := startGC(t)
	keep := kvStore("gcwf-keep", v1.DeletionRetain)
	keep.Tables[0].Owner = "s1"
	e.workflow(t, "gcwf", "rg1", []string{"s1"}, keep)
	e.waitExists(t, v1.KindKVStore, "gcwf-keep")
	e.putKey(t, "gcwf-keep")
	e.del(t, v1.KindWorkflow, "gcwf")
	e.waitGone(t, v1.KindFunction, "gcwf-s1")
	require.Never(t, func() bool { return !e.exists(t, v1.KindKVStore, "gcwf-keep") || !e.hasKey(t, "gcwf-keep") }, time.Second, 50*time.Millisecond)
	obj, err := e.c.Get(e.ctx, v1.KindKVStore, "default", "gcwf-keep")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("gcwf-s1"), obj.(*v1.KVStore).Spec.Tables[0].Owner, "its table owner is a Function that no longer exists")
}

// scenario: removed-step-function-deleted
func TestScenarioRemovedStepFunctionDeleted(t *testing.T) {
	e := startGC(t)
	e.workflow(t, "gcwf", "rg1", []string{"s1", "s2"})
	e.waitServes(t, "gcwf-s1", "gcwf-s2")
	e.workflow(t, "gcwf", "rg1", []string{"s1"})
	e.waitGone(t, v1.KindFunction, "gcwf-s2")
	e.waitNotFound(t, "gcwf-s2")
	require.Equal(t, http.StatusOK, e.invoke(t, "gcwf-s1"))
}

// scenario: removed-kv-store-kept-until-workflow-delete
func TestScenarioRemovedKvStoreKeptUntilWorkflowDelete(t *testing.T) {
	e := startGC(t)
	e.workflow(t, "gcwf", "rg1", []string{"s1"}, kvStore("gcwf-state", v1.DeletionDelete), kvStore("gcwf-extra", v1.DeletionDelete))
	e.waitExists(t, v1.KindKVStore, "gcwf-extra")
	e.putKey(t, "gcwf-extra")
	e.workflow(t, "gcwf", "rg1", []string{"s1"}, kvStore("gcwf-state", v1.DeletionDelete))
	require.Never(t, func() bool { return !e.exists(t, v1.KindKVStore, "gcwf-extra") || !e.hasKey(t, "gcwf-extra") }, time.Second, 50*time.Millisecond)
	e.del(t, v1.KindWorkflow, "gcwf")
	e.waitGone(t, v1.KindKVStore, "gcwf-extra", "gcwf-state")
}

// scenario: workflow-refuses-anothers-child-or-users-function
func TestScenarioWorkflowRefusesAnothersChildOrUsersFunction(t *testing.T) {
	e := startGC(t)
	e.workflow(t, "a", "rg1", []string{"b-c"}, kvStore("shared", v1.DeletionDelete))
	e.waitExists(t, v1.KindFunction, "a-b-c")
	e.waitExists(t, v1.KindKVStore, "shared")
	e.putKey(t, "shared")
	e.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "u-s", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)},
	})
	e.workflow(t, "b", "rg1", []string{"x"}, kvStore("shared", v1.DeletionDelete))
	e.workflow(t, "a-b", "rg1", []string{"c"})
	e.workflow(t, "u", "rg1", []string{"s"})
	for name, reason := range map[string]string{"b": "KVStoreNotOwned", "a-b": "FunctionNotOwned", "u": "FunctionNotOwned"} {
		require.Eventually(t, func() bool { return readyReason(t, e, v1.KindWorkflow, name) == reason }, 30*time.Second, 50*time.Millisecond, "%s is %s", name, reason)
	}
	for _, n := range []string{"b", "a-b", "u"} {
		e.del(t, v1.KindWorkflow, n)
	}
	require.Never(t, func() bool {
		return !e.exists(t, v1.KindFunction, "a-b-c") || !e.exists(t, v1.KindFunction, "u-s") || !e.exists(t, v1.KindKVStore, "shared") || !e.hasKey(t, "shared")
	}, 2*time.Second, 50*time.Millisecond)
}

// scenario: identity-delete-collects-secret
func TestScenarioIdentityDeleteCollectsSecret(t *testing.T) {
	e := startGC(t)
	e.apply(t, &v1.Identity{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity},
		ObjectMeta: v1.ObjectMeta{Name: "ext", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.IdentitySpec{Type: v1.IdentityTypeExternal},
	})
	e.waitExists(t, v1.KindSecret, "ext")
	e.del(t, v1.KindIdentity, "ext")
	e.waitGone(t, v1.KindSecret, "ext")
}

// scenario: site-delete-collects-route
func TestScenarioSiteDeleteCollectsRoute(t *testing.T) {
	e := startGC(t)
	ref, _ := pushSiteBundle(t, e.layout, "web", map[string]string{"index.html": "<!doctype html><title>web</title>"})
	e.apply(t, &v1.Site{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite},
		ObjectMeta: v1.ObjectMeta{Name: "web", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.SiteSpec{
			Image:   ref,
			Bucket:  v1.SiteBucket{Name: "web-assets"},
			Prefix:  "web",
			Ingress: v1.SiteIngress{Host: "web.example.com", Public: true},
		},
	})
	host := func() int {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.dp+"/", nil)
		require.NoError(t, err)
		req.Host = "web.example.com"
		return e.do(t, req)
	}
	require.Eventually(t, func() bool { return host() == http.StatusOK }, 30*time.Second, 100*time.Millisecond, "the site serves its host")
	e.del(t, v1.KindSite, "web")
	e.waitGone(t, v1.KindRoute, "web")
	require.Eventually(t, func() bool { return host() == http.StatusNotFound }, gcWithin, 20*time.Millisecond, "the host answers 404")
	require.True(t, e.exists(t, v1.KindBucket, "web-assets"), "the Bucket remains")
}

// scenario: function-delete-collects-revisions
func TestScenarioFunctionDeleteCollectsRevisions(t *testing.T) {
	e := startGC(t)
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "fn", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)},
	}
	e.apply(t, fn)
	e.waitExists(t, v1.KindRevision, "fn-1")
	fn.Spec.Scaling.IdleTimeout = time.Minute
	e.apply(t, fn)
	e.waitExists(t, v1.KindRevision, "fn-2")
	e.del(t, v1.KindFunction, "fn")
	require.Eventually(t, func() bool {
		revs, err := e.c.List(e.ctx, v1.KindRevision, "default")
		require.NoError(t, err)
		for _, r := range revs {
			if strings.HasPrefix(string(r.GetObjectMeta().Name), "fn-") {
				return false
			}
		}
		return true
	}, gcWithin, 20*time.Millisecond, "no Revision of fn remains")
}

// scenario: crash-before-collection-swept
func TestScenarioCrashBeforeCollectionSwept(t *testing.T) {
	dir := filepath.Join(shortDataDir(t), "store")
	open := func() store.Store {
		eng, err := bstore.Open(dir, bstore.WithValueLogGCInterval(0))
		require.NoError(t, err)
		return store.New(eng)
	}
	e := startGC(t, funcd.WithStore(open()))
	e.workflow(t, "gcwf", "rg1", []string{"s1", "s2"}, kvStore("gcwf-state", v1.DeletionDelete))
	e.waitExists(t, v1.KindFunction, "gcwf-s1", "gcwf-s2")
	e.waitExists(t, v1.KindKVStore, "gcwf-state")
	e.stop()

	st := open()
	require.NoError(t, st.Delete(context.Background(), v1.KindWorkflow.GVK(), "default", "gcwf", ""))
	require.NoError(t, st.Close())

	e = startGC(t, funcd.WithStore(open()))
	e.waitGone(t, v1.KindFunction, "gcwf-s1", "gcwf-s2")
	e.waitGone(t, v1.KindKVStore, "gcwf-state")
}

func group(name string) *v1.ResourceGroup {
	return &v1.ResourceGroup{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindResourceGroup.GVK().APIVersion(), Kind: v1.KindResourceGroup},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: v1.ResourceGroupName(name)},
	}
}

// scenario: resourcegroup-nonempty-delete-refused
func TestScenarioResourcegroupNonemptyDeleteRefused(t *testing.T) {
	e := startGC(t)
	e.apply(t, group("team"))
	e.apply(t, &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: "a", Namespace: "default", ResourceGroup: "team"},
	})
	err := e.c.Delete(e.ctx, v1.KindResourceGroup, "default", "team")
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "ConfigMap/a")
	require.True(t, e.exists(t, v1.KindResourceGroup, "team"))
	require.True(t, e.exists(t, v1.KindConfigMap, "a"))
}

// scenario: resourcegroup-force-delete-cascades
func TestScenarioResourcegroupForceDeleteCascades(t *testing.T) {
	e := startGC(t)
	e.apply(t, group("team"))
	e.workflow(t, "gcwf", "team", []string{"s1"}, kvStore("gcwf-state", v1.DeletionDelete))
	e.waitExists(t, v1.KindFunction, "gcwf-s1")
	e.waitExists(t, v1.KindKVStore, "gcwf-state")
	e.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "a", Namespace: "default", ResourceGroup: "team"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: e.image(t)},
	})
	e.apply(t, &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: "c", Namespace: "default", ResourceGroup: "team"},
	})
	e.del(t, v1.KindResourceGroup, "team", sdk.Force())
	for kind, names := range map[v1.Kind][]string{
		v1.KindResourceGroup: {"team"}, v1.KindWorkflow: {"gcwf"}, v1.KindFunction: {"gcwf-s1", "a"},
		v1.KindKVStore: {"gcwf-state"}, v1.KindConfigMap: {"c"},
	} {
		for _, n := range names {
			require.False(t, e.exists(t, kind, n), "%s/%s is gone", kind, n)
		}
	}
}

func (e *gcEnv) groupInvocations(t *testing.T, group v1.ResourceGroupName) int {
	t.Helper()
	invs, err := e.c.List(context.Background(), v1.KindInvocation, "default")
	require.NoError(t, err)
	n := 0
	for _, inv := range invs {
		if inv.GetObjectMeta().ResourceGroup == group {
			n++
		}
	}
	return n
}

// scenario: resourcegroup-force-with-backlogged-sensor
func TestScenarioResourcegroupForceWithBackloggedSensor(t *testing.T) {
	e := startGC(t, funcd.WithDeadLetterQueue("", 1, 0, 0))
	e.apply(t, group("team"))
	writeStep(t, e.src, "stuck", `export async function handle() { await new Promise((r) => setTimeout(r, 120000)); }`)
	e.apply(t, &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: "stuck", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: pushStepImage(t, e.layout, e.src, "stuck")},
	})
	e.apply(t, &v1.EventSource{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		ObjectMeta: v1.ObjectMeta{Name: "clock", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.EventSourceSpec{Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "beat", Interval: 100 * time.Millisecond}}}},
	})
	e.apply(t, &v1.Sensor{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: "s", Namespace: "default", ResourceGroup: "team"},
		Spec: v1.SensorSpec{
			On: []v1.Dependency{{Name: "beat", Source: "clock", Event: "beat"}},
			Do: []v1.Action{
				{Name: "miss", On: "beat", Function: "missing"},
				{Name: "hang", On: "beat", Function: "stuck"},
			},
		},
	})
	require.Eventually(t, func() bool { return e.groupInvocations(t, "team") > 0 }, 30*time.Second, 50*time.Millisecond, "s records an Invocation in team")
	e.del(t, v1.KindResourceGroup, "team", sdk.Force())
	require.Zero(t, e.groupInvocations(t, "team"), "no Invocation of s remains in team")
	require.False(t, e.exists(t, v1.KindSensor, "s"))
	require.False(t, e.exists(t, v1.KindResourceGroup, "team"))
}

// scenario: resourcegroup-force-stops-at-protected-member
func TestScenarioResourcegroupForceStopsAtProtectedMember(t *testing.T) {
	e := startGC(t)
	e.apply(t, group("team"))
	e.apply(t, &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: "lake", Namespace: "default", ResourceGroup: "team"},
		Spec:       v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: "writer"}}},
	})
	require.NoError(t, e.blob.Put(context.Background(), "s3/default/lake/raw/part-0", []byte("rows"), blob.PutOptions{}))
	err := e.c.Delete(e.ctx, v1.KindResourceGroup, "default", "team", sdk.Force())
	require.Equal(t, fault.Conflict, fault.KindOf(err), "got %v", err)
	require.ErrorContains(t, err, `Bucket "lake"`)
	require.True(t, e.exists(t, v1.KindBucket, "lake"))
	require.True(t, e.exists(t, v1.KindResourceGroup, "team"))
}
