package v1alpha1

import (
	"net/netip"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Admission caps that bound compiled-policy memory (ADR-0117 §4a): a policy compiles to at most
// maxEgressRules permits, each of bounded destination/port size, so the compiled-set size is
// deterministically bounded by (caps × policies), independent of user behavior.
const (
	maxEgressRules = 64
	maxEgressDests = 64
	maxEgressPorts = 32
)

// EgressPolicy is a namespaced, pure-policy allow-list (implicit default-deny) governing worker egress
// (ADR-0117, F81). No status (implements Object, not StatusObject). A namespace with no EgressPolicy ⇒
// its workers reach nothing external. It is the first high-level typed policy CRD that compiles to
// Cedar (internal/auth/cedar.CompileEgressPolicy). Example (YAML block syntax):
//
//	apiVersion: funcd.io/v1alpha1
//	kind: EgressPolicy
//	metadata:
//	  name: default-egress
//	  namespace: acme
//	  resourceGroup: rg1
//	spec:
//	  appliesTo: [etl]        # optional; omit ⇒ every Function in the namespace
//	  rules:
//	    - to:
//	        domains: ["api.x.com", "*.x.com"]
//	        cidrs:   ["10.0.0.0/8"]
//	      ports: [443, 5432]
type EgressPolicy struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       EgressPolicySpec `json:"spec"`
}

// EgressPolicySpec is an allow-list of egress rules, optionally scoped to named Functions.
type EgressPolicySpec struct {
	// Rules is the allow-list; each grants egress to its `to` on its `ports`. At least one required.
	Rules []EgressRule `json:"rules"`
	// AppliesTo scopes every rule to these Functions (by name, this namespace); empty ⇒ the whole namespace.
	AppliesTo []ObjectName `json:"appliesTo,omitempty"`
}

// EgressRule permits egress to a destination set on a port set.
type EgressRule struct {
	// To is the destination match; at least one of domains/cidrs is required (Validate enforces).
	To EgressTo `json:"to"`
	// Ports are the destination ports (1..65535); empty ⇒ any port to the matched destination.
	Ports []int `json:"ports,omitempty"`
}

// EgressTo is a destination match: SNI/Host domains (wildcard "*.x.com" ok) and/or CIDRs.
type EgressTo struct {
	Domains []string `json:"domains,omitempty"`
	CIDRs   []string `json:"cidrs,omitempty"`
}

// GroupVersionKind returns the constant GVK for EgressPolicy.
func (ep *EgressPolicy) GroupVersionKind() GroupVersionKind { return KindEgressPolicy.GVK() }

// Validate performs envelope validation then the allow-list rules: at least one rule; each rule has a
// non-empty `to` (≥1 domain or CIDR); domains are DNS names or "*."-prefixed wildcards; CIDRs parse
// (netip.ParsePrefix); ports are 1..65535 (fault.Invalid otherwise).
func (ep *EgressPolicy) Validate() error {
	if err := validateMeta(ep.TypeMeta, &ep.ObjectMeta, KindEgressPolicy); err != nil {
		return err
	}
	const op = "EgressPolicy.Validate"
	if len(ep.Spec.Rules) == 0 {
		return fault.Invalidf(op, "spec.rules must list at least one rule")
	}
	if len(ep.Spec.Rules) > maxEgressRules {
		return fault.Invalidf(op, "spec.rules has %d rules, exceeds the cap of %d", len(ep.Spec.Rules), maxEgressRules)
	}
	for i := range ep.Spec.Rules {
		rule := &ep.Spec.Rules[i]
		if len(rule.To.Domains) == 0 && len(rule.To.CIDRs) == 0 {
			return fault.Invalidf(op, "spec.rules[%d].to must set at least one of domains/cidrs", i)
		}
		if n := len(rule.To.Domains) + len(rule.To.CIDRs); n > maxEgressDests {
			return fault.Invalidf(op, "spec.rules[%d].to has %d destinations, exceeds the cap of %d", i, n, maxEgressDests)
		}
		if len(rule.Ports) > maxEgressPorts {
			return fault.Invalidf(op, "spec.rules[%d].ports has %d ports, exceeds the cap of %d", i, len(rule.Ports), maxEgressPorts)
		}
		for _, d := range rule.To.Domains {
			if !validEgressDomain(d) {
				return fault.Invalidf(op, "spec.rules[%d].to.domains %q is not a valid domain or \"*.\"-wildcard", i, d)
			}
		}
		for _, c := range rule.To.CIDRs {
			if _, err := netip.ParsePrefix(c); err != nil {
				return fault.Invalidf(op, "spec.rules[%d].to.cidrs %q is not a valid CIDR: %v", i, c, err)
			}
		}
		for _, p := range rule.Ports {
			if p < 1 || p > 65535 {
				return fault.Invalidf(op, "spec.rules[%d].ports %d is out of range (1..65535)", i, p)
			}
		}
	}
	for i, name := range ep.Spec.AppliesTo {
		if err := name.Validate(); err != nil {
			return fault.Invalidf(op, "spec.appliesTo[%d] %q is not a valid function name: %v", i, name, err)
		}
	}
	return nil
}

// validEgressDomain reports whether d is a DNS name (each label a DNS-1123 label) or a "*."-prefixed
// wildcard whose remaining suffix is a DNS name (e.g. "*.x.com"). A bare "*" is rejected.
func validEgressDomain(d string) bool {
	if d == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(d, "*."); ok {
		return validDNSName(rest)
	}
	return validDNSName(d)
}

// validDNSName reports whether s is a dotted DNS name: at least one label, each a DNS-1123 label.
func validDNSName(s string) bool {
	if s == "" {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}
