package lake_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/edge/router"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/route"
	"github.com/pyvvo/funcd/internal/secrets"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/freeport"
)

// fakeProvider is a provider.Runtime double: it records the ProviderSpec it was Converge'd with and
// the refs it was asked to Teardown, so a test can assert what the reconciler assembled WITHOUT a
// real container runtime. It NEVER creates a Function (proving the engine is not a backing Function).
type fakeProvider struct {
	converged []provider.ProviderSpec
	tornDown  []provider.ProviderRef
	status    provider.ProviderStatus
	err       error
}

func (f *fakeProvider) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	f.converged = append(f.converged, spec)
	if f.err != nil {
		return provider.ProviderStatus{}, f.err
	}
	return f.status, nil
}

func (f *fakeProvider) Teardown(_ context.Context, ref provider.ProviderRef) error {
	f.tornDown = append(f.tornDown, ref)
	return nil
}

func (f *fakeProvider) lastSpec(t *testing.T) provider.ProviderSpec {
	t.Helper()
	require.NotEmpty(t, f.converged, "Converge was never called")
	return f.converged[len(f.converged)-1]
}

// fakeSecrets is a lakesvc.SecretResolver double resolving any named Secret to a fixed env map.
type fakeSecrets struct {
	env map[string]string
	err error
}

func (f fakeSecrets) ResolveEnv(_ context.Context, _ auth.Identity, _ v1.NamespaceName, _ []string) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.env, nil
}

// mkLake builds a VALID Lake: the lake (lakehouse/gold) is always present as
// a spec.blob binding (Lake.Validate requires the lake prefix to be a declared blob
// binding so the engine gets the per-fn S3 keypair). Extra blob bindings the caller passes are
// appended; a caller-supplied {lakehouse,gold} binding is not duplicated.
func mkLake(name string, blob ...v1.FunctionBlob) *v1.Lake {
	cs := &v1.Lake{}
	cs.TypeMeta = v1.TypeMeta{APIVersion: v1.KindLake.GVK().APIVersion(), Kind: v1.KindLake}
	cs.Name, cs.Namespace, cs.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cs.Spec.Location = v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	hasLocationBinding := false
	for _, b := range blob {
		if b.Bucket == cs.Spec.Location.Bucket && b.Prefix == cs.Spec.Location.Prefix {
			hasLocationBinding = true
			break
		}
	}
	if !hasLocationBinding {
		cs.Spec.Bindings.Blob = append(cs.Spec.Bindings.Blob, v1.FunctionBlob{Alias: "lake", Bucket: "lakehouse", Prefix: "gold"})
	}
	cs.Spec.Bindings.Blob = append(cs.Spec.Bindings.Blob, blob...)
	return cs
}

// engineIdentity is the engine identity kept in status.Function (repointed from a backing Function):
// "<cs-name>-duckdb".
func engineIdentity(csName string) v1.ObjectName { return v1.ObjectName(csName + "-duckdb") }

// newReconciler builds a reconciler over the fake provider (+ optional deps tweaks).
func newReconciler(t *testing.T, st store.Store, prov provider.Runtime, opts func(*lakesvc.ReconcilerDeps)) *lakesvc.Reconciler {
	t.Helper()
	d := lakesvc.ReconcilerDeps{
		Store:    st,
		Provider: prov,
		ImageFor: func(rt string) string { return "funcd/runtime-" + rt },
	}
	if opts != nil {
		opts(&d)
	}
	r, err := lakesvc.NewReconciler(d)
	require.NoError(t, err)
	return r
}

func reconcileOnce(t *testing.T, r *lakesvc.Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	require.NoError(t, err)
}

// seedLakeBucket stores the lakehouse Bucket (gold prefix) so ADR-0121's reconcile-time bucket-
// existence gate resolves and the reconciler proceeds past it to Converge. Owner is the lake itself
// (the single writer) — owner EXISTENCE is no longer admission-checked (ADR-0121), only the prefix must be
// present for the binding to resolve.
func seedLakeBucket(t *testing.T, st store.Store) {
	t.Helper()
	b := &v1.Bucket{}
	b.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	b.Name, b.Namespace, b.ResourceGroup = "lakehouse", "default", "rg1"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "gold", Owner: v1.FunctionOwner("lake")}}
	_, err := st.Create(context.Background(), b)
	require.NoError(t, err)
}

// scenario: lake-uses-provider-runtime — a Lake reconciles into a call to the
// provider-runtime's Converge with a ProviderSpec (image=duckdb, port=8080, readiness GET / 200,
// pinned single replica, the lake s3:// key + FUNCD_QUACK_PORT in env). NO backing Function is
// created in the store (the engine is NOT a Function).
func TestReconcile_lake_uses_provider_runtime(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.Derive = func(kind v1.Kind, _, name string) (string, string) {
			return "AKIA-" + string(kind) + "-" + name, "secret-" + name
		}
		d.S3Endpoint = "http://10.63.0.1:9000"
	})
	_, err := st.Create(ctx, mkLake("lake",
		v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	spec := prov.lastSpec(t)
	require.Equal(t, provider.ProviderRef{Namespace: "default", Name: "lake"}, spec.Ref, "provider identity is the Lake (ns, name)")
	require.Equal(t, "funcd/runtime-duckdb", spec.Image, "the curated duckdb engine image")
	require.Equal(t, 8080, spec.Port, "the Quack serving port")
	require.Equal(t, provider.ReadinessProbe{Path: "/", ExpectStatus: 200}, spec.Readiness, "the Quack HTTP readiness probe (not the funcd shim's)")
	require.Equal(t, 1, spec.Replicas, "pinned single writer")
	require.Nil(t, spec.Route, "internal-only in V1 — no ingress route programmed")

	// env: the ADR-0085 keypair (derived over the provider identity) + lake key + quack port.
	require.NotContains(t, spec.Env, "AWS_ACCESS_KEY_ID", "the key pair is issued with the engine, not in its spec (ADR-0270)")
	require.NotNil(t, spec.IssueEnv)
	issued := spec.IssueEnv()
	require.Equal(t, "AKIA-Lake-lake", issued["AWS_ACCESS_KEY_ID"], "the engine key is derived over its Lake identity")
	require.Equal(t, "secret-lake", issued["AWS_SECRET_ACCESS_KEY"])
	require.Equal(t, "us-east-1", spec.Env["AWS_REGION"])
	require.Equal(t, "http://10.63.0.1:9000", spec.Env["AWS_ENDPOINT_URL_S3"])
	require.Equal(t, "s3://lakehouse/gold/_ducklake/catalog.db", spec.Env["FUNCD_DUCKLAKE_CATALOG"])
	require.Equal(t, "8080", spec.Env["FUNCD_QUACK_PORT"])

	// NO backing Function exists — the engine is not a Function (scenario: provider-not-a-function).
	_, gerr := st.Get(ctx, v1.KindFunction.GVK(), "default", engineIdentity("lake"))
	require.Error(t, gerr, "the provider-runtime deploys the engine — NO backing Function is created")

	// status.Function is kept, repointed at the engine identity; status reflects the provider.
	csObj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.Lake)
	require.Equal(t, engineIdentity("lake"), cs.Status.Function)
}

