package v1alpha1

// RuntimeClass is a cluster-scoped resource defining a function runtime environment.
// Status-bearing. Behavioral fields (flavor params) owned by F12.
type RuntimeClass struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RuntimeClassSpec   `json:"spec"`
	Status     RuntimeClassStatus `json:"status,omitempty"`
}

// RuntimeClassSpec holds the desired state. Behavioral fields owned by F12.
type RuntimeClassSpec struct {
	Handler string `json:"handler,omitempty"`
}

// RuntimeClassStatus holds the observed state.
type RuntimeClassStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for RuntimeClass.
func (rc *RuntimeClass) GroupVersionKind() GroupVersionKind { return KindRuntimeClass.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (rc *RuntimeClass) Validate() error {
	return validateMeta(rc.TypeMeta, &rc.ObjectMeta, KindRuntimeClass)
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (rc *RuntimeClass) GetStatus() *Status { return &rc.Status.Status }
