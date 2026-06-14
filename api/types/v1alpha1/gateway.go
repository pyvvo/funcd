package v1alpha1

// Gateway is a cluster-scoped resource representing the API gateway / ingress controller.
// Status-bearing. Behavioral fields (listeners, TLS) owned by F10.
type Gateway struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     GatewayStatus `json:"status,omitempty"`
}

// GatewayStatus holds the observed state.
type GatewayStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Gateway.
func (gw *Gateway) GroupVersionKind() GroupVersionKind { return KindGateway.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (gw *Gateway) Validate() error { return validateMeta(gw.TypeMeta, &gw.ObjectMeta, KindGateway) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (gw *Gateway) GetStatus() *Status { return &gw.Status.Status }
