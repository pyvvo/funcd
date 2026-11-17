package controlplane

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
)

// APITokenMinter mints the token of a declared APIToken (FEAT-0002 F248). Optional; when set, NewServer registers POST
// /apis/funcd.io/v1alpha1/apitokens/{name}/token.
type APITokenMinter interface {
	MintAPIToken(ctx context.Context, name v1.ObjectName, ttl time.Duration) (string, time.Time, error)
}

// RegisterAPIToken registers POST …/apitokens/{name}/token: it mints the token of the declared APIToken, for its role and
// namespaces. Minting again makes the token before stop working.
func RegisterAPIToken(api huma.API, m APITokenMinter, authz auth.Authorizer) {
	registerTokenRoute(api, tokenRoute{
		op: "mintAPIToken", path: "/apis/funcd.io/v1alpha1/apitokens/{name}/token",
		summary: "Mint an API token",
		description: "The token is funcd.<name>.<secret>, for the role and the namespaces of the declared APIToken, and " +
			"the token minted before stops working. It works 30 days, or for the ttl, from 1s to 8760h.",
		ttlWhen: "The ttl is not a duration from 1s to 8760h.", ttlFix: "Send a duration such as 720h, or none for 30 days.",
		kind: v1.KindAPIToken, kinds: "APITokens", mint: m.MintAPIToken,
	}, authz)
}

type stubAPITokenMinter struct{}

func (stubAPITokenMinter) MintAPIToken(context.Context, v1.ObjectName, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// RegisterStubAPIToken registers the API-token route with inert deps for spec generation.
func RegisterStubAPIToken(api huma.API) {
	RegisterAPIToken(api, stubAPITokenMinter{}, stubLogAuthorizer{})
}
