package s3gateway

import (
	"context"

	"github.com/versity/versitygw/auth"
	"github.com/versity/versitygw/s3api/utils"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
)

// principal is the resolved S3 caller (ADR-0085): an in-platform Function or CatalogService Ref or an
// external S3Identity, plus its namespace. It is mapped to a Cedar principal for the
// ADR-0080 PEP.
type principal struct {
	ref       authz.EntityRef // the Cedar principal (Function, CatalogService, Identity or S3Identity)
	namespace v1.NamespaceName
}

// accountFromCtx reads the versitygw-authenticated account from the request context.
// versitygw stores it as a fiber local (utils.ContextKeyAccount), which fiber v3
// backs by fasthttp's RequestCtx user-values; the backend is handed that RequestCtx
// (a context.Context), so ctx.Value(<key>) returns the account set after SigV4.
// Fail-closed: a missing/zero account yields ok=false.
func accountFromCtx(ctx context.Context) (auth.Account, bool) {
	v := ctx.Value(string(utils.ContextKeyAccount))
	acct, ok := v.(auth.Account)
	if !ok || acct.Access == "" {
		return auth.Account{}, false
	}
	return acct, true
}

// principalFor maps an authenticated account to its funcd principal (ADR-0085): an
// in-platform access decodes to a Function or CatalogService Ref of the kind the key
// carries (ADR-0175); any other (external) access becomes an S3Identity scoped to the
// namespace the ExternalKeys store recorded. external may be nil ⇒ only in-platform.
func principalFor(acct auth.Account, external ExternalKeys) (principal, error) {
	if kind, ns, name, ok := decodeAccess(acct.Access); ok {
		return principal{
			ref:       authz.EntityRef{Type: kind, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)},
			namespace: v1.NamespaceName(ns),
		}, nil
	}
	if external != nil {
		if _, ns, ok := external.Lookup(acct.Access); ok {
			// ADR-0135: a managed-Identity-issued key resolves to the Identity::"<ns>/<name>" principal
			// (RolesAssignment grants it); any other external key stays the legacy S3Identity principal.
			if idNS, name, isID := DecodeIdentityAccess(acct.Access); isID {
				return principal{
					ref:       authz.EntityRef{Type: v1.KindIdentity, Namespace: v1.NamespaceName(idNS), Name: v1.ObjectName(name)},
					namespace: v1.NamespaceName(idNS),
				}, nil
			}
			return principal{
				ref:       authz.EntityRef{Type: v1.KindS3Identity, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(acct.Access)},
				namespace: v1.NamespaceName(ns),
			}, nil
		}
	}
	return principal{}, fault.Forbiddenf("s3gateway.principalFor", "unrecognized S3 principal")
}
