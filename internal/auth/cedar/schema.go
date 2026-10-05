// Package cedar is the cedar-go authorization driver (ADR-0074) behind the auth.Authorizer
// port (ADR-0018): it answers per-object, fine-grained data-plane decisions (KV first) that
// the coarse RBAC driver cannot express. It is DEFAULT-DENY — with no permitting Policy a
// kv::read is denied; the built-in forbid(kv::write) unless principal == resource.owner makes
// writes single-writer. The compiled PolicySet is cached and recompiled on a Policy change;
// per call only the request-relevant entities (principal Function + resource KVTable + its
// parent KVStore) are resolved from the metastore, never a full-store rebuild. It is a leaf
// under internal/auth, like the rbac driver.
package cedar

import (
	"encoding/json"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

// Cedar entity-type names (ADR-0074). The cedar text references these literally, e.g.
// Function::"<ns>/<name>", KVStore::"<ns>/<store>", KVTable::"<ns>/<store>/<table>".
const (
	entityTypeFunction = "Function"
	entityTypeKVStore  = "KVStore"
	entityTypeKVTable  = "KVTable"
	// ADR-0080 (S3 frontend): the blob data domain + sub-domain + the external SigV4 principal.
	entityTypeBucket     = "Bucket"     // the blob domain (the KVStore parallel)
	entityTypeBlobPrefix = "BlobPrefix" // a sub-domain carrying owner (the KVTable parallel)
	entityTypeS3Identity = "S3Identity" // the external SigV4 principal only (not an in-platform Function)
	// ADR-0135 (F100): a user-assigned managed identity — an external caller principal resolved from an
	// Identity-issued keypair. Distinct from S3Identity (the raw-key principal); a RolesAssignment grants it.
	entityTypeIdentity = "Identity"
	// ADR-0137 (F102): the catalog serving-layer resource — the CatalogService a catalog::query targets.
	// A Function's spec.catalogs are its `catalogBindings` (binding-as-query-grant); a Catalog-scoped
	// RolesAssignment grants an Identity query on it. The catalog PEP proxy is the enforcement point.
	entityTypeCatalogService = "CatalogService"
	// ADR-0117 (F81 egress): the ephemeral outbound destination — carries {ip, port, domains} attrs,
	// materialized from the request EntityRef's Path (no MetaReader read). Its id is the "<ip>:<port>" prefix.
	entityTypeNetDestination = "NetDestination"
)

// defaultRegistry is the assembled capability registry (ADR-0116): the three migrated capabilities
// (kv, invoke, s3) + the two default principal sources (Function, CatalogService; each resolves only its
// own principal type, ADR-0175). The
// schema vocabulary (KnownAction/KnownEntityType), the built-in PolicySet (Builtins), and the default
// EntityProvider are all assembled from it — replacing the hand-listed curatedActions/curatedEntityTypes
// maps. A new capability registers here (or in a consumer's own Registry) with no shared-code edit. The
// inputs are static + valid, so NewRegistry never errors; the explicit `_` discards the (nil) error
// (a panic is forbidden outside main).
//
//nolint:gochecknoglobals // the assembled default capability registry (ADR-0116)
var defaultRegistry, _ = NewRegistry(
	[]Capability{KVCapability(), InvokeCapability(), S3Capability(), EgressCapability(), CatalogCapability()},
	[]PrincipalSource{FunctionPrincipalSource(), CatalogServicePrincipalSource()},
)

// KnownAction reports whether action is in the assembled schema vocabulary (ADR-0116).
func KnownAction(action string) bool { return defaultRegistry.KnownAction(auth.Action(action)) }

// KnownEntityType reports whether entityType is in the assembled schema vocabulary (ADR-0116).
func KnownEntityType(entityType string) bool { return defaultRegistry.KnownEntityType(entityType) }

// policyScope is the structured form of one parsed Cedar policy's scope (ADR-0074): the action it
// names and the principal/resource entity types it references. It mirrors cedar-go's policy JSON.
type policyScope struct {
	Action struct {
		Entity struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"entity"`
	} `json:"action"`
	Principal scopeEntity `json:"principal"`
	Resource  scopeEntity `json:"resource"`
}

type scopeEntity struct {
	Entity struct {
		Type string `json:"type"`
	} `json:"entity"`
}

// ValidateCedar checks that text PARSES as Cedar and that every statement references only the
// curated actions + entity types (ADR-0074 policy-validity). It returns fault.Invalid on a parse
// error or an off-schema action/entity-type — the policy-validity admission surfaces it. cedar-go's
// own schema validator is experimental, so this is the curated, fixed-schema check the driver owns.
func ValidateCedar(text string) error {
	const op = "cedar.ValidateCedar"
	list, err := cedar.NewPolicyListFromBytes("policy", []byte(text))
	if err != nil {
		return fault.Invalidf(op, "cedar policy does not parse: %v", err)
	}
	if len(list) == 0 {
		return fault.Invalidf(op, "cedar policy is empty (no statements)")
	}
	for _, pol := range list {
		raw, merr := pol.MarshalJSON()
		if merr != nil {
			return fault.Invalidf(op, "cannot inspect cedar policy: %v", merr)
		}
		var sc policyScope
		if jerr := json.Unmarshal(raw, &sc); jerr != nil {
			return fault.Invalidf(op, "cannot decode cedar policy scope: %v", jerr)
		}
		// The action scope (op == "==") names a concrete action; "All" leaves it empty (then the
		// statement is action-agnostic, which is allowed — but for the curated KV schema we require a
		// named action so a Policy can't accidentally grant every action).
		if sc.Action.Entity.ID == "" {
			return fault.Invalidf(op, "cedar policy must name a specific action (e.g. action == Action::%q)", string(auth.ActionKVRead))
		}
		if !KnownAction(sc.Action.Entity.ID) {
			return fault.Invalidf(op, "cedar policy references unknown action %q (curated: kv::read, kv::write, link::invoke, s3::read, s3::write)", sc.Action.Entity.ID)
		}
		for _, et := range []string{sc.Principal.Entity.Type, sc.Resource.Entity.Type} {
			if et != "" && !KnownEntityType(et) {
				return fault.Invalidf(op, "cedar policy references unknown entity type %q (curated: Function, KVStore, KVTable, Bucket, BlobPrefix, S3Identity)", et)
			}
		}
	}
	return nil
}
