package cedar

import (
	"context"
	"strings"

	cedartypes "github.com/cedar-policy/cedar-go/types"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// Capability declares one authorization capability (kv, invoke, s3, egress, …) so the schema
// vocabulary, the EntityProvider, and the built-in permits are ASSEMBLED from the registered set —
// registering a new capability adds no branch to shared code (ADR-0116). All funcs are typed (no any).
//
// Two orthogonal axes are kept separate (judge B1): a Capability is an {action + resource + built-in}
// on a PRINCIPAL; *where a principal's binding attributes come from* is a PrincipalSource (below), so the
// ADR-0088 provider principal (a CatalogService, ADR-0175) is NOT a fourth capability — s3 is one
// capability with two principal sources.
type Capability struct {
	// Name is the capability's short id (e.g. "kv", "invoke", "s3", "egress") — for diagnostics/dedup.
	// Unique across the registry.
	Name string
	// Actions are the Cedar actions this capability authorizes (e.g. auth.ActionKVRead, ActionKVWrite).
	// Unique across the registry (no two capabilities share an action).
	Actions []auth.Action
	// EmitsEntityType is the primary Cedar entity type this capability materializes as a resource (e.g.
	// entityTypeKVTable). Used ONLY for schema-vocabulary assembly (KnownEntityType) — NOT a dispatch key
	// (an EntityRef.Type v1.Kind never equals the Cedar entity type: KindKVStore→KVTable, KindBucket→
	// BlobPrefix). Resource dispatch is by the Resource func's ok, below.
	EmitsEntityType cedartypes.EntityType
	// SupportingEntityTypes are the additional Cedar entity types this capability contributes to the
	// schema vocabulary beyond its primary EmitsEntityType — the resource's parent (KVStore for kv,
	// Bucket for s3) and any alternate principal type it admits (S3Identity for s3). A Policy may scope
	// on these (`resource in KVStore::"…"`, `principal == S3Identity::"…"`), so KnownEntityType must
	// accept them; they are assembled from the registry exactly like EmitsEntityType (never hand-listed).
	SupportingEntityTypes []cedartypes.EntityType
	// Resource materializes the resource entity (UID + parents + owner attr) for a request whose resource
	// this capability owns. ok==false ⇒ not ours (try the next capability) — this ok is the SOLE resource-
	// dispatch mechanism. A dangling resource still returns ok==true with bare entities (evaluable,
	// default-deny). nil ⇒ the capability owns no resource shape (Policy-only over an existing entity).
	Resource func(ctx context.Context, r MetaReader, resource auth.EntityRef) (em cedartypes.EntityMap, ok bool, err error)
	// PrincipalBinding declares this capability's binding-as-grant attribute on the principal: Attr is the
	// principal attribute name (e.g. "kvBindings"); Bind materializes its members from a resolved
	// principal object (see PrincipalSource). The provider sets Attr whenever Bind returns a non-nil slice
	// (possibly empty), matching today's EntitiesFor (a Function always carries links/kvBindings/blobBindings;
	// the built-ins also `has`-guard, so empty ≡ absent). A Bind that returns nil for a source type
	// contributes nothing (attr absent) — e.g. kv/invoke for a CatalogService. Zero value ⇒ no binding.
	PrincipalBinding PrincipalBinding
	// Builtin is this capability's built-in permit policy text (the .cedar granting the capability when
	// principal.<attr>.contains(resource)); "" ⇒ no built-in (Policy-only). Each policy carries a unique
	// @id annotation, its stable name in a deny reason.
	Builtin string
}

// PrincipalBinding is one binding-as-grant attribute a capability contributes to a principal.
type PrincipalBinding struct {
	// Attr is the Cedar principal attribute (e.g. "kvBindings", "links", "blobBindings").
	Attr string
	// Bind materializes the attribute's Set members from a resolved principal object. src is whatever a
	// PrincipalSource resolved (a *v1.Function or a *v1.CatalogService); a capability that doesn't apply to
	// that source type returns nil (e.g. kv/invoke return nil for a CatalogService — providers have no
	// links/kv). ns is the principal namespace (for building same-namespace resource UIDs).
	Bind func(ns v1.NamespaceName, src PrincipalObject) []cedartypes.EntityUID
}

// PrincipalObject is a resolved principal-backing object (a *v1.Function or *v1.CatalogService); a
// closed interface (only api/types implements it, via an unexported marker) so PrincipalBinding.Bind
// type-switches without any. It aliases v1.PrincipalObject: the marker method must live in the same
// package as the implementing types, so the sealed interface is declared in api/types/v1alpha1 and
// re-exported here under the cedar name.
type PrincipalObject = v1.PrincipalObject

// PrincipalSource resolves the principal's backing object for a principal EntityRef, in order. The
// default sources are Function and CatalogService (ADR-0088), each resolving only a principal of its own
// type (ADR-0175) — the provider case is a principal SOURCE, not a capability. A source returns ok==false
// to defer to the next.
type PrincipalSource func(ctx context.Context, r MetaReader, p auth.EntityRef) (obj PrincipalObject, ok bool, err error)

// Registry is a deduped set of capabilities + an ordered principal-source list. It assembles the shared
// cedar artifacts (the composite EntityProvider, the KnownAction/KnownEntityType vocabulary, and the
// built-in PolicySet) from the registered set (ADR-0116).
type Registry struct {
	caps    []Capability
	sources []PrincipalSource
}

// NewRegistry validates (unique Name, unique Action across capabilities; ≥1 source) and returns the
// registry. A duplicate Name/Action or an empty source list is a fault.Invalid wiring error.
func NewRegistry(caps []Capability, sources []PrincipalSource) (*Registry, error) {
	const op = "cedar.NewRegistry"
	if len(sources) == 0 {
		return nil, fault.Invalidf(op, "at least one principal source is required")
	}
	names := make(map[string]bool, len(caps))
	actions := make(map[auth.Action]bool)
	for _, c := range caps {
		if c.Name == "" {
			return nil, fault.Invalidf(op, "capability has an empty Name")
		}
		if names[c.Name] {
			return nil, fault.Invalidf(op, "duplicate capability Name %q", c.Name)
		}
		names[c.Name] = true
		for _, a := range c.Actions {
			if actions[a] {
				return nil, fault.Invalidf(op, "duplicate action %q across capabilities", string(a))
			}
			actions[a] = true
		}
	}
	return &Registry{
		caps:    append([]Capability(nil), caps...),
		sources: append([]PrincipalSource(nil), sources...),
	}, nil
}

// KnownAction reports whether a is one of the registered capabilities' actions (the assembled schema
// vocabulary — replaces the hand-listed curatedActions map).
func (reg *Registry) KnownAction(a auth.Action) bool {
	for _, c := range reg.caps {
		for _, ca := range c.Actions {
			if ca == a {
				return true
			}
		}
	}
	return false
}

// KnownEntityType reports whether t is one of the registered capabilities' entity types (primary
// EmitsEntityType ∪ SupportingEntityTypes — the assembled schema vocabulary, replacing the hand-listed
// curatedEntityTypes map).
func (reg *Registry) KnownEntityType(t string) bool {
	for _, c := range reg.caps {
		if string(c.EmitsEntityType) == t {
			return true
		}
		for _, et := range c.SupportingEntityTypes {
			if string(et) == t {
				return true
			}
		}
	}
	return false
}

// Builtins returns the concatenated built-in permit policy text (the union of the capabilities'
// Builtin, in registry order so the compiled PolicySet is deterministic).
func (reg *Registry) Builtins() string {
	texts := make([]string, 0, len(reg.caps))
	for _, c := range reg.caps {
		if c.Builtin == "" {
			continue
		}
		texts = append(texts, c.Builtin)
	}
	return strings.Join(texts, "\n")
}