// scenario: provider-bindings-injected (ADR-0087) and lake-service-bindings (ADR-0246) — spec.bindings.secret
// (QUACK_TOKEN) + spec.bindings.config (DUCKDB_*) appear in the ProviderSpec.Env merged from the resolved Secret +
// ConfigMap Data, as spec.secrets and spec.config did.
func TestScenario_lake_service_bindings(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}

	// a ConfigMap carrying DUCKDB_* engine tuning.
	cm := &v1.ConfigMap{}
	cm.TypeMeta = v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}
	cm.Name, cm.Namespace, cm.ResourceGroup = "lake-engine-config", "default", "rg1"
	cm.Spec.Data = map[string]string{"DUCKDB_MEMORY_LIMIT": "3GB", "DUCKDB_THREADS": "2"}
	_, err := st.Create(ctx, cm)
	require.NoError(t, err)

	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.Secrets = fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
	})
	cs := mkLake("lake", v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"})
	cs.Spec.Bindings.Secret = []v1.ObjectName{"lake-quack-token"}
	cs.Spec.Bindings.Config = []v1.ObjectName{"lake-engine-config"}
	_, err = st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	env := prov.lastSpec(t).Env
	require.Equal(t, "change-me", env["QUACK_TOKEN"], "the resolved Secret Data (the Quack token) is in the engine env")
	require.Equal(t, "3GB", env["DUCKDB_MEMORY_LIMIT"], "the resolved ConfigMap Data (engine tuning) is in the engine env")
	require.Equal(t, "2", env["DUCKDB_THREADS"])
}

// scenario: save-interval-from-config (ADR-0274) — lake.saveInterval reaches every engine as its own
// FUNCD_LAKE_SAVE_INTERVAL, which a bound ConfigMap cannot replace; without it the engine gets no key.
func TestScenarioSaveIntervalFromConfig(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		interval time.Duration
		want     string
	}{{3 * time.Second, "3s"}, {1500 * time.Millisecond, "1s500ms"}, {0, ""}} {
		st := store.New(storemem.New())
		seedLakeBucket(t, st)
		prov := &fakeProvider{}
		cm := &v1.ConfigMap{}
		cm.TypeMeta = v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}
		cm.Name, cm.Namespace, cm.ResourceGroup = "lake-engine-config", "default", "rg1"
		cm.Spec.Data = map[string]string{"FUNCD_LAKE_SAVE_INTERVAL": "1h", "LAKE_SAVE_INTERVAL": "1h"}
		_, err := st.Create(ctx, cm)
		require.NoError(t, err)

		r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
			d.Secrets = fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
			d.SaveInterval = tc.interval
		})
		cs := mkLake("lake", v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"})
		cs.Spec.Bindings.Secret = []v1.ObjectName{"lake-quack-token"}
		cs.Spec.Bindings.Config = []v1.ObjectName{"lake-engine-config"}
		_, err = st.Create(ctx, cs)
		require.NoError(t, err)

		reconcileOnce(t, r, "lake")

		env := prov.lastSpec(t).Env
		require.Equal(t, tc.want, env["FUNCD_LAKE_SAVE_INTERVAL"], tc.interval)
		require.NotContains(t, env, "LAKE_SAVE_INTERVAL", "a bound key the engine does not read is dropped")
	}
}

// TestEngineEnvOwnKeysWinOverBoundData: a bound ConfigMap or Secret cannot replace the engine's own S3 endpoint,
// region or key pair, and only the keys the engine reads (QUACK_TOKEN, DUCKDB_*) come through.
func TestEngineEnvOwnKeysWinOverBoundData(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{}

	cm := &v1.ConfigMap{}
	cm.TypeMeta = v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap}
	cm.Name, cm.Namespace, cm.ResourceGroup = "lake-engine-config", "default", "rg1"
	cm.Spec.Data = map[string]string{
		"AWS_ENDPOINT_URL":           "http://elsewhere.invalid:9000",
		"AWS_ENDPOINT_URL_S3":        "http://elsewhere.invalid:9000",
		"AWS_REGION":                 "elsewhere-1",
		"DUCKDB_THREADS":             "2",
		"HTTP_PROXY":                 "http://elsewhere.invalid:3128",
		"PYTHONPATH":                 "/tmp/elsewhere",
		"DUCKDB_EXTENSION_DIRECTORY": "/tmp/elsewhere",
	}
	_, err := st.Create(ctx, cm)
	require.NoError(t, err)

	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.Derive = func(v1.Kind, string, string) (string, string) { return "OWN-ACCESS", "own-secret" }
		d.S3Endpoint = "http://10.63.0.1:9000"
		d.Secrets = fakeSecrets{env: map[string]string{
			"AWS_ACCESS_KEY_ID": "BOUND-ACCESS", "AWS_SECRET_ACCESS_KEY": "bound-secret", "QUACK_TOKEN": "tok-1", "HTTPS_PROXY": "http://elsewhere.invalid:3128",
		}}
	})
	cs := mkLake("lake", v1.FunctionBlob{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"})
	cs.Spec.Bindings.Secret = []v1.ObjectName{"lake-quack-token"}
	cs.Spec.Bindings.Config = []v1.ObjectName{"lake-engine-config"}
	_, err = st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	spec := prov.lastSpec(t)
	require.NotContains(t, spec.Env, "AWS_ACCESS_KEY_ID", "the bound pair is dropped; the engine's own is issued with it (ADR-0270)")
	require.NotNil(t, spec.IssueEnv)
	env := maps.Clone(spec.Env) // the engine's env at create: its spec's env with the issued pair laid over it
	maps.Copy(env, spec.IssueEnv())
	require.Equal(t, "http://10.63.0.1:9000", env["AWS_ENDPOINT_URL"])
	require.Equal(t, "http://10.63.0.1:9000", env["AWS_ENDPOINT_URL_S3"])
	require.Equal(t, "us-east-1", env["AWS_REGION"])
	require.Equal(t, "OWN-ACCESS", env["AWS_ACCESS_KEY_ID"])
	require.Equal(t, "own-secret", env["AWS_SECRET_ACCESS_KEY"])
	require.Equal(t, "2", env["DUCKDB_THREADS"], "a DUCKDB_ key comes through")
	require.Equal(t, "tok-1", env["QUACK_TOKEN"], "the Quack token comes through")
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "PYTHONPATH", "DUCKDB_EXTENSION_DIRECTORY"} {
		require.NotContains(t, env, k, "a bound key the engine does not read is dropped")
	}
}

