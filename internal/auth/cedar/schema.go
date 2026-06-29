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

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/auth"
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
)

// curatedActions is the fixed set of Cedar actions this driver recognizes (ADR-0074). cedar-go's
// schema validator is experimental, so policy validity is checked against this curated set + the
// curated entity types below (the parser handles syntax; this handles the vocabulary). Future
// actions (link::invoke, egress::send, …) extend this set in their consumer ADRs.
//
//nolint:gochecknoglobals // a fixed, effectively-const curated schema (ADR-0074/0075)
var curatedActions = map[auth.Action]bool{
	auth.ActionKVRead:     true,
	auth.ActionKVWrite:    true,
	auth.ActionLinkInvoke: true, // ADR-0075: fn→fn invoke (Function principal + Function resource)
	auth.ActionS3Read:     true, // ADR-0080: S3 read over the blob substrate (BlobPrefix resource)
	auth.ActionS3Write:    true, // ADR-0080: S3 write over the blob substrate (single-writer = prefix owner)
}

// curatedEntityTypes is the fixed set of Cedar entity types this driver models (ADR-0074).
//
//nolint:gochecknoglobals // a fixed, effectively-const curated schema (ADR-0074)
var curatedEntityTypes = map[string]bool{
	entityTypeFunction:   true,
	entityTypeKVStore:    true,
	entityTypeKVTable:    true,
	entityTypeBucket:     true, // ADR-0080
	entityTypeBlobPrefix: true, // ADR-0080
	entityTypeS3Identity: true, // ADR-0080
}

// KnownAction reports whether action is in the curated schema (ADR-0074).
func KnownAction(action string) bool { return curatedActions[auth.Action(action)] }

// KnownEntityType reports whether entityType is in the curated schema (ADR-0074).
func KnownEntityType(entityType string) bool { return curatedEntityTypes[entityType] }

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
