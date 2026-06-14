package v1alpha1

// Worker is a cluster-scoped resource representing a compute node.
// Status-bearing. Behavioral fields (capacity, heartbeat) owned by multi-node ADR.
type Worker struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     WorkerStatus `json:"status,omitempty"`
}

// WorkerStatus holds the observed state.
type WorkerStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Worker.
func (w *Worker) GroupVersionKind() GroupVersionKind { return KindWorker.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (w *Worker) Validate() error { return validateMeta(w.TypeMeta, &w.ObjectMeta, KindWorker) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (w *Worker) GetStatus() *Status { return &w.Status.Status }
