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
// built-in PolicySet (ADR-0116). builtin_kv.cedar: spec.bindings.kv binding-as-read-grant + generalized single-writer (ADR-0076/0136). builtin_invoke.cedar (ADR-0075): a declared
// link grants invoke. builtin_s3.cedar (ADR-0080): spec.bindings.blob binding-as-read-grant + prefix-owner write.

//go:embed builtin_kv.cedar
var builtinKVPolicies string

//go:embed builtin_invoke.cedar
var builtinInvokePolicies string

//go:embed builtin_s3.cedar
var builtinS3Policies string

//go:embed builtin_lake.cedar
var builtinLakePolicies string

//go:embed builtin_addon.cedar
var builtinAddonPolicies string

//go:embed builtin_sql.cedar
var builtinSQLPolicies string

//go:embed builtin_job.cedar
var builtinJobPolicies string

// --- Cedar UID builders (the entity ID encodes the resource's namespaced identity) ----------------
// Shared by the capability materializers below and by principalUID/resourceUID (the driver's request
// path in cedar.go).

func functionUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeFunction, cedartypes.String(string(ns)+"/"+string(name)))
}

func jobUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeJob, cedartypes.String(string(ns)+"/"+string(name)))
}

// workloadUID is the entity of the workload ref names, by its kind (ADR-0351 Decision 3): a Function, a Lake and a
// Job of one name are three entities.
func workloadUID(ns v1.NamespaceName, ref v1.WorkloadRef) cedartypes.EntityUID {
	switch ref.Kind {
	case v1.KindLake:
		return lakeUID(ns, ref.Name)
	case v1.KindJob:
		return jobUID(ns, ref.Name)
	}
	return functionUID(ns, ref.Name)
}

// workloadBindings are the bindings of a Function or a Job principal, which the capabilities materialize alike
// (ADR-0351 Decision 1); any other principal has none of a workload's.
func workloadBindings(src PrincipalObject) (*v1.FunctionBindings, bool) {
	switch o := src.(type) {
	case *v1.Function:
		return &o.Spec.Bindings, true
	case *v1.Job:
		return &o.Spec.Bindings, true
	}
	return nil, false
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

// lakeUID is the Lake UID for a (namespace, lake) serving-layer resource
// (ADR-0137): Lake::"<ns>/<lake>". It mirrors bucketUID's shape so a `lakeBindings`
// member and the request resource UID are byte-identical and `principal.lakeBindings.contains(resource)`
// matches.
func lakeUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeLake, cedartypes.String(string(ns)+"/"+string(name)))
}

// sqlDatabaseUID is the SQLDatabase UID (ADR-0233): SQLDatabase::"<ns>/<database>", byte-identical to a
// `sqlBindings` member so `principal.sqlBindings.contains(resource)` matches.
func sqlDatabaseUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeSQLDatabase, cedartypes.String(string(ns)+"/"+string(name)))
}

