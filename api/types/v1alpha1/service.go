package v1alpha1

import (
	huma "github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
)

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
	// KV is set when Type == ServiceTypeKV (ADR-0019, F14).
	KV *KVServiceSpec `json:"kv,omitempty"`
	// Blob is set when Type == ServiceTypeBlob (ADR-0021, F23).
	Blob *BlobServiceSpec `json:"blob,omitempty"`
}

// ServiceType is an open typed-string discriminator for service kinds.
// Enumerated values are defined by the service ADRs (F14, F23).
type ServiceType string

// Schema carries ServiceType's enum constraint into the generated OpenAPI (ADR-0048).
func (ServiceType) Schema(huma.Registry) *huma.Schema {
	return enumSchema(string(ServiceTypeKV), string(ServiceTypeBlob))
}

const (
	// ServiceTypeKV is the key/value service (ADR-0019, F14).
	ServiceTypeKV ServiceType = "kv"
	// ServiceTypeBlob is the blob storage service (ADR-0021, F23).
	ServiceTypeBlob ServiceType = "blob"
)

// KVServiceSpec configures a KV service binding (ADR-0019, F14): the binding name a
// function references, which becomes the key-prefix segment <namespace>/<binding>/<key>.
type KVServiceSpec struct {
	Binding string `json:"binding" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// BlobServiceSpec configures a blob service binding (ADR-0021, F23): the binding name a
// function references, the key-prefix segment <namespace>/<binding>/<key>.
type BlobServiceSpec struct {
	Binding string `json:"binding" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

// ServiceStatus holds the observed state.
type ServiceStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Service.
func (s *Service) GroupVersionKind() GroupVersionKind { return KindService.GVK() }

// Validate performs envelope validation, then the cross-field rules JSON Schema can't express
// (ADR-0048): the sub-spec present iff its discriminator matches, and the matching binding
// non-empty. The `type` enum and binding pattern are schema-enforced at the edge (not re-checked here).
func (s *Service) Validate() error {
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindService); err != nil {
		return err
	}
	const op = "Service.Validate"
	switch s.Spec.Type {
	case ServiceTypeKV:
		if s.Spec.Blob != nil {
			return fault.Invalidf(op, "spec.blob must be empty when spec.type is %q", ServiceTypeKV)
		}
		if s.Spec.KV == nil {
			return fault.Invalidf(op, "spec.kv is required when spec.type is %q", ServiceTypeKV)
		}
		if s.Spec.KV.Binding == "" {
			return fault.Invalidf(op, "spec.kv.binding must not be empty")
		}
	case ServiceTypeBlob:
		if s.Spec.KV != nil {
			return fault.Invalidf(op, "spec.kv must be empty when spec.type is %q", ServiceTypeBlob)
		}
		if s.Spec.Blob == nil {
			return fault.Invalidf(op, "spec.blob is required when spec.type is %q", ServiceTypeBlob)
		}
		if s.Spec.Blob.Binding == "" {
			return fault.Invalidf(op, "spec.blob.binding must not be empty")
		}
	default:
		return fault.Invalidf(op, "unknown service type %q", s.Spec.Type)
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *Service) GetStatus() *Status { return &s.Status.Status }
