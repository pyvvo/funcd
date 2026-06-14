package v1alpha1

// EventSource is a namespaced resource that configures an event trigger source.
// Status-bearing. Behavioral spec fields (source config) owned by F16.
type EventSource struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       EventSourceSpec   `json:"spec"`
	Status     EventSourceStatus `json:"status,omitempty"`
}

// EventSourceSpec holds the desired state. Behavioral fields owned by F16.
type EventSourceSpec struct{}

// EventSourceStatus holds the observed state.
type EventSourceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for EventSource.
func (es *EventSource) GroupVersionKind() GroupVersionKind { return KindEventSource.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (es *EventSource) Validate() error {
	return validateMeta(es.TypeMeta, &es.ObjectMeta, KindEventSource)
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (es *EventSource) GetStatus() *Status { return &es.Status.Status }
