//go:build e2e

package funcd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// envEngine is a Lake engine that is Ready at once and records the environment of its last converge, so a
// test sees which ConfigMap the engine runs on.
type envEngine struct {
	addr string
	mu   sync.Mutex
	env  map[string]string
}

func (e *envEngine) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.env = spec.Env
	return provider.ProviderStatus{Running: 1, Ready: true, Address: e.addr}, nil
}

func (*envEngine) Teardown(context.Context, provider.ProviderRef) error { return nil }

func (e *envEngine) lastEnv() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.env
}

// lakeRig is a running platform whose lake engines are Ready at once, driven through the API.
type lakeRig struct {
	p      *Platform
	c      *sdk.Client
	engine *envEngine
}

func startLakeRig(t *testing.T) *lakeRig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	engine := &envEngine{addr: strings.TrimPrefix(srv.URL, "http://")}
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	p, err := New(InMemory(), WithRuntime(rt), WithLakeProviderRuntime(engine))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	return &lakeRig{p: p, c: c, engine: engine}
}

func (r *lakeRig) apply(t *testing.T, obj v1.Object) error {
	t.Helper()
	_, err := r.c.Apply(context.Background(), obj)
	return err
}

func (r *lakeRig) get(t *testing.T, kind v1.Kind, name v1.ObjectName) v1.Object {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	require.NoError(t, err)
	return obj
}

func (r *lakeRig) exists(t *testing.T, kind v1.Kind, name v1.ObjectName) bool {
	t.Helper()
	_, err := r.p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	if fault.KindOf(err) == fault.NotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

// waitLake waits until Lake lake is in phase with the Ready condition's reason ("" for Ready).
func (r *lakeRig) waitLake(t *testing.T, phase v1.Phase, reason string) *v1.Lake {
	t.Helper()
	var cs *v1.Lake
	require.Eventually(t, func() bool {
		cs = r.get(t, v1.KindLake, "lake").(*v1.Lake)
		c, ok := cs.Status.Conditions.Get("Ready")
		return cs.Status.Phase == phase && ok && c.Reason == reason
	}, 10*time.Second, 20*time.Millisecond, "Lake lake is %s %s", phase, reason)
	return cs
}

// lakeOn is Lake lake whose lake and one blob binding are prefix lake of bucket, with mutate applied.
func lakeOn(bucket v1.ObjectName, mutate func(*v1.LakeSpec)) *v1.Lake {
	cs := &v1.Lake{TypeMeta: v1.TypeMeta{APIVersion: v1.KindLake.GVK().APIVersion(), Kind: v1.KindLake}}
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Location = v1.LakeLocation{Bucket: bucket, Prefix: "lake"}
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: bucket, Prefix: "lake"}}
	if mutate != nil {
		mutate(&cs.Spec)
	}
	return cs
}

func ownLakehouse(s *v1.LakeSpec) {
	s.Buckets = []v1.BucketEntry{{Name: "lakehouse", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake"}}}}}
}

// scenario: lake-owns-its-bucket
func TestScenarioLakeOwnsItsBucket(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	require.NoError(t, r.apply(t, lakeOn("lakehouse", ownLakehouse)))

	cs := r.waitLake(t, v1.PhaseReady, "")
	require.Equal(t, v1.LakeOwner("lake"), cs.Spec.Buckets[0].Prefixes[0].Owner, "lake-store-owner writes the lake in")
	b := r.get(t, v1.KindBucket, "lakehouse").(*v1.Bucket)
	require.True(t, ownedstore.Marked(b.OwnerReferences, cs), "Bucket lakehouse carries lake's marker")
	require.Equal(t, []v1.BucketPrefix{{Name: "lake", Owner: v1.LakeOwner("lake")}}, b.Spec.Prefixes, "prefix lake is owned by lake")
}

// scenario: lake-ref-bucket
func TestScenarioLakeLocationBucket(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	landing := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	landing.Name, landing.Namespace, landing.ResourceGroup = "landing", "default", "rg1"
	landing.Spec.Prefixes = []v1.BucketPrefix{{Name: "lake", Owner: v1.LakeOwner("lake")}}
	require.NoError(t, r.apply(t, landing))
	before := r.get(t, v1.KindBucket, "landing").(*v1.Bucket)

	require.NoError(t, r.apply(t, lakeOn("landing", func(s *v1.LakeSpec) { s.Buckets = []v1.BucketEntry{{Ref: "landing"}} })))
	r.waitLake(t, v1.PhaseReady, "")
	require.NoError(t, r.c.Delete(context.Background(), v1.KindLake, "default", "lake"))
	require.Eventually(t, func() bool { return !r.exists(t, v1.KindLake, "lake") }, 10*time.Second, 20*time.Millisecond)
	after := r.get(t, v1.KindBucket, "landing").(*v1.Bucket)
	require.Equal(t, before.Spec, after.Spec, "landing stays, unchanged")
	require.Empty(t, after.OwnerReferences, "a ref is never marked")
}

// scenario: lake-secret-check
func TestScenarioLakeSecretCheck(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	require.NoError(t, r.apply(t, lakeOn("lakehouse", func(s *v1.LakeSpec) {
		ownLakehouse(s)
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "quack", Keys: []string{"QUACK_TOKEN"}}}
		s.Bindings.Secret = []v1.ObjectName{"quack"}
	})))
	r.waitLake(t, v1.PhasePending, ownedstore.ReasonSecretNotFound)
	require.False(t, r.exists(t, v1.KindBucket, "lakehouse"), "nothing is written while a declared Secret is incomplete")

	sec := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	sec.Name, sec.Namespace, sec.ResourceGroup = "quack", "default", "rg1"
	sec.Spec.Type, sec.Spec.Data = "Opaque", map[string][]byte{"OTHER": []byte("x")}
	require.NoError(t, r.apply(t, sec))
	cs := r.waitLake(t, v1.PhasePending, ownedstore.ReasonSecretKeyMissing)
	c, _ := cs.Status.Conditions.Get("Ready")
	require.NotContains(t, c.Message, "x", "no Secret value enters the status")

	sec.Spec.Data = map[string][]byte{"QUACK_TOKEN": []byte("token")}
	require.NoError(t, r.apply(t, sec))
	r.waitLake(t, v1.PhaseReady, "")
	require.Equal(t, "token", r.engine.lastEnv()["QUACK_TOKEN"], "the engine starts with the declared key")
}

