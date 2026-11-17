package controlplane

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
)

// APITokenMinter mints the token of a declared APIToken (FEAT-0002 F248). Optional; when set, NewServer registers POST
// /apis/funcd.io/v1alpha1/apitokens/{name}/token.
type APITokenMinter interface {
	MintAPIToken(ctx context.Context, name v1.ObjectName, ttl time.Duration) (string, time.Time, error)
}

type apiTokenInput struct {
	Name v1.ObjectName `path:"name" doc:"The APIToken's name."`
	Body struct {
		TTL string `json:"ttl,omitempty" doc:"How long the token works, as a duration from 1s to 8760h, such as 720h. Empty means 720h, 30 days."`
	}
}

type apiTokenOutput struct {
	Body struct {
		Token     string       `json:"token" doc:"The API token, funcd.<name>.<secret>. funcd keeps only its hash; it is shown once."`
		ExpiresAt v1.Timestamp `json:"expiresAt" doc:"When the token stops working."`
	}
}

// RegisterAPIToken registers POST …/apitokens/{name}/token: it mints the token of the declared APIToken, for its role and
// namespaces. Minting again makes the token before stop working. An admin action: it authorizes an update of the
// APIToken, a cluster-scoped kind.
func RegisterAPIToken(api huma.API, m APITokenMinter, authz auth.Authorizer) {
	at := verbCases{kind: "APIToken", kinds: "APITokens"}
	huma.Register(api, Describe(huma.Operation{
		OperationID: "mintAPIToken",
		Method:      http.MethodPost,
		Path:        "/apis/funcd.io/v1alpha1/apitokens/{name}/token",
		Tags:        []string{"APIToken"},
	}, "Mint an API token", unauthorized(), forbidden("update APITokens"), at.notFound(), badPath(at.path()),
		problem(fault.Invalid, "The ttl is not a duration from 1s to 8760h.", "Send a duration such as 720h, or none for 30 days."),
	), func(ctx context.Context, in *apiTokenInput) (*apiTokenOutput, error) {
		const op = "controlplane.mintAPIToken"
		id, ok := middleware.IdentityFrom(ctx)
		if !ok {
			return nil, wrapFaultError(fault.Unauthorizedf(op, "no authenticated identity"))
		}
		dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbUpdate, Kind: v1.KindAPIToken})
		if err != nil {
			return nil, wrapFaultError(fault.Wrapf(err, fault.Internal, op, "authorize"))
		}
		if !dec.Allowed {
			return nil, wrapFaultError(fault.Forbiddenf(op, "update APIToken denied: %s", dec.Reason))
		}
		var ttl time.Duration
		if in.Body.TTL != "" {
			if ttl, err = time.ParseDuration(in.Body.TTL); err != nil {
				return nil, wrapFaultError(fault.Invalidf(op, "ttl %q is not a duration", in.Body.TTL))
			}
		}
		token, expires, err := m.MintAPIToken(ctx, in.Name, ttl)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		out := &apiTokenOutput{}
		out.Body.Token, out.Body.ExpiresAt = token, v1.NewTimestamp(expires)
		return out, nil
	})
}

type stubAPITokenMinter struct{}

func (stubAPITokenMinter) MintAPIToken(context.Context, v1.ObjectName, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// RegisterStubAPIToken registers the API-token route with inert deps for spec generation.
func RegisterStubAPIToken(api huma.API) {
	RegisterAPIToken(api, stubAPITokenMinter{}, stubLogAuthorizer{})
}