// scenario: provider-pinned-single-writer — the assembled ProviderSpec is pinned at exactly one
// replica (no scale-to-zero).
func TestReconcile_provider_pinned_single_writer(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")
	require.Equal(t, 1, prov.lastSpec(t).Replicas, "pinned single replica (no scale-to-zero)")
}

// scenario (Ready reflection) — when the provider reports Ready, the Lake is Ready and its
// endpoint is the netns Address (internal-only, no ingress route).
func TestReconcile_ready_reflects_provider_status(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	csObj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.Lake)
	require.Equal(t, v1.PhaseReady, cs.Status.Phase)
	require.Equal(t, "10.63.0.7:8080", cs.Status.Endpoint, "internal-only ⇒ endpoint is the netns Address")
	cond, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, cond.Status)
}

// ADR-0199 Decision 5: every Ready the reconciler writes (engine Ready, engine not Ready, held for a missing
// Bucket) records the generation it observed, also after a spec change.
func TestReadyObservesGeneration(t *testing.T) {
	for name, tc := range map[string]struct {
		bucket bool
		status provider.ProviderStatus
		want   v1.ConditionStatus
	}{
		"engine ready":     {bucket: true, status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, want: v1.ConditionTrue},
		"engine not ready": {bucket: true, want: v1.ConditionFalse},
		"bucket missing":   {status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, want: v1.ConditionFalse},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(storemem.New())
			if tc.bucket {
				seedLakeBucket(t, st)
			}
			r := newReconciler(t, st, &fakeProvider{status: tc.status}, nil)
			_, err := st.Create(ctx, mkLake("lake"))
			require.NoError(t, err)
			get := func() *v1.Lake {
				obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.Lake)
			}
			reconcileOnce(t, r, "lake")
			cond, ok := get().Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, int64(1), cond.ObservedGeneration)

			cs := get()
			cs.Spec.Resources.Memory = "4Gi"
			_, err = st.Update(ctx, cs)
			require.NoError(t, err)
			reconcileOnce(t, r, "lake")
			cs = get()
			require.Equal(t, int64(2), cs.Generation)
			cond, ok = cs.Status.Conditions.Get("Ready")
			require.True(t, ok)
			require.Equal(t, tc.want, cond.Status)
			require.Equal(t, cs.Generation, cond.ObservedGeneration)
		})
	}
}

// scenario (ADR-0137 internal enforcement) — when a Manager is wired and the provider reports Ready,
// the reconciler Ensures a node-private lake PEP proxy fronting the engine and publishes the PROXY
// address (bare 127.0.0.1:<port> — the function wraps it in quack://) as Status.Endpoint — internal
// functions inject the proxy, not the engine. A delete then Removes the proxy.
func TestReconcile_ready_publishes_proxy_endpoint(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	csObj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.Lake)
	require.Equal(t, v1.PhaseReady, cs.Status.Phase)
	require.True(t, strings.HasPrefix(cs.Status.Endpoint, "127.0.0.1:"),
		"Status.Endpoint is the node-private lake PEP proxy address (bare host:port), got %q", cs.Status.Endpoint)

	// delete → the proxy is Removed (idempotent; no assertion needed beyond no panic/error).
	require.NoError(t, st.Delete(ctx, v1.KindLake.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
}

// scenario: lake-engine-move-keeps-endpoint — the engine comes back on a new netns IP (a crashed
// instance re-converged): the reconciler re-Ensures the proxy with the new upstream, and Status.Endpoint,
// the URL consumers were injected with, stays the same: a running worker keeps the env it started with.
func TestReconcile_engine_move_keeps_proxy_endpoint(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	endpoint := func() string {
		obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.Lake).Status.Endpoint
	}

	reconcileOnce(t, r, "lake")
	before := endpoint()
	require.NotEmpty(t, before)

	prov.status.Address = "10.63.0.9:8080"
	reconcileOnce(t, r, "lake")
	require.Equal(t, before, endpoint(), "the engine moved, but the endpoint consumers hold is unchanged")
}

// scenario: crashed-lake-engine-restarts (ADR-0142) — a Ready lake asks to run again after the supervision
// period, with no write; that pass converges the engine the provider recreates after a crash — first starting, then
// Ready on a new address — and the endpoint consumers were injected with stays the same.
func TestScenarioCrashedLakeEngineRestarts(t *testing.T) {
	const period = 50 * time.Millisecond
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.SupervisionPeriod = period
	})
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}
	get := func() *v1.Lake {
		obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.Lake)
	}

	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, period, res.RequeueAfter, "a Ready lake comes back after the supervision period")
	proxyURL := get().Status.Endpoint

	prov.status = provider.ProviderStatus{Running: 1, Ready: false, Address: "10.63.0.9:8080"}
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhasePending, get().Status.Phase, "the recreated engine is still starting")
	require.Equal(t, period, res.RequeueAfter, "the engine wait is min(enginePollInterval, supervisionPeriod) (ADR-0163 Decision 7)")

	prov.status.Ready = true
	res, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, prov.converged, 3, "every pass converged the engine")
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
	require.Equal(t, period, res.RequeueAfter)
}

