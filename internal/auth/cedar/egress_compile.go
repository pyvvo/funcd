package cedar

import (
	"encoding/json"
	"net/netip"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// egressConnectAction mirrors auth.ActionEgressConnect for building the compiled permits' Action UID.
const egressConnectAction = "egress::connect"

// --- Cedar JSON EST DSL -----------------------------------------------------------------------------
// Each permit is assembled as a Cedar JSON EST document (an estNode tree), json.Marshal'd (which escapes
// every value — a domain/CIDR string becomes a JSON value node, never interpolated policy syntax, so a
// crafted `");…"` domain is inert), parsed with cedar.Policy.UnmarshalJSON, then rendered to canonical
// text with MarshalCedar. This is the M1 injection-safe construction: values are typed EST nodes, not
// fmt.Sprintf text-templating.
//
// estNode is one node of that untyped JSON EST tree (object/array/scalar). By construction it is
// arbitrary JSON, so it is the interface literal — NOT the banned `any` alias (forbidigo/ADR-0002 §4
// bans `any`; the `interface{}` literal is the sanctioned escape for a genuine JSON-shape value).
type estNode = interface{}

func estVar(n string) estNode { return map[string]estNode{"Var": n} }
func estVal(v estNode) estNode {
	return map[string]estNode{"Value": v} // string or int (Long)
}
func estAccess(l estNode, attr string) estNode {
	return map[string]estNode{".": map[string]estNode{"left": l, "attr": attr}}
}
func estEq(l, r estNode) estNode {
	return map[string]estNode{"==": map[string]estNode{"left": l, "right": r}}
}
func estAnd(l, r estNode) estNode {
	return map[string]estNode{"&&": map[string]estNode{"left": l, "right": r}}
}
func estOr(l, r estNode) estNode {
	return map[string]estNode{"||": map[string]estNode{"left": l, "right": r}}
}
func estContains(l, r estNode) estNode {
	return map[string]estNode{"contains": map[string]estNode{"left": l, "right": r}}
}
func estIP(s string) estNode {
	return map[string]estNode{"ip": []estNode{estVal(s)}}
}
func estIsInRange(l, r estNode) estNode {
	return map[string]estNode{"isInRange": []estNode{l, r}}
}
func estIs(l estNode, entityType string) estNode {
	return map[string]estNode{"is": map[string]estNode{"left": l, "entity_type": entityType}}
}
func estEntity(typ, id string) estNode {
	return map[string]estNode{"Value": map[string]estNode{"__entity": map[string]estNode{"type": typ, "id": id}}}
}

// CompileEgressPolicy compiles one namespace's EgressPolicy into SYNTHETIC v1.Policy objects (Spec.Cedar),
// listed by the policy source alongside user KindPolicy and compiled by the existing compile()/
// NewPolicyListFromBytes path — NOT a pre-built PolicySet (ADR-0117, M1). ONE permit per RULE (the
// namespace-attribute collapse): a domain/wildcard matches via resource.domains.contains, a CIDR via
// resource.ip.isInRange(ip("…")), ports as a set. The principal scope is the entity's existing
// `namespace` attribute (`principal is Function && principal.namespace == "<ns>"`) for a whole-namespace policy, or a bounded
// disjunction over the explicit spec.appliesTo list (`principal == Function::"<ns>/<fn>"`) — O(rules),
// independent of the namespace Function count, using NO Cedar group entity and NO edit to the registry
// assembly. funcs is the namespace Function-set (drives the cache revision; the compiled set does not
// depend on it). A nil ep or no rules ⇒ no policies (default-deny holds).
//
// Each permit is assembled as a Cedar JSON EST document and parsed with cedar.Policy.UnmarshalJSON
// (injection-safe: every domain/CIDR is a JSON value node, never interpolated syntax; a malformed CIDR
// is a compile error, never templated). MarshalCedar renders each permit back to canonical Cedar text
// to preserve the M1 PolicySource→compile() contract.
func CompileEgressPolicy(ns v1.NamespaceName, ep *v1.EgressPolicy, funcs []v1.ObjectName) ([]v1.Policy, error) {
	const op = "cedar.CompileEgressPolicy"
	if ep == nil || len(ep.Spec.Rules) == 0 {
		return nil, nil
	}

	principalScope := namespaceScope(ns)
	if len(ep.Spec.AppliesTo) > 0 {
		principalScope = appliesToScope(ns, ep.Spec.AppliesTo)
	}

	var b strings.Builder
	for i := range ep.Spec.Rules {
		body, err := ruleBody(principalScope, &ep.Spec.Rules[i])
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "compile rule %d", i)
		}
		pol := map[string]estNode{
			"effect":    "permit",
			"principal": map[string]estNode{"op": "All"},
			"action": map[string]estNode{
				"op":     "==",
				"entity": map[string]estNode{"type": "Action", "id": egressConnectAction},
			},
			"resource":   map[string]estNode{"op": "All"},
			"conditions": []estNode{map[string]estNode{"kind": "when", "body": body}},
		}
		raw, merr := json.Marshal(pol)
		if merr != nil {
			return nil, fault.Wrapf(merr, fault.Internal, op, "marshal EST for rule %d", i)
		}
		var p cedar.Policy
		if uerr := p.UnmarshalJSON(raw); uerr != nil {
			return nil, fault.Invalidf(op, "rule %d does not parse as Cedar: %v", i, uerr)
		}
		b.Write(p.MarshalCedar())
		b.WriteString("\n")
	}

	syn := v1.Policy{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "egress-" + ep.Name, Namespace: ns, ResourceGroup: ep.ResourceGroup},
		Spec:       v1.PolicySpec{Cedar: b.String()},
	}
	return []v1.Policy{syn}, nil
}

