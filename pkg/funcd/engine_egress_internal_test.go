package funcd

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cedarauth "github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/network"
	"github.com/pyvvo/funcd/internal/network/egress"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// storedAddonObject is the stored add-on object name of ns, of the add-on kind kind.
func storedAddonObject(ns v1.NamespaceName, name v1.ObjectName, kind string) *v1.AddonObject {
	o := &v1.AddonObject{TypeMeta: tmeta(v1.KindAddonObject), Spec: v1.AddonObjectSpec{Kind: kind}}
	o.Name, o.Namespace, o.ResourceGroup = name, ns, "rg1"
	return o
}

// engineEgress is an executor with both egress indexes over st and rt, filled once from the runtime as the platform fills
// them, and the PDP over st.
func engineEgress(t *testing.T, st store.Store, rt runtime.Runtime) (*executor, auth.Authorizer) {
	t.Helper()
	e := &executor{cfg: &config{store: st, runtime: rt}, egressWorkers: egress.NewMemoryWorkerIndex(), egressEngines: egress.NewMemoryWorkerIndex()}
	e.egressSources = egress.Sources(e.egressWorkers, e.egressEngines)
	e.reconcileEgressWorkers(context.Background(), map[netip.Addr]auth.EntityRef{})
	ep, err := cedarauth.NewEntityProvider(cedarMetaReader{st})
	require.NoError(t, err)
	pdp, err := cedarauth.New(cedarauth.Deps{Entities: ep, Policies: policySource{st}})
	require.NoError(t, err)
	return e, pdp
}

// reach is what the source src gets for example.com: whether the forwarder answers its lookup, and the gateway's
// decision on a connection to example.com:443, named as the gateway names its source and audits it.
func reach(t *testing.T, e *executor, pdp auth.Authorizer, src string) (answered bool, rec egress.AuditRecord) {
	t.Helper()
	ref, ok := e.egressSources.Lookup(netip.MustParseAddr(src))
	if !ok {
		return false, egress.AuditRecord{Reason: "unknown source worker " + src}
	}
	names, err := storeDomainRules{e.cfg.store}.For(context.Background(), ref)
	require.NoError(t, err)
	nd := auth.NetDestination{IP: netip.MustParseAddr("93.184.216.34"), Port: 443, Domains: []string{"example.com"}}
	resource := nd.Ref(ref.Namespace)
	dec, err := pdp.Authorize(context.Background(), auth.Request{Identity: auth.Identity{Principal: &ref},
		Action: auth.ActionEgressConnect, Resource: &resource})
	require.NoError(t, err)
	return slices.Contains(names, "example.com"),
		egress.AuditRecord{Namespace: string(ref.Namespace), Function: string(ref.Name), Allowed: dec.Allowed, Reason: dec.Reason}
}

func tunnelPolicy(ns v1.NamespaceName, appliesTo ...v1.AppliesToRef) *v1.EgressPolicy {
	ep := egressToExample(ns)
	ep.Name, ep.Spec.AppliesTo = "tunnel", appliesTo
	return ep
}

// scenario: engine-egress-named — with egress on, the engine of Tunnel public, named by an EgressPolicy item of its kind
// and its name, gets its lookup of example.com answered and reaches example.com:443; the Function public gets neither
// (ADR-0352 Decisions 3 and 4). The egress lane runs it on containerd, as its case engine-egress-named.
func TestScenarioEngineEgressNamed(t *testing.T) {
	t.Parallel()
	fn := lakeFunction("team")
	fn.Name = "public"
	st := store.New(memory.New())
	seedObjects(t, st, fn, storedAddonObject("team", "public", "Tunnel"), tunnelPolicy("team", v1.AppliesToRef{Kind: "Tunnel", Name: "public"}))
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	runningWorker(rt, "team", "public", v1.KindFunction, "10.63.0.2")
	runningWorker(rt, "team", "public", v1.KindAddonObject, "10.63.0.3")
	e, pdp := engineEgress(t, st, rt)

	ref, ok := e.egressSources.Lookup(netip.MustParseAddr("10.63.0.3"))
	require.True(t, ok)
	require.Equal(t, auth.EntityRef{Type: v1.KindAddonObject, Namespace: "team", Name: "public"}, ref, "the engine's source is its object")
	answered, rec := reach(t, e, pdp, "10.63.0.3")
	require.True(t, answered, "the engine's lookup is answered")
	require.True(t, rec.Allowed, "and it reaches example.com:443: %s", rec.Reason)

	answered, rec = reach(t, e, pdp, "10.63.0.2")
	require.False(t, answered, "the Function of the object's name gets no answer")
	require.False(t, rec.Allowed, "nor the connection")
}