// TestIssue104_ConvergeErrorMarksLakeNotReady: a Ready lake whose engine cannot be recreated (Converge fails)
// is reported not Ready, drops the endpoint of the dead engine and retracts its edge route, while the pass still
// fails so the controller backs off; once Converge succeeds it is Ready again on the same proxy URL.
func TestIssue104_ConvergeErrorMarksLakeNotReady(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	routes := newRecordingRoutes()
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.Routes = routes
	})
	cs := mkLake("lake")
	cs.Spec.Ingress = &v1.Ingress{Enabled: new(true), Path: "/lake/lake"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}
	get := func() *v1.Lake {
		obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
		require.NoError(t, gerr)
		return obj.(*v1.Lake)
	}

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	proxyURL := get().Status.Endpoint
	require.Len(t, routes.get(lakeRouteSource), 1)

	prov.err = errors.New("create engine replica 0: pull image: not found")
	_, err = r.Reconcile(ctx, req)
	require.Error(t, err, "a failed converge still fails the pass, so the controller backs off")
	got := get()
	require.Equal(t, v1.PhasePending, got.Status.Phase, "an engine that cannot be recreated is not Ready")
	cond, ok := got.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "EngineConvergeFailed", cond.Reason)
	require.NotEqual(t, proxyURL, got.Status.Endpoint, "the endpoint of the dead engine is no longer published")
	require.Empty(t, routes.get(lakeRouteSource), "the edge route is retracted while the engine is down")

	prov.err = nil
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, v1.PhaseReady, get().Status.Phase)
	require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
}

// TestIssue372_NotReadyGateStopsServing: a Ready lake that loses its Bucket or a bound Secret stops its engine,
// unpublishes its endpoint and stops forwarding through the proxy consumers were injected with; once the binding is
// back it is Ready again on the same proxy URL (#59).
func TestIssue372_NotReadyGateStopsServing(t *testing.T) {
	cases := []struct {
		name   string
		unbind func(t *testing.T, st store.Store, secrets *fakeSecrets)
		rebind func(t *testing.T, st store.Store, secrets *fakeSecrets)
	}{
		{
			name: "bucket-deleted",
			unbind: func(t *testing.T, st store.Store, _ *fakeSecrets) {
				require.NoError(t, st.Delete(context.Background(), v1.KindBucket.GVK(), "default", "lakehouse", ""))
			},
			rebind: func(t *testing.T, st store.Store, _ *fakeSecrets) { seedLakeBucket(t, st) },
		},
		{
			name: "secret-deleted",
			unbind: func(_ *testing.T, _ store.Store, secrets *fakeSecrets) {
				secrets.err = errors.New(`secret "quack" not found`)
			},
			rebind: func(_ *testing.T, _ store.Store, secrets *fakeSecrets) { secrets.err = nil },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var engineHits atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				engineHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(engine.Close)
			st := store.New(storemem.New())
			seedLakeBucket(t, st)
			secrets := &fakeSecrets{env: map[string]string{"QUACK_TOKEN": "change-me"}}
			mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
			t.Cleanup(mgr.Shutdown)
			prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(engine.URL, "http://")}}
			r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
				d.Secrets = secrets
				d.Proxy = mgr
			})
			cs := mkLake("lake")
			cs.Spec.Bindings.Secret = []v1.ObjectName{"quack"}
			_, err := st.Create(ctx, cs)
			require.NoError(t, err)
			get := func() *v1.Lake {
				obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.Lake)
			}
			query := func(addr string) int {
				resp, qerr := http.Get("http://" + addr + "/")
				require.NoError(t, qerr)
				require.NoError(t, resp.Body.Close())
				return resp.StatusCode
			}

			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			proxyURL := get().Status.Endpoint
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(1), engineHits.Load())

			tc.unbind(t, st, secrets)
			reconcileOnce(t, r, "lake")
			got := get()
			require.Equal(t, v1.PhasePending, got.Status.Phase)
			require.Empty(t, got.Status.Endpoint, "a not-Ready lake publishes no endpoint")
			require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown, "the engine is stopped")
			require.Equal(t, http.StatusServiceUnavailable, query(proxyURL), "the proxy consumers hold no longer forwards")
			require.Equal(t, int32(1), engineHits.Load(), "no query reached the engine")

			tc.rebind(t, st, secrets)
			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(2), engineHits.Load())
		})
	}
}

// scenario: provider-torn-down (delete path) — a deleted (absent) Lake tears the engine
// down via the provider-runtime; it is idempotent (a missing engine is fine).
func TestReconcile_delete_tears_down_provider(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	reconcileOnce(t, r, "lake")

	// delete the Lake, then reconcile — the engine is torn down.
	require.NoError(t, st.Delete(ctx, v1.KindLake.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
	require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown,
		"a deleted Lake tears the engine down via the provider-runtime")

	// a second delete-reconcile is a no-op-safe (idempotent — Teardown called again, no error).
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.tornDown, 2)
}

// scenario (fail-closed) — a Lake declaring spec.secrets with NO resolver wired holds the
// service not-Ready (BindingResolveFailed) and does NOT converge the engine.
func TestReconcile_secrets_without_resolver_fails_closed(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st) // ADR-0121: reconcile-time bucket-existence gate needs the bound Bucket present
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil) // no Secrets resolver
	cs := mkLake("lake")
	cs.Spec.Bindings.Secret = []v1.ObjectName{"lake-quack-token"}
	_, err := st.Create(ctx, cs)
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")

	require.Empty(t, prov.converged, "no engine is converged when a declared secret can't be resolved")
	csObj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	got := csObj.(*v1.Lake)
	require.Equal(t, v1.PhasePending, got.Status.Phase)
	cond, ok := got.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BindingResolveFailed", cond.Reason)
}

