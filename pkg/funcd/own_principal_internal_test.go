package funcd

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// runningWorker seeds rt with a running worker of kind at ip.
func runningWorker(rt *recordingRuntime, ns v1.NamespaceName, name v1.ObjectName, kind v1.Kind, ip string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	id := runtime.InstanceID(string(ns) + "/" + string(name) + "/" + string(kind))
	rt.insts[id] = runtime.Instance{ID: id, Namespace: ns, Name: name, OwnerKind: kind, State: runtime.StateRunning, IP: ip}
}

func seedObjects(t *testing.T, st store.Store, objs ...v1.Object) {
	t.Helper()
	for _, o := range objs {
		_, err := st.Create(context.Background(), o)
		require.NoError(t, err, "seed %s", o.GroupVersionKind().Kind)
	}
}

func lakeFunction(ns v1.NamespaceName, blob ...v1.FunctionBlob) *v1.Function {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "lake", ns, "rg1"
	fn.Spec = v1.FunctionSpec{Runtime: "nodejs22", Blob: blob}
	return fn
}

// lakeCatalogService is CatalogService lake bound to lakehouse/raw, its catalog prefix.
func lakeCatalogService(ns v1.NamespaceName) *v1.CatalogService {
	cs := &v1.CatalogService{TypeMeta: v1.TypeMeta{APIVersion: v1.KindCatalogService.GVK().APIVersion(), Kind: v1.KindCatalogService}}
	cs.Name, cs.Namespace, cs.ResourceGroup = "lake", ns, "rg1"
	cs.Spec = v1.CatalogServiceSpec{Blob: []v1.FunctionBlob{{Alias: "raw", Bucket: "lakehouse", Prefix: "raw"}}, Catalog: v1.CatalogRef{Bucket: "lakehouse", Prefix: "raw"}}
	return cs
}

func egressToExample(ns v1.NamespaceName, appliesTo ...v1.ObjectName) *v1.EgressPolicy {
	ep := &v1.EgressPolicy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindEgressPolicy.GVK().APIVersion(), Kind: v1.KindEgressPolicy}}
	ep.Name, ep.Namespace, ep.ResourceGroup = "egress", ns, "rg1"
	ep.Spec = v1.EgressPolicySpec{
		AppliesTo: appliesTo,
		Rules:     []v1.EgressRule{{To: v1.EgressTo{Domains: []string{"example.com"}}, Ports: []int{443}}},
	}
	return ep
}

// egressConnect decides a connection from src to example.com:443 the way the egress gateway's decideConnect
// does: the source IP resolves through the worker index, then the PDP decides egress::connect. It returns the
// audit record the gateway writes.
func egressConnect(t *testing.T, p *Platform, pdp auth.Authorizer, src string) egress.AuditRecord {
	t.Helper()
	ref, ok := p.egressWorkers.Lookup(netip.MustParseAddr(src))
	require.True(t, ok, "worker %s is indexed", src)
	nd := auth.NetDestination{IP: netip.MustParseAddr("93.184.216.34"), Port: 443, Domains: []string{"example.com"}}
	resource := nd.Ref(ref.Namespace)
	dec, err := pdp.Authorize(context.Background(), auth.Request{
		Identity: auth.Identity{Principal: &ref},
		Action:   auth.ActionEgressConnect,
		Resource: &resource,
	})
	require.NoError(t, err)
	return egress.AuditRecord{Namespace: string(ref.Namespace), Function: string(ref.Name), Allowed: dec.Allowed, Reason: dec.Reason}
}

func egressPlatform(t *testing.T, st store.Store, rt runtime.Runtime) (*Platform, auth.Authorizer) {
	t.Helper()
	p := &Platform{cfg: &config{store: st, runtime: rt}, egressWorkers: egress.NewMemoryWorkerIndex()}
	p.reconcileEgressWorkers(context.Background(), map[netip.Addr]auth.EntityRef{})
	ep, err := cedarauth.NewEntityProvider(cedarMetaReader{st})
	require.NoError(t, err)
	pdp, err := cedarauth.New(cedarauth.Deps{Entities: ep, Policies: policySource{st}})
	require.NoError(t, err)
	return p, pdp
}

