package cedar

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// egressReg is the registry with the egress capability added (the composition-root shape).
func egressReg(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewRegistry(
		[]Capability{KVCapability(), InvokeCapability(), S3Capability(), EgressCapability()},
		[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
	)
	require.NoError(t, err)
	return reg
}

// decideEgress runs the exact composition the driver performs for an egress::connect decision: the
// registry-assembled built-ins + the compiled EgressPolicy Cedar text + the per-request entities.
func decideEgress(t *testing.T, reg *Registry, m MetaReader, principal auth.EntityRef, dst auth.NetDestination, extraCedar string) bool {
	t.Helper()
	resource := dst.Ref(principal.Namespace)
	ep, err := reg.EntityProvider(m)
	require.NoError(t, err)
	em, err := ep.EntitiesFor(context.Background(), principal, resource)
	require.NoError(t, err)
	ps, err := cedar.NewPolicySetFromBytes("builtin", []byte(reg.Builtins()))
	require.NoError(t, err)
	if extraCedar != "" {
		list, lerr := cedar.NewPolicyListFromBytes("egress", []byte(extraCedar))
		require.NoError(t, lerr, "the compiled EgressPolicy text parses")
		for j, pol := range list {
			ps.Add(cedartypes.PolicyID(fmt.Sprintf("egress#%d", j)), pol)
		}
	}
	pUID, err := principalUID(principal)
	require.NoError(t, err)
	rUID, err := resourceUID(resource)
	require.NoError(t, err)
	dec, _ := cedar.Authorize(ps, em, cedartypes.Request{
		Principal: pUID,
		Action:    cedartypes.NewEntityUID("Action", cedartypes.String(auth.ActionEgressConnect)),
		Resource:  rUID,
		Context:   cedartypes.NewRecord(cedartypes.RecordMap{}),
	})
	return bool(dec)
}

func etlPolicy() *v1.EgressPolicy {
	return &v1.EgressPolicy{
		ObjectMeta: v1.ObjectMeta{Name: "egress", Namespace: "acme", ResourceGroup: "rg1"},
		Spec: v1.EgressPolicySpec{
			AppliesTo: []v1.ObjectName{"etl"},
			Rules: []v1.EgressRule{
				{To: v1.EgressTo{Domains: []string{"*.x.com"}}, Ports: []int{443}},
				{To: v1.EgressTo{CIDRs: []string{"10.0.0.0/8"}}, Ports: []int{5432}},
			},
		},
	}
}

// scenario: egresspolicy-compiles-to-cedar — an EgressPolicy allowing *.x.com:443 + 10.0.0.0/8:5432 for
// appliesTo [etl] yields the expected egress::connect permits as synthetic v1.Policy Cedar text, and it
// enforces at the decision level (declared domain/CIDR permitted, everything else default-deny).
func TestScenarioEgressPolicyCompilesToCedar(t *testing.T) {
	t.Parallel()

	pols, err := CompileEgressPolicy("acme", etlPolicy(), []v1.ObjectName{"etl", "other"})
	require.NoError(t, err)
	require.Len(t, pols, 1, "one synthetic v1.Policy per namespace")
	text := pols[0].Spec.Cedar

	// The permits are canonical Cedar rendered by MarshalCedar (multi-line scope); assert the scoped
	// principal + action + the typed guards.
	require.Contains(t, text, `principal == Function::"acme/etl"`, "the permit is scoped to the appliesTo function")
	require.Contains(t, text, `action == Action::"egress::connect"`)
	require.Contains(t, text, `resource.domains.contains("*.x.com")`)
	require.Contains(t, text, `resource.ip.isInRange(ip("10.0.0.0/8"))`)
	require.Contains(t, text, `resource.port == 443`)
	require.Contains(t, text, `resource.port == 5432`)
	require.NotContains(t, text, `Function::"acme/other"`, "appliesTo scopes the permit to etl only")

	reg := egressReg(t)
	m := regMeta{fns: map[string]*v1.Function{
		"acme/etl":   {ObjectMeta: v1.ObjectMeta{Name: "etl", Namespace: "acme", ResourceGroup: "rg1"}},
		"acme/other": {ObjectMeta: v1.ObjectMeta{Name: "other", Namespace: "acme", ResourceGroup: "rg1"}},
	}}
	etl := auth.EntityRef{Type: v1.KindFunction, Namespace: "acme", Name: "etl"}
	other := auth.EntityRef{Type: v1.KindFunction, Namespace: "acme", Name: "other"}

	// allowed: attested *.x.com token at the resolved IP on :443.
	dstDomain := auth.NetDestination{IP: netip.MustParseAddr("93.184.216.34"), Port: 443, Domains: []string{"*.x.com"}}
	require.True(t, decideEgress(t, reg, m, etl, dstDomain, text), "declared wildcard domain on :443 is permitted")

	// allowed: CIDR match on :5432 (no domain needed).
	dstCIDR := auth.NetDestination{IP: netip.MustParseAddr("10.1.2.3"), Port: 5432}
	require.True(t, decideEgress(t, reg, m, etl, dstCIDR, text), "declared CIDR on :5432 is permitted")

	// denied: wrong port.
	dstWrongPort := auth.NetDestination{IP: netip.MustParseAddr("93.184.216.34"), Port: 8080, Domains: []string{"*.x.com"}}
	require.False(t, decideEgress(t, reg, m, etl, dstWrongPort, text), "an undeclared port is denied")

	// denied: appliesTo scoping — a function outside appliesTo gets no permit.
	require.False(t, decideEgress(t, reg, m, other, dstDomain, text), "a function outside appliesTo is denied")

	// scenario: no-egresspolicy-denies-all (decision level) — no compiled policy ⇒ default-deny.
	require.False(t, decideEgress(t, reg, m, etl, dstDomain, ""), "no EgressPolicy ⇒ nothing external")
}

// scenario: egresspolicy-compiles-to-cedar — a single combined rule (domains + cidrs + ports) for
// appliesTo [etl] renders the expected typed guards via the JSON EST, and an untrusted injection-laden
// domain compiles inertly (never a second permit).
func TestEgress_CompilesToCedar(t *testing.T) {
	t.Parallel()

	ep := &v1.EgressPolicy{
		ObjectMeta: v1.ObjectMeta{Name: "egress", Namespace: "acme", ResourceGroup: "rg1"},
		Spec: v1.EgressPolicySpec{
			AppliesTo: []v1.ObjectName{"etl"},
			Rules: []v1.EgressRule{
				{To: v1.EgressTo{Domains: []string{"*.x.com"}, CIDRs: []string{"10.0.0.0/8"}}, Ports: []int{443, 5432}},
			},
		},
	}
	pols, err := CompileEgressPolicy("acme", ep, []v1.ObjectName{"etl"})
	require.NoError(t, err)
	require.Len(t, pols, 1)
	text := pols[0].Spec.Cedar

	require.Contains(t, text, `resource.domains.contains("*.x.com")`)
	require.Contains(t, text, `resource.ip.isInRange(ip("10.0.0.0/8"))`)
	require.Contains(t, text, `resource.port == 443`)
	require.Contains(t, text, `resource.port == 5432`)
	require.Contains(t, text, `principal == Function::"acme/etl"`)
	require.Equal(t, 1, strings.Count(text, "permit ("), "one rule ⇒ exactly one permit")

	// Injection probe: an untrusted domain crafted to break out of the string cannot inject a clause.
	// json.Marshal escapes it into a JSON value node, so it renders as an inert quoted string — the
	// crafted `permit(` (no space) is NOT a policy head (real permits render as `permit (`).
	inj := &v1.EgressPolicy{
		ObjectMeta: v1.ObjectMeta{Name: "inj", Namespace: "acme"},
		Spec: v1.EgressPolicySpec{
			Rules: []v1.EgressRule{
				{To: v1.EgressTo{Domains: []string{`a");permit(principal,action,resource);//`}}},
			},
		},
	}
	ipols, err := CompileEgressPolicy("acme", inj, nil)
	require.NoError(t, err, "the crafted domain compiles (inertly), it does not error the pipeline")
	itext := ipols[0].Spec.Cedar
	require.Equal(t, 1, strings.Count(itext, "permit ("), "the injection string does not add a second permit")
	require.Contains(t, itext, `contains("a\");permit(principal,action,resource);//")`, "the crafted string is an escaped, inert value")
}

// scenario: egress-collapse-is-function-count-independent — a whole-namespace policy (appliesTo empty)
// with N rules compiles to exactly N permits scoped `principal.namespace == …`, independent of the
// K-Function set it is compiled against (no Function:: head, no group entity).
func TestEgress_CollapseFunctionCountIndependent(t *testing.T) {
	t.Parallel()

	ep := &v1.EgressPolicy{
		ObjectMeta: v1.ObjectMeta{Name: "egress", Namespace: "acme"},
		Spec: v1.EgressPolicySpec{
			Rules: []v1.EgressRule{
				{To: v1.EgressTo{Domains: []string{"a.com"}}, Ports: []int{443}},
				{To: v1.EgressTo{CIDRs: []string{"10.0.0.0/8"}}},
				{To: v1.EgressTo{Domains: []string{"*.b.com"}}, Ports: []int{80}},
			},
		},
	}
	few := make([]v1.ObjectName, 2)
	for i := range few {
		few[i] = v1.ObjectName("f" + string(rune('0'+i)))
	}
	many := make([]v1.ObjectName, 50)
	for i := range many {
		many[i] = v1.ObjectName("fn" + string(rune('a'+i%26)) + string(rune('0'+i%10)))
	}

	for _, tc := range []struct {
		name  string
		funcs []v1.ObjectName
	}{{"2 functions", few}, {"50 functions", many}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pols, err := CompileEgressPolicy("acme", ep, tc.funcs)
			require.NoError(t, err)
			require.Len(t, pols, 1)
			text := pols[0].Spec.Cedar
			require.Equal(t, 3, strings.Count(text, "permit ("), "N rules ⇒ N permits, independent of function count")
			require.Equal(t, 3, strings.Count(text, `principal.namespace == "acme"`), "every permit is namespace-scoped")
			require.NotContains(t, text, `Function::`, "the whole-namespace collapse uses no Function:: head")
		})
	}
}

