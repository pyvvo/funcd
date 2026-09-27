package cedar

import (
	"context"
	"encoding/json"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The funcd data-plane action strings a Role may grant. Write actions gate the single-writer forbid via
// the `writers` set (WriterGrants); the rest compile to Cedar permits (CompileRolesAssignment).
const (
	actionS3Read       = "s3::read"
	actionS3Write      = "s3::write"
	actionKVRead       = "kv::read"
	actionKVWrite      = "kv::write"
	actionLinkInvoke   = "link::invoke"
	actionCatalogQuery = "catalog::query" // == string(auth.ActionCatalogQuery); read-shaped (a permit, ADR-0137)
)

func isWriteAction(a string) bool { return a == actionS3Write || a == actionKVWrite }

// builtinRoles is the fixed DATA-PLANE role catalog (ADR-0136). Managing assignments is control-plane
// RBAC (ADR-0018), a separate plane — so `Owner` here is full data read+write, not assignment management.
//
//nolint:gochecknoglobals // an effectively-const catalog
var builtinRoles = map[string][]string{
	"Blob Data Reader": {actionS3Read},
	"Blob Data Writer": {actionS3Read, actionS3Write},
	"KV Data Reader":   {actionKVRead},
	"KV Data Writer":   {actionKVRead, actionKVWrite},
	"Function Invoker": {actionLinkInvoke},
	// ADR-0137 (F102): catalog::query is read-shaped. "Catalog Contributor" is a forward-compat alias —
	// byte-identical to the reader today, reserved to gain catalog::write when the deferred DDL grant lands.
	"Catalog Query Reader": {actionCatalogQuery},
	"Catalog Contributor":  {actionCatalogQuery},
	"Reader":               {actionS3Read, actionKVRead},
	"Contributor":          {actionS3Read, actionS3Write, actionKVRead, actionKVWrite},
	"Owner":                {actionS3Read, actionS3Write, actionKVRead, actionKVWrite},
}

// RoleResolver resolves a RoleRef (a built-in name or a custom Role resource) to its action set.
type RoleResolver interface {
	Actions(ctx context.Context, ref v1.RoleRef, ns v1.NamespaceName) ([]string, bool)
}

// WriterLister returns the writer principal UIDs (beyond the legacy owner) authorized to WRITE a blob
// prefix / kv table, from writer-role RolesAssignments (ADR-0136). It backs the generalized single-writer
// forbid's `writers` set. A nil WriterLister ⇒ owner-only writers (back-compat, no role grants).
type WriterLister interface {
	BlobWriters(ctx context.Context, ns v1.NamespaceName, bucket, prefix string) []cedartypes.EntityUID
	KVWriters(ctx context.Context, ns v1.NamespaceName, store, table string) []cedartypes.EntityUID
}

// storeRoleResolver resolves built-in roles from the catalog and custom Roles from the metastore.
type storeRoleResolver struct{ r MetaReader }

// NewRoleResolver builds the production role resolver over a MetaReader.
func NewRoleResolver(r MetaReader) RoleResolver { return storeRoleResolver{r: r} }

func (s storeRoleResolver) Actions(ctx context.Context, ref v1.RoleRef, ns v1.NamespaceName) ([]string, bool) {
	if ref.Kind == v1.RoleRefKindBuiltin {
		acts, ok := builtinRoles[ref.Name]
		return acts, ok
	}
	obj, err := s.r.Get(ctx, v1.KindRole.GVK(), ns, v1.ObjectName(ref.Name))
	if err != nil {
		return nil, false // a missing custom Role grants nothing (fail-closed)
	}
	role, ok := obj.(*v1.Role)
	if !ok {
		return nil, false
	}
	return role.Spec.Actions, true
}

// resolvedEntry is one flattened (principal, scope) with the top-level defaults applied.
type resolvedEntry struct {
	principal v1.PrincipalRef
	scope     v1.ScopeRef
	actions   []string
}

// resolveEntries flattens a RolesAssignment's entries with defaults + resolves each roleRef to actions.
func resolveEntries(ctx context.Context, ra *v1.RolesAssignment, roles RoleResolver) []resolvedEntry {
	out := make([]resolvedEntry, 0, len(ra.Spec.Assignments))
	for _, e := range ra.Spec.Assignments {
		p := e.Principal
		if p == nil {
			p = ra.Spec.Principal
		}
		s := e.Scope
		if s == nil {
			s = ra.Spec.Scope
		}
		if p == nil || s == nil {
			continue // invalid entry (Validate rejects; belt-and-suspenders)
		}
		acts, ok := roles.Actions(ctx, e.RoleRef, ra.Namespace)
		if !ok {
			continue // a missing role grants nothing
		}
		out = append(out, resolvedEntry{principal: *p, scope: *s, actions: acts})
	}
	return out
}

// principalEntity maps a PrincipalRef to its Cedar (type, id).
func principalEntity(ns v1.NamespaceName, p v1.PrincipalRef) (typ, id string) {
	t := entityTypeFunction
	if p.Kind == v1.PrincipalKindIdentity {
		t = entityTypeIdentity
	}
	return t, string(ns) + "/" + string(p.Name)
}

// CompileRolesAssignment compiles a RolesAssignment's READ/query/invoke grants into synthetic v1.Policy
// Cedar permits (ADR-0136; the injection-safe EST path, mirroring CompileEgressPolicy). WRITE grants are
// NOT permits (a Cedar forbid overrides a permit) — they are surfaced via WriterGrants for the resource
// materializer's `writers` set. A missing role / empty grant ⇒ no policy (default-deny holds).
func CompileRolesAssignment(ctx context.Context, ra *v1.RolesAssignment, roles RoleResolver) ([]v1.Policy, error) {
	const op = "cedar.CompileRolesAssignment"
	if ra == nil || len(ra.Spec.Assignments) == 0 {
		return nil, nil
	}
	var b strings.Builder
	for _, e := range resolveEntries(ctx, ra, roles) {
		pType, pID := principalEntity(ra.Namespace, e.principal)
		resHead, whenBody, ok := scopeHead(ra.Namespace, e.scope)
		if !ok {
			continue
		}
		for _, a := range e.actions {
			if isWriteAction(a) {
				continue // write grants gate the forbid via WriterGrants, not a permit
			}
			pol := map[string]estNode{
				"effect":    "permit",
				"principal": map[string]estNode{"op": "==", "entity": map[string]estNode{"type": pType, "id": pID}},
				"action":    map[string]estNode{"op": "==", "entity": map[string]estNode{"type": "Action", "id": a}},
				"resource":  resHead,
			}
			if whenBody != nil {
				pol["conditions"] = []estNode{map[string]estNode{"kind": "when", "body": whenBody}}
			}
			raw, merr := json.Marshal(pol)
			if merr != nil {
				return nil, fault.Wrapf(merr, fault.Internal, op, "marshal EST")
			}
			var p cedar.Policy
			if uerr := p.UnmarshalJSON(raw); uerr != nil {
				return nil, fault.Invalidf(op, "assignment does not parse as Cedar: %v", uerr)
			}
			b.Write(p.MarshalCedar())
			b.WriteString("\n")
		}
	}
	if b.Len() == 0 {
		return nil, nil
	}
	syn := v1.Policy{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName("rolesassignment-" + string(ra.Name)), Namespace: ra.Namespace, ResourceGroup: ra.ResourceGroup},
		Spec:       v1.PolicySpec{Cedar: b.String()},
	}
	return []v1.Policy{syn}, nil
}