// scenario: lake-config-hashed
func TestScenarioLakeConfigHashed(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	tuning := func(threads string) func(*v1.LakeSpec) {
		return func(s *v1.LakeSpec) {
			ownLakehouse(s)
			s.ConfigMaps = []v1.ConfigMapEntry{{Name: "tuning", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": threads}}}}
			s.Bindings.Config = []v1.ObjectName{"tuning"}
		}
	}
	require.NoError(t, r.apply(t, lakeOn("lakehouse", tuning("1"))))
	cs := r.waitLake(t, v1.PhaseReady, "")
	first := v1.AppConfigMapName("tuning", v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": "1"}})
	require.Equal(t, []v1.ObjectName{first}, cs.Spec.Bindings.Config, "the binding names the stored ConfigMap")
	cm := r.get(t, v1.KindConfigMap, first).(*v1.ConfigMap)
	require.True(t, v1.ControlledBy(cm.OwnerReferences, v1.KindLake, cs.UID), "lake controls tuning-<hash>")
	require.Eventually(t, func() bool { return r.engine.lastEnv()["DUCKDB_THREADS"] == "1" }, 10*time.Second, 20*time.Millisecond)

	require.NoError(t, r.apply(t, lakeOn("lakehouse", tuning("2"))))
	second := v1.AppConfigMapName("tuning", v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": "2"}})
	require.Eventually(t, func() bool {
		return r.engine.lastEnv()["DUCKDB_THREADS"] == "2" && r.exists(t, v1.KindConfigMap, second) && !r.exists(t, v1.KindConfigMap, first)
	}, 10*time.Second, 20*time.Millisecond, "the engine restarts on %s, and %s goes", second, first)
}

// scenario: lake-binding-needs-root-entry
func TestScenarioLakeBindingNeedsRootEntry(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	err := r.apply(t, lakeOn("ghost", nil))
	require.ErrorContains(t, err, `admission.binding-declared: spec.bindings.blob[0]: Bucket "ghost" is not declared in spec.buckets`)
	require.False(t, r.exists(t, v1.KindLake, "lake"))
}

// scenario: nested-lake-has-no-root
func TestScenarioNestedLakeHasNoRoot(t *testing.T) {
	t.Parallel()
	r := startLakeRig(t)
	nested := lakeOn("lakehouse", ownLakehouse).Spec

	fn := lakeFunction("default")
	fn.Name = "reader"
	fn.Spec.Buckets = []v1.BucketEntry{{Name: "lakehouse", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake", Owner: v1.LakeOwner("lake")}}}}}
	fn.Spec.Lakes = []v1.LakeEntry{{Name: "lake", LakeSpec: nested}}
	require.ErrorContains(t, r.apply(t, fn),
		"spec.lakes[0].buckets: a lake nested in a Function declares nothing at its root; declare it at the Function's root")

	app := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp}}
	app.Name, app.Namespace, app.ResourceGroup = "lakes", "default", "rg1"
	app.Spec.Buckets = fn.Spec.Buckets
	app.Spec.Lakes = fn.Spec.Lakes
	require.ErrorContains(t, r.apply(t, app),
		"spec.lakes[0].buckets: a lake nested in an App declares nothing at its root; declare it at the App's root")
}
