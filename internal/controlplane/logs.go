package controlplane

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/workflow"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
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
	Namespace v1.NamespaceName `path:"namespace" doc:"The Function's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The Function's name."`
	LogFilters
}

// LogFilters are the query parameters that every logs read takes. It is exported because huma skips unexported
// embedded fields.
type LogFilters struct {
	Since    string `query:"since" doc:"Only the lines since then: a duration back from now, such as 15m or 1h30m, or a UTC timestamp with three fractional digits, such as 2026-10-07T22:00:00.000Z. Left out, the lines from the first."`
	Severity string `query:"severity" doc:"Only the lines at this level or above: trace, debug, info, warn, error or fatal."`
	Limit    int    `query:"limit" doc:"The most lines to answer, the most recent ones: 1000 when left out, and at most 10000."`
}

// logFilterCases are the answers of the parameters that LogFilters holds.
func logFilterCases() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "The since parameter is neither a duration, such as 15m, nor a UTC timestamp with "+
			"three fractional digits, such as 2026-10-07T22:00:00.000Z.", "Send since in one of those two forms."),
		problem(fault.Invalid, "The severity parameter is none of trace, debug, info, warn, error and fatal.",
			"Send one of those levels, or leave severity out."),
		blank(http.StatusUnprocessableEntity, "The limit parameter is not an integer.", "Send limit as a whole number."),
		nodeAway(),
	}
}

type logsOutput struct {
	Body struct {
		Items []logread.Line `json:"items" doc:"The most recent lines that match, at most limit, oldest first."`
	}
}

// RegisterLogs registers the namespaced function-logs read operation. It authorizes get/Function in the
// namespace BEFORE reading — the ADR-0084 query-time tenant scope: admin sees any namespace,
// developer/viewer only a bound one, else 403. The readable namespace is the authorized path segment,
// never a client-asserted filter. NewServer calls it when a reader is configured; spec generation calls
// RegisterStubLogs so the operation is documented in the committed OpenAPI.
func RegisterLogs(api huma.API, q LogQuerier, authz auth.Authorizer) {
	huma.Register(api, Describe(huma.Operation{
		OperationID: "getFunctionLogs",
		Method:      http.MethodGet,
		Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/functions/{name}/logs",
		Tags:        []string{"Function"},
	}, "Read the logs of a Function", slices.Concat([]ErrorCase{unauthorized(),
		forbidden("read Functions in the namespace"), badPath("namespace or the name")}, logFilterCases())...,
	), func(ctx context.Context, in *logsInput) (*logsOutput, error) {
		if err := authorizeLogs(ctx, authz, v1.KindFunction, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		return readLogs(ctx, q, in.LogFilters, logread.Query{Namespace: string(in.Namespace), Function: string(in.Name)})
	})
}

// readLogs answers the lines that query names, filtered as f asks.
func readLogs(ctx context.Context, q LogQuerier, f LogFilters, query logread.Query) (*logsOutput, error) {
	since, err := parseSince(f.Since)
	if err != nil {
		return nil, wrapFaultError(err)
	}
	minSev, ok := logread.SeverityNumber(f.Severity)
	if !ok {
		return nil, wrapFaultError(fault.Invalidf("controlplane.logs", "unknown severity %q", f.Severity))
	}
	query.Since, query.MinSeverityNumber, query.Limit = since, minSev, f.Limit
	lines, err := q.Read(ctx, query)
	if err != nil {
		return nil, wrapFaultError(err)
	}
	out := &logsOutput{}
	out.Body.Items = lines
	return out, nil
}

// engineLogsInput is the logs read of an object that runs an engine (ADR-0342 Decision 3). Its path params are typed as
// logsInput's are, so the prefix the reader builds stays in the authorized namespace.
type engineLogsInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
	LogFilters
}