// KVCapability is the kv::read/kv::write capability (ADR-0074/0076): spec.bindings.kv → the `kvBindings` Set of
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
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeKVStore, entityTypeJob},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return kvResource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "kvBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				bs, ok := workloadBindings(src)
				if !ok {
					return nil // providers have no spec.bindings.kv → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(bs.KV))
				for _, b := range bs.KV {
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
		if tb.Name == resource.Path && !tb.Owner.IsZero() {
			ownerUID := workloadUID(ks.Namespace, tb.Owner)
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

// InvokeCapability is the link::invoke capability (ADR-0075): spec.bindings.links → the `links` Set of target
// Function entities (link-as-grant); the resource is the target Function (a bare entity — the built-in
// permit reads principal.links, not the target's attrs, so default-deny holds even if the target is
// missing). It owns no principal-owned resource beyond the target Function.
func InvokeCapability() Capability {
	return Capability{
		Name:                  "invoke",
		Actions:               []auth.Action{auth.ActionLinkInvoke},
		EmitsEntityType:       entityTypeFunction,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeJob},
		Resource:              invokeResource,
		PrincipalBinding: PrincipalBinding{
			Attr: "links",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				bs, ok := workloadBindings(src)
				if !ok {
					return nil // providers have no spec.bindings.links → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(bs.Links))
				for _, l := range bs.Links {
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

// S3Capability is the s3::read/s3::write capability (ADR-0080): spec.bindings.blob → the `blobBindings` Set of
// BlobPrefix entity-refs (read binding-as-grant); the resource is the BlobPrefix + its parent Bucket +
// the prefix owner (a Function or a Lake entity, by the owner's kind, ADR-0275). Its Bind handles BOTH a *v1.Function and a
// *v1.Lake (ADR-0088 — both carry spec.bindings.blob). It admits the external S3Identity principal type.
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
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeBucket, entityTypeS3Identity, entityTypeIdentity, entityTypeJob},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return s3Resource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "blobBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				var blobs []v1.FunctionBlob
				switch o := src.(type) {
				case *v1.Function:
					blobs = o.Spec.Bindings.Blob
				case *v1.Job:
					blobs = o.Spec.Bindings.Blob
				case *v1.Lake:
					blobs = o.Spec.Bindings.Blob // ADR-0088: a provider's spec.bindings.blob are its blobBindings
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
	// the owner the Bucket names, a Function or a Lake by its kind with no other lookup (ADR-0275), PLUS any
	// writer-role RolesAssignment grants. An empty writers ⇒ the forbid fires (owner-less/unassigned ⇒ unwritable,
	// default-deny). Type-agnostic: a role grant can make an external Identity a writer.
	// The single `owner` attr is kept for user policies that compare `principal == resource.owner`; `writers` is the
	// set the generalized forbid consults (owner + role grants).
	var writerVals []cedartypes.Value
	for _, pfx := range bkt.Spec.Prefixes {
		if pfx.Name == resource.Path && !pfx.Owner.IsZero() {
			ownerUID := workloadUID(bkt.Namespace, pfx.Owner)
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

// LakeCapability is the lake::query capability (ADR-0137): spec.bindings.lake → the `lakeBindings`
// Set of Lake entity-refs (binding-as-query-grant); the resource is the Lake the
// query targets. It is READ-SHAPED — like invoke it owns no writable resource, so there is NO
// WriterLister and NO write action; the built-in permit reads principal.lakeBindings, not the
// resource's attrs, so default-deny holds even if the target is missing. External Identities are
// granted via a Lake-scoped RolesAssignment (compiled to a permit), not this built-in.
func LakeCapability() Capability {
	return Capability{
		Name:                  "lake",
		Actions:               []auth.Action{auth.ActionLakeQuery},
		EmitsEntityType:       entityTypeLake,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeJob},
		Resource:              lakeResource,
		PrincipalBinding: PrincipalBinding{
			Attr: "lakeBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				bs, ok := workloadBindings(src)
				if !ok {
					return nil // providers have no spec.bindings.lake → contribute nothing (inert)
				}
				out := make([]cedartypes.EntityUID, 0, len(bs.Lake))
				for _, b := range bs.Lake {
					out = append(out, lakeUID(ns, b.Lake))
				}
				return out
			},
		},
		Builtin: builtinLakePolicies,
	}
}

// lakeResource materializes the target Lake as a bare entity carrying its namespace attr
// (ADR-0137) — the exact invokeResource mirror. The namespace attr backs a Namespace-scoped
// RolesAssignment grant (resource.namespace == "<ns>"); the built-in binding permit needs only the UID.
// ok==false for a non-Lake resource (defer to the next capability); no MetaReader read — a
// missing Lake still yields an evaluable bare entity (default-deny).
func lakeResource(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
	if resource.Type != v1.KindLake {
		return nil, false, nil
	}
	rUID := lakeUID(resource.Namespace, resource.Name)
	em := cedartypes.EntityMap{
		rUID: {UID: rUID, Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)})},
	}
	return em, true, nil
}

// FunctionPrincipalSource resolves a Function principal's backing *v1.Function (ADR-0074); a principal of
// any other type is not its own (ADR-0175).
func FunctionPrincipalSource() PrincipalSource {
	return principalSource[*v1.Function](v1.KindFunction, "cedar.FunctionPrincipalSource", "function")
}

// JobPrincipalSource resolves a Job principal's backing *v1.Job (ADR-0351 Decision 1): every worker of its runs acts as
// the Job, never as a same-named Function, and its bindings are materialized as a Function's are.
func JobPrincipalSource() PrincipalSource {
	return principalSource[*v1.Job](v1.KindJob, "cedar.JobPrincipalSource", "job")
}

// LakePrincipalSource resolves a Lake principal's backing *v1.Lake (ADR-0088,
// ADR-0175): an engine is its own principal, never a same-named Function. Its spec.bindings.blob are its
// blobBindings; it has no links/kv.
func LakePrincipalSource() PrincipalSource {
	return principalSource[*v1.Lake](v1.KindLake, "cedar.LakePrincipalSource", "lake")
}

// AddonObjectPrincipalSource resolves an add-on object principal's backing *v1.AddonObject (ADR-0332 Decision 4): its
// engine asks for each of its Secrets as the object, as a Lake's does. Its spec.bindings.secret are its
// secretBindings; it has no other binding.
func AddonObjectPrincipalSource() PrincipalSource {
	return principalSource[*v1.AddonObject](v1.KindAddonObject, "cedar.AddonObjectPrincipalSource", "add-on object")
}

// principalSource resolves a principal of kind to its stored T; a principal of another type, or with no such object,
// is not its own (ADR-0175).
func principalSource[T PrincipalObject](kind v1.Kind, op, what string) PrincipalSource {
	return func(ctx context.Context, r MetaReader, p auth.EntityRef) (PrincipalObject, bool, error) {
		if p.Type != kind {
			return nil, false, nil
		}
		obj, err := r.Get(ctx, kind.GVK(), p.Namespace, p.Name)
		if err != nil {
			if fault.KindOf(err) == fault.NotFound {
				return nil, false, nil
			}
			return nil, false, fault.Wrapf(err, fault.Internal, op, "get %s %q", what, p.Name)
		}
		o, ok := obj.(T)
		if !ok {
			return nil, false, nil
		}
		return o, true, nil
	}
}

func addonObjectUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeAddonObject, cedartypes.String(string(ns)+"/"+string(name)))
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
		Name:                  "egress",
		Actions:               []auth.Action{auth.ActionEgressConnect},
		EmitsEntityType:       entityTypeNetDestination,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeJob},
		Resource:              netDestinationResource,
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

// SQLCapability is the sql::read/sql::write capability (ADR-0233): spec.bindings.sql → the `sqlBindings` Set of SQLDatabase
// entity-refs (read binding-as-grant); the resource is the SQLDatabase with its owner and `writers`. Owner-only writers.
func SQLCapability() Capability { return sqlCapability(nil) }

// SQLCapabilityWithWriters is SQLCapability with a WriterLister, so a writer-role RolesAssignment joins a database's
// `writers` set (ADR-0136).
func SQLCapabilityWithWriters(w WriterLister) Capability { return sqlCapability(w) }

// AddonCapability is the addon::call capability (ADR-0338 Decision 5): spec.bindings.addon → the `addonBindings` Set of
// AddonObject entity-refs (binding-as-call-grant); the resource is the add-on object whose engine the call reaches. Like
// lake::query it owns no writable resource: no WriterLister, no write action.
func AddonCapability() Capability {
	return Capability{
		Name:                  "addon",
		Actions:               []auth.Action{auth.ActionAddonCall},
		EmitsEntityType:       entityTypeAddonObject,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeJob},
		Resource:              namespacedResource(v1.KindAddonObject, addonObjectUID),
		PrincipalBinding: PrincipalBinding{
			Attr: "addonBindings",
			Bind: bindFunction(func(b *v1.FunctionBindings) []v1.FunctionAddon { return b.Addon },
				func(ns v1.NamespaceName, b v1.FunctionAddon) cedartypes.EntityUID {
					return addonObjectUID(ns, b.Object)
				}),
		},
		Builtin: builtinAddonPolicies,
	}
}

// namespacedResource is a Capability.Resource that materializes a resource of kind as a bare entity with its namespace
// attr, as lakeResource does: an add-on object for addon::call, a Job for job::start and job::read. A missing object
// still yields an evaluable entity (default-deny), and another resource gives ok==false.
func namespacedResource(kind v1.Kind, uidOf func(v1.NamespaceName, v1.ObjectName) cedartypes.EntityUID) func(context.Context, MetaReader, auth.EntityRef) (cedartypes.EntityMap, bool, error) {
	return func(_ context.Context, _ MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
		if resource.Type != kind {
			return nil, false, nil
		}
		uid := uidOf(resource.Namespace, resource.Name)
		return cedartypes.EntityMap{
			uid: {UID: uid, Attributes: cedartypes.NewRecord(cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)})},
		}, true, nil
	}
}

