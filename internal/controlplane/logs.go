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

// RunLogGetter is the minimal metastore view the run-log querier needs (ADR-0106): Get a WorkflowRun
// (for its status.traceId) and its Workflow (for --step→function resolution). store.Store satisfies it.
type RunLogGetter interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// WorkflowRunLogQuerier resolves a run's status.traceId and returns its namespace-wide, trace-filtered
// logs (ADR-0106) — the whole composition (parent + sub-workflow child steps share the trace, ADR-0104).
type WorkflowRunLogQuerier interface {
	// RunLogs resolves the run's status.traceId and returns the namespace-wide, trace-filtered logs.
	// The querier sets q.Namespace and the resolved q.TraceID; the caller supplies the filter fields
	// (Since/MinSeverityNumber/Limit). A non-empty q.Function is a STEP NAME to resolve (the --step
	// drill-down), rewritten to that step's function; the trace filter is kept, so it returns only this
	// run's lines for that function. An empty status.traceId (a legacy run) ⇒ an empty result, not an error.
	RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error)
}

// runLogQuerier is the metastore+reader-backed WorkflowRunLogQuerier (ADR-0106).
type runLogQuerier struct {
	store  RunLogGetter
	reader LogQuerier
}

// NewWorkflowRunLogQuerier builds the run-scoped log querier over the metastore + the funclog reader.
func NewWorkflowRunLogQuerier(s RunLogGetter, reader LogQuerier) WorkflowRunLogQuerier {
	return &runLogQuerier{store: s, reader: reader}
}

func (r *runLogQuerier) RunLogs(ctx context.Context, ns v1.NamespaceName, run v1.ObjectName, q logread.Query) ([]logread.Line, error) {
	const op = "controlplane.runlogs"
	obj, err := r.store.Get(ctx, v1.KindWorkflowRun.GVK(), ns, run)
	if err != nil {
		return nil, err
	}
	wr, ok := obj.(*v1.WorkflowRun)
	if !ok {
		return nil, fault.Internalf(op, "object %q is not a WorkflowRun", run)
	}
	if wr.Status.TraceID == "" {
		return nil, nil // a legacy/traceless run: empty result, not an error
	}
	q.Namespace = string(ns)
	q.TraceID = wr.Status.TraceID
	// --step drill-down: a non-empty Function arrives as the step NAME; resolve it to the step's function
	// via the run's pinned workflow. Keeps the trace filter (so it's this run's lines for that function).
	if q.Function != "" {
		fn, rerr := r.resolveStepFunction(ctx, ns, wr.Spec.Workflow, v1.ObjectName(q.Function))
		if rerr != nil {
			return nil, rerr
		}
		q.Function = fn
	}
	return r.reader.Read(ctx, q)
}

// resolveStepFunction maps a step name to its function (ADR-0106): a FunctionStep with Ref ⇒ that
// function; with Image ⇒ the materialized <workflow>-<step>. A builtin/sub-workflow/unknown step has no
// single function to read — an Invalid error names why.
func (r *runLogQuerier) resolveStepFunction(ctx context.Context, ns v1.NamespaceName, workflow, step v1.ObjectName) (string, error) {
	const op = "controlplane.runlogs"
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), ns, workflow)
	if err != nil {
		return "", err
	}
	wf, ok := obj.(*v1.Workflow)
	if !ok {
		return "", fault.Internalf(op, "object %q is not a Workflow", workflow)
	}
	for i := range wf.Spec.Steps {
		s := &wf.Spec.Steps[i]
		if s.Name != step {
			continue
		}
		if s.Function == nil {
			return "", fault.Invalidf(op, "step %q is a builtin/sub-workflow step and has no single function to read", step)
		}
		if s.Function.Ref != "" {
			return string(s.Function.Ref), nil
		}
		return string(workflow) + "-" + string(step), nil // materialized owned function
	}
	return "", fault.Invalidf(op, "step %q not found in workflow %q", step, workflow)
}

// runLogsInput is the run-scoped logs read request (ADR-0106): the same filters as logsInput plus the
// optional --step drill-down. The path params are typed, so huma confines the namespace at the boundary.
type runLogsInput struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
	Since     string           `query:"since" doc:"RFC3339 time or a Go duration (e.g. 15m); empty ⇒ no lower bound"`
	Severity  string           `query:"severity" doc:"minimum level: trace|debug|info|warn|error|fatal"`
	Limit     int              `query:"limit" doc:"max records returned, most-recent first-bounded; default 1000, capped 10000"`
	Step      string           `query:"step" doc:"narrow to one step's function (the --step drill-down)"`
}

// RegisterWorkflowRunLogs registers GET …/workflowruns/{name}/logs (ADR-0106): it authorizes get/WorkflowRun
// in the namespace, resolves the run's status.traceId, and returns the namespace-wide, trace-filtered logs
// (the whole composition). NewServer calls it when a run-log querier is configured; spec generation uses the
// stub so the route is documented in the committed OpenAPI.
func RegisterWorkflowRunLogs(api huma.API, q WorkflowRunLogQuerier, authz auth.Authorizer) {
	huma.Register(api, huma.Operation{
		OperationID: "getWorkflowRunLogs",
		Method:      http.MethodGet,
		Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/workflowruns/{name}/logs",
		Tags:        []string{"WorkflowRun"},
	}, func(ctx context.Context, in *runLogsInput) (*logsOutput, error) {
		if err := authorizeRunLogs(ctx, authz, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		since, err := parseSince(in.Since)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		minSev, ok := logread.SeverityNumber(in.Severity)
		if !ok {
			return nil, wrapFaultError(fault.Invalidf("controlplane.runlogs", "unknown severity %q", in.Severity))
		}
		lines, err := q.RunLogs(ctx, in.Namespace, in.Name, logread.Query{
			Function:          in.Step, // a step NAME; the querier resolves it (empty ⇒ namespace-wide)
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

// authorizeRunLogs runs the ADR-0018 PEP for a run-scoped logs read: the caller must be allowed to get
// WorkflowRuns in ns (query-time tenant scoping — the trace-id selects within the namespace, never grants).
func authorizeRunLogs(ctx context.Context, authz auth.Authorizer, ns v1.NamespaceName) error {
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf("controlplane.runlogs", "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: v1.KindWorkflowRun, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "controlplane.runlogs", "authorize run logs read")
	}
	if !dec.Allowed {
		return fault.Forbiddenf("controlplane.runlogs", "read run logs in %q denied: %s", ns, dec.Reason)
	}
	return nil
}

// stubRunLogQuerier backs RegisterStubWorkflowRunLogs (spec generation) — inert, shape only.
type stubRunLogQuerier struct{}

func (stubRunLogQuerier) RunLogs(context.Context, v1.NamespaceName, v1.ObjectName, logread.Query) ([]logread.Line, error) {
	return nil, nil
}

// RegisterStubWorkflowRunLogs registers the run-logs operation with inert deps for spec generation, so the
// route is documented in the committed OpenAPI even though it mounts at runtime only with a blob substrate.
func RegisterStubWorkflowRunLogs(api huma.API) {
	RegisterWorkflowRunLogs(api, stubRunLogQuerier{}, stubLogAuthorizer{})
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