// addonEngineLogsInput is engineLogsInput for an object of an add-on's kind, named by the kind's plural.
type addonEngineLogsInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Plural    string           `path:"plural" doc:"The plural of the object's add-on kind: the kind in lower case with an s."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
	LogFilters
}

// EngineKinds resolves an add-on kind's plural to the kind whose objects run an engine (ADR-0342 Decision 3):
// fault.NotFound when no Addon registers one of that plural, a settings kind's plural included.
type EngineKinds interface {
	EngineKind(ctx context.Context, plural string) (string, error)
}

// RegisterEngineLogs registers the logs reads of the objects that run an engine (ADR-0342 Decision 3): Lakes,
// SQLDatabases and the objects of each add-on's kind. Each authorizes get on the object's kind in the namespace before
// it reads, as RegisterLogs does for a Function.
func RegisterEngineLogs(api huma.API, q LogQuerier, authz auth.Authorizer, kinds EngineKinds) {
	for _, k := range []struct {
		kind   v1.Kind
		plural string
	}{{v1.KindLake, "lakes"}, {v1.KindSQLDatabase, "sqldatabases"}} {
		huma.Register(api, Describe(huma.Operation{
			OperationID: "get" + string(k.kind) + "Logs",
			Method:      http.MethodGet,
			Path:        apiPrefix + "/namespaces/{namespace}/" + k.plural + "/{name}/logs",
			Tags:        []string{string(k.kind)},
		}, "Read the logs of a "+string(k.kind)+"'s engine", slices.Concat([]ErrorCase{unauthorized(),
			forbidden("read " + string(k.kind) + "s in the namespace"), badPath("namespace or the name")}, logFilterCases())...,
		), func(ctx context.Context, in *engineLogsInput) (*logsOutput, error) {
			if err := authorizeLogs(ctx, authz, k.kind, in.Namespace); err != nil {
				return nil, wrapFaultError(err)
			}
			return readLogs(ctx, q, in.LogFilters, logread.Query{Namespace: string(in.Namespace), Kind: string(k.kind),
				Name: string(in.Name)})
		})
	}
	huma.Register(api, Describe(huma.Operation{
		OperationID: "getAddonObjectLogs",
		Method:      http.MethodGet,
		Path:        apiPrefix + "/namespaces/{namespace}/{plural}/{name}/logs",
		Tags:        []string{"AddonObject"},
	}, "Read the logs of an add-on object's engine", slices.Concat([]ErrorCase{unauthorized(),
		forbidden("read add-on objects in the namespace"), badPath("namespace or the name"),
		problem(fault.NotFound, "No Addon registers a kind of the plural whose objects run an engine: none registers the "+
			"plural, or it is one of an Addon's settings kinds, whose objects run none.",
			"Check the plural: the kind in lower case with an s.")}, logFilterCases())...,
	), func(ctx context.Context, in *addonEngineLogsInput) (*logsOutput, error) {
		if err := authorizeLogs(ctx, authz, v1.KindAddonObject, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		kind, err := kinds.EngineKind(ctx, in.Plural)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return readLogs(ctx, q, in.LogFilters, logread.Query{Namespace: string(in.Namespace), Kind: kind, Name: string(in.Name)})
	})
}

// jobRunLogsInput is the logs read of a JobRun (ADR-0347 Decision 8). Its path params are typed as logsInput's are.
type jobRunLogsInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The run's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The run's name."`
	LogFilters
}