// JobCapability is the job::start and job::read capability (ADR-0349 Decision 1): spec.bindings.job → the
// `jobBindings` Set of Job entity-refs (binding-as-grant of both actions); the resource is the bound Job, which ADR-0351
// also makes a principal. Like addon::call it owns no writable resource: no WriterLister, and a missing Job still
// yields an evaluable entity.
func JobCapability() Capability {
	return Capability{
		Name:            "job",
		Actions:         []auth.Action{auth.ActionJobStart, auth.ActionJobRead},
		EmitsEntityType: entityTypeJob,
		Resource:        namespacedResource(v1.KindJob, jobUID),
		PrincipalBinding: PrincipalBinding{
			Attr: "jobBindings",
			Bind: bindFunction(func(b *v1.FunctionBindings) []v1.FunctionJob { return b.Job },
				func(ns v1.NamespaceName, b v1.FunctionJob) cedartypes.EntityUID { return jobUID(ns, b.Job) }),
		},
		Builtin: builtinJobPolicies,
	}
}

// bindFunction is a PrincipalBinding.Bind over a Function's or a Job's bindings, one entity per binding; any other
// principal contributes nothing.
func bindFunction[B v1.FunctionSQL | v1.FunctionLake | v1.FunctionLink | v1.FunctionAddon | v1.FunctionJob](bindings func(*v1.FunctionBindings) []B, uid func(v1.NamespaceName, B) cedartypes.EntityUID) func(v1.NamespaceName, PrincipalObject) []cedartypes.EntityUID {
	return func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
		all, ok := workloadBindings(src)
		if !ok {
			return nil
		}
		bs := bindings(all)
		out := make([]cedartypes.EntityUID, 0, len(bs))
		for _, b := range bs {
			out = append(out, uid(ns, b))
		}
		return out
	}
}

