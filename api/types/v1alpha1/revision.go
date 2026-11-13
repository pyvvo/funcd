package v1alpha1

// Revision is a namespaced, immutable snapshot of a Function at a point in time.
// Status-bearing. Behavioral fields (frozen snapshot payload) owned by F13.
type Revision struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RevisionSpec   `json:"spec"`
	Status     RevisionStatus `json:"status,omitempty"`
}

// RevisionSpec holds the immutable identity + frozen snapshot of a revision.
// Number is the revision sequence number. The Runtime/Handler/Image snapshot
// (F13/ADR-0020, flattened by ADR-0097) pins "what was validated ships" — immutable once stamped.
type RevisionSpec struct {
	Function    ObjectRef   `json:"function" doc:"The Function whose spec this Revision snapshots."`
	Number      int64       `json:"number" minimum:"1" doc:"The generation of the Function's spec that this Revision snapshots."`
	Runtime     RuntimeName `json:"runtime,omitempty" doc:"The runtime that the Revision's workers run: the Function's spec.runtime when funcd created the Revision."`
	Handler     string      `json:"handler,omitempty" pattern:"^[A-Za-z_][A-Za-z0-9_.]*$" doc:"The export that the Revision's workers call: the Function's spec.handler when funcd created the Revision."`
	Image       string      `json:"image,omitempty" doc:"The artifact that the Revision's workers run: the Function's spec.image when funcd created the Revision."`
	ImageDigest string      `json:"imageDigest,omitempty" pattern:"^sha256:[a-f0-9]{64}$" doc:"The digest that the Revision's workers pull the image by: the Function's spec.imageDigest, or the digest that its image resolved to when funcd created the Revision. It never changes, even when a tag moves."`
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
