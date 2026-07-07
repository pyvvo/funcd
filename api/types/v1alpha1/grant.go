package v1alpha1

// Grant is a namespaced, pure-policy resource (KindGrant stays registered). ADR-0072 made it a KV
// binding ({function, binding, store, mode}); ADR-0073 supersedes that — KV bindings moved to
// Function.spec.kv and authorization to the PDP/IAM — so GrantSpec reverts to its reserved empty
// placeholder. KindGrant remains registered (CRUD plumbing intact) for a future IAM ADR to fill;
// nothing in KV references Grant anymore. Grant has no status — it implements Object, not StatusObject.
type Grant struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       GrantSpec `json:"spec"`
}

// GrantSpec is the reserved IAM-V2 placeholder (ADR-0073): empty until a future IAM ADR defines it.
type GrantSpec struct{}

// GroupVersionKind returns the constant GVK for Grant.
func (g *Grant) GroupVersionKind() GroupVersionKind { return KindGrant.GVK() }

// Validate performs envelope validation via the shared validateMeta helper. The empty GrantSpec has no
// structural rules of its own (reserved placeholder — ADR-0073).
func (g *Grant) Validate() error {
	return validateMeta(g.TypeMeta, &g.ObjectMeta, KindGrant)
}
