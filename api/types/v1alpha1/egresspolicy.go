package v1alpha1

// EgressPolicy is a namespaced, pure-policy resource that defines egress network rules.
// EgressPolicy does NOT have status — it implements Object, not StatusObject.
// Behavioral fields (rules) owned by network-manager ADR (V2).
type EgressPolicy struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       EgressPolicySpec `json:"spec"`
}

// EgressPolicySpec holds the desired state. Behavioral fields owned by network-manager ADR (V2).
type EgressPolicySpec struct{}

// GroupVersionKind returns the constant GVK for EgressPolicy.
func (ep *EgressPolicy) GroupVersionKind() GroupVersionKind { return KindEgressPolicy.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (ep *EgressPolicy) Validate() error {
	return validateMeta(ep.TypeMeta, &ep.ObjectMeta, KindEgressPolicy)
}
