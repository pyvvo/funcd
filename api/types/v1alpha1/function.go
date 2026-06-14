package v1alpha1

// Function is a namespaced resource representing a deployable function/agent.
// Status-bearing. Behavioral spec fields owned by F10–F14.
//
// NOTE: `json:",inline"` flattens TypeMeta because stdlib encoding/json treats an empty
// tag name as "anonymous → promote fields" (the `inline` option itself is a no-op in
// stdlib; it's a yaml/k8s-codec convention). ObjectMeta keeps the name "metadata", so
// it nests. The roundtrip test guards this — do not rename the TypeMeta tag.
type Function struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       FunctionSpec   `json:"spec"`
	Status     FunctionStatus `json:"status,omitempty"`
}

// FunctionSpec holds the desired state. Behavioral fields are appended by feature ADRs:
//
//	runtime               → F12 (runtime port)
//	handler/artifact/shape → F13 (function contract & lifecycle)
//	scaling                → F11 (scale-to-zero)
//	triggers               → F10/F16 (routes / eventing)
//	services               → F14 (KV) and the service pattern
type FunctionSpec struct{}

// FunctionStatus holds the observed state. Behavioral fields appended by F11/F13
// (e.g. Replicas, CurrentRevision).
type FunctionStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Function.
func (f *Function) GroupVersionKind() GroupVersionKind { return KindFunction.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (f *Function) Validate() error { return validateMeta(f.TypeMeta, &f.ObjectMeta, KindFunction) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (f *Function) GetStatus() *Status { return &f.Status.Status }
