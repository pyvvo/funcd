package v1alpha1

// Service is a namespaced resource representing a platform service binding.
// Status-bearing. Behavioral spec fields (driver config, binding) owned by F14/F23.
type Service struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ServiceSpec   `json:"spec"`
	Status     ServiceStatus `json:"status,omitempty"`
}

// ServiceSpec holds the desired state. Behavioral fields owned by F14/F23.
// ServiceType is an open typed-string discriminator; enumerated values owned by service ADRs.
type ServiceSpec struct {
	Type ServiceType `json:"type,omitempty"`
}

// ServiceType is an open typed-string discriminator for service kinds.
// Enumerated values are defined by the service ADRs (F14, F23).
type ServiceType string

// ServiceStatus holds the observed state.
type ServiceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Service.
func (s *Service) GroupVersionKind() GroupVersionKind { return KindService.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (s *Service) Validate() error { return validateMeta(s.TypeMeta, &s.ObjectMeta, KindService) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *Service) GetStatus() *Status { return &s.Status.Status }