// RegisterJobRunLogs registers GET …/jobruns/{name}/logs (ADR-0347 Decision 8). It authorizes get/JobRun in the
// namespace, then answers the run's own lines, its worker's raw output included, with the lines that the calls the run
// made wrote under its status.traceId, oldest first and at most limit of them.
func RegisterJobRunLogs(api huma.API, q LogQuerier, s RunLogGetter, authz auth.Authorizer) {
	huma.Register(api, Describe(huma.Operation{
		OperationID: "getJobRunLogs",
		Method:      http.MethodGet,
		Path:        apiPrefix + "/namespaces/{namespace}/jobruns/{name}/logs",
		Tags:        []string{"JobRun"},
	}, "Read the logs of a JobRun", slices.Concat([]ErrorCase{unauthorized(),
		forbidden("read JobRuns in the namespace"),
		problem(fault.NotFound, "No JobRun of that name exists in the namespace.", "Check the name and the namespace."),
		badPath("namespace or the name")}, logFilterCases())...,
	), func(ctx context.Context, in *jobRunLogsInput) (*logsOutput, error) {
		if err := authorizeLogs(ctx, authz, v1.KindJobRun, in.Namespace); err != nil {
			return nil, wrapFaultError(err)
		}
		if _, err := parseSince(in.Since); err != nil {
			return nil, wrapFaultError(err)
		}
		if _, ok := logread.SeverityNumber(in.Severity); !ok {
			return nil, wrapFaultError(fault.Invalidf("controlplane.jobrunlogs", "unknown severity %q", in.Severity))
		}
		obj, err := s.Get(ctx, v1.KindJobRun.GVK(), in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		run, ok := obj.(*v1.JobRun)
		if !ok {
			return nil, wrapFaultError(fault.Internalf("controlplane.jobrunlogs", "object %q is not a JobRun", in.Name))
		}
		out, err := readLogs(ctx, q, in.LogFilters, logread.Query{Namespace: string(in.Namespace), Kind: string(v1.KindJobRun),
			Name: string(in.Name)})
		if err != nil || run.Status.TraceID == "" {
			return out, err
		}
		// the trace's read skips the runs' own segments, which the read above took (logread's engine rule)
		calls, err := readLogs(ctx, q, in.LogFilters, logread.Query{Namespace: string(in.Namespace), TraceID: run.Status.TraceID})
		if err != nil {
			return nil, err
		}
		lines := append(out.Body.Items, calls.Body.Items...)
		slices.SortStableFunc(lines, func(a, b logread.Line) int { return time.Time(a.Time).Compare(time.Time(b.Time)) })
		out.Body.Items = lines[max(len(lines)-logLimit(in.Limit), 0):]
		return out, nil
	})
}

// logLimit is the most lines a read answers for limit, as logread bounds it.
func logLimit(limit int) int {
	if limit <= 0 {
		return logread.DefaultLimit
	}
	return min(limit, logread.MaxLimit)
}

// RegisterStubJobRunLogs registers the JobRun logs operation with inert deps, for spec generation (ADR-0347).
func RegisterStubJobRunLogs(api huma.API) {
	RegisterJobRunLogs(api, stubLogQuerier{}, stubRunGetter{}, stubLogAuthorizer{})
}

// stubRunGetter backs RegisterStubJobRunLogs: spec generation never reads a run.
type stubRunGetter struct{}

func (stubRunGetter) Get(context.Context, v1.GroupVersionKind, v1.NamespaceName, v1.ObjectName) (v1.Object, error) {
	return nil, fault.NotFoundf("controlplane.stub", "no store")
}

// RunLogGetter is the minimal metastore view the run-log querier needs (ADR-0106): Get a WorkflowRun
// (for its status.traceId) and, for a run with no pinned spec, its Workflow. store.Store satisfies it.
type RunLogGetter interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

// RunRecordGetter is the engine's run-state view the run-log querier resolves --step against: the spec a
// run pinned at start (ADR-0094). runstate.Store satisfies it.
type RunRecordGetter interface {
	Get(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*runstate.Record, error)
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

// runLogQuerier is the metastore+run-state+reader-backed WorkflowRunLogQuerier (ADR-0106).
type runLogQuerier struct {
	store  RunLogGetter
	runs   RunRecordGetter
	reader LogQuerier
}

// NewWorkflowRunLogQuerier builds the run-scoped log querier over the metastore, the engine's run records
// and the funclog reader.
func NewWorkflowRunLogQuerier(s RunLogGetter, runs RunRecordGetter, reader LogQuerier) WorkflowRunLogQuerier {
	return &runLogQuerier{store: s, runs: runs, reader: reader}
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
		spec, rerr := r.runSpec(ctx, wr)
		if rerr != nil {
			return nil, rerr
		}
		fn, rerr := resolveStepFunction(wr.Spec.Workflow, spec, v1.ObjectName(q.Function))
		if rerr != nil {
			return nil, rerr
		}
		q.Function = fn
	}
	return r.reader.Read(ctx, q)
}

// runSpec returns the spec run executes: the one its engine record pinned at start (ADR-0094), so an
// edited or deleted Workflow does not move a --step read. A run with no record of its own (not started
// yet, or a record that closed without a spec) falls back to the live Workflow.
func (r *runLogQuerier) runSpec(ctx context.Context, run *v1.WorkflowRun) (*v1.WorkflowSpec, error) {
	const op = "controlplane.runlogs"
	rec, err := r.runs.Get(ctx, run.Namespace, run.Name)
	switch {
	case err == nil && !workflow.ForeignRecord(rec, run.UID) && len(rec.Spec.Steps) > 0:
		return &rec.Spec, nil
	case err != nil && fault.KindOf(err) != fault.NotFound:
		return nil, err
	}
	obj, err := r.store.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
	if err != nil {
		return nil, err
	}
	wf, ok := obj.(*v1.Workflow)
	if !ok {
		return nil, fault.Internalf(op, "object %q is not a Workflow", run.Spec.Workflow)
	}
	return &wf.Spec, nil
}

// resolveStepFunction maps a step name to its function in spec (ADR-0106): a FunctionStep with Ref ⇒ that
// function; with Image ⇒ the materialized <workflow>-<step>. A builtin/sub-workflow/unknown step has no
// single function to read — an Invalid error names why.
func resolveStepFunction(workflow v1.ObjectName, spec *v1.WorkflowSpec, step v1.ObjectName) (string, error) {
	const op = "controlplane.runlogs"
	for i := range spec.Steps {
		s := &spec.Steps[i]
		if s.Name != step {
			continue
		}
		if s.Function == nil {
			return "", fault.Invalidf(op, "step %q is a builtin/sub-workflow step and has no single function to read", step)
		}
		if s.Function.Ref != "" {
			return string(s.Function.Ref), nil
		}
		return string(v1.StepFunctionName(workflow, step)), nil // materialized owned function
	}
	return "", fault.Invalidf(op, "step %q not found in workflow %q", step, workflow)
}

// runLogsInput is the run-scoped logs read request (ADR-0106): the same filters as logsInput plus the
// optional --step drill-down. The path params are typed, so huma confines the namespace at the boundary.
type runLogsInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The run's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The run's name."`
	LogFilters
	Step string `query:"step" doc:"Only the lines of one step's Function, named by the step's name in the run's Workflow. Left out, the lines of every Function that the run called, those of its child workflows included."`
}

