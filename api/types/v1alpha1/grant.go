package v1alpha1

// Grant is a namespaced, pure-policy resource that defines an authorization grant.
// Grant does NOT have status — it implements Object, not StatusObject.
// Behavioral fields (subjects, verbs) owned by the IAM ADR (V2).
type Grant struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       GrantSpec `json:"spec"`
}

// GrantSpec holds the desired state. Behavioral fields owned by IAM ADR (V2).
type GrantSpec struct{}

// GroupVersionKind returns the constant GVK for Grant.
func (g *Grant) GroupVersionKind() GroupVersionKind { return KindGrant.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (g *Grant) Validate() error { return validateMeta(g.TypeMeta, &g.ObjectMeta, KindGrant) }
