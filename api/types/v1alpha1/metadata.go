package v1alpha1

import (
	"crypto/rand"
	"encoding/hex"
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
	KindNamespace      Kind = "Namespace"
	KindResourceGroup  Kind = "ResourceGroup"
	KindFunction       Kind = "Function"
	KindRevision       Kind = "Revision"
	KindRoute          Kind = "Route"
	KindService        Kind = "Service"
	KindEventSource    Kind = "EventSource"
	KindConfigMap      Kind = "ConfigMap"
	KindSecret         Kind = "Secret"
	KindGrant          Kind = "Grant"
	KindEgressPolicy   Kind = "EgressPolicy"
	KindInvocation     Kind = "Invocation"
	KindRuntimeClass   Kind = "RuntimeClass"
	KindWorkerNode     Kind = "WorkerNode"
	KindGateway        Kind = "Gateway"
	KindKVStore        Kind = "KVStore"
	KindBucket         Kind = "Bucket"
	KindCatalogService Kind = "CatalogService"
	KindPolicy         Kind = "Policy"
	KindWorkflow       Kind = "Workflow"
	KindWorkflowRun    Kind = "WorkflowRun"
	KindSensor         Kind = "Sensor"
	// KindIdentity is a user-assigned managed identity (ADR-0135, F100): a stored, status-bearing CRUD
	// resource whose reconciler issues a credential. It resolves on the wire to an S3Identity Cedar
	// principal (KindS3Identity below is the auth-only wire principal; Identity is the CRUD resource).
	KindIdentity Kind = "Identity"
	// KindRole (ADR-0136, F101) is a named data-plane permission set (a stored value-type, like Grant).
	KindRole Kind = "Role"
	// KindRolesAssignment (ADR-0136, F101) grants many (principal, role, scope) entries in one stored
	// value-type; the PDP compiles it to Cedar permits + the single-writer `writers` set.
	KindRolesAssignment Kind = "RolesAssignment"
	// KindS3Identity is the external SigV4 S3 principal (ADR-0080): NOT a stored/CRUD resource —
	// it has no metastore registration (no NewObject/AllKinds/handlers), it exists only as a Cedar
	// principal type the cedar driver materializes for the external-sigv4 authz path. It is excluded
	// from Kind.Validate's known-CRUD set by design.
	KindS3Identity Kind = "S3Identity"
	// KindNetDestination is the ephemeral egress destination a worker connects to (ADR-0117, F81):
	// an AUTHORIZATION-ONLY kind (the exact KindS3Identity precedent) — NOT a stored/CRUD resource, so
	// it has no metastore registration (excluded from Kind.Validate's CRUD set / NewObject / AllKinds).
	// It exists only as the Cedar resource type the egress capability materializes for an egress::connect
	// decision. Its EntityRef reinterprets the fields: the destination rides the plain-string Path,
	// PINNED encoding Path = "<dst-ip>:<port>#<domain1>,<domain2>,…" (the "#…" domain segment is the
	// forwarder-attested set, empty for a pure-IP/CIDR target); Name/Namespace are unused.
	KindNetDestination Kind = "NetDestination"
)

// Validate returns fault.Invalid if the Kind is not one of the known kinds.
func (k Kind) Validate() error {
	switch k {
	case KindNamespace, KindResourceGroup, KindFunction, KindRevision,
		KindRoute, KindService, KindEventSource, KindConfigMap,
		KindSecret, KindGrant, KindEgressPolicy, KindInvocation,
		KindRuntimeClass, KindWorkerNode, KindGateway, KindKVStore, KindBucket,
		KindCatalogService, KindPolicy, KindWorkflow, KindWorkflowRun, KindSensor,
		KindIdentity, KindRole, KindRolesAssignment:
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
	// Name is the resource name. omitempty (not "required") so a create may omit it in favour of
	// GenerateName; the store still requires a non-empty Name (ObjectMeta.Validate) once assigned.
	Name ObjectName `json:"name,omitempty"`
	// GenerateName is an optional NAME PREFIX for server-side name generation (the k8s pattern): when Name
	// is empty and GenerateName is set, the create path fills Name = GenerateName + a random suffix
	// (GenerateObjectName), retrying on the rare collision. Ignored once Name is set. A plain prefix string
	// (a trailing '-' is idiomatic and allowed — the generated Name is what must be a valid label).
	GenerateName    string            `json:"generateName,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,62}$"`
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

// nameSuffixHexBytes is the random-suffix width for GenerateObjectName: 4 crypto/rand bytes → 8 hex chars,
// i.e. 16^8 ≈ 4.3e9 names per prefix (uniqueness is still enforced by store.Create's conflict retry).
const nameSuffixHexBytes = 4

// GenerateObjectName builds a unique object name from a prefix: the prefix (truncated so the whole name
// stays within the 63-char DNS-1123 label limit) followed by a random 8-hex-char suffix. It is the single
// name-generation convention for server-side ObjectMeta.GenerateName and the Sensor's WorkflowRun names.
func GenerateObjectName(prefix string) ObjectName {
	if max := 63 - nameSuffixHexBytes*2; len(prefix) > max {
		prefix = prefix[:max]
	}
	b := make([]byte, nameSuffixHexBytes)
	if _, err := rand.Read(b); err != nil { // crypto/rand failure is near-impossible; degrade to a fixed suffix
		return ObjectName(prefix + "00000000")
	}
	return ObjectName(prefix + hex.EncodeToString(b))
}

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
// ConfigMap, Secret, Grant, and EgressPolicy do NOT implement it.
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
	case KindConfigMap:
		cfg := &ConfigMap{}
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
	case KindBucket:
		b := &Bucket{}
		b.TypeMeta = typeMetaFor(k)
		return b, true
	case KindCatalogService:
		cs := &CatalogService{}
		cs.TypeMeta = typeMetaFor(k)
		return cs, true
	case KindPolicy:
		pol := &Policy{}
		pol.TypeMeta = typeMetaFor(k)
		return pol, true
	case KindWorkflow:
		wf := &Workflow{}
		wf.TypeMeta = typeMetaFor(k)
		return wf, true
	case KindSensor:
		se := &Sensor{}
		se.TypeMeta = typeMetaFor(k)
		return se, true
	case KindWorkflowRun:
		wr := &WorkflowRun{}
		wr.TypeMeta = typeMetaFor(k)
		return wr, true
	case KindIdentity:
		id := &Identity{}
		id.TypeMeta = typeMetaFor(k)
		return id, true
	case KindRole:
		ro := &Role{}
		ro.TypeMeta = typeMetaFor(k)
		return ro, true
	case KindRolesAssignment:
		ra := &RolesAssignment{}
		ra.TypeMeta = typeMetaFor(k)
		return ra, true
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
		KindConfigMap,
		KindSecret,
		KindGrant,
		KindEgressPolicy,
		KindInvocation,
		KindRuntimeClass,
		KindWorkerNode,
		KindGateway,
		KindKVStore,
		KindBucket,
		KindCatalogService,
		KindPolicy,
		KindWorkflow,
		KindWorkflowRun,
		KindSensor,
		KindIdentity,
		KindRole,
		KindRolesAssignment,
	}
}

// typeMetaFor returns a TypeMeta with the apiVersion and kind pre-filled.
func typeMetaFor(k Kind) TypeMeta {
	return TypeMeta{
		APIVersion: k.GVK().APIVersion(),
		Kind:       k,
	}
}
