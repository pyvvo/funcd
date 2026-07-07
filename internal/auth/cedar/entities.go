package cedar

import (
	"context"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// EntityProvider resolves ONLY the request-relevant Cedar entities (the principal Function, the
// resource KVTable, and its parent KVStore) for one Authorize call (ADR-0074) — never a full-store
// rebuild. Entities are materialized from the metastore's existing resources (no duplicate state):
// KVTable carries owner (a Function entity-REFERENCE, so principal == resource.owner compares
// entities) + namespace/resourceGroup; KVTable in KVStore via parents.
type EntityProvider interface {
	EntitiesFor(ctx context.Context, principal, resource auth.EntityRef) (cedartypes.EntityMap, error)
}

// MetaReader is the read-only metastore view the default EntityProvider needs (ADR-0074): it reads
// the caller Function (for the principal entity) and the target KVStore (for the resource KVTable's
// owner/parent). Declared here so the cedar package stays a near-leaf; the wiring adapts store.Store.
type MetaReader interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// metaEntityProvider is the default EntityProvider over the metastore (ADR-0074).
type metaEntityProvider struct{ r MetaReader }

// NewEntityProvider builds the metastore-backed EntityProvider (ADR-0074). r is required.
func NewEntityProvider(r MetaReader) (EntityProvider, error) {
	if r == nil {
		return nil, fault.Invalidf("cedar.NewEntityProvider", "meta reader is required")
	}
	return metaEntityProvider{r: r}, nil
}

// --- Cedar UID builders (the entity ID encodes the resource's namespaced identity) ----------------

func functionUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeFunction, cedartypes.String(string(ns)+"/"+string(name)))
}

func kvStoreUID(ns v1.NamespaceName, store v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeKVStore, cedartypes.String(string(ns)+"/"+string(store)))
}

func kvTableUID(ns v1.NamespaceName, store v1.ObjectName, table string) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeKVTable, cedartypes.String(string(ns)+"/"+string(store)+"/"+table))
}

func bucketUID(ns v1.NamespaceName, bucket v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeBucket, cedartypes.String(string(ns)+"/"+string(bucket)))
}

// blobPrefixUID is the BlobPrefix UID for a (bucket, prefix) sub-domain (ADR-0080). It mirrors
// kvTableUID's shape exactly so a `blobBindings` member and the resource UID are byte-identical and
// `principal.blobBindings.contains(resource)` matches.
func blobPrefixUID(ns v1.NamespaceName, bucket v1.ObjectName, prefix string) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeBlobPrefix, cedartypes.String(string(ns)+"/"+string(bucket)+"/"+prefix))
}

func s3IdentityUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeS3Identity, cedartypes.String(string(ns)+"/"+string(name)))
}

// principalUID maps a principal EntityRef to its Cedar UID. A Function is the in-platform,
// connection-scoped principal (KV/invoke/blob); an S3Identity is the external SigV4 principal
// (ADR-0080). Any other type is an Internal fault (a wiring bug).
func principalUID(p auth.EntityRef) (cedartypes.EntityUID, error) {
	switch p.Type {
	case v1.KindFunction:
		return functionUID(p.Namespace, p.Name), nil
	case v1.KindS3Identity:
		return s3IdentityUID(p.Namespace, p.Name), nil
	default:
		return cedartypes.EntityUID{}, fault.Internalf("cedar.principalUID", "principal kind %q is not modeled (only Function, S3Identity)", p.Type)
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
	default:
		return resourceTableUID(res)
	}
}