// scenario: sni-spoof-denied (decision level, B1) — a connect asserting SNI=*.x.com to an IP the
// forwarder never resolved for it has an EMPTY attested domain set (the gateway drops the unattested
// SNI), so no domain permit fires and the unrelated IP is not in any allowed CIDR ⇒ denied.
func TestScenarioEgressSNISpoofDenied(t *testing.T) {
	t.Parallel()
	reg := egressReg(t)
	m := regMeta{fns: map[string]*v1.Function{
		"acme/etl": {ObjectMeta: v1.ObjectMeta{Name: "etl", Namespace: "acme", ResourceGroup: "rg1"}},
	}}
	etl := auth.EntityRef{Type: v1.KindFunction, Namespace: "acme", Name: "etl"}
	pols, err := CompileEgressPolicy("acme", etlPolicy(), []v1.ObjectName{"etl"})
	require.NoError(t, err)

	// The gateway built a NetDestination with an EMPTY attested set (SNI dropped because unattested).
	spoof := auth.NetDestination{IP: netip.MustParseAddr("8.8.8.8"), Port: 443, Domains: nil}
	require.False(t, decideEgress(t, reg, m, etl, spoof, pols[0].Spec.Cedar),
		"a spoofed SNI to an unattested IP has no forwarder record ⇒ denied")
}

