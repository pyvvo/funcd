package v1alpha1

// WorkerNode is a cluster-scoped resource representing a compute node.
// Status-bearing. Behavioral fields (capacity, heartbeat) owned by multi-node ADR.
type WorkerNode struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     WorkerNodeStatus `json:"status,omitempty"`
}

// WorkerNodeStatus holds the observed state.
type WorkerNodeStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for WorkerNode.
func (w *WorkerNode) GroupVersionKind() GroupVersionKind { return KindWorkerNode.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (w *WorkerNode) Validate() error { return validateMeta(w.TypeMeta, &w.ObjectMeta, KindWorkerNode) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (w *WorkerNode) GetStatus() *Status { return &w.Status.Status }