// scenario: lake-waits-for-bucket (ADR-0121) — a Lake binding a not-yet-applied Bucket is
// ADMITTED and held not-Ready (BucketNotFound), converging its engine only once the Bucket exists. This is
// the reconcile-time replacement for the removed lake-blob-validity admission (no synchronous reject).
func TestReconcile_waits_for_bucket(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New()) // deliberately NOT seeding the bucket — the gate must catch its absence
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, nil)
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)

	reconcileOnce(t, r, "lake")
	require.Empty(t, prov.converged, "no engine converges while the bound Bucket is absent")
	csObj, err := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cs := csObj.(*v1.Lake)
	require.Equal(t, v1.PhasePending, cs.Status.Phase)
	cond, ok := cs.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionFalse, cond.Status)
	require.Equal(t, "BucketNotFound", cond.Reason)

	// apply the Bucket → the next reconcile resolves the gate and converges the engine (convergence).
	seedLakeBucket(t, st)
	reconcileOnce(t, r, "lake")
	require.NotEmpty(t, prov.converged, "once the Bucket exists the engine converges")
}

// A Lake applied before the ConfigMap or Secret it binds is held not-Ready and requeued, so its engine
// converges once the binding exists: no ConfigMap or Secret event reconciles the Lake again (issue #77).
func TestIssue77_MissingBindingRecoversWhenApplied(t *testing.T) {
	t.Parallel()
	for _, kind := range []v1.Kind{v1.KindConfigMap, v1.KindSecret} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			st := store.New(storemem.New())
			seedLakeBucket(t, st)
			prov := &fakeProvider{}
			sr, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: rbac.New()})
			require.NoError(t, err)
			r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Secrets = sr })
			cs := mkLake("lake")
			if kind == v1.KindConfigMap {
				cs.Spec.Bindings.Config = []v1.ObjectName{"late"}
			} else {
				cs.Spec.Bindings.Secret = []v1.ObjectName{"late"}
			}
			_, err = st.Create(ctx, cs)
			require.NoError(t, err)

			res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
			require.NoError(t, err)
			require.Empty(t, prov.converged, "no engine converges while a bound %s is absent", kind)
			require.Positive(t, res.RequeueAfter, "a missing binding requeues the Lake")

			binding, ok := v1.NewObject(kind)
			require.True(t, ok)
			meta := binding.GetObjectMeta()
			meta.Name, meta.Namespace, meta.ResourceGroup = "late", "default", "rg1"
			_, err = st.Create(ctx, binding)
			require.NoError(t, err)

			reconcileOnce(t, r, "lake")
			require.NotEmpty(t, prov.converged, "the engine converges once its binding exists")
		})
	}
}

// DEFERRED node-gated scenarios (ADR-0086/0087): the live-DuckDB scenarios need the native DuckDB
// image (a separate process) on real containerd, so they run on the homebox/Lima FUNCD_IT=1 lane
// (the ADR-0080 s3gateway precedent), NOT this in-process suite:
//   - query-over-quack              — a Quack SELECT reads bound Parquet via the F47 S3 surface.
//   - write-creates-ducklake-snapshot — an INSERT writes Parquet + a DuckLake catalog snapshot on blob.
//   - tenant-isolation              — A's endpoint reading B's bucket → 403 (the F47 keypair, cryptographic).
//   - arbitrary-url-confined        — a non-funcd URL is refused by the shim lockdown.
//   - lake-persists-across-restart — the lake (loaded from blob) survives a replica restart.

// edgeHarness is a store, a real edge aggregator and router, the Route reconciler and a lake reconciler with
// a Ready engine behind a real PEP proxy; NotifyFunc records owners so run can re-run them as the controller does.
type edgeHarness struct {
	st       store.Store
	rtr      router.Router
	prov     *fakeProvider
	routes   *route.Reconciler
	lakes    *lakesvc.Reconciler
	notified []router.Owner
}

func newEdgeHarness(t *testing.T, modes router.ModeFunc) *edgeHarness {
	t.Helper()
	h := &edgeHarness{st: store.New(storemem.New()), rtr: router.New()}
	agg := router.NewAggregator(h.rtr, modes, func(o router.Owner) { h.notified = append(h.notified, o) }, "", nil)
	var err error
	h.routes, err = route.NewReconciler(route.Deps{Store: h.st, Routes: agg})
	require.NoError(t, err)
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, h.st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	h.prov = &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	h.lakes = newReconciler(t, h.st, h.prov, func(d *lakesvc.ReconcilerDeps) {
		d.Proxy = mgr
		d.Routes = agg
	})
	seedLakeBucket(t, h.st)
	return h
}

// run reconciles req, then every owner the aggregator asked to re-run, until none is left.
func (h *edgeHarness) run(t *testing.T, req controller.Request) {
	t.Helper()
	queue := []controller.Request{req}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		var err error
		if r.GVK == v1.KindRoute.GVK() {
			_, err = h.routes.Reconcile(context.Background(), r)
		} else {
			_, err = h.lakes.Reconcile(context.Background(), r)
		}
		require.NoError(t, err)
		for _, o := range h.notified {
			queue = append(queue, controller.Request{GVK: o.Kind.GVK(), Namespace: o.Namespace, Name: o.Name})
		}
		h.notified = nil
	}
}

func (h *edgeHarness) condition(t *testing.T, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, ct v1.ConditionType) v1.Condition {
	t.Helper()
	obj, err := h.st.Get(context.Background(), gvk, ns, name)
	require.NoError(t, err)
	var conds v1.Conditions
	switch o := obj.(type) {
	case *v1.Route:
		conds = o.Status.Conditions
	case *v1.Lake:
		conds = o.Status.Conditions
	}
	c, ok := conds.Get(ct)
	require.True(t, ok, "%s %s/%s has %s", gvk.Kind, ns, name, ct)
	return c
}