// scenario: egress-capability-registered — egress::connect + NetDestination are Known in the assembled
// vocabulary, a NetDestination request materializes {ip, port, domains} + resourceUID cases it, and the
// existing capabilities are unaffected (kv still Known + resolvable).
func TestScenarioEgressCapabilityRegistered(t *testing.T) {
	t.Parallel()
	reg := egressReg(t)

	require.True(t, reg.KnownAction(auth.ActionEgressConnect), "egress::connect joins the assembled vocabulary")
	require.True(t, reg.KnownEntityType(entityTypeNetDestination), "NetDestination joins the assembled vocabulary")

	// existing capabilities unaffected.
	require.True(t, reg.KnownAction(auth.ActionKVRead))
	require.True(t, reg.KnownEntityType(entityTypeKVTable))

	// A NetDestination request materializes the entity with {ip, port, domains}.
	dst := auth.NetDestination{IP: netip.MustParseAddr("93.184.216.34"), Port: 443, Domains: []string{"api.x.com"}}
	em, ok, err := netDestinationResource(context.Background(), nil, dst.Ref("acme"))
	require.NoError(t, err)
	require.True(t, ok, "the egress capability owns the NetDestination resource")
	uid := netDestUID("93.184.216.34:443")
	ent, found := em[uid]
	require.True(t, found, "the materialized entity is keyed by the <ip>:<port> UID")
	portVal, hasPort := ent.Attributes.Get("port")
	require.True(t, hasPort, "the entity carries a port attr")
	require.Equal(t, cedartypes.Long(443), portVal)

	// resourceUID cases KindNetDestination to the same UID (byte-for-byte agreement).
	rUID, err := resourceUID(dst.Ref("acme"))
	require.NoError(t, err)
	require.Equal(t, uid, rUID, "resourceUID and the materializer agree on the NetDestination UID")

	// A non-NetDestination resource defers (ok==false) — no hijack of kv/s3.
	_, ok, err = netDestinationResource(context.Background(), nil, auth.EntityRef{Type: v1.KindKVStore, Namespace: "acme", Name: "orders", Path: "t"})
	require.NoError(t, err)
	require.False(t, ok, "the egress capability defers a non-NetDestination resource")
}
