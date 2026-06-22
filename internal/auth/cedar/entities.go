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

// principalUID maps a principal EntityRef to its Cedar UID. Only Function principals are modeled
// for KV (ADR-0074); an unmodeled type is an Internal fault (a wiring bug).
func principalUID(p auth.EntityRef) (cedartypes.EntityUID, error) {
	if p.Type != v1.KindFunction {
		return cedartypes.EntityUID{}, fault.Internalf("cedar.principalUID", "principal kind %q is not modeled (only Function)", p.Type)
	}
	return functionUID(p.Namespace, p.Name), nil
}

// resourceTableUID maps a resource EntityRef (a KVStore name + a table Path) to its KVTable UID
// (ADR-0074). The KV PEP addresses a table as {Type: KindKVStore, Name: store, Path: table}.
func resourceTableUID(res auth.EntityRef) (cedartypes.EntityUID, error) {
	if res.Type != v1.KindKVStore || res.Path == "" {
		return cedartypes.EntityUID{}, fault.Internalf("cedar.resourceTableUID", "resource must be a KVStore with a table Path (got %q path=%q)", res.Type, res.Path)
	}
	return kvTableUID(res.Namespace, res.Name, res.Path), nil
}

// resourceUID maps a resource EntityRef to its Cedar UID, dispatching on type (ADR-0075): a
// KVStore-with-Path is a KVTable (KV, ADR-0074); a Function is the invoke target (link::invoke).
// An unmodeled resource is an Internal fault (a wiring bug).
func resourceUID(res auth.EntityRef) (cedartypes.EntityUID, error) {
	if res.Type == v1.KindFunction {
		return functionUID(res.Namespace, res.Name), nil
	}
	return resourceTableUID(res)
}

// EntitiesFor builds the request-relevant entity store (ADR-0074/0075): the principal Function and
// the resource entity. For KV the resource is the KVTable (with its owner entity-ref +
// namespace/resourceGroup attrs and its parent KVStore); for invoke (ADR-0075) the resource is the
// target Function. The principal Function additionally carries a `links` Set attribute (the caller's
// spec.links targets as Function entity-refs) so the built-in link::invoke permit can self-enforce
// "declared". Only the request-relevant entities are resolved — never a full-store rebuild.
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
			em[pUID] = cedartypes.Entity{UID: pUID, Attributes: cedartypes.NewRecord(attrs)}
		}
	} else if fault.KindOf(ferr) != fault.NotFound {
		return nil, fault.Wrapf(ferr, fault.Internal, op, "get function %q", principal.Name)
	}

	// Resource: invoke (ADR-0075) addresses a target Function; KV (ADR-0074) addresses a KVTable.
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
