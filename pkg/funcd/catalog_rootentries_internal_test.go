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

// envEngine is a CatalogService engine that is Ready at once and records the environment of its last converge, so a
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

// catalogRig is a running platform whose catalog engines are Ready at once, driven through the API.
type catalogRig struct {
	p      *Platform
	c      *sdk.Client
	engine *envEngine
}

func startCatalogRig(t *testing.T) *catalogRig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	engine := &envEngine{addr: strings.TrimPrefix(srv.URL, "http://")}
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	p, err := New(InMemory(), WithRuntime(rt), WithCatalogProviderRuntime(engine))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	return &catalogRig{p: p, c: c, engine: engine}
}

func (r *catalogRig) apply(t *testing.T, obj v1.Object) error {
	t.Helper()
	_, err := r.c.Apply(context.Background(), obj)
	return err
}

func (r *catalogRig) get(t *testing.T, kind v1.Kind, name v1.ObjectName) v1.Object {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	require.NoError(t, err)
	return obj
}

func (r *catalogRig) exists(t *testing.T, kind v1.Kind, name v1.ObjectName) bool {
	t.Helper()
	_, err := r.p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	if fault.KindOf(err) == fault.NotFound {
		return false
	}
	require.NoError(t, err)
	return true
}

// waitCatalog waits until CatalogService lake is in phase with the Ready condition's reason ("" for Ready).
func (r *catalogRig) waitCatalog(t *testing.T, phase v1.Phase, reason string) *v1.CatalogService {
	t.Helper()
	var cs *v1.CatalogService
	require.Eventually(t, func() bool {
		cs = r.get(t, v1.KindCatalogService, "lake").(*v1.CatalogService)
		c, ok := cs.Status.Conditions.Get("Ready")
		return cs.Status.Phase == phase && ok && c.Reason == reason
	}, 10*time.Second, 20*time.Millisecond, "CatalogService lake is %s %s", phase, reason)
	return cs
}

// lakeOn is CatalogService lake whose catalog and one blob binding are prefix lake of bucket, with mutate applied.
func lakeOn(bucket v1.ObjectName, mutate func(*v1.CatalogServiceSpec)) *v1.CatalogService {
	cs := &v1.CatalogService{TypeMeta: v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}}
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", "default", "rg1"
	cs.Spec.Catalog = v1.CatalogRef{Bucket: bucket, Prefix: "lake"}
	cs.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "lake", Bucket: bucket, Prefix: "lake"}}
	if mutate != nil {
		mutate(&cs.Spec)
	}
	return cs
}

func ownLakehouse(s *v1.CatalogServiceSpec) {
	s.Buckets = []v1.BucketEntry{{Name: "lakehouse", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake"}}}}}
}

// scenario: catalog-owns-its-bucket
func TestScenarioCatalogOwnsItsBucket(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	require.NoError(t, r.apply(t, lakeOn("lakehouse", ownLakehouse)))

	cs := r.waitCatalog(t, v1.PhaseReady, "")
	require.Equal(t, v1.ObjectName("lake"), cs.Spec.Buckets[0].Prefixes[0].Owner, "catalog-store-owner writes the catalog in")
	b := r.get(t, v1.KindBucket, "lakehouse").(*v1.Bucket)
	require.True(t, ownedstore.Marked(b.OwnerReferences, cs), "Bucket lakehouse carries lake's marker")
	require.Equal(t, []v1.BucketPrefix{{Name: "lake", Owner: "lake"}}, b.Spec.Prefixes, "prefix lake is owned by lake")
}

// scenario: catalog-ref-bucket
func TestScenarioCatalogRefBucket(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	landing := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	landing.Name, landing.Namespace, landing.ResourceGroup = "landing", "default", "rg1"
	landing.Spec.Prefixes = []v1.BucketPrefix{{Name: "lake", Owner: "lake"}}
	require.NoError(t, r.apply(t, landing))
	before := r.get(t, v1.KindBucket, "landing").(*v1.Bucket)

	require.NoError(t, r.apply(t, lakeOn("landing", func(s *v1.CatalogServiceSpec) { s.Buckets = []v1.BucketEntry{{Ref: "landing"}} })))
	r.waitCatalog(t, v1.PhaseReady, "")
	require.NoError(t, r.c.Delete(context.Background(), v1.KindCatalogService, "default", "lake"))
	require.Eventually(t, func() bool { return !r.exists(t, v1.KindCatalogService, "lake") }, 10*time.Second, 20*time.Millisecond)
	after := r.get(t, v1.KindBucket, "landing").(*v1.Bucket)
	require.Equal(t, before.Spec, after.Spec, "landing stays, unchanged")
	require.Empty(t, after.OwnerReferences, "a ref is never marked")
}

// scenario: catalog-secret-check
func TestScenarioCatalogSecretCheck(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	require.NoError(t, r.apply(t, lakeOn("lakehouse", func(s *v1.CatalogServiceSpec) {
		ownLakehouse(s)
		s.SecretDeclarations = []v1.SecretDeclaration{{Name: "quack", Keys: []string{"QUACK_TOKEN"}}}
		s.Bindings.Secret = []v1.ObjectName{"quack"}
	})))
	r.waitCatalog(t, v1.PhasePending, ownedstore.ReasonSecretNotFound)
	require.False(t, r.exists(t, v1.KindBucket, "lakehouse"), "nothing is written while a declared Secret is incomplete")

	sec := &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret}}
	sec.Name, sec.Namespace, sec.ResourceGroup = "quack", "default", "rg1"
	sec.Spec.Type, sec.Spec.Data = "Opaque", map[string][]byte{"OTHER": []byte("x")}
	require.NoError(t, r.apply(t, sec))
	cs := r.waitCatalog(t, v1.PhasePending, ownedstore.ReasonSecretKeyMissing)
	c, _ := cs.Status.Conditions.Get("Ready")
	require.NotContains(t, c.Message, "x", "no Secret value enters the status")

	sec.Spec.Data = map[string][]byte{"QUACK_TOKEN": []byte("token")}
	require.NoError(t, r.apply(t, sec))
	r.waitCatalog(t, v1.PhaseReady, "")
	require.Equal(t, "token", r.engine.lastEnv()["QUACK_TOKEN"], "the engine starts with the declared key")
}