func sqlCapability(writers WriterLister) Capability {
	return Capability{
		Name:                  "sql",
		Actions:               []auth.Action{auth.ActionSQLRead, auth.ActionSQLWrite},
		EmitsEntityType:       entityTypeSQLDatabase,
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeJob},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return sqlResource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "sqlBindings",
			Bind: bindFunction(func(b *v1.FunctionBindings) []v1.FunctionSQL { return b.SQL },
				func(ns v1.NamespaceName, b v1.FunctionSQL) cedartypes.EntityUID {
					return sqlDatabaseUID(ns, b.Database)
				}),
		},
		Builtin: builtinSQLPolicies,
	}
}

// sqlResource materializes the SQLDatabase with namespace, resourceGroup, owner and writers (ADR-0233). ok==false for
// another resource; a missing database is a bare entity with its namespace (default-deny for writes).
func sqlResource(ctx context.Context, r MetaReader, resource auth.EntityRef, writers WriterLister) (cedartypes.EntityMap, bool, error) {
	const op = "cedar.SQLCapability.Resource"
	if resource.Type != v1.KindSQLDatabase {
		return nil, false, nil
	}
	uid := sqlDatabaseUID(resource.Namespace, resource.Name)
	attrs := cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)}
	obj, err := r.Get(ctx, v1.KindSQLDatabase.GVK(), resource.Namespace, resource.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return cedartypes.EntityMap{uid: {UID: uid, Attributes: cedartypes.NewRecord(attrs)}}, true, nil
	case err != nil:
		return nil, false, fault.Wrapf(err, fault.Internal, op, "get sqldatabase %q", resource.Name)
	}
	db, ok := obj.(*v1.SQLDatabase)
	if !ok {
		return nil, false, fault.Internalf(op, "object %q is not a SQLDatabase", resource.Name)
	}
	attrs["resourceGroup"] = cedartypes.String(db.ResourceGroup)
	var listed []cedartypes.EntityUID
	if writers != nil {
		listed = writers.SQLWriters(ctx, resource.Namespace, string(resource.Name))
	}
	addOwnerAndWriters(attrs, db.Namespace, db.Spec.Owner, listed)
	return cedartypes.EntityMap{uid: {UID: uid, Attributes: cedartypes.NewRecord(attrs)}}, true, nil
}

