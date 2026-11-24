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

// JoinTokenMinter mints a worker node's one-time join token (FEAT-0002 decision 2). Optional; when set, NewServer
// registers POST /apis/funcd.io/v1alpha1/workernodes/{name}/token.
type JoinTokenMinter interface {
	MintJoinToken(ctx context.Context, node v1.ObjectName, ttl time.Duration) (string, time.Time, error)
}

type tokenInput struct {
	Name v1.ObjectName `path:"name" doc:"The name of the object that the token is for."`
	Body struct {
		TTL string `json:"ttl,omitempty" doc:"How long the token works, as a duration such as 30m. Empty means the route's default."`
	}
}

type tokenOutput struct {
	Body struct {
		Token     string       `json:"token" doc:"The token. funcd keeps only its hash, and shows the token once."`
		ExpiresAt v1.Timestamp `json:"expiresAt" doc:"When the token stops working."`
	}
}

// tokenRoute is a route that mints the token of a cluster-scoped object and keeps only its hash (FEAT-0002). It is an
// admin action: it authorizes an update of the object's kind.
type tokenRoute struct {
	op, path, summary, description string
	ttlWhen, ttlFix                string
	kind                           v1.Kind
	kinds                          string
	mint                           func(ctx context.Context, name v1.ObjectName, ttl time.Duration) (string, time.Time, error)
}

func registerTokenRoute(api huma.API, r tokenRoute, authz auth.Authorizer) {
	vc := verbCases{kind: string(r.kind), kinds: r.kinds}
	huma.Register(api, Describe(huma.Operation{
		OperationID: r.op,
		Method:      http.MethodPost,
		Path:        r.path,
		Tags:        []string{string(r.kind)},
		Description: r.description,
	}, r.summary, append([]ErrorCase{unauthorized(), vc.forbidden("update " + an(string(r.kind))), vc.notFound(),
		badPath(vc.path()), badQuery(), problem(fault.Invalid, r.ttlWhen, r.ttlFix)}, bodyCases()...)...,
	), func(ctx context.Context, in *tokenInput) (*tokenOutput, error) {
		op := "controlplane." + r.op
		id, ok := middleware.IdentityFrom(ctx)
		if !ok {
			return nil, wrapFaultError(fault.Unauthorizedf(op, "no authenticated identity"))
		}
		dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbUpdate, Kind: r.kind})
		if err != nil {
			return nil, wrapFaultError(fault.Wrapf(err, fault.Internal, op, "authorize"))
		}
		if !dec.Allowed {
			return nil, wrapFaultError(fault.Forbiddenf(op, "update %s denied: %s", r.kind, dec.Reason))
		}
		var ttl time.Duration
		if in.Body.TTL != "" {
			if ttl, err = time.ParseDuration(in.Body.TTL); err != nil {
				return nil, wrapFaultError(fault.Invalidf(op, "ttl %q is not a duration", in.Body.TTL))
			}
		}
		token, expires, err := r.mint(ctx, in.Name, ttl)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		out := &tokenOutput{}
		out.Body.Token, out.Body.ExpiresAt = token, v1.NewTimestamp(expires)
		return out, nil
	})
}

// RegisterJoinToken registers POST …/workernodes/{name}/token: it mints a one-time join token bound to the declared
// WorkerNode name, which `funcd node join` swaps for the node's certificate. Minting a token makes any earlier unused one
// stop working.
func RegisterJoinToken(api huma.API, m JoinTokenMinter, authz auth.Authorizer) {
	registerTokenRoute(api, tokenRoute{
		op: "mintWorkerNodeJoinToken", path: "/apis/funcd.io/v1alpha1/workernodes/{name}/token",
		summary: "Mint a worker node's join token",
		description: "The token is K10<CA hash>::<node>:<secret>, for one join of the declared WorkerNode: funcd node join " +
			"swaps it for the node's certificate, and a token minted before and not used stops working. It works an hour, " +
			"or for the ttl, from 1s to 24h.",
		ttlWhen: "The ttl is not a duration from 1s to 24h.", ttlFix: "Send a duration such as 30m, or none for an hour.",
		kind: v1.KindWorkerNode, kinds: "WorkerNodes", mint: m.MintJoinToken,
	}, authz)
}

type stubJoinTokenMinter struct{}

func (stubJoinTokenMinter) MintJoinToken(context.Context, v1.ObjectName, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// RegisterStubJoinToken registers the join-token route with inert deps for spec generation.
func RegisterStubJoinToken(api huma.API) {
	RegisterJoinToken(api, stubJoinTokenMinter{}, stubLogAuthorizer{})
}
