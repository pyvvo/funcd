package v1alpha1

// Namespace is a cluster-scoped resource that defines a tenant boundary.
// Status-bearing.
type Namespace struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     NamespaceStatus `json:"status,omitempty"`
}

// NamespaceStatus holds the observed state.
type NamespaceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Namespace.
func (n *Namespace) GroupVersionKind() GroupVersionKind { return KindNamespace.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (n *Namespace) Validate() error { return validateMeta(n.TypeMeta, &n.ObjectMeta, KindNamespace) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (n *Namespace) GetStatus() *Status { return &n.Status.Status }