// EntitiesFor builds the request-relevant entity store (ADR-0074/0075): the principal Function and
// the resource entity. For KV the resource is the KVTable (with its owner entity-ref +
// namespace/resourceGroup attrs and its parent KVStore); for invoke (ADR-0075) the resource is the
// target Function. The principal Function additionally carries a `links` Set attribute (the caller's
// spec.links targets as Function entity-refs) so the built-in link::invoke permit can self-enforce
// "declared", and a `kvBindings` Set (the caller's spec.kv tables as KVTable entity-refs) so the
// built-in kv::read permit (ADR-0076) grants read on a bound table. Only the request-relevant entities
// are resolved — never a full-store rebuild.
func (p metaEntityProvider) EntitiesFor(ctx context.Context, principal, resource auth.EntityRef) (cedartypes.EntityMap, error) {
	const op = "cedar.EntitiesFor"
	em := cedartypes.EntityMap{}

	// Principal: the caller Function. Read it for its attributes (namespace/resourceGroup + the
	// link::invoke `links` Set); a missing caller still yields a bare principal entity so default-deny
	// applies (no permit can match).
	pUID, err := principalUID(principal)
	if err != nil {
		return nil, err
	}
	em[pUID] = cedartypes.Entity{
		UID:        pUID,
		Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(principal.Namespace)}),
	}
	if fobj, ferr := p.r.Get(ctx, v1.KindFunction.GVK(), principal.Namespace, principal.Name); ferr == nil {
		if fn, ok := fobj.(*v1.Function); ok {
			attrs := cedartypes.RecordMap{
				"namespace":     cedartypes.String(fn.Namespace),
				"resourceGroup": cedartypes.String(fn.ResourceGroup),
			}
			// links: the caller's spec.links targets as Function entity-refs (same namespace, ADR-0075),
			// so the built-in permit's principal.links.contains(resource) compares entities.
			links := make([]cedartypes.Value, 0, len(fn.Spec.Links))
			for _, l := range fn.Spec.Links {
				links = append(links, functionUID(principal.Namespace, l.Target))
			}
			attrs["links"] = cedartypes.NewSet(links...)
			// kvBindings: the caller's spec.kv tables as KVTable entity-refs (same namespace, ADR-0076),
			// so the built-in permit's principal.kvBindings.contains(resource) grants kv::read on a bound
			// table. Per-table (not per-store): a binding grants read on that one table only.
			kvBindings := make([]cedartypes.Value, 0, len(fn.Spec.KV))
			for _, b := range fn.Spec.KV {
				kvBindings = append(kvBindings, kvTableUID(principal.Namespace, b.Store, b.Table))
			}
			attrs["kvBindings"] = cedartypes.NewSet(kvBindings...)
			// blobBindings: the caller's spec.blob (bucket, prefix) as BlobPrefix entity-refs (same
			// namespace, ADR-0080), so the built-in permit's principal.blobBindings.contains(resource)
			// grants s3::read on a bound prefix. Per-prefix (not per-bucket): a binding grants read on
			// that one (bucket, prefix) only — the exact KV per-table model.
			blobBindings := make([]cedartypes.Value, 0, len(fn.Spec.Blob))
			for _, b := range fn.Spec.Blob {
				blobBindings = append(blobBindings, blobPrefixUID(principal.Namespace, b.Bucket, b.Prefix))
			}
			attrs["blobBindings"] = cedartypes.NewSet(blobBindings...)
			em[pUID] = cedartypes.Entity{UID: pUID, Attributes: cedartypes.NewRecord(attrs)}
		}
	} else if fault.KindOf(ferr) == fault.NotFound {
		// Provider fallback (ADR-0088): an add-on provider (CatalogService, ADR-0086/0087) is a
		// first-class S3 principal — its spec.blob are its blobBindings. FUNCTION-FIRST: only reached
		// when no Function of this name exists, so a same-named Function always wins (deterministic).
		// The principal UID is unchanged (functionUID, name-based), so the s3::write owner comparison
		// still matches when a prefix's owner names this provider. A provider has no links/kv → those
		// Sets stay absent (inert).
		if csobj, cserr := p.r.Get(ctx, v1.KindCatalogService.GVK(), principal.Namespace, principal.Name); cserr == nil {
			if cs, ok := csobj.(*v1.CatalogService); ok {
				attrs := cedartypes.RecordMap{
					"namespace":     cedartypes.String(cs.Namespace),
					"resourceGroup": cedartypes.String(cs.ResourceGroup),
				}
				blobBindings := make([]cedartypes.Value, 0, len(cs.Spec.Blob))
				for _, b := range cs.Spec.Blob {
					blobBindings = append(blobBindings, blobPrefixUID(principal.Namespace, b.Bucket, b.Prefix))
				}
				attrs["blobBindings"] = cedartypes.NewSet(blobBindings...)
				em[pUID] = cedartypes.Entity{UID: pUID, Attributes: cedartypes.NewRecord(attrs)}
			}
		} else if fault.KindOf(cserr) != fault.NotFound {
			return nil, fault.Wrapf(cserr, fault.Internal, op, "get catalogservice %q", principal.Name)
		}
	} else {
		return nil, fault.Wrapf(ferr, fault.Internal, op, "get function %q", principal.Name)
	}

	// Resource: invoke (ADR-0075) addresses a target Function; KV (ADR-0074) addresses a KVTable;
	// S3 (ADR-0080) addresses a BlobPrefix (a Bucket name + a prefix Path).
	if resource.Type == v1.KindFunction {
		rUID := functionUID(resource.Namespace, resource.Name)
		// A bare target entity suffices: the built-in permit reads principal.links, not the target's
		// attrs, so default-deny holds even if the target Function is missing from the store.
		em[rUID] = cedartypes.Entity{
			UID:        rUID,
			Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)}),
		}
		return em, nil
	}
	if resource.Type == v1.KindBucket {
		return p.bucketEntities(ctx, em, resource)
	}

	// Resource: the KVTable + its parent KVStore. Read the store for the table's owner + attrs.
	tUID, err := resourceTableUID(resource)
	if err != nil {
		return nil, err
	}
	sUID := kvStoreUID(resource.Namespace, resource.Name)

	sobj, serr := p.r.Get(ctx, v1.KindKVStore.GVK(), resource.Namespace, resource.Name)
	if serr != nil {
		if fault.KindOf(serr) == fault.NotFound {
			// A dangling resource: emit bare entities so the request is evaluable (default-deny;
			// the built-in owner-forbid has no owner to match ⇒ writes denied too).
			em[sUID] = cedartypes.Entity{UID: sUID}
			em[tUID] = cedartypes.Entity{UID: tUID, Parents: cedartypes.NewEntityUIDSet(sUID)}
			return em, nil
		}
		return nil, fault.Wrapf(serr, fault.Internal, op, "get kvstore %q", resource.Name)
	}
	ks, ok := sobj.(*v1.KVStore)
	if !ok {
		return nil, fault.Internalf(op, "object %q is not a KVStore", resource.Name)
	}

	storeAttrs := cedartypes.RecordMap{
		"namespace":     cedartypes.String(ks.Namespace),
		"resourceGroup": cedartypes.String(ks.ResourceGroup),
	}
	em[sUID] = cedartypes.Entity{UID: sUID, Attributes: cedartypes.NewRecord(storeAttrs)}

	tableAttrs := cedartypes.RecordMap{
		"namespace":     cedartypes.String(ks.Namespace),
		"resourceGroup": cedartypes.String(ks.ResourceGroup),
	}
	// owner is a Function entity-REFERENCE so `principal == resource.owner` compares entities.
	for _, tb := range ks.Spec.Tables {
		if tb.Name == resource.Path && tb.Owner != "" {
			tableAttrs["owner"] = functionUID(ks.Namespace, tb.Owner)
			break
		}
	}
	em[tUID] = cedartypes.Entity{
		UID:        tUID,
		Parents:    cedartypes.NewEntityUIDSet(sUID),
		Attributes: cedartypes.NewRecord(tableAttrs),
	}
	return em, nil
}

