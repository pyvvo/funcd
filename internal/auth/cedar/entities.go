package cedar

import (
	"context"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// EntityProvider resolves ONLY the request-relevant Cedar entities (the principal + the resource + its
// parents) for one Authorize call (ADR-0074) — never a full-store rebuild. Entities are materialized
// from the metastore's existing resources (no duplicate state); the registry (ADR-0116) composes it
// from the registered capabilities' resource materializers + principal-binding Sets.
type EntityProvider interface {
	EntitiesFor(ctx context.Context, principal, resource auth.EntityRef) (cedartypes.EntityMap, error)
}

// MetaReader is the read-only metastore view the EntityProvider needs (ADR-0074): it reads the caller
// Function (for the principal entity) and the target resource (for the owner/parent). Declared here so
// the cedar package stays a near-leaf; the wiring adapts store.Store.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// NewEntityProvider builds the default metastore-backed EntityProvider (ADR-0074): the composite
// provider assembled from the default capability registry (kv, invoke, s3 + the Function/CatalogService
// sources). r is required. Consumers that register additional capabilities build their own Registry and
// call (*Registry).EntityProvider directly (ADR-0116).
func NewEntityProvider(r MetaReader) (EntityProvider, error) {
	return defaultRegistry.EntityProvider(r)
}

// registryProvider is the composite EntityProvider assembled by a Registry (ADR-0116): per call it
// resolves the principal via the ordered sources, attaches every capability's PrincipalBinding Set, then
// dispatches the resource to the first capability whose Resource returns ok==true.
type registryProvider struct {
	reg *Registry
	r   MetaReader
}

// EntityProvider returns the composite provider over r (ADR-0116). r is required.
func (reg *Registry) EntityProvider(r MetaReader) (EntityProvider, error) {
	if r == nil {
		return nil, fault.Invalidf("cedar.Registry.EntityProvider", "meta reader is required")
	}
	return registryProvider{reg: reg, r: r}, nil
}

// EntitiesFor builds the request-relevant entity store (ADR-0074/0116): the principal entity (with every
// capability's binding Set) and the resource entity (dispatched to the owning capability). Only the
// request-relevant entities are resolved — never a full-store rebuild.
func (p registryProvider) EntitiesFor(ctx context.Context, principal, resource auth.EntityRef) (cedartypes.EntityMap, error) {
	const op = "cedar.EntitiesFor"
	em := cedartypes.EntityMap{}

	// Principal: a bare entity first (namespace attr only) so a missing/unresolved principal still yields
	// an evaluable entity and default-deny applies (no permit can match).
	pUID, err := principalUID(principal)
	if err != nil {
		return nil, err
	}
	em[pUID] = cedartypes.Entity{
		UID:        pUID,
		Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(principal.Namespace)}),
	}

	// Resolve the principal's backing object via the sources, each of which resolves only its own principal
	// type (ADR-0175). The first ok wins; none ⇒ the bare principal above stands (default-deny).
	var obj PrincipalObject
	for _, src := range p.reg.sources {
		o, ok, serr := src(ctx, p.r, principal)
		if serr != nil {
			return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "resolve principal %q", principal.Name)
		}
		if ok {
			obj = o
			break
		}
	}
	if obj != nil {
		ns, rg := principalMeta(obj)
		attrs := cedartypes.RecordMap{
			"namespace":     cedartypes.String(ns),
			"resourceGroup": cedartypes.String(rg),
		}
		// Attach every capability's binding Set. A capability whose Bind returns nil for this source type
		// contributes nothing (attr absent — e.g. kv/invoke for a CatalogService); a non-nil (possibly
		// empty) slice sets the attr (a Function always carries links/kvBindings/blobBindings, matching
		// today's EntitiesFor — the built-ins `has`-guard, so empty ≡ absent).
		for _, c := range p.reg.caps {
			if c.PrincipalBinding.Attr == "" || c.PrincipalBinding.Bind == nil {
				continue
			}
			members := c.PrincipalBinding.Bind(principal.Namespace, obj)
			if members == nil {
				continue
			}
			vals := make([]cedartypes.Value, 0, len(members))
			for _, uid := range members {
				vals = append(vals, uid)
			}
			attrs[cedartypes.String(c.PrincipalBinding.Attr)] = cedartypes.NewSet(vals...)
		}
		em[pUID] = cedartypes.Entity{UID: pUID, Attributes: cedartypes.NewRecord(attrs)}
	}

	// Resource: dispatch to the first capability whose Resource returns ok==true (the sole dispatch
	// mechanism — never a v1.Kind↔entity-type match). None ⇒ an unmodeled resource (a wiring bug).
	for _, c := range p.reg.caps {
		if c.Resource == nil {
			continue
		}
		rem, ok, rerr := c.Resource(ctx, p.r, resource)
		if rerr != nil {
			return nil, rerr
		}
		if !ok {
			continue
		}
		for k, v := range rem {
			em[k] = v
		}
		return em, nil
	}
	return nil, fault.Internalf(op, "resource kind %q is not modeled by any capability", resource.Type)
}

