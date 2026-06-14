package v1alpha1

// ResourceGroup is a namespaced resource for logical grouping of resources.
// Status-bearing.
type ResourceGroup struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ResourceGroupSpec   `json:"spec"`
	Status     ResourceGroupStatus `json:"status,omitempty"`
}

// ResourceGroupSpec holds the desired state.
// Behavioral fields (cascade-delete behavior) owned by controller ADR (F08).
type ResourceGroupSpec struct {
	Description string `json:"description,omitempty"`
}

// ResourceGroupStatus holds the observed state.
type ResourceGroupStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for ResourceGroup.
func (rg *ResourceGroup) GroupVersionKind() GroupVersionKind { return KindResourceGroup.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (rg *ResourceGroup) Validate() error {
	return validateMeta(rg.TypeMeta, &rg.ObjectMeta, KindResourceGroup)
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (rg *ResourceGroup) GetStatus() *Status { return &rg.Status.Status }
