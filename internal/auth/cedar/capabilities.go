package cedar

import (
	"context"
	_ "embed"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
)

// The always-on built-in rules the driver ships (NOT user Policies) are authored as Cedar in the
// `.cedar` files alongside this one and embedded — far more legible/editable than inline Go strings.
// Each is owned by its Capability (its Builtin field); the registry concatenates them into the
// built-in PolicySet (ADR-0116). builtin_kv.cedar: kv::write single-writer. builtin_kv_read.cedar
// (ADR-0076): a declared spec.kv binding grants kv::read. builtin_invoke.cedar (ADR-0075): a declared
// link grants invoke. builtin_s3.cedar (ADR-0080): spec.blob binding-as-read-grant + prefix-owner write.

//go:embed builtin_kv.cedar
var builtinKVPolicies string

//go:embed builtin_kv_read.cedar
var builtinKVReadPolicies string

//go:embed builtin_invoke.cedar
var builtinInvokePolicies string

//go:embed builtin_s3.cedar
var builtinS3Policies string

//go:embed builtin_s3_dev.cedar
var builtinS3DevPolicies string

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

// KVCapability is the kv::read/kv::write capability (ADR-0074/0076): spec.kv → the `kvBindings` Set of
// KVTable entity-refs (read binding-as-grant); the resource is the KVTable + its parent KVStore + the
// table owner (a Function entity-REFERENCE, so `principal == resource.owner` compares entities). It owns
// both built-ins: the write single-writer rule and the read binding-grant.
func KVCapability() Capability {
	return Capability{
		Name:                  "kv",
		Actions:               []auth.Action{auth.ActionKVRead, auth.ActionKVWrite},
		EmitsEntityType:       entityTypeKVTable,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeKVStore},
		Resource:              kvResource,
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
		Builtin: builtinKVPolicies + "\n" + builtinKVReadPolicies,
	}
}

// kvResource materializes the resource KVTable + its parent KVStore + owner (ADR-0074). ok==false for a
// non-KVStore resource (defer to the next capability); a dangling store emits bare entities (ok==true,
// default-deny).
func kvResource(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
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
func S3Capability() Capability { return s3Capability(builtinS3Policies) }

// S3CapabilityDevRelaxedWrites is the DEV-ONLY variant (funcdctl dev): identical to S3Capability but its
// built-in DROPS the single-writer write forbid, so any authenticated principal may write any prefix
// locally. This unblocks two dev realities the prod model (correctly) does not: seeding a no-owner drop
// prefix (`aws s3 cp` into `landing`), and a producer writing a prefix whose dev-provisioned owner was
// mis-inferred from bindings (which cannot tell producer from consumer). Reads stay binding-gated; NEVER
// used in production, where write == prefix owner is the medallion-layer consistency invariant.
func S3CapabilityDevRelaxedWrites() Capability { return s3Capability(builtinS3DevPolicies) }

// s3Capability builds the s3::read/s3::write capability with the given built-in PolicySet (ADR-0080): the
// two variants differ ONLY in that built-in (prod single-writer vs the dev no-owner-write relaxation).
func s3Capability(builtin string) Capability {
	return Capability{
		Name:                  "s3",
		Actions:               []auth.Action{auth.ActionS3Read, auth.ActionS3Write},
		EmitsEntityType:       entityTypeBlobPrefix,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeBucket, entityTypeS3Identity},
		Resource:              s3Resource,
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
func s3Resource(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
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
