package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
)

// AppRetrier is the App reconciler's retry seam (ADR-0214 Decision 7): it starts again, without waiting, the failed
// hook of an App's latest revision.
type AppRetrier interface {
	Retry(ctx context.Context, ns v1.NamespaceName, app v1.ObjectName) error
}

type appRetryInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
}

// RegisterAppRetry registers POST …/namespaces/{namespace}/apps/{name}/retry (ADR-0214 Decision 7), an imperative
// action like the DLQ replay: it authorizes an update of the App and calls the retrier. Being a POST action, it takes
// no If-Match (ADR-0210).
func RegisterAppRetry(api huma.API, retrier AppRetrier, authz auth.Authorizer) {
	huma.Register(api, huma.Operation{
		OperationID:                  "retryApp",
		Method:                       http.MethodPost,
		Path:                         "/apis/funcd.io/v1alpha1/namespaces/{namespace}/apps/{name}/retry",
		Tags:                         []string{"App"},
		RejectUnknownQueryParameters: true,
	}, func(ctx context.Context, in *appRetryInput) (*struct{}, error) {
		if err := authorizeAppRetry(ctx, authz, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(retrier.Retry(ctx, in.Namespace, in.Name))
	})
}

func authorizeAppRetry(ctx context.Context, authz auth.Authorizer, ns v1.NamespaceName) error {
	const op = "controlplane.retryApp"
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf(op, "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbUpdate, Kind: v1.KindApp, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "authorize app retry")
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "update App in %q denied: %s", ns, dec.Reason)
	}
	return nil
}

type stubAppRetrier struct{}

func (stubAppRetrier) Retry(context.Context, v1.NamespaceName, v1.ObjectName) error { return nil }

// RegisterStubAppRetry registers the retry operation with inert deps for spec generation.
func RegisterStubAppRetry(api huma.API) {
	RegisterAppRetry(api, stubAppRetrier{}, stubLogAuthorizer{})
}
