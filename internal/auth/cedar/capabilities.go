package cedar

import (
	"context"
	_ "embed"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// The always-on built-in rules the driver ships (NOT user Policies) are authored as Cedar in the
// `.cedar` files alongside this one and embedded — far more legible/editable than inline Go strings.
// Each is owned by its Capability (its Builtin field); the registry concatenates them into the
// built-in PolicySet (ADR-0116). builtin_kv.cedar: spec.kv binding-as-read-grant + generalized single-writer (ADR-0076/0136). builtin_invoke.cedar (ADR-0075): a declared
// link grants invoke. builtin_s3.cedar (ADR-0080): spec.blob binding-as-read-grant + prefix-owner write.

//go:embed builtin_kv.cedar
var builtinKVPolicies string

//go:embed builtin_invoke.cedar
var builtinInvokePolicies string

//go:embed builtin_s3.cedar
var builtinS3Policies string

//go:embed builtin_catalog.cedar
var builtinCatalogPolicies string

// --- Cedar UID builders (the entity ID encodes the resource's namespaced identity) ----------------
// Shared by the capability materializers below and by principalUID/resourceUID (the driver's request
// path in cedar.go).

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

// identityUID is the Cedar UID for a user-assigned managed identity (ADR-0135): Identity::"<ns>/<name>".
func identityUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeIdentity, cedartypes.String(string(ns)+"/"+string(name)))
}

// catalogServiceUID is the CatalogService UID for a (namespace, catalog) serving-layer resource
// (ADR-0137): CatalogService::"<ns>/<catalog>". It mirrors bucketUID's shape so a `catalogBindings`
// member and the request resource UID are byte-identical and `principal.catalogBindings.contains(resource)`
// matches.
func catalogServiceUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeCatalogService, cedartypes.String(string(ns)+"/"+string(name)))
}

// KVCapability is the kv::read/kv::write capability (ADR-0074/0076): spec.kv → the `kvBindings` Set of
// KVTable entity-refs (read binding-as-grant); the resource is the KVTable + its parent KVStore + the
// table owner (a Function entity-REFERENCE, so `principal == resource.owner` compares entities). It owns
// both built-ins: the write single-writer rule and the read binding-grant.
func KVCapability() Capability { return kvCapability(nil) }

// KVCapabilityWithWriters is KVCapability with a WriterLister so a writer-role RolesAssignment (ADR-0136)
// contributes to a table's `writers` set. KVCapability() (nil lister) is owner-only (back-compat).
func KVCapabilityWithWriters(w WriterLister) Capability { return kvCapability(w) }

func kvCapability(writers WriterLister) Capability {
	return Capability{
		Name:                  "kv",
		Actions:               []auth.Action{auth.ActionKVRead, auth.ActionKVWrite},
		EmitsEntityType:       entityTypeKVTable,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeKVStore},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return kvResource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "kvBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				fn, ok := src.(*v1.Function)
				if !ok {
					return nil // providers have no spec.kv → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(fn.Spec.KV))
				for _, b := range fn.Spec.KV {
					out = append(out, kvTableUID(ns, b.Store, b.Table))
				}
				return out
			},
		},
		Builtin: builtinKVPolicies,
	}
}

