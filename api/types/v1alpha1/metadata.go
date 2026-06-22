package v1alpha1

import (
	"fmt"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
)

// Group and Version are the wire identity of every funcd resource.
const (
	Group   = "funcd.io"
	Version = "v1alpha1"
)

// Kind is the typed set of resource kinds.
type Kind string

const (
	KindNamespace     Kind = "Namespace"
	KindResourceGroup Kind = "ResourceGroup"
	KindFunction      Kind = "Function"
	KindRevision      Kind = "Revision"
	KindRoute         Kind = "Route"
	KindService       Kind = "Service"
	KindEventSource   Kind = "EventSource"
	KindConfig        Kind = "Config"
	KindSecret        Kind = "Secret"
	KindGrant         Kind = "Grant"
	KindEgressPolicy  Kind = "EgressPolicy"
	KindInvocation    Kind = "Invocation"
	KindRuntimeClass  Kind = "RuntimeClass"
	KindWorkerNode    Kind = "WorkerNode"
	KindGateway       Kind = "Gateway"
	KindKVStore       Kind = "KVStore"
	KindPolicy        Kind = "Policy"
)

// Validate returns fault.Invalid if the Kind is not one of the known kinds.
func (k Kind) Validate() error {
	switch k {
	case KindNamespace, KindResourceGroup, KindFunction, KindRevision,
		KindRoute, KindService, KindEventSource, KindConfig,
		KindSecret, KindGrant, KindEgressPolicy, KindInvocation,
		KindRuntimeClass, KindWorkerNode, KindGateway, KindKVStore, KindPolicy:
		return nil
	default:
		return fault.Invalidf("Kind.Validate", "unknown kind %q", k)
	}
}

// Namespaced reports whether this kind is namespaced. Cluster-scoped kinds:
// Namespace, RuntimeClass, WorkerNode, Gateway.
func (k Kind) Namespaced() bool {
	switch k {
	case KindNamespace, KindRuntimeClass, KindWorkerNode, KindGateway:
		return false
	default:
		return true
	}
}

// GVK returns the GroupVersionKind for this kind.
func (k Kind) GVK() GroupVersionKind {
	return GroupVersionKind{Group: Group, Version: Version, Kind: k}
}

// GroupVersionKind is the (Group, Version, Kind) triple used for registry lookup,
// ObjectRef identity, and codegen.
type GroupVersionKind struct {
	Group   string
	Version string
	Kind    Kind
}

// APIVersion returns the wire apiVersion string, e.g. "funcd.io/v1alpha1".
func (gvk GroupVersionKind) APIVersion() string {
	return fmt.Sprintf("%s/%s", gvk.Group, gvk.Version)
}

// String returns a human-readable representation.
func (gvk GroupVersionKind) String() string {
	return fmt.Sprintf("%s/%s, Kind=%s", gvk.Group, gvk.Version, gvk.Kind)
}

// TypeMeta carries the wire identity of a resource.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       Kind   `json:"kind"`
}