// scenario: engine-egress-default-deny — with egress on, an engine that no policy names, or that only a policy with an
// empty appliesTo covers, is refused, and the refusal is audited under its object's namespace and name.
func TestScenarioEngineEgressDefaultDeny(t *testing.T) {
	t.Parallel()
	for name, policies := range map[string][]v1.Object{
		"no policy":                    nil,
		"an empty appliesTo":           {egressToExample("team")},
		"a policy of another workload": {tunnelPolicy("team", v1.AppliesToRef{Kind: "Function", Name: "public"})},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			st := store.New(memory.New())
			seedObjects(t, st, append([]v1.Object{storedAddonObject("team", "public", "Tunnel")}, policies...)...)
			rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
			runningWorker(rt, "team", "public", v1.KindAddonObject, "10.63.0.3")
			e, pdp := engineEgress(t, st, rt)

			answered, rec := reach(t, e, pdp, "10.63.0.3")
			require.False(t, answered)
			require.False(t, rec.Allowed)
			require.Equal(t, "team", rec.Namespace)
			require.Equal(t, "public", rec.Function)
		})
	}
}

// scenario: applies-to-names-the-kind — a policy that names the Tunnel public lets out nothing of an IdentityProvider
// public created after the Tunnel is gone: the item names the kind as well as the name (ADR-0352 Decision 4).
func TestScenarioAppliesToNamesTheKind(t *testing.T) {
	t.Parallel()
	st := store.New(memory.New())
	seedObjects(t, st, storedAddonObject("team", "public", "IdentityProvider"), tunnelPolicy("team", v1.AppliesToRef{Kind: "Tunnel", Name: "public"}))
	rt := &recordingRuntime{insts: map[runtime.InstanceID]runtime.Instance{}}
	runningWorker(rt, "team", "public", v1.KindAddonObject, "10.63.0.3")
	e, pdp := engineEgress(t, st, rt)

	answered, rec := reach(t, e, pdp, "10.63.0.3")
	require.False(t, answered, "no lookup is answered")
	require.False(t, rec.Allowed, "and no connection passes")
}

// tunnelAddon registers the kind Tunnel, whose settings kinds are rauthy's, and tunnel is its object public of ns.
func tunnelAddon() *v1.Addon {
	a := rauthySettingsAddon("frpc")
	a.Spec.Runtimes[0].Attributes.Kind = "Tunnel"
	return a
}

func tunnel(ns v1.NamespaceName) *v1.AddonObject {
	o := identityProvider(ns)
	o.Kind, o.Name, o.Spec.Ingress = "Tunnel", "public", nil
	return o
}

// scenario: applies-to-kind-checked — an item of a kind no Addon registers, of a settings kind, of an engineless kind,
// or of the Addon's kind in another case gets 400 naming the item; a kind that is no kind name gets the schema's 422
// (ADR-0352 Decision 1).
func TestScenarioAppliesToKindChecked(t *testing.T) {
	t.Parallel()
	e := startAddons(t)
	e.apply(t, e.admin, tunnelAddon())
	e.apply(t, e.admin, infraStackAddon())
	const path = "/apis/funcd.io/v1alpha1/namespaces/default/egresspolicies"
	for kind, want := range map[string]int{
		"Gateway":              http.StatusBadRequest,
		"RauthyPasswordPolicy": http.StatusBadRequest,
		"InfraStack":           http.StatusBadRequest,
		"TUNNEL":               http.StatusBadRequest,
		"tunnel":               http.StatusUnprocessableEntity,
	} {
		status, detail := e.send(t, http.MethodPost, path, addonDevToken, tunnelPolicy("default", v1.AppliesToRef{Kind: kind, Name: "public"}))
		require.Equal(t, want, status, "%s: %s", kind, detail)
		require.Contains(t, detail, "appliesTo", "%s: the refusal names the item", kind)
	}
	e.apply(t, e.dev, tunnelPolicy("default", v1.AppliesToRef{Kind: "Tunnel", Name: "public"}, v1.AppliesToRef{Kind: "Job", Name: "tofu"}))
}

// scenario: egress-off-keeps-the-fence — with server.network.egress off, the engine of a Tunnel that a policy names is
// fenced with no endpoint, as before; on, it has no output fence, so its connections meet the gateway (ADR-0352
// Decision 2). It admits only funcd either way.
func TestScenarioEgressOffKeepsTheFence(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		opts   []Option
		fenced bool
	}{
		"egress off": {nil, true},
		"egress on":  {[]Option{WithEgressIsolation(network.New(false, nil), network.Policy{})}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := startAddons(t, tc.opts...)
			e.apply(t, e.admin, tunnelAddon())
			e.apply(t, e.dev, tunnelPolicy("default", v1.AppliesToRef{Kind: "Tunnel", Name: "public"}))
			e.apply(t, e.dev, tunnel("default"))
			e.waitReason(t, "default", "public", v1.PhaseReady, "")
			spec, running, _ := e.rt.state(ref("default", "public"))
			require.True(t, running)
			require.Equal(t, tc.fenced, spec.EgressOnly != nil, "an output fence")
			require.Empty(t, spec.EgressOnly, "with no endpoint")
			require.True(t, spec.HostOnly, "only funcd reaches the engine")
		})
	}
}