// addOwnerAndWriters sets a resource's `owner`, its owner Function when named, and `writers`, that owner plus the
// writer-role grants listed (ADR-0136); a resource with neither gets no `writers`, so a single-writer forbid fires.
func addOwnerAndWriters(attrs cedartypes.RecordMap, ns v1.NamespaceName, owner v1.WorkloadRef, listed []cedartypes.EntityUID) {
	var writerVals []cedartypes.Value
	if !owner.IsZero() {
		ownerUID := workloadUID(ns, owner)
		attrs["owner"] = ownerUID
		writerVals = append(writerVals, ownerUID)
	}
	for _, w := range listed {
		writerVals = append(writerVals, w)
	}
	if len(writerVals) > 0 {
		attrs["writers"] = cedartypes.NewSet(writerVals...)
	}
}

//go:embed builtin_secret.cedar
var builtinSecretPolicies string

func secretUID(ns v1.NamespaceName, name v1.ObjectName) cedartypes.EntityUID {
	return cedartypes.NewEntityUID(entityTypeSecret, cedartypes.String(string(ns)+"/"+string(name)))
}

// SecretCapability is the secret::read and secret::write capability (ADR-0321, ADR-0327): a Function's, a Lake's or
// an add-on object's (ADR-0332) spec.bindings.secret → the `secretBindings` Set of Secret entity-refs
// (binding-as-read-grant); the resource is the Secret with its owner and `writers`. Owner-only writers.
func SecretCapability() Capability { return secretCapability(nil) }

// SecretCapabilityWithWriters is SecretCapability with a WriterLister, so a writer-role RolesAssignment joins a Secret's
// `writers` set (ADR-0327).
func SecretCapabilityWithWriters(w WriterLister) Capability { return secretCapability(w) }

func secretCapability(writers WriterLister) Capability {
	return Capability{
		Name:            "secret",
		Actions:         []auth.Action{auth.ActionSecretRead, auth.ActionSecretWrite},
		EmitsEntityType: entityTypeSecret,
		// An add-on object is a principal of secret::read (ADR-0332 Decision 4) and the resource of addon::call
		// (ADR-0338 Decision 5), so a Policy names it under either capability.
		SupportingEntityTypes: []cedartypes.EntityType{entityTypeAddonObject, entityTypeJob},
		Resource: func(ctx context.Context, r MetaReader, resource auth.EntityRef) (cedartypes.EntityMap, bool, error) {
			return secretResource(ctx, r, resource, writers)
		},
		PrincipalBinding: PrincipalBinding{
			Attr: "secretBindings",
			Bind: func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID {
				var names []v1.ObjectName
				switch o := src.(type) {
				case *v1.Function:
					names = o.Spec.Bindings.Secret
				case *v1.Job:
					names = o.Spec.Bindings.Secret
				case *v1.Lake:
					names = o.Spec.Bindings.Secret
				case *v1.AddonObject:
					names = o.Spec.Bindings.Secret
				default:
					return nil
				}
				out := make([]cedartypes.EntityUID, 0, len(names))
				for _, n := range names {
					out = append(out, secretUID(ns, n))
				}
				return out
			},
		},
		Builtin: builtinSecretPolicies,
	}
}

// secretResource materializes the Secret with its namespace, owner and writers, as sqlResource does (ADR-0327 Decision
// 1). A missing Secret is a bare entity with its namespace, which a Namespace-scoped grant reads and no write passes.
func secretResource(ctx context.Context, r MetaReader, resource auth.EntityRef, writers WriterLister) (cedartypes.EntityMap, bool, error) {
	const op = "cedar.SecretCapability.Resource"
	if resource.Type != v1.KindSecret {
		return nil, false, nil
	}
	uid := secretUID(resource.Namespace, resource.Name)
	attrs := cedartypes.RecordMap{"namespace": cedartypes.String(resource.Namespace)}
	obj, err := r.Get(ctx, v1.KindSecret.GVK(), resource.Namespace, resource.Name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return cedartypes.EntityMap{uid: {UID: uid, Attributes: cedartypes.NewRecord(attrs)}}, true, nil
	case err != nil:
		return nil, false, fault.Wrapf(err, fault.Internal, op, "get secret %q", resource.Name)
	}
	sec, ok := obj.(*v1.Secret)
	if !ok {
		return nil, false, fault.Internalf(op, "object %q is not a Secret", resource.Name)
	}
	var listed []cedartypes.EntityUID
	if writers != nil {
		listed = writers.SecretWriters(ctx, resource.Namespace, string(resource.Name))
	}
	addOwnerAndWriters(attrs, sec.Namespace, sec.Spec.Owner, listed)
	return cedartypes.EntityMap{uid: {UID: uid, Attributes: cedartypes.NewRecord(attrs)}}, true, nil
}