// bucketEntities materializes the resource BlobPrefix + its parent Bucket (ADR-0080) — the exact
// KVTable/KVStore mirror. The BlobPrefix carries its `owner` (a Function entity-REFERENCE) read from
// the Bucket CRD's spec.prefixes[].owner, so the write owner-forbid compares `principal == resource.
// owner`; an owner-less prefix carries no owner attr (so `resource has owner` is false → read-only).
func (p metaEntityProvider) bucketEntities(ctx context.Context, em cedartypes.EntityMap, resource auth.EntityRef) (cedartypes.EntityMap, error) {
	const op = "cedar.EntitiesFor"
	pfxUID, err := resourceBlobPrefixUID(resource)
	if err != nil {
		return nil, err
	}
	bUID := bucketUID(resource.Namespace, resource.Name)

	bobj, berr := p.r.Get(ctx, v1.KindBucket.GVK(), resource.Namespace, resource.Name)
	if berr != nil {
		if fault.KindOf(berr) == fault.NotFound {
			// A dangling resource: emit bare entities so the request is evaluable (default-deny;
			// the built-in owner-forbid has no owner to match ⇒ writes denied too).
			em[bUID] = cedartypes.Entity{UID: bUID}
			em[pfxUID] = cedartypes.Entity{UID: pfxUID, Parents: cedartypes.NewEntityUIDSet(bUID)}
			return em, nil
		}
		return nil, fault.Wrapf(berr, fault.Internal, op, "get bucket %q", resource.Name)
	}
	bkt, ok := bobj.(*v1.Bucket)
	if !ok {
		return nil, fault.Internalf(op, "object %q is not a Bucket", resource.Name)
	}

	bucketAttrs := cedartypes.RecordMap{
		"namespace":     cedartypes.String(bkt.Namespace),
		"resourceGroup": cedartypes.String(bkt.ResourceGroup),
	}
	em[bUID] = cedartypes.Entity{UID: bUID, Attributes: cedartypes.NewRecord(bucketAttrs)}

	prefixAttrs := cedartypes.RecordMap{
		"namespace":     cedartypes.String(bkt.Namespace),
		"resourceGroup": cedartypes.String(bkt.ResourceGroup),
	}
	// owner is a Function entity-REFERENCE so `principal == resource.owner` compares entities.
	for _, pfx := range bkt.Spec.Prefixes {
		if pfx.Name == resource.Path && pfx.Owner != "" {
			prefixAttrs["owner"] = functionUID(bkt.Namespace, pfx.Owner)
			break
		}
	}
	em[pfxUID] = cedartypes.Entity{
		UID:        pfxUID,
		Parents:    cedartypes.NewEntityUIDSet(bUID),
		Attributes: cedartypes.NewRecord(prefixAttrs),
	}
	return em, nil
}