// kvResource materializes the resource KVTable + its parent KVStore + owner (ADR-0074). ok==false for a
// non-KVStore resource (defer to the next capability); a dangling store emits bare entities (ok==true,
// default-deny).
func kvResource(ctx context.Context, r MetaReader, resource auth.EntityRef, writers WriterLister) (cedartypes.EntityMap, bool, error) {
	const op = "cedar.KVCapability.Resource"
	if resource.Type != v1.KindKVStore || resource.Path == "" {
		return nil, false, nil
	}
	em := cedartypes.EntityMap{}
	tUID := kvTableUID(resource.Namespace, resource.Name, resource.Path)
	sUID := kvStoreUID(resource.Namespace, resource.Name)

	sobj, serr := r.Get(ctx, v1.KindKVStore.GVK(), resource.Namespace, resource.Name)
	if serr != nil {
		if fault.KindOf(serr) == fault.NotFound {
			// A dangling resource: emit bare entities so the request is evaluable (default-deny;
			// the built-in owner-forbid has no owner to match ⇒ writes denied too).
			em[sUID] = cedartypes.Entity{UID: sUID}
			em[tUID] = cedartypes.Entity{UID: tUID, Parents: cedartypes.NewEntityUIDSet(sUID)}
			return em, true, nil
		}
		return nil, false, fault.Wrapf(serr, fault.Internal, op, "get kvstore %q", resource.Name)
	}
	ks, ok := sobj.(*v1.KVStore)
	if !ok {
		return nil, false, fault.Internalf(op, "object %q is not a KVStore", resource.Name)
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
	// writers: the legacy owner (Function ref, back-compat) PLUS writer-role RolesAssignment grants
	// (ADR-0136). Empty ⇒ the forbid fires (owner-less/unassigned ⇒ read-only, default-deny).
	var writerVals []cedartypes.Value
	for _, tb := range ks.Spec.Tables {
		if tb.Name == resource.Path && tb.Owner != "" {
			ownerUID := functionUID(ks.Namespace, tb.Owner)
			tableAttrs["owner"] = ownerUID // kept for back-compat (user policies compare resource.owner)
			writerVals = append(writerVals, ownerUID)
			break
		}
	}
	if writers != nil {
		for _, w := range writers.KVWriters(ctx, resource.Namespace, string(resource.Name), resource.Path) {
			writerVals = append(writerVals, w)
		}
	}
	if len(writerVals) > 0 {
		tableAttrs["writers"] = cedartypes.NewSet(writerVals...)
	}
	em[tUID] = cedartypes.Entity{
		UID:        tUID,
		Parents:    cedartypes.NewEntityUIDSet(sUID),
		Attributes: cedartypes.NewRecord(tableAttrs),
	}
	return em, true, nil
}

// InvokeCapability is the link::invoke capability (ADR-0075): spec.links → the `links` Set of target
// Function entities (link-as-grant); the resource is the target Function (a bare entity — the built-in
// permit reads principal.links, not the target's attrs, so default-deny holds even if the target is
// missing). It owns no principal-owned resource beyond the target Function.
func InvokeCapability() Capability {
	return Capability{
		Name:            "invoke",
		Actions:         []auth.Action{auth.ActionLinkInvoke},
		EmitsEntityType: entityTypeFunction,
		Resource:        invokeResource,
		PrincipalBinding: PrincipalBinding{
			Attr: "links",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				fn, ok := src.(*v1.Function)
				if !ok {
					return nil // providers have no spec.links → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(fn.Spec.Links))
				for _, l := range fn.Spec.Links {
					out = append(out, functionUID(ns, l.Target))
				}
				return out
			},
		},
		Builtin: builtinInvokePolicies,
	}
}

// invokeResource materializes the target Function as a bare entity (ADR-0075). ok==false for a
// non-Function resource.
func invokeResource(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
	if resource.Type != v1.KindFunction {
		return nil, false, nil
	}
	rUID := functionUID(resource.Namespace, resource.Name)
	em := cedartypes.EntityMap{
		rUID: {UID: rUID, Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)})},
	}
	return em, true, nil
}

// S3Capability is the s3::read/s3::write capability (ADR-0080): spec.blob → the `blobBindings` Set of
// BlobPrefix entity-refs (read binding-as-grant); the resource is the BlobPrefix + its parent Bucket +
// the prefix owner (a Function entity-REFERENCE). Its Bind handles BOTH a *v1.Function and a
// *v1.CatalogService (ADR-0088 — both carry spec.blob). It admits the external S3Identity principal type.
func S3Capability() Capability { return s3Capability(builtinS3Policies, nil) }

// S3CapabilityWithWriters is S3Capability with a WriterLister so a writer-role RolesAssignment (ADR-0136)
// contributes to a prefix's `writers` set (the generalized single-writer forbid). The compose root uses
// this; S3Capability() (nil lister) is owner-only (back-compat).
func S3CapabilityWithWriters(w WriterLister) Capability { return s3Capability(builtinS3Policies, w) }

// s3Capability builds the s3::read/s3::write capability with the built-in single-writer PolicySet (ADR-0080).
// writers (may be nil) contributes role-assigned writers to a prefix's `writers` set (ADR-0136) — how both
// prod and `funcdctl dev` let a non-owner principal write (dev grants the dev principal Blob Data Writer).
func s3Capability(builtin string, writers WriterLister) Capability {
	return Capability{
		Name:                  "s3",
		Actions:               []auth.Action{auth.ActionS3Read, auth.ActionS3Write},
		EmitsEntityType:       entityTypeBlobPrefix,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeBucket, entityTypeS3Identity, entityTypeIdentity},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return s3Resource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "blobBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				var blobs []v1.FunctionBlob
				switch o := src.(type) {
				case *v1.Function:
					blobs = o.Spec.Blob
				case *v1.CatalogService:
					blobs = o.Spec.Blob // ADR-0088: a provider's spec.blob are its blobBindings
				default:
					return nil
				}
				out := make([]cedartypes.EntityUID, 0, len(blobs))
				for _, b := range blobs {
					out = append(out, blobPrefixUID(ns, b.Bucket, b.Prefix))
				}
				return out
			},
		},
		Builtin: builtin,
	}
}