// RegisterWorkflowRunLogs registers GET …/workflowruns/{name}/logs (ADR-0106): it authorizes get/WorkflowRun
// in the namespace, resolves the run's status.traceId, and returns the namespace-wide, trace-filtered logs
// (the whole composition). NewServer calls it when a run-log querier is configured; spec generation uses the
// stub so the route is documented in the committed OpenAPI.
func RegisterWorkflowRunLogs(api huma.API, q WorkflowRunLogQuerier, authz auth.Authorizer) {
	huma.Register(api, Describe(huma.Operation{
		OperationID: "getWorkflowRunLogs",
		Method:      http.MethodGet,
		Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/workflowruns/{name}/logs",
		Tags:        []string{"WorkflowRun"},
	}, "Read the logs of a WorkflowRun", slices.Concat([]ErrorCase{unauthorized(),
		forbidden("read WorkflowRuns in the namespace"),
		problem(fault.NotFound, "No WorkflowRun of that name exists in the namespace.", "Check the name and the namespace."),
		badPath("namespace or the name")}, logFilterCases(), []ErrorCase{
		problem(fault.Invalid, "The run has started, and the step parameter names no step of its Workflow.",
			"Send the name of one of its steps, or leave step out."),
		problem(fault.Invalid, "The run has started, and the step parameter names a builtin or workflow step, which "+
			"calls no Function of its own.", "Name a function step, or leave step out."),
	})...,
	), func(ctx context.Context, in *runLogsInput) (*logsOutput, error) {
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

// stubEngineKinds backs RegisterStubEngineLogs: spec generation never resolves a plural.
type stubEngineKinds struct{}

func (stubEngineKinds) EngineKind(context.Context, string) (string, error) { return "", nil }

// RegisterStubEngineLogs registers the engines' logs operations with inert deps, for spec generation (ADR-0342).
func RegisterStubEngineLogs(api huma.API) {
	RegisterEngineLogs(api, stubLogQuerier{}, stubLogAuthorizer{}, stubEngineKinds{})
}

// authorizeLogs runs the ADR-0018 PEP for a logs read: the caller must be allowed to get objects of kind in ns, the
// Function or the object whose engine wrote the lines.
func authorizeLogs(ctx context.Context, authz auth.Authorizer, kind v1.Kind, ns v1.NamespaceName) error {
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf("controlplane.logs", "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: kind, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "controlplane.logs", "authorize logs read")
	}
	if !dec.Allowed {
		return fault.Forbiddenf("controlplane.logs", "read logs in %q denied: %s", ns, dec.Reason)
	}
	return nil
}

// parseSince accepts a duration in ADR-0194's grammar ("15m" ⇒ now-15m) or a timestamp in ADR-0196's form;
// "" ⇒ zero (no lower bound). Anything else is fault.Invalid naming both forms (#824).
func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := v1.ParseDuration(s); err == nil {
		return time.Now().Add(-time.Duration(d)), nil
	}
	var ts v1.Timestamp
	if err := ts.UnmarshalJSON([]byte(strconv.Quote(s))); err == nil {
		return time.Time(ts), nil
	}
	return time.Time{}, fault.Invalidf("controlplane.logs", "since %q is not %s", s, logread.SinceForms)
}
