package v1alpha1

import "github.com/green-0-rabbit/funcd/api/fault"

// Grant is a namespaced, pure-policy resource that binds a subject function to a target KVStore
// under a local binding alias and an access mode (ADR-0072). It is KV-scoped for V1.1 (generalizing
// it to other resources is a later ADR). Grant does NOT have status — it implements Object, not
// StatusObject. The Grant, its subject function, and its target store all live in one namespace
// (the Grant's) — V1.1 is same-namespace KV.
type Grant struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       GrantSpec `json:"spec"`
}

// GrantSpec is the desired state of a KV Grant (ADR-0072): which function gets which store under
// which binding alias, at which mode. Subject + store are same-namespace as the Grant (V1.1).
type GrantSpec struct {
	// Function is the subject function (in this Grant's namespace) the binding is granted to.
	Function ObjectName `json:"function"`
	// Binding is the local alias the function uses (e.g. "counters"); a DNS-1123 label.
	Binding string `json:"binding" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	// Store is the target KVStore (in this Grant's namespace).
	Store ObjectName `json:"store"`
	// Mode is the access mode: ro (get/list) or rw (get/list/put/delete, the single writer).
	Mode KVMode `json:"mode"`
}

// GroupVersionKind returns the constant GVK for Grant.
func (g *Grant) GroupVersionKind() GroupVersionKind { return KindGrant.GVK() }

// Validate performs envelope validation via the shared validateMeta helper, then the GrantSpec
// rules JSON Schema can't fully express: mode is ro|rw, binding is a DNS-1123 label, function and
// store are non-empty. Cross-resource rules (the store + function exist, single-writer) are
// admissions (ADR-0072), not structural Validate.
func (g *Grant) Validate() error {
	if err := validateMeta(g.TypeMeta, &g.ObjectMeta, KindGrant); err != nil {
		return err
	}
	const op = "Grant.Validate"
	if g.Spec.Function == "" {
		return fault.Invalidf(op, "spec.function is required")
	}
	if g.Spec.Store == "" {
		return fault.Invalidf(op, "spec.store is required")
	}
	if !dnsLabel.MatchString(g.Spec.Binding) {
		return fault.Invalidf(op, "spec.binding %q is not a valid DNS-1123 label", g.Spec.Binding)
	}
	return g.Spec.Mode.Validate()
}