// TestScenario_lake_loses_to_earlier_route covers scenario: lake-loses-to-earlier-route — Route a/r and
// Lake default/lake on (h, /q), reconciled in either order: the Route serves the claim and is Ready, the
// lake reports IngressReady=False (RouteConflict naming Route a/r) and stays Ready.
func TestScenario_lake_loses_to_earlier_route(t *testing.T) {
	routeReq := controller.Request{GVK: v1.KindRoute.GVK(), Namespace: "a", Name: "r"}
	lakeReq := controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}
	for name, order := range map[string][]controller.Request{
		"route-first": {routeReq, lakeReq},
		"lake-first":  {lakeReq, routeReq},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := newEdgeHarness(t, nil)
			fn := &v1.Function{}
			fn.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
			fn.Name, fn.Namespace, fn.ResourceGroup = "fn", "a", "rg1"
			fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
			_, err := h.st.Create(ctx, fn)
			require.NoError(t, err)
			rt := &v1.Route{}
			rt.TypeMeta = v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}
			rt.Name, rt.Namespace, rt.ResourceGroup = "r", "a", "rg1"
			rt.Spec = v1.RouteSpec{Host: "h", Rules: []v1.RouteRule{{Path: "/q", Backend: v1.RouteBackend{Function: "fn"}}}}
			_, err = h.st.Create(ctx, rt)
			require.NoError(t, err)
			cs := mkLake("lake")
			cs.Spec.Ingress = &v1.Ingress{Enabled: new(true), Host: "h", Path: "/q"}
			_, err = h.st.Create(ctx, cs)
			require.NoError(t, err)

			for _, req := range order {
				h.run(t, req)
			}

			m, ok := h.rtr.Resolve("h", "/q", "GET")
			require.True(t, ok)
			require.Equal(t, v1.NamespaceName("a"), m.Namespace)
			require.Equal(t, v1.ObjectName("fn"), m.Function, "/q on h reaches a/r's Function")
			require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindRoute.GVK(), "a", "r", "Ready").Status)
			ing := h.condition(t, v1.KindLake.GVK(), "default", "lake", "IngressReady")
			require.Equal(t, v1.ConditionFalse, ing.Status)
			require.Equal(t, "RouteConflict", ing.Reason)
			require.Contains(t, ing.Message, "Route a/r")
			require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindLake.GVK(), "default", "lake", "Ready").Status)
		})
	}
}

// TestScenario_lake_host_required_explicit covers scenario: lake-host-required-explicit — a host-less
// lake ingress in an explicit-mode namespace gets no edge entry and IngressReady=False (HostRequired), while
// the lake stays Ready and its engine keeps serving the functions bound to it.
func TestScenario_lake_host_required_explicit(t *testing.T) {
	ctx := context.Background()
	h := newEdgeHarness(t, func(context.Context, v1.NamespaceName) (v1.ExposureMode, error) {
		return v1.ExposureExplicit, nil
	})
	h.run(t, controller.Request{GVK: v1.KindRoute.GVK()})
	cs := mkLake("lake")
	cs.Spec.Ingress = &v1.Ingress{Enabled: new(true), Path: "/q"}
	_, err := h.st.Create(ctx, cs)
	require.NoError(t, err)

	h.run(t, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})

	_, ok := h.rtr.Resolve("any", "/q", "GET")
	require.False(t, ok, "no edge entry exists for the lake")
	ing := h.condition(t, v1.KindLake.GVK(), "default", "lake", "IngressReady")
	require.Equal(t, v1.ConditionFalse, ing.Status)
	require.Equal(t, "HostRequired", ing.Reason)
	require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindLake.GVK(), "default", "lake", "Ready").Status)
	obj, err := h.st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	endpoint := obj.(*v1.Lake).Status.Endpoint
	require.NotEmpty(t, endpoint, "spec.lakes consumers still get the proxy endpoint")
	require.NotEqual(t, "10.63.0.7:8080", endpoint, "the endpoint is the PEP proxy, not the engine")
	require.Empty(t, h.prov.tornDown, "the engine is not torn down")
}

// storeFunction stores Function name in ns default with change applied; processed marks its spec processed.
func storeFunction(t *testing.T, st store.Store, name string, processed bool, change func(*v1.Function)) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindFunction)
	require.True(t, ok)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Replicas = 1
	fn.Spec.Runtime = "nodejs22"
	fn.Spec.Handler = "handle"
	fn.Spec.Image = "file:///handler.mjs"
	if processed {
		fn.Status.ObservedGeneration = 1
	}
	if change != nil {
		change(fn)
	}
	_, err := st.Create(context.Background(), fn)
	require.NoError(t, err)
}

func bindsLake(fn *v1.Function) {
	fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "lake", Lake: "lake"}}
}

// TestReconcileRecordsProxyPort: every pass with the proxy wired records the listener's port before its branch writes
// the status, and a new run binds that port again. Not parallel, nor its subtests: a port bound in parallel could take
// the recorded port between one run's Shutdown and the next run's rebind. The first run's listener is bound on a
// freeport port before the pass, as another test process's :0 bind could take an ephemeral one in between (#758).
func TestReconcileRecordsProxyPort(t *testing.T) {
	cases := map[string]struct {
		status provider.ProviderStatus
		bucket bool
		phase  v1.Phase
	}{
		"ready":            {status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}, bucket: true, phase: v1.PhaseReady},
		"engine-not-ready": {status: provider.ProviderStatus{Running: 1, Address: "10.63.0.7:8080"}, bucket: true, phase: v1.PhasePending},
		"hold-not-ready":   {phase: v1.PhasePending},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(storemem.New())
			if tc.bucket {
				seedLakeBucket(t, st)
			}
			_, err := st.Create(ctx, mkLake("lake"))
			require.NoError(t, err)
			port := freeport.Port(t)
			for run := range 2 {
				mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
				if run == 0 {
					_, _, err = mgr.Listen("default", "lake", port)
					require.NoError(t, err)
				}
				r := newReconciler(t, st, &fakeProvider{status: tc.status}, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
				reconcileOnce(t, r, "lake")
				obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
				require.NoError(t, gerr)
				cs := obj.(*v1.Lake)
				require.Equal(t, tc.phase, cs.Status.Phase)
				require.Equal(t, port, cs.Status.ProxyPort, "the pass records the listener's port; a new run binds it again")
				url, ok := mgr.ProxyURL("default", "lake")
				require.True(t, ok)
				require.Equal(t, "127.0.0.1:"+strconv.Itoa(cs.Status.ProxyPort), url)
				mgr.Shutdown()
			}
		})
	}
}

// failingFunctionList fails every List of Functions.
type failingFunctionList struct{ store.Store }

func (s failingFunctionList) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	if gvk == v1.KindFunction.GVK() {
		return store.List{}, errors.New("list failed")
	}
	return s.Store.List(ctx, gvk, opts)
}