// scopeHead returns the permit's resource head (== / in / All) + an optional `when` body for a namespace
// scope. ok=false for a malformed scope.
func scopeHead(ns v1.NamespaceName, s v1.ScopeRef) (resHead estNode, whenBody estNode, ok bool) {
	switch s.Kind {
	case v1.ScopeKindNamespace:
		// resource All + when { resource.namespace == "<ns>" } — the attribute-scoped grant.
		return map[string]estNode{"op": "All"}, estEq(estAccess(estVar("resource"), "namespace"), estVal(string(ns))), true
	case v1.ScopeKindBucketPrefix:
		// name is "<bucket>/<prefix>" → BlobPrefix::"<ns>/<bucket>/<prefix>".
		id := string(ns) + "/" + s.Name
		return map[string]estNode{"op": "==", "entity": map[string]estNode{"type": entityTypeBlobPrefix, "id": id}}, nil, true
	case v1.ScopeKindCatalog:
		// name is "<catalog>" → CatalogService::"<ns>/<catalog>" (ADR-0137). The grant bounds to that one
		// catalog (it does not leak past its scope).
		id := string(ns) + "/" + s.Name
		return map[string]estNode{"op": "==", "entity": map[string]estNode{"type": entityTypeCatalogService, "id": id}}, nil, true
	case v1.ScopeKindKVStore:
		// name is "<store>" (whole store → resource in KVStore) or "<store>/<table>" (exact table).
		if strings.Contains(s.Name, "/") {
			id := string(ns) + "/" + s.Name
			return map[string]estNode{"op": "==", "entity": map[string]estNode{"type": entityTypeKVTable, "id": id}}, nil, true
		}
		id := string(ns) + "/" + s.Name
		return map[string]estNode{"op": "in", "entity": map[string]estNode{"type": entityTypeKVStore, "id": id}}, nil, true
	default:
		return nil, nil, false
	}
}

// WriterGrant is one principal authorized to WRITE a scope (a s3::write / kv::write grant).
type WriterGrant struct {
	Principal cedartypes.EntityUID
	Scope     v1.ScopeRef
}

// WriterGrants returns the write-authorizing (principal, scope) grants of a RolesAssignment — consumed by
// the s3/kv resource materializer to build a resource's `writers` set (the generalized single-writer
// forbid). Excludes the legacy owner (added separately by the materializer).
func WriterGrants(ctx context.Context, ra *v1.RolesAssignment, roles RoleResolver) []WriterGrant {
	if ra == nil {
		return nil
	}
	var out []WriterGrant
	for _, e := range resolveEntries(ctx, ra, roles) {
		if !hasWrite(e.actions) {
			continue
		}
		pType, pID := principalEntity(ra.Namespace, e.principal)
		out = append(out, WriterGrant{
			Principal: cedartypes.NewEntityUID(cedartypes.EntityType(pType), cedartypes.String(pID)),
			Scope:     e.scope,
		})
	}
	return out
}

func hasWrite(actions []string) bool {
	for _, a := range actions {
		if isWriteAction(a) {
			return true
		}
	}
	return false
}