// scenario: catalog-config-hashed
func TestScenarioCatalogConfigHashed(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	tuning := func(threads string) func(*v1.CatalogServiceSpec) {
		return func(s *v1.CatalogServiceSpec) {
			ownLakehouse(s)
			s.ConfigMaps = []v1.ConfigMapEntry{{Name: "tuning", ConfigMapSpec: v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": threads}}}}
			s.Bindings.Config = []v1.ObjectName{"tuning"}
		}
	}
	require.NoError(t, r.apply(t, lakeOn("lakehouse", tuning("1"))))
	cs := r.waitCatalog(t, v1.PhaseReady, "")
	first := v1.AppConfigMapName("tuning", v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": "1"}})
	require.Equal(t, []v1.ObjectName{first}, cs.Spec.Bindings.Config, "the binding names the stored ConfigMap")
	cm := r.get(t, v1.KindConfigMap, first).(*v1.ConfigMap)
	require.True(t, v1.ControlledBy(cm.OwnerReferences, v1.KindCatalogService, cs.UID), "lake controls tuning-<hash>")
	require.Eventually(t, func() bool { return r.engine.lastEnv()["DUCKDB_THREADS"] == "1" }, 10*time.Second, 20*time.Millisecond)

	require.NoError(t, r.apply(t, lakeOn("lakehouse", tuning("2"))))
	second := v1.AppConfigMapName("tuning", v1.ConfigMapSpec{Data: map[string]string{"DUCKDB_THREADS": "2"}})
	require.Eventually(t, func() bool {
		return r.engine.lastEnv()["DUCKDB_THREADS"] == "2" && r.exists(t, v1.KindConfigMap, second) && !r.exists(t, v1.KindConfigMap, first)
	}, 10*time.Second, 20*time.Millisecond, "the engine restarts on %s, and %s goes", second, first)
}

// scenario: catalog-binding-needs-root-entry
func TestScenarioCatalogBindingNeedsRootEntry(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	err := r.apply(t, lakeOn("ghost", nil))
	require.ErrorContains(t, err, `admission.binding-declared: spec.bindings.blob[0]: Bucket "ghost" is not declared in spec.buckets`)
	require.False(t, r.exists(t, v1.KindCatalogService, "lake"))
}

// scenario: nested-catalog-has-no-root
func TestScenarioNestedCatalogHasNoRoot(t *testing.T) {
	t.Parallel()
	r := startCatalogRig(t)
	nested := lakeOn("lakehouse", ownLakehouse).Spec

	fn := lakeFunction("default")
	fn.Name = "reader"
	fn.Spec.Buckets = []v1.BucketEntry{{Name: "lakehouse", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "lake", Owner: "lake"}}}}}
	fn.Spec.CatalogServices = []v1.CatalogEntry{{Name: "lake", CatalogServiceSpec: nested}}
	require.ErrorContains(t, r.apply(t, fn),
		"spec.catalogs[0].buckets: a catalog nested in a Function declares nothing at its root; declare it at the Function's root")

	app := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp}}
	app.Name, app.Namespace, app.ResourceGroup = "lakes", "default", "rg1"
	app.Spec.Buckets = fn.Spec.Buckets
	app.Spec.Catalogs = fn.Spec.CatalogServices
	require.ErrorContains(t, r.apply(t, app),
		"spec.catalogs[0].buckets: a catalog nested in an App declares nothing at its root; declare it at the App's root")
}