// s3Resource materializes the resource BlobPrefix + its parent Bucket + owner (ADR-0080) — the exact
// KVTable/KVStore mirror. ok==false for a non-Bucket resource; a dangling bucket emits bare entities.
func s3Resource(ctx context.Context, r MetaReader, resource auth.EntityRef, writers WriterLister) (cedartypes.EntityMap, bool, error) {
	const op = "cedar.S3Capability.Resource"
	if resource.Type != v1.KindBucket || resource.Path == "" {
		return nil, false, nil
	}
	em := cedartypes.EntityMap{}
	pfxUID := blobPrefixUID(resource.Namespace, resource.Name, resource.Path)
	bUID := bucketUID(resource.Namespace, resource.Name)

	bobj, berr := r.Get(ctx, v1.KindBucket.GVK(), resource.Namespace, resource.Name)
	if berr != nil {
		if fault.KindOf(berr) == fault.NotFound {
			em[bUID] = cedartypes.Entity{UID: bUID}
			em[pfxUID] = cedartypes.Entity{UID: pfxUID, Parents: cedartypes.NewEntityUIDSet(bUID)}
			return em, true, nil
		}
		return nil, false, fault.Wrapf(berr, fault.Internal, op, "get bucket %q", resource.Name)
	}
	bkt, ok := bobj.(*v1.Bucket)
	if !ok {
		return nil, false, fault.Internalf(op, "object %q is not a Bucket", resource.Name)
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
	// writers is the set of principals authorized to WRITE this prefix (ADR-0136 generalized single-writer):
	// the legacy owner (a Function ref — back-compat) PLUS any writer-role RolesAssignment grants. An empty
	// writers ⇒ the forbid fires (owner-less/unassigned ⇒ unwritable, default-deny). Type-agnostic: a role
	// grant can make an external Identity a writer.
	// The single `owner` attr is kept (back-compat for user policies that compare `principal ==
	// resource.owner`); `writers` is the set the generalized forbid consults (owner + role grants).
	var writerVals []cedartypes.Value
	for _, pfx := range bkt.Spec.Prefixes {
		if pfx.Name == resource.Path && pfx.Owner != "" {
			ownerUID := functionUID(bkt.Namespace, pfx.Owner)
			prefixAttrs["owner"] = ownerUID
			writerVals = append(writerVals, ownerUID)
			break
		}
	}
	if writers != nil {
		for _, w := range writers.BlobWriters(ctx, resource.Namespace, string(resource.Name), resource.Path) {
			writerVals = append(writerVals, w)
		}
	}
	if len(writerVals) > 0 {
		prefixAttrs["writers"] = cedartypes.NewSet(writerVals...)
	}
	em[pfxUID] = cedartypes.Entity{
		UID:        pfxUID,
		Parents:    cedartypes.NewEntityUIDSet(bUID),
		Attributes: cedartypes.NewRecord(prefixAttrs),
	}
	return em, true, nil
}

// CatalogCapability is the catalog::query capability (ADR-0137): spec.catalogs → the `catalogBindings`
// Set of CatalogService entity-refs (binding-as-query-grant); the resource is the CatalogService the
// query targets. It is READ-SHAPED — like invoke it owns no writable resource, so there is NO
// WriterLister and NO write action; the built-in permit reads principal.catalogBindings, not the
// resource's attrs, so default-deny holds even if the target is missing. External Identities are
// granted via a Catalog-scoped RolesAssignment (compiled to a permit), not this built-in.
func CatalogCapability() Capability {
	return Capability{
		Name:            "catalog",
		Actions:         []auth.Action{auth.ActionCatalogQuery},
		EmitsEntityType: entityTypeCatalogService,
		Resource:        catalogResource,
		PrincipalBinding: PrincipalBinding{
			Attr: "catalogBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				fn, ok := src.(*v1.Function)
				if !ok {
					return nil // providers have no spec.catalogs → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(fn.Spec.Catalogs))
				for _, b := range fn.Spec.Catalogs {
					out = append(out, catalogServiceUID(ns, b.Catalog))
				}
				return out
			},
		},
		Builtin: builtinCatalogPolicies,
	}
}

// catalogResource materializes the target CatalogService as a bare entity carrying its namespace attr
// (ADR-0137) — the exact invokeResource mirror. The namespace attr backs a Namespace-scoped
// RolesAssignment grant (resource.namespace == "<ns>"); the built-in binding permit needs only the UID.
// ok==false for a non-CatalogService resource (defer to the next capability); no MetaReader read — a
// missing CatalogService still yields an evaluable bare entity (default-deny).
func catalogResource(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
	if resource.Type != v1.KindCatalogService {
		return nil, false, nil
	}
	rUID := catalogServiceUID(resource.Namespace, resource.Name)
	em := cedartypes.EntityMap{
		rUID: {UID: rUID, Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)})},
	}
	return em, true, nil
}