// namespaceScope is the whole-namespace principal guard: principal is Function && principal.namespace ==
// "<ns>". The type test keeps every other principal of the namespace, such as a CatalogService engine,
// out of the grant (ADR-0175).
func namespaceScope(ns v1.NamespaceName) estNode {
	return estAnd(estIs(estVar("principal"), entityTypeFunction), estEq(estAccess(estVar("principal"), "namespace"), estVal(string(ns))))
}

// appliesToScope is the bounded named-appliesTo disjunction: (principal == Function::"<ns>/<fn>" || …).
// The id "<ns>/<fn>" matches functionUID exactly so it compares to the request principal UID.
func appliesToScope(ns v1.NamespaceName, appliesTo []v1.ObjectName) estNode {
	var scope estNode
	for i, fn := range appliesTo {
		eq := estEq(estVar("principal"), estEntity(entityTypeFunction, string(ns)+"/"+string(fn)))
		if i == 0 {
			scope = eq
			continue
		}
		scope = estOr(scope, eq)
	}
	return scope
}

// ruleBody assembles one rule's `when` body: principalScope && [portSet] && destSet — the AND ordered so
// the rendered text reads principalScope && portSet && destSet (matching the ADR).
func ruleBody(principalScope estNode, rule *v1.EgressRule) (estNode, error) {
	body := principalScope
	if len(rule.Ports) > 0 {
		body = estAnd(body, portSet(rule.Ports))
	}
	dest, err := destSet(rule.To)
	if err != nil {
		return nil, err
	}
	return estAnd(body, dest), nil
}

// portSet is the port OR-fold: (resource.port == <p1> || …). Ports are int → Long value nodes.
func portSet(ports []int) estNode {
	var node estNode
	for i, p := range ports {
		eq := estEq(estAccess(estVar("resource"), "port"), estVal(p))
		if i == 0 {
			node = eq
			continue
		}
		node = estOr(node, eq)
	}
	return node
}

// destSet is the destination OR-fold: resource.domains.contains("<domain>") per domain (json.Marshal
// escapes the string — injection-safe), resource.ip.isInRange(ip("<cidr>")) per CIDR. Each CIDR is
// parsed with netip.ParsePrefix first (a malformed CIDR is a compile error, never interpolated); the
// parsed prefix's canonical String() is the ip() literal. Validate guarantees ≥1 destination.
func destSet(to v1.EgressTo) (estNode, error) {
	const op = "cedar.destSet"
	var node estNode
	set := false
	or := func(n estNode) {
		if !set {
			node, set = n, true
			return
		}
		node = estOr(node, n)
	}
	for _, d := range to.Domains {
		or(estContains(estAccess(estVar("resource"), "domains"), estVal(d)))
	}
	for _, c := range to.CIDRs {
		pfx, perr := netip.ParsePrefix(c)
		if perr != nil {
			return nil, fault.Invalidf(op, "invalid CIDR %q: %v", c, perr)
		}
		or(estIsInRange(estAccess(estVar("resource"), "ip"), estIP(pfx.String())))
	}
	if !set {
		return nil, fault.Invalidf(op, "rule has no destination (domains/cidrs)")
	}
	return node, nil
}