// TestReconcileDeleteKeepsBoundListener: a deleted lake's listener is released, not closed, while a Function binds
// the lake or the bound-Functions check fails; it closes once none binds it.
func TestReconcileDeleteKeepsBoundListener(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	prov := &fakeProvider{status: provider.ProviderStatus{Running: 1, Ready: true, Address: "10.63.0.7:8080"}}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	storeFunction(t, st, "reader", true, bindsLake)
	reconcileOnce(t, r, "lake")
	url, ok := mgr.ProxyURL("default", "lake")
	require.True(t, ok)

	require.NoError(t, st.Delete(ctx, v1.KindLake.GVK(), "default", "lake", ""))
	reconcileOnce(t, r, "lake")
	got, ok := mgr.ProxyURL("default", "lake")
	require.True(t, ok, "reader binds lake, so its listener stays")
	require.Equal(t, url, got)
	require.Equal(t, []v1.ObjectName{"lake"}, mgr.Released("default"))
	require.Equal(t, []provider.ProviderRef{{Namespace: "default", Name: "lake"}}, prov.tornDown, "the engine is torn down")

	require.NoError(t, st.Delete(ctx, v1.KindFunction.GVK(), "default", "reader", ""))
	failing := newReconciler(t, failingFunctionList{st}, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err = failing.Reconcile(ctx, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.Error(t, err, "a failed bound-Functions check fails the pass")
	_, ok = mgr.ProxyURL("default", "lake")
	require.True(t, ok, "and keeps the listener")

	reconcileOnce(t, r, "lake")
	_, ok = mgr.ProxyURL("default", "lake")
	require.False(t, ok, "no Function binds lake, so its listener closes")
}

// TestAnyFunctionBindsCountsSwitchingFunction: a Function binds a lake it names, and every lake of its
// namespace while a worker of an earlier spec may run.
func TestAnyFunctionBindsCountsSwitchingFunction(t *testing.T) {
	cases := map[string]struct {
		processed bool
		change    func(*v1.Function)
		want      bool
	}{
		"steady":      {processed: true, change: func(fn *v1.Function) { fn.Status.CurrentRevision, fn.Status.ServingRevision = "r1", "r1" }},
		"other":       {processed: true, change: func(fn *v1.Function) { fn.Spec.Bindings.Lake = []v1.FunctionLake{{Alias: "sea", Lake: "sea"}} }},
		"binds":       {processed: true, change: bindsLake, want: true},
		"unprocessed": {want: true},
		"switching":   {processed: true, change: func(fn *v1.Function) { fn.Status.CurrentRevision, fn.Status.ServingRevision = "r2", "r1" }, want: true},
		"draining":    {processed: true, change: func(fn *v1.Function) { fn.Status.DrainingRevision = "r1" }, want: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := store.New(storemem.New())
			storeFunction(t, st, "fn", tc.processed, tc.change)
			binds, err := lakesvc.AnyFunctionBinds(context.Background(), st, "default", "lake")
			require.NoError(t, err)
			require.Equal(t, tc.want, binds)
			binds, err = lakesvc.AnyFunctionBinds(context.Background(), st, "ops", "lake")
			require.NoError(t, err)
			require.False(t, binds, "only Functions of the lake's namespace count")
		})
	}
}

// TestMapFunctionMapsReleasedListeners: a Function event maps to the released listeners of its namespace only.
func TestMapFunctionMapsReleasedListeners(t *testing.T) {
	st := store.New(storemem.New())
	fn := &v1.Function{}
	fn.Name, fn.Namespace = "reader", "default"
	require.Nil(t, newReconciler(t, st, &fakeProvider{}, nil).MapFunction(context.Background(), fn), "no proxy wired maps nothing")

	mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	for _, ref := range []struct {
		ns   v1.NamespaceName
		name v1.ObjectName
	}{{"default", "lake"}, {"default", "kept"}, {"ops", "lake"}} {
		_, _, err := mgr.Listen(ref.ns, ref.name, 0)
		require.NoError(t, err)
	}
	mgr.Release("default", "lake")
	mgr.Release("ops", "lake")
	r := newReconciler(t, st, &fakeProvider{}, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	require.Equal(t, []controller.Request{{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}},
		r.MapFunction(context.Background(), fn))
}

// TestIssue716_NotReadyEngineStopsServing: a pass that finds the engine not Ready, whether it moved and is not
// probed Ready yet or Converge failed, suspends the proxy consumers hold, so it forwards nothing (and no engine
// token) to the previous engine until a Ready pass retargets it on the same URL.
func TestIssue716_NotReadyEngineStopsServing(t *testing.T) {
	cases := []struct {
		name     string
		notReady func(prov *fakeProvider)
		wantErr  bool
	}{
		{
			name: "engine-moved",
			notReady: func(prov *fakeProvider) {
				prov.status = provider.ProviderStatus{Running: 1, Ready: false, Address: "10.63.0.250:8080"}
			},
		},
		{
			name:     "converge-failed",
			notReady: func(prov *fakeProvider) { prov.err = errors.New("create engine replica 0: pull image: not found") },
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var engineHits atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				engineHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(engine.Close)
			engineReady := provider.ProviderStatus{Running: 1, Ready: true, Address: strings.TrimPrefix(engine.URL, "http://")}
			st := store.New(storemem.New())
			seedLakeBucket(t, st)
			mgr := lakegw.NewManager("", lakegw.NewLakeKeys(nil, st), nil, nil)
			t.Cleanup(mgr.Shutdown)
			prov := &fakeProvider{status: engineReady}
			r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
				d.Secrets = &fakeSecrets{env: map[string]string{"QUACK_TOKEN": "shared-engine-token"}}
				d.Proxy = mgr
			})
			cs := mkLake("lake")
			cs.Spec.Bindings.Secret = []v1.ObjectName{"quack"}
			_, err := st.Create(ctx, cs)
			require.NoError(t, err)
			req := controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"}
			get := func() *v1.Lake {
				obj, gerr := st.Get(ctx, v1.KindLake.GVK(), "default", "lake")
				require.NoError(t, gerr)
				return obj.(*v1.Lake)
			}
			query := func(addr string) int {
				resp, qerr := http.Get("http://" + addr + "/")
				require.NoError(t, qerr)
				require.NoError(t, resp.Body.Close())
				return resp.StatusCode
			}

			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			proxyURL := get().Status.Endpoint
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(1), engineHits.Load())

			tc.notReady(prov)
			_, err = r.Reconcile(ctx, req)
			require.Equal(t, tc.wantErr, err != nil, "reconcile error: %v", err)
			require.Equal(t, v1.PhasePending, get().Status.Phase)
			require.Equal(t, http.StatusServiceUnavailable, query(proxyURL), "the proxy consumers hold no longer forwards")
			require.Equal(t, int32(1), engineHits.Load(), "no query reached the previous engine")

			prov.status, prov.err = engineReady, nil
			reconcileOnce(t, r, "lake")
			require.Equal(t, v1.PhaseReady, get().Status.Phase)
			require.Equal(t, proxyURL, get().Status.Endpoint, "consumers keep the URL they were injected with")
			require.Equal(t, http.StatusOK, query(proxyURL))
			require.Equal(t, int32(2), engineHits.Load())
		})
	}
}