// ObjectMeta is the shared metadata embedded by every resource kind.
// resourceGroup is required for namespaced kinds (F22); tags are optional.
// Carrier bookkeeping fields (UID, Generation, ResourceVersion, …) are
// set by the store; their lifecycle semantics are owned by the store/controller ADRs.
type ObjectMeta struct {
	Name            ObjectName        `json:"name"`
	Namespace       NamespaceName     `json:"namespace,omitempty"`
	ResourceGroup   ResourceGroupName `json:"resourceGroup,omitempty"`
	Tags            Tags              `json:"tags,omitempty"`
	UID             UID               `json:"uid,omitempty"`
	Generation      int64             `json:"generation,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	CreationTime    time.Time         `json:"creationTimestamp,omitempty"`
	DeletionTime    *time.Time        `json:"deletionTimestamp,omitempty"`
	OwnerReferences []OwnerReference  `json:"ownerReferences,omitempty"`
	Finalizers      []string          `json:"finalizers,omitempty"`
}

// Validate checks the ObjectMeta against envelope rules. Scope is derived from
// k.Namespaced() — the kind is the single source of scope.
func (m *ObjectMeta) Validate(k Kind) error {
	if err := m.Name.Validate(); err != nil {
		return fault.Invalidf("ObjectMeta.Validate", "invalid name: %v", err)
	}
	if k.Namespaced() {
		if m.Namespace == "" {
			return fault.Invalidf("ObjectMeta.Validate", "namespace is required for namespaced kind %q", k)
		}
		if err := m.Namespace.Validate(); err != nil {
			return fault.Invalidf("ObjectMeta.Validate", "invalid namespace: %v", err)
		}
		if m.ResourceGroup == "" {
			return fault.Invalidf("ObjectMeta.Validate", "resourceGroup is required for kind %q", k)
		}
		if err := m.ResourceGroup.Validate(); err != nil {
			return fault.Invalidf("ObjectMeta.Validate", "invalid resourceGroup: %v", err)
		}
	} else {
		if m.Namespace != "" {
			return fault.Invalidf("ObjectMeta.Validate", "namespace must be empty for cluster-scoped kind %q", k)
		}
		// cluster-scoped kinds do not require a resourceGroup
		if m.ResourceGroup != "" {
			if err := m.ResourceGroup.Validate(); err != nil {
				return fault.Invalidf("ObjectMeta.Validate", "invalid resourceGroup: %v", err)
			}
		}
	}
	return nil
}

// Promoted accessors — pointer receiver so they satisfy the Object interface on *Kind.

// GetObjectMeta returns the ObjectMeta pointer for generic store/admission stamping.
func (m *ObjectMeta) GetObjectMeta() *ObjectMeta { return m }

// GetName returns the resource name.
func (m *ObjectMeta) GetName() ObjectName { return m.Name }

// GetNamespace returns the namespace (empty for cluster-scoped kinds).
func (m *ObjectMeta) GetNamespace() NamespaceName { return m.Namespace }

// GetResourceGroup returns the resource group.
func (m *ObjectMeta) GetResourceGroup() ResourceGroupName { return m.ResourceGroup }

// GetGeneration returns the current generation.
func (m *ObjectMeta) GetGeneration() int64 { return m.Generation }

// ObjectRef addresses an object within the single v1alpha1 group/version.
// Cross-version refs (the v1 graduation) would add an apiVersion field.
type ObjectRef struct {
	Kind      Kind          `json:"kind"`
	Namespace NamespaceName `json:"namespace,omitempty"`
	Name      ObjectName    `json:"name"`
}

// OwnerReference establishes a parent-child relationship between resources.
type OwnerReference struct {
	ObjectRef          `json:",inline"`
	UID                UID  `json:"uid"`
	Controller         bool `json:"controller,omitempty"`
	BlockOwnerDeletion bool `json:"blockOwnerDeletion,omitempty"`
}

// Object is the generic interface satisfied by every resource kind. The store
// persists and the controller watches through this interface — no per-kind switch.
type Object interface {
	GroupVersionKind() GroupVersionKind
	GetObjectMeta() *ObjectMeta
	GetName() ObjectName
	GetNamespace() NamespaceName
	GetResourceGroup() ResourceGroupName
	GetGeneration() int64
	Validate() error
}

// StatusObject is the optional extension implemented by kinds with observed state,
// giving the controller a generic status write-back seam.
// Config, Secret, Grant, and EgressPolicy do NOT implement it.
type StatusObject interface {
	Object
	GetStatus() *Status
}

// validateMeta is the shared envelope check every kind's Validate() calls.
// It verifies TypeMeta matches the kind's GVK, then delegates to ObjectMeta.Validate.
func validateMeta(tm TypeMeta, m *ObjectMeta, k Kind) error {
	expectedGVK := k.GVK()
	if tm.APIVersion != expectedGVK.APIVersion() {
		return fault.Invalidf("validateMeta", "apiVersion %q does not match kind %q", tm.APIVersion, k)
	}
	if tm.Kind != k {
		return fault.Invalidf("validateMeta", "kind %q does not match expected kind %q", tm.Kind, k)
	}
	return m.Validate(k)
}

// Registry — stateless (no package-level mutable state; ADR-0002 §5).

// NewObject returns a zero-valued typed Object for the given kind with TypeMeta stamped.
// Returns false if the kind is unknown.
func NewObject(k Kind) (Object, bool) {
	switch k {
	case KindNamespace:
		ns := &Namespace{}
		ns.TypeMeta = typeMetaFor(k)
		return ns, true
	case KindResourceGroup:
		rg := &ResourceGroup{}
		rg.TypeMeta = typeMetaFor(k)
		return rg, true
	case KindFunction:
		fn := &Function{}
		fn.TypeMeta = typeMetaFor(k)
		return fn, true
	case KindRevision:
		rev := &Revision{}
		rev.TypeMeta = typeMetaFor(k)
		return rev, true
	case KindRoute:
		r := &Route{}
		r.TypeMeta = typeMetaFor(k)
		return r, true
	case KindService:
		svc := &Service{}
		svc.TypeMeta = typeMetaFor(k)
		return svc, true
	case KindEventSource:
		es := &EventSource{}
		es.TypeMeta = typeMetaFor(k)
		return es, true
	case KindConfig:
		cfg := &Config{}
		cfg.TypeMeta = typeMetaFor(k)
		return cfg, true
	case KindSecret:
		s := &Secret{}
		s.TypeMeta = typeMetaFor(k)
		return s, true
	case KindGrant:
		g := &Grant{}
		g.TypeMeta = typeMetaFor(k)
		return g, true
	case KindEgressPolicy:
		ep := &EgressPolicy{}
		ep.TypeMeta = typeMetaFor(k)
		return ep, true
	case KindInvocation:
		inv := &Invocation{}
		inv.TypeMeta = typeMetaFor(k)
		return inv, true
	case KindRuntimeClass:
		rc := &RuntimeClass{}
		rc.TypeMeta = typeMetaFor(k)
		return rc, true
	case KindWorkerNode:
		w := &WorkerNode{}
		w.TypeMeta = typeMetaFor(k)
		return w, true
	case KindGateway:
		gw := &Gateway{}
		gw.TypeMeta = typeMetaFor(k)
		return gw, true
	case KindKVStore:
		ks := &KVStore{}
		ks.TypeMeta = typeMetaFor(k)
		return ks, true
	case KindPolicy:
		pol := &Policy{}
		pol.TypeMeta = typeMetaFor(k)
		return pol, true
	default:
		return nil, false
	}
}

// AllKinds returns all kinds in a stable order.
func AllKinds() []Kind {
	return []Kind{
		KindNamespace,
		KindResourceGroup,
		KindFunction,
		KindRevision,
		KindRoute,
		KindService,
		KindEventSource,
		KindConfig,
		KindSecret,
		KindGrant,
		KindEgressPolicy,
		KindInvocation,
		KindRuntimeClass,
		KindWorkerNode,
		KindGateway,
		KindKVStore,
		KindPolicy,
	}
}

// typeMetaFor returns a TypeMeta with the apiVersion and kind pre-filled.
func typeMetaFor(k Kind) TypeMeta {
	return TypeMeta{
		APIVersion: k.GVK().APIVersion(),
		Kind:       k,
	}
}
