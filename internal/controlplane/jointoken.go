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

type joinTokenInput struct {
	Name v1.ObjectName `path:"name" doc:"The WorkerNode's name."`
	Body struct {
		TTL string `json:"ttl,omitempty" doc:"How long the token works, as a duration from 1s to 24h, such as 30m. Empty means an hour."`
	}
}

type joinTokenOutput struct {
	Body struct {
		Token     string       `json:"token" doc:"The one-time join token, K10<CA hash>::<node>:<secret>. funcd keeps only its hash; it is shown once."`
		ExpiresAt v1.Timestamp `json:"expiresAt" doc:"When the token stops working."`
	}
}

// RegisterJoinToken registers POST …/workernodes/{name}/token: it mints a one-time join token bound to the declared
// WorkerNode name, which `funcd node join` swaps for the node's certificate. Minting a token makes any earlier unused one
// stop working. An admin action: it authorizes an update of the WorkerNode, a cluster-scoped kind.
func RegisterJoinToken(api huma.API, m JoinTokenMinter, authz auth.Authorizer) {
	wn := verbCases{kind: "WorkerNode", kinds: "WorkerNodes"}
	huma.Register(api, Describe(huma.Operation{
		OperationID: "mintWorkerNodeJoinToken",
		Method:      http.MethodPost,
		Path:        "/apis/funcd.io/v1alpha1/workernodes/{name}/token",
		Tags:        []string{"WorkerNode"},
	}, "Mint a worker node's join token", unauthorized(), forbidden("update WorkerNodes"), wn.notFound(),
		badPath(wn.path()),
		problem(fault.Invalid, "The ttl is not a duration from 1s to 24h.", "Send a duration such as 30m, or none for an hour."),
	), func(ctx context.Context, in *joinTokenInput) (*joinTokenOutput, error) {
		const op = "controlplane.mintJoinToken"
		id, ok := middleware.IdentityFrom(ctx)
		if !ok {
			return nil, wrapFaultError(fault.Unauthorizedf(op, "no authenticated identity"))
		}
		dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbUpdate, Kind: v1.KindWorkerNode})
		if err != nil {
			return nil, wrapFaultError(fault.Wrapf(err, fault.Internal, op, "authorize"))
		}
		if !dec.Allowed {
			return nil, wrapFaultError(fault.Forbiddenf(op, "update WorkerNode denied: %s", dec.Reason))
		}
		var ttl time.Duration
		if in.Body.TTL != "" {
			if ttl, err = time.ParseDuration(in.Body.TTL); err != nil {
				return nil, wrapFaultError(fault.Invalidf(op, "ttl %q is not a duration", in.Body.TTL))
			}
		}
		token, expires, err := m.MintJoinToken(ctx, in.Name, ttl)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		out := &joinTokenOutput{}
		out.Body.Token, out.Body.ExpiresAt = token, v1.NewTimestamp(expires)
		return out, nil
	})
}

type stubJoinTokenMinter struct{}

func (stubJoinTokenMinter) MintJoinToken(context.Context, v1.ObjectName, time.Duration) (string, time.Time, error) {
	return "", time.Time{}, nil
}

// RegisterStubJoinToken registers the join-token route with inert deps for spec generation.
func RegisterStubJoinToken(api huma.API) {
	RegisterJoinToken(api, stubJoinTokenMinter{}, stubLogAuthorizer{})
}