// scenario: engine-egress-is-catalogservice — the egress index maps each worker to the principal of its owner
// kind: with Function and CatalogService lake in one namespace, an EgressPolicy (whole-namespace, or appliesTo
// [lake]) allows the Function and denies the engine, audited under its name; in a namespace with only
// CatalogService lake the engine is indexed, denied and audited, not an unknown source.
func TestScenarioEngineEgressIsCatalogService(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		appliesTo []v1.ObjectName
	}{{"whole namespace", nil}, {"appliesTo lake", []v1.ObjectName{"lake"}}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			seedObjects(t, st, lakeFunction("acme"), lakeCatalogService("acme"), egressToExample("acme", tc.appliesTo...))
			rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
			runningWorker(rt, "acme", "lake", v1.KindFunction, "10.63.0.2")
			runningWorker(rt, "acme", "lake", v1.KindCatalogService, "10.63.0.3")
			p, pdp := egressPlatform(t, st, rt)

			fnRef, _ := p.egressWorkers.Lookup(netip.MustParseAddr("10.63.0.2"))
			require.Equal(t, v1.KindFunction, fnRef.Type)
			require.True(t, egressConnect(t, p, pdp, "10.63.0.2").Allowed, "the Function egresses with its grant")

			engineRef, _ := p.egressWorkers.Lookup(netip.MustParseAddr("10.63.0.3"))
			require.Equal(t, auth.EntityRef{Type: v1.KindCatalogService, Namespace: "acme", Name: "lake"}, engineRef)
			rec := egressConnect(t, p, pdp, "10.63.0.3")
			require.False(t, rec.Allowed, "no EgressPolicy grant reaches the engine")
			require.Equal(t, "lake", rec.Function)
		})
	}
	t.Run("only a CatalogService in the namespace", func(t *testing.T) {
		t.Parallel()
		st := store.New(memory.New())
		seedObjects(t, st, lakeCatalogService("solo"), egressToExample("solo"))
		rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
		runningWorker(rt, "solo", "lake", v1.KindCatalogService, "10.63.0.4")
		runningWorker(rt, "solo", "stray", v1.KindIdentity, "10.63.0.5")
		p, pdp := egressPlatform(t, st, rt)

		rec := egressConnect(t, p, pdp, "10.63.0.4")
		require.False(t, rec.Allowed)
		require.Equal(t, "solo", rec.Namespace)
		require.Equal(t, "lake", rec.Function)
		_, ok := p.egressWorkers.Lookup(netip.MustParseAddr("10.63.0.5"))
		require.False(t, ok, "a worker of another owner kind is not indexed")
	})
	t.Run("a failed CatalogService list keeps the index", func(t *testing.T) {
		t.Parallel()
		st := store.New(memory.New())
		seedObjects(t, st, lakeCatalogService("solo"))
		rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
		runningWorker(rt, "solo", "lake", v1.KindCatalogService, "10.63.0.4")
		p := &Platform{cfg: &config{store: st, runtime: rt}, egressWorkers: egress.NewMemoryWorkerIndex()}
		prev := p.reconcileEgressWorkers(context.Background(), map[netip.Addr]auth.EntityRef{})

		p.cfg.store = catalogListFails{st}
		require.Equal(t, prev, p.reconcileEgressWorkers(context.Background(), prev))
		ref, ok := p.egressWorkers.Lookup(netip.MustParseAddr("10.63.0.4"))
		require.True(t, ok, "the engine stays indexed")
		require.Equal(t, v1.KindCatalogService, ref.Type)
	})
}

type catalogListFails struct{ store.Store }

func (s catalogListFails) List(ctx context.Context, gvk v1.GroupVersionKind, opts store.ListOptions) (store.List, error) {
	if gvk.Kind == v1.KindCatalogService {
		return store.List{}, fault.Unavailablef("catalogListFails", "list %s", gvk.Kind)
	}
	return s.Store.List(ctx, gvk, opts)
}

type fixedMaterializer struct{}

func (fixedMaterializer) Materialize(context.Context, *v1.Function) (string, error) {
	return "/art/app.mjs", nil
}

// scenario: keys-reissued-on-upgrade — on a fresh runtime the Function and catalog reconcilers re-create Function
// lake and engine lake; each worker's env key is the keypair derived for its own kind and authenticates at the
// gateway.
func TestScenarioKeysReissuedOnUpgrade(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	logger, serve := platformRun()
	p, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), logger, WithRuntime(rt), WithRuntimeShim("node", "shim.mjs"), WithMaterializer(fixedMaterializer{}),
			WithS3Gateway(addr, "", 0, "", dataDir))
	}, serve)

	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "a"}, {Name: "raw"}}}
	fn := lakeFunction("default", v1.FunctionBlob{Alias: "a", Bucket: "lakehouse", Prefix: "a"})
	fn.Spec.Image = "file:///art/app.mjs"
	fn.Spec.Handler = "app.handler"
	fn.Spec.Scaling.MinReplicas = 1
	seedObjects(t, p.cfg.store, bucket, fn, lakeCatalogService("default"))

	view, ok := s3BucketFor(p.cfg.blob, p.cfg.store)("default", "lakehouse")
	require.True(t, ok)
	require.NoError(t, view.Put(ctx, "a/x", []byte("fn"), blob.PutOptions{}))
	require.NoError(t, view.Put(ctx, "raw/x", []byte("engine"), blob.PutOptions{}))

	master, err := s3gateway.LoadOrCreateMaster("", dataDir)
	require.NoError(t, err)
	for _, tc := range []struct {
		kind v1.Kind
		key  string
		want string
	}{{v1.KindFunction, "a/x", "fn"}, {v1.KindCatalogService, "raw/x", "engine"}} {
		var spec runtime.WorkerSpec
		require.Eventually(t, func() bool {
			for _, ws := range rt.created()["lake"] {
				if ws.OwnerKind == tc.kind {
					spec = ws
					return true
				}
			}
			return false
		}, 15*time.Second, 20*time.Millisecond, "the %s reconciler re-creates its worker", tc.kind)

		want := s3gateway.DeriveKeypair(master, tc.kind, "default", "lake")
		require.Equal(t, want.AccessKey, spec.Env["AWS_ACCESS_KEY_ID"], tc.kind)
		require.Equal(t, want.SecretKey, spec.Env["AWS_SECRET_ACCESS_KEY"], tc.kind)

		cfg, cerr := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"),
			awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(spec.Env["AWS_ACCESS_KEY_ID"], spec.Env["AWS_SECRET_ACCESS_KEY"], "")))
		require.NoError(t, cerr)
		endpoint := "http://" + addr
		client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		})
		out, gerr := client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("lakehouse"), Key: aws.String(tc.key)})
		require.NoError(t, gerr, "the %s key authenticates", tc.kind)
		var body bytes.Buffer
		_, rerr := body.ReadFrom(out.Body)
		_ = out.Body.Close()
		require.NoError(t, rerr)
		require.Equal(t, tc.want, body.String())
	}
}