// FunctionPrincipalSource resolves the principal's backing *v1.Function (the primary source, ADR-0074).
func FunctionPrincipalSource() PrincipalSource {
	return func(ctx context.Context, r MetaReader, p auth.EntityRef) (PrincipalObject, bool, error) {
		const op = "cedar.FunctionPrincipalSource"
		obj, err := r.Get(ctx, v1.KindFunction.GVK(), p.Namespace, p.Name)
		if err != nil {
			if fault.KindOf(err) == fault.NotFound {
				return nil, false, nil // defer to the next source (CatalogService fallback)
			}
			return nil, false, fault.Wrapf(err, fault.Internal, op, "get function %q", p.Name)
		}
		fn, ok := obj.(*v1.Function)
		if !ok {
			return nil, false, nil
		}
		return fn, true, nil
	}
}

// CatalogServicePrincipalSource is the ADR-0088 fallback: resolve a *v1.CatalogService when no Function
// of this name exists (Function-first). A provider is a first-class S3 principal — its spec.blob are its
// blobBindings; it has no links/kv.
func CatalogServicePrincipalSource() PrincipalSource {
	return func(ctx context.Context, r MetaReader, p auth.EntityRef) (PrincipalObject, bool, error) {
		const op = "cedar.CatalogServicePrincipalSource"
		obj, err := r.Get(ctx, v1.KindCatalogService.GVK(), p.Namespace, p.Name)
		if err != nil {
			if fault.KindOf(err) == fault.NotFound {
				return nil, false, nil
			}
			return nil, false, fault.Wrapf(err, fault.Internal, op, "get catalogservice %q", p.Name)
		}
		cs, ok := obj.(*v1.CatalogService)
		if !ok {
			return nil, false, nil
		}
		return cs, true, nil
	}
}

// netDestUID is the NetDestination UID for a "<ip>:<port>" id (ADR-0117). It is byte-matched by both
// EgressCapability.Resource (which materializes the entity) and resourceUID (the driver request path),
// so the request resource UID and the materialized entity agree.
func netDestUID(id string) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeNetDestination, cedartypes.String(id))
}

// EgressCapability registers egress on the ADR-0116 registry (ADR-0117, F81): Action egress::connect;
// resource entity NetDestination materialized from the request EntityRef's Path (no MetaReader read —
// the destination is ephemeral); NO PrincipalBinding (governed by EgressPolicy, not a spec.<field>
// grant) and NO Builtin (default-deny — grants come only from a compiled EgressPolicy). Its Resource
// emits the UID + attributes {ip, port, domains}; the driver's resourceUID gains a matching
// KindNetDestination case (entities.go).
func EgressCapability() Capability {
	return Capability{
		Name:            "egress",
		Actions:         []auth.Action{auth.ActionEgressConnect},
		EmitsEntityType: entityTypeNetDestination,
		Resource:        netDestinationResource,
		// PrincipalBinding: zero value ⇒ NONE. Builtin: "" ⇒ NONE (default-deny).
	}
}

// netDestinationResource materializes the NetDestination entity (UID + {ip, port, domains}) from the
// request EntityRef's Path — the B1 forwarder-attested destination the gateway built. ok==false for a
// non-NetDestination resource (defer to the next capability). No MetaReader read: the destination is
// ephemeral, never stored.
func netDestinationResource(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
	const op = "cedar.EgressCapability.Resource"
	if resource.Type != v1.KindNetDestination {
		return nil, false, nil
	}
	nd, err := auth.ParseNetDestPath(resource.Path)
	if err != nil {
		return nil, false, fault.Wrapf(err, fault.Internal, op, "parse NetDestination path %q", resource.Path)
	}
	// ip is a Cedar ip() value so a CIDR rule can `resource.ip.isInRange(ip("10.0.0.0/8"))`.
	ipVal, perr := cedartypes.ParseIPAddr(nd.IP.String())
	if perr != nil {
		return nil, false, fault.Wrapf(perr, fault.Internal, op, "parse dst ip %q", nd.IP)
	}
	domainVals := make([]cedartypes.Value, 0, len(nd.Domains))
	for _, d := range nd.Domains {
		domainVals = append(domainVals, cedartypes.String(d))
	}
	uid := netDestUID(nd.UIDString())
	attrs := cedartypes.RecordMap{
		"ip":      ipVal,
		"port":    cedartypes.Long(nd.Port),
		"domains": cedartypes.NewSet(domainVals...),
	}
	return cedartypes.EntityMap{uid: {UID: uid, Attributes: cedartypes.NewRecord(attrs)}}, true, nil
}
