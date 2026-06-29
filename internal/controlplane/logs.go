package controlplane

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/funclog/logread"
)

// LogQuerier is the control-plane's view of the function-log reader (logread.Reader, ADR-0084). An
// optional server dep; when set, NewServer registers GET …/functions/{name}/logs.
type LogQuerier interface {
	Read(ctx context.Context, q logread.Query) ([]logread.Line, error)
}

// logsInput is the namespaced function-logs read request. The path params are typed
// v1.NamespaceName/v1.ObjectName, so huma enforces their DNS-label schema at the boundary (no `/`, no
// `..`) — the prefix the reader builds is always confined to the authorized namespace.
type logsInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
	Since     string           `query:"since" doc:"RFC3339 time or a Go duration (e.g. 15m); empty ⇒ no lower bound"`
	Severity  string           `query:"severity" doc:"minimum level: trace|debug|info|warn|error|fatal"`
	Limit     int              `query:"limit" doc:"max records returned, most-recent first-bounded; default 1000, capped 10000"`
}

type logsOutput struct {
	Body struct {
		Items []logread.Line `json:"items"`
	}
}

// RegisterLogs registers the namespaced function-logs read operation. It authorizes get/Function in the
// namespace BEFORE reading — the ADR-0084 query-time tenant scope: admin sees any namespace,
// developer/viewer only a bound one, else 403. The readable namespace is the authorized path segment,
// never a client-asserted filter. NewServer calls it when a reader is configured; spec generation calls
// RegisterStubLogs so the operation is documented in the committed OpenAPI.
func RegisterLogs(api huma.API, q LogQuerier, authz auth.Authorizer) {
	huma.Register(api, huma.Operation{
		OperationID: "getFunctionLogs",
		Method:      http.MethodGet,
		Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/functions/{name}/logs",
		Tags:        []string{"Function"},
	}, func(ctx context.Context, in *logsInput) (*logsOutput, error) {
		if err := authorizeLogs(ctx, authz, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		since, err := parseSince(in.Since)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		minSev, ok := logread.SeverityNumber(in.Severity)
		if !ok {
			return nil, wrapFaultError(fault.Invalidf("controlplane.logs", "unknown severity %q", in.Severity))
		}
		lines, err := q.Read(ctx, logread.Query{
			Namespace:         string(in.Namespace),
			Function:          string(in.Name),
			Since:             since,
			MinSeverityNumber: minSev,
			Limit:             in.Limit,
		})
		if err != nil {
			return nil, wrapFaultError(err)
		}
		out := &logsOutput{}
		out.Body.Items = lines
		return out, nil
	})
}

// stubLogQuerier + stubLogAuthorizer back RegisterStubLogs: spec generation needs the operation's
// path/params/response shape, never the handler behavior, so these are inert.
type stubLogQuerier struct{}

func (stubLogQuerier) Read(context.Context, logread.Query) ([]logread.Line, error) { return nil, nil }

type stubLogAuthorizer struct{}

func (stubLogAuthorizer) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}

// RegisterStubLogs registers the logs operation with inert deps — for spec generation (specgen + the
// golden test), where only the operation shape matters, so the route is documented in the committed spec
// even though it is mounted at runtime only when a blob substrate is present.
func RegisterStubLogs(api huma.API) { RegisterLogs(api, stubLogQuerier{}, stubLogAuthorizer{}) }

// authorizeLogs runs the ADR-0018 PEP for a logs read: the caller must be allowed to get Functions in ns.
func authorizeLogs(ctx context.Context, authz auth.Authorizer, ns v1.NamespaceName) error {
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf("controlplane.logs", "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: v1.KindFunction, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "controlplane.logs", "authorize logs read")
	}
	if !dec.Allowed {
		return fault.Forbiddenf("controlplane.logs", "read logs in %q denied: %s", ns, dec.Reason)
	}
	return nil
}

// parseSince accepts an RFC3339 timestamp or a Go duration ("15m" ⇒ now-15m); "" ⇒ zero (no lower bound).
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fault.Invalidf("controlplane.logs", "since %q is not an RFC3339 time or a Go duration", s)
}