// ADR-0238: the proxies bind the bridge address, which appears only with the first container. A pass that cannot bind
// it yet still converges the engine, whose container creates the bridge, and binds on a later pass; failing the pass
// instead would never start the engine.
func TestReconcileConvergesBeforeTheBridgeAddressExists(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{status: provider.ProviderStatus{Reason: "Starting"}}
	mgr := lakegw.NewManager("192.0.2.1", lakegw.NewLakeKeys(nil, st), nil, nil)
	t.Cleanup(mgr.Shutdown)
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.Proxy = mgr })
	_, err := st.Create(ctx, mkLake("lake"))
	require.NoError(t, err)
	reconcileOnce(t, r, "lake")
	require.Len(t, prov.converged, 1, "the engine is converged though the proxy could not bind")
}

// TestScenario_exposure_lake_default_path covers scenario: exposure-lake (ADR-0241 Decision 9) — an ingress
// with enabled: true and no path answers at /lake/<name>; one that is not enabled is Disabled and
// IngressReady=False while the lake stays Ready.
func TestScenario_exposure_lake_default_path(t *testing.T) {
	ctx := context.Background()
	h := newEdgeHarness(t, nil)
	h.run(t, controller.Request{GVK: v1.KindRoute.GVK()})
	on, off := mkLake("bi"), mkLake("lake")
	on.Spec.Ingress = &v1.Ingress{Enabled: new(true)}
	off.Spec.Ingress = &v1.Ingress{}
	for _, cs := range []*v1.Lake{on, off} {
		_, err := h.st.Create(ctx, cs)
		require.NoError(t, err)
		h.run(t, controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: cs.Name})
	}

	m, ok := h.rtr.Resolve("any", "/lake/bi/q", "GET")
	require.True(t, ok)
	require.NotEmpty(t, m.Upstream, "the default path reaches the PEP proxy")
	require.Equal(t, "Programmed", h.condition(t, v1.KindLake.GVK(), "default", "bi", "IngressReady").Reason)

	_, ok = h.rtr.Resolve("any", "/lake/lake", "GET")
	require.False(t, ok)
	ing := h.condition(t, v1.KindLake.GVK(), "default", "lake", "IngressReady")
	require.Equal(t, v1.ConditionFalse, ing.Status)
	require.Equal(t, "Disabled", ing.Reason)
	require.Equal(t, v1.ConditionTrue, h.condition(t, v1.KindLake.GVK(), "default", "lake", "Ready").Status)
}

// scenario: switch-does-not-matter — the reconciler fences every engine to its S3 gateway, reading no egress switch,
// and refuses a gateway named by a hostname rather than start the engine unfenced (ADR-0269).
func TestScenarioLakeSetsEgressOnly(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		want     []netip.AddrPort
	}{
		{"http://10.63.0.1:9000", []netip.AddrPort{netip.MustParseAddrPort("10.63.0.1:9000")}},
		{"", []netip.AddrPort{}},
	} {
		st := store.New(storemem.New())
		seedLakeBucket(t, st)
		prov := &fakeProvider{}
		r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.S3Endpoint = tc.endpoint })
		_, err := st.Create(context.Background(), mkLake("lake"))
		require.NoError(t, err)
		reconcileOnce(t, r, "lake")
		require.NotNil(t, prov.lastSpec(t).EgressOnly, tc.endpoint)
		require.Equal(t, tc.want, prov.lastSpec(t).EgressOnly, tc.endpoint)
	}

	st := store.New(storemem.New())
	seedLakeBucket(t, st)
	prov := &fakeProvider{}
	r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) { d.S3Endpoint = "https://s3.example.com" })
	_, err := st.Create(context.Background(), mkLake("lake"))
	require.NoError(t, err)
	reconcileOnce(t, r, "lake")
	obj, err := st.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	cond, ok := obj.(*v1.Lake).Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, "GatewayEndpointNotAnAddress", cond.Reason)
}

// scenario: workers-renewed (ADR-0270) — a Lake whose engine carries a key pair asks Converge to replace the engine at
// its renewal age; with the S3 gateway off its engine carries no pair and no age.
func TestScenarioLakeEngineRenewalAge(t *testing.T) {
	for _, gateway := range []bool{true, false} {
		st := store.New(storemem.New())
		seedLakeBucket(t, st)
		prov := &fakeProvider{}
		r := newReconciler(t, st, prov, func(d *lakesvc.ReconcilerDeps) {
			if gateway {
				d.Derive = func(v1.Kind, string, string) (string, string) { return "AK", "SK" }
			}
			d.RenewalAge = func(kind v1.Kind, ns, name string) time.Duration {
				require.Equal(t, v1.KindLake, kind)
				require.Equal(t, "default/lake", ns+"/"+name)
				return 20 * time.Hour
			}
		})
		_, err := st.Create(context.Background(), mkLake("lake"))
		require.NoError(t, err)
		reconcileOnce(t, r, "lake")
		want := time.Duration(0)
		if gateway {
			want = 20 * time.Hour
		}
		require.Equal(t, want, prov.lastSpec(t).MaxAge, "gateway %v", gateway)
	}
}
