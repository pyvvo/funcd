package v1alpha1

// Revision is a namespaced, immutable snapshot of a Function at a point in time.
// Status-bearing. Behavioral fields (frozen snapshot payload) owned by F13.
type Revision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RevisionSpec   `json:"spec"`
	Status     RevisionStatus `json:"status,omitempty"`
}

// RevisionSpec holds the immutable identity of a revision.
// Number is the revision sequence number. Behavioral fields owned by F13.
type RevisionSpec struct {
	Function ObjectRef `json:"function"`
	Number   int64     `json:"number"`
}

// RevisionStatus holds the observed state.
type RevisionStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Revision.
func (r *Revision) GroupVersionKind() GroupVersionKind { return KindRevision.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (r *Revision) Validate() error { return validateMeta(r.TypeMeta, &r.ObjectMeta, KindRevision) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (r *Revision) GetStatus() *Status { return &r.Status.Status }
