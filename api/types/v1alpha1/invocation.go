package v1alpha1

import "time"

// Invocation is a namespaced, read-only resource representing a single function invocation record.
// Status-bearing.
type Invocation struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     InvocationStatus `json:"status,omitempty"`
}

// InvocationStatus holds the observed invocation record.
// Behavioral fields (retention policy) owned by F16.
type InvocationStatus struct {
	Status    `json:",inline"`
	StartTime time.Time `json:"startTime,omitempty"`
	EndTime   time.Time `json:"endTime,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// GroupVersionKind returns the constant GVK for Invocation.
func (inv *Invocation) GroupVersionKind() GroupVersionKind { return KindInvocation.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
// Note: Invocation is namespaced (KindInvocation.Namespaced() is true).
func (inv *Invocation) Validate() error {
	return validateMeta(inv.TypeMeta, &inv.ObjectMeta, KindInvocation)
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (inv *Invocation) GetStatus() *Status { return &inv.Status.Status }