// principalMeta reads a resolved principal object's namespace + resourceGroup (a closed type-switch over
// the PrincipalObject set — a principal-level detail, not a per-capability branch).
func principalMeta(obj PrincipalObject) (v1.NamespaceName, v1.ResourceGroupName) {
	switch o := obj.(type) {
	case *v1.Function:
		return o.Namespace, o.ResourceGroup
	case *v1.CatalogService:
		return o.Namespace, o.ResourceGroup
	default:
		return "", ""
	}
}

// --- The driver request-path UID mappers (cedar.go builds the cedar Request from these) ------------

// principalUID maps a principal EntityRef to its Cedar UID. A Function is the in-platform,
// connection-scoped principal (KV/invoke/blob); a CatalogService is its engine's principal (ADR-0175);
// an S3Identity is the external SigV4 principal (ADR-0080). Any other type is an Internal fault (a
// wiring bug).
func principalUID(p auth.EntityRef) (cedartypes.EntityUID, error) {
	switch p.Type {
	case v1.KindFunction:
		return functionUID(p.Namespace, p.Name), nil
	case v1.KindS3Identity:
		return s3IdentityUID(p.Namespace, p.Name), nil
	case v1.KindIdentity:
		return identityUID(p.Namespace, p.Name), nil
	case v1.KindCatalogService:
		return catalogServiceUID(p.Namespace, p.Name), nil
	default:
		return cedartypes.EntityUID{}, fault.Internalf("cedar.principalUID", "principal kind %q is not modeled (only Function, CatalogService, S3Identity, Identity)", p.Type)
	}
}

// resourceTableUID maps a resource EntityRef (a KVStore name + a table Path) to its KVTable UID
// (ADR-0074). The KV PEP addresses a table as {Type: KindKVStore, Name: store, Path: table}.
func resourceTableUID(res auth.EntityRef) (cedartypes.EntityUID, error) {
	if res.Type != v1.KindKVStore || res.Path == "" {
		return cedartypes.EntityUID{}, fault.Internalf("cedar.resourceTableUID", "resource must be a KVStore with a table Path (got %q path=%q)", res.Type, res.Path)
	}
	return kvTableUID(res.Namespace, res.Name, res.Path), nil
}

// resourceBlobPrefixUID maps a resource EntityRef (a Bucket name + a prefix Path) to its BlobPrefix
// UID (ADR-0080). The blob PEP addresses a prefix as {Type: KindBucket, Name: bucket, Path: prefix} —
// the exact KVTable shape so the materialized UID matches the principal's blobBindings member.
func resourceBlobPrefixUID(res auth.EntityRef) (cedartypes.EntityUID, error) {
	if res.Type != v1.KindBucket || res.Path == "" {
		return cedartypes.EntityUID{}, fault.Internalf("cedar.resourceBlobPrefixUID", "resource must be a Bucket with a prefix Path (got %q path=%q)", res.Type, res.Path)
	}
	return blobPrefixUID(res.Namespace, res.Name, res.Path), nil
}

// resourceUID maps a resource EntityRef to its Cedar UID, dispatching on type (ADR-0075/0080): a
// KVStore-with-Path is a KVTable (KV, ADR-0074); a Bucket-with-Path is a BlobPrefix (S3, ADR-0080);
// a Function is the invoke target (link::invoke). An unmodeled resource is an Internal fault (a
// wiring bug).
func resourceUID(res auth.EntityRef) (cedartypes.EntityUID, error) {
	switch res.Type {
	case v1.KindFunction:
		return functionUID(res.Namespace, res.Name), nil
	case v1.KindBucket:
		return resourceBlobPrefixUID(res)
	case v1.KindCatalogService:
		// ADR-0137 (F102): the catalog::query resource — CatalogService::"<ns>/<catalog>", byte-matching
		// what CatalogCapability.Resource emits and a Function's catalogBindings members.
		return catalogServiceUID(res.Namespace, res.Name), nil
	case v1.KindNetDestination:
		// ADR-0117 (F81): the egress destination — its UID is the Path's "<ip>:<port>" prefix,
		// byte-matching what EgressCapability.Resource emits (mirroring principalUID's S3Identity case).
		nd, err := auth.ParseNetDestPath(res.Path)
		if err != nil {
			return cedartypes.EntityUID{}, fault.Wrapf(err, fault.Internal, "cedar.resourceUID", "parse NetDestination path %q", res.Path)
		}
		return netDestUID(nd.UIDString()), nil
	default:
		return resourceTableUID(res)
	}
}
