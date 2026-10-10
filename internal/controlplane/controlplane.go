// Package controlplane owns the huma API registration, typed operations,
// the Handlers seam, and the api/fault → huma problem+json error bridge (ADR-0005).
//
// ADR-0005 owns the routes/contract; P-L/F07 fills Handlers + middleware/admission.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Handlers is the seam: one method per operation. Stub impl for spec-gen + tests;
// real impl (store/controller-backed) is provided by P-L/F07.
type Handlers interface {
	// Namespace (cluster-scoped)
	GetNamespace(ctx context.Context, name v1.ObjectName) (v1.Namespace, error)
	CreateNamespace(ctx context.Context, ns v1.Namespace) (v1.Namespace, error)
	ListNamespaces(ctx context.Context) ([]v1.Namespace, error)
	ReplaceNamespace(ctx context.Context, name v1.ObjectName, ns v1.Namespace) (v1.Namespace, error)
	DeleteNamespace(ctx context.Context, name v1.ObjectName, rv string) error

	// ResourceGroup (namespaced)
	GetResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ResourceGroup, error)
	CreateResourceGroup(ctx context.Context, rg v1.ResourceGroup) (v1.ResourceGroup, error)
	ListResourceGroups(ctx context.Context, ns v1.NamespaceName) ([]v1.ResourceGroup, error)
	ReplaceResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rg v1.ResourceGroup) (v1.ResourceGroup, error)
	DeleteResourceGroup(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, force bool, rv string) error

	// Function (namespaced)
	GetFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Function, error)
	CreateFunction(ctx context.Context, fn v1.Function) (v1.Function, error)
	ListFunctions(ctx context.Context, ns v1.NamespaceName) ([]v1.Function, error)
	ReplaceFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, fn v1.Function) (v1.Function, error)
	DeleteFunction(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Revision (namespaced)
	GetRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Revision, error)
	ListRevisions(ctx context.Context, ns v1.NamespaceName) ([]v1.Revision, error)

	// Route (namespaced)
	GetRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Route, error)
	CreateRoute(ctx context.Context, rt v1.Route) (v1.Route, error)
	ListRoutes(ctx context.Context, ns v1.NamespaceName) ([]v1.Route, error)
	ReplaceRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rt v1.Route) (v1.Route, error)
	DeleteRoute(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Service (namespaced)
	GetService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Service, error)
	CreateService(ctx context.Context, svc v1.Service) (v1.Service, error)
	ListServices(ctx context.Context, ns v1.NamespaceName) ([]v1.Service, error)
	ReplaceService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, svc v1.Service) (v1.Service, error)
	DeleteService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// EventSource (namespaced)
	GetEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EventSource, error)
	CreateEventSource(ctx context.Context, es v1.EventSource) (v1.EventSource, error)
	ListEventSources(ctx context.Context, ns v1.NamespaceName) ([]v1.EventSource, error)
	ReplaceEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, es v1.EventSource) (v1.EventSource, error)
	DeleteEventSource(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// ConfigMap (namespaced)
	GetConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ConfigMap, error)
	CreateConfigMap(ctx context.Context, cfg v1.ConfigMap) (v1.ConfigMap, error)
	ListConfigMaps(ctx context.Context, ns v1.NamespaceName) ([]v1.ConfigMap, error)
	ReplaceConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, cfg v1.ConfigMap) (v1.ConfigMap, error)
	DeleteConfigMap(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Secret (namespaced)
	GetSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Secret, error)
	CreateSecret(ctx context.Context, sec v1.Secret) (v1.Secret, error)
	ListSecrets(ctx context.Context, ns v1.NamespaceName) ([]v1.Secret, error)
	ReplaceSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, sec v1.Secret) (v1.Secret, error)
	DeleteSecret(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Grant (namespaced)
	GetGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Grant, error)
	CreateGrant(ctx context.Context, g v1.Grant) (v1.Grant, error)
	ListGrants(ctx context.Context, ns v1.NamespaceName) ([]v1.Grant, error)
	ReplaceGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, g v1.Grant) (v1.Grant, error)
	DeleteGrant(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// EgressPolicy (namespaced)
	GetEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EgressPolicy, error)
	CreateEgressPolicy(ctx context.Context, ep v1.EgressPolicy) (v1.EgressPolicy, error)
	ListEgressPolicies(ctx context.Context, ns v1.NamespaceName) ([]v1.EgressPolicy, error)
	ReplaceEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ep v1.EgressPolicy) (v1.EgressPolicy, error)
	DeleteEgressPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Invocation (namespaced)
	GetInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Invocation, error)
	CreateInvocation(ctx context.Context, inv v1.Invocation) (v1.Invocation, error)
	ListInvocations(ctx context.Context, ns v1.NamespaceName) ([]v1.Invocation, error)
	ReplaceInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, inv v1.Invocation) (v1.Invocation, error)
	DeleteInvocation(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// RuntimeClass (cluster-scoped)
	GetRuntimeClass(ctx context.Context, name v1.ObjectName) (v1.RuntimeClass, error)
	CreateRuntimeClass(ctx context.Context, rc v1.RuntimeClass) (v1.RuntimeClass, error)
	ListRuntimeClasses(ctx context.Context) ([]v1.RuntimeClass, error)
	ReplaceRuntimeClass(ctx context.Context, name v1.ObjectName, rc v1.RuntimeClass) (v1.RuntimeClass, error)
	DeleteRuntimeClass(ctx context.Context, name v1.ObjectName, rv string) error

	// WorkerNode (cluster-scoped)
	GetWorkerNode(ctx context.Context, name v1.ObjectName) (v1.WorkerNode, error)
	CreateWorkerNode(ctx context.Context, w v1.WorkerNode) (v1.WorkerNode, error)
	ListWorkerNodes(ctx context.Context) ([]v1.WorkerNode, error)
	ReplaceWorkerNode(ctx context.Context, name v1.ObjectName, w v1.WorkerNode) (v1.WorkerNode, error)
	DeleteWorkerNode(ctx context.Context, name v1.ObjectName, rv string) error

	// Gateway (cluster-scoped)
	GetGateway(ctx context.Context, name v1.ObjectName) (v1.Gateway, error)
	CreateGateway(ctx context.Context, gw v1.Gateway) (v1.Gateway, error)
	ListGateways(ctx context.Context) ([]v1.Gateway, error)
	ReplaceGateway(ctx context.Context, name v1.ObjectName, gw v1.Gateway) (v1.Gateway, error)
	DeleteGateway(ctx context.Context, name v1.ObjectName, rv string) error

	// KVStore (namespaced) — ADR-0072
	GetKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.KVStore, error)
	CreateKVStore(ctx context.Context, ks v1.KVStore) (v1.KVStore, error)
	ListKVStores(ctx context.Context, ns v1.NamespaceName) ([]v1.KVStore, error)
	ReplaceKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ks v1.KVStore) (v1.KVStore, error)
	DeleteKVStore(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error
	// HandoverKVStore makes workflow the owner of a kept store (ADR-0178 Decision 6).
	HandoverKVStore(ctx context.Context, ns v1.NamespaceName, name, workflow v1.ObjectName) (v1.KVStore, error)

	// Bucket (namespaced) — ADR-0080
	GetBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Bucket, error)
	CreateBucket(ctx context.Context, b v1.Bucket) (v1.Bucket, error)
	ListBuckets(ctx context.Context, ns v1.NamespaceName) ([]v1.Bucket, error)
	ReplaceBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, b v1.Bucket) (v1.Bucket, error)
	DeleteBucket(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// CatalogService (namespaced) — ADR-0086
	GetCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.CatalogService, error)
	CreateCatalogService(ctx context.Context, cs v1.CatalogService) (v1.CatalogService, error)
	ListCatalogServices(ctx context.Context, ns v1.NamespaceName) ([]v1.CatalogService, error)
	ReplaceCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, cs v1.CatalogService) (v1.CatalogService, error)
	DeleteCatalogService(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Identity (namespaced) — ADR-0135, FEAT-0008/F100
	GetIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Identity, error)
	CreateIdentity(ctx context.Context, id v1.Identity) (v1.Identity, error)
	ListIdentities(ctx context.Context, ns v1.NamespaceName) ([]v1.Identity, error)
	ReplaceIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, id v1.Identity) (v1.Identity, error)
	DeleteIdentity(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Role + RolesAssignment (namespaced) — ADR-0136, FEAT-0008/F101
	GetRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Role, error)
	CreateRole(ctx context.Context, ro v1.Role) (v1.Role, error)
	ListRoles(ctx context.Context, ns v1.NamespaceName) ([]v1.Role, error)
	ReplaceRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ro v1.Role) (v1.Role, error)
	DeleteRole(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error
	GetRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.RolesAssignment, error)
	CreateRolesAssignment(ctx context.Context, ra v1.RolesAssignment) (v1.RolesAssignment, error)
	ListRolesAssignments(ctx context.Context, ns v1.NamespaceName) ([]v1.RolesAssignment, error)
	ReplaceRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, ra v1.RolesAssignment) (v1.RolesAssignment, error)
	DeleteRolesAssignment(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Site (namespaced) — ADR-0139, FEAT-0003/F103
	GetSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Site, error)
	CreateSite(ctx context.Context, si v1.Site) (v1.Site, error)
	ListSites(ctx context.Context, ns v1.NamespaceName) ([]v1.Site, error)
	ReplaceSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, si v1.Site) (v1.Site, error)
	DeleteSite(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// App (namespaced) — ADR-0199, FEAT-0010/F113
	GetApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.App, error)
	CreateApp(ctx context.Context, app v1.App) (v1.App, error)
	ListApps(ctx context.Context, ns v1.NamespaceName) ([]v1.App, error)
	ReplaceApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, app v1.App) (v1.App, error)
	DeleteApp(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// AppRevision (namespaced, read-only) — ADR-0200, FEAT-0010/F114
	GetAppRevision(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.AppRevision, error)
	ListAppRevisions(ctx context.Context, ns v1.NamespaceName) ([]v1.AppRevision, error)

	// Policy (namespaced) — ADR-0074
	GetPolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Policy, error)
	CreatePolicy(ctx context.Context, pol v1.Policy) (v1.Policy, error)
	ListPolicies(ctx context.Context, ns v1.NamespaceName) ([]v1.Policy, error)
	ReplacePolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, pol v1.Policy) (v1.Policy, error)
	DeletePolicy(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Workflow (namespaced) — ADR-0094
	GetWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Workflow, error)
	CreateWorkflow(ctx context.Context, wf v1.Workflow) (v1.Workflow, error)
	ListWorkflows(ctx context.Context, ns v1.NamespaceName) ([]v1.Workflow, error)
	ReplaceWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, wf v1.Workflow) (v1.Workflow, error)
	DeleteWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// WorkflowRun (namespaced) — ADR-0094
	GetWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowRun, error)
	CreateWorkflowRun(ctx context.Context, run v1.WorkflowRun) (v1.WorkflowRun, error)
	ListWorkflowRuns(ctx context.Context, ns v1.NamespaceName) ([]v1.WorkflowRun, error)
	ReplaceWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, run v1.WorkflowRun) (v1.WorkflowRun, error)
	DeleteWorkflowRun(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error

	// Sensor (namespaced) — ADR-0109
	GetSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Sensor, error)
	CreateSensor(ctx context.Context, se v1.Sensor) (v1.Sensor, error)
	ListSensors(ctx context.Context, ns v1.NamespaceName) ([]v1.Sensor, error)
	ReplaceSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, se v1.Sensor) (v1.Sensor, error)
	DeleteSensor(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, rv string) error
}

// NewAPI builds the huma API on a chi router and registers all operations against h.
func NewAPI(r chi.Router, h Handlers) huma.API {
	installFaultErrors()
	cfg := huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0",
			Info: &huma.Info{
				Title:   "funcd API",
				Version: "v1alpha1",
				Description: "funcd control-plane API — code-first via huma. " +
					"Generated from typed Go operations; not hand-authored.",
			},
			Components:     &huma.Components{Schemas: newInlineRegistry()},
			OnAddOperation: []huma.AddOpFunc{rejectUnknownQuery},
		},
		DocsPath:    "/docs",
		OpenAPIPath: "/openapi",
		// Register application/problem+json as a format so huma can marshal
		// faultError (which embeds fault.Problem with RFC 9457 JSON tags).
		// huma v2 only ships application/json and application/yaml by default.
		Formats: map[string]huma.Format{
			"application/json": {
				Marshal:   jsonMarshal,
				Unmarshal: jsonUnmarshal,
			},
			"application/problem+json": {
				Marshal:   jsonMarshal,
				Unmarshal: jsonUnmarshal,
			},
		},
		DefaultFormat: "application/json",
	}
	api := humachi.New(r, cfg)
	installRouterErrors(r, api)

	RegisterRoutes(api, h)
	return api
}

// installRouterErrors renders the router's own 404 and 405 through huma, as problem+json.
func installRouterErrors(r chi.Router, api huma.API) {
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		_ = huma.WriteErr(api, humachi.NewContext(nil, req, w), http.StatusNotFound,
			"no route for "+req.Method+" "+req.URL.EscapedPath())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", strings.Join(allowedMethods(r, req), ", "))
		_ = huma.WriteErr(api, humachi.NewContext(nil, req, w), http.StatusMethodNotAllowed,
			"method "+req.Method+" is not allowed")
	})
}

// allowedMethods lists the methods r serves on the request's path. chi's own 405 writes one Allow line
// per method, so a client that reads one header value sees a single, random method (RFC 9110 §10.2.1).
func allowedMethods(r chi.Routes, req *http.Request) []string {
	path := req.URL.RawPath
	if path == "" {
		path = req.URL.Path
	}
	if rctx := chi.RouteContext(req.Context()); rctx != nil && rctx.RoutePath != "" {
		path = rctx.RoutePath
	}
	var allowed []string
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		if r.Match(chi.NewRouteContext(), m, path) {
			allowed = append(allowed, m)
		}
	}
	return allowed
}

// jsonMarshal is the standard JSON marshaler used by huma.
func jsonMarshal(w io.Writer, v interface{}) error {
	return json.NewEncoder(w).Encode(v)
}

// jsonUnmarshal is the standard JSON unmarshaler used by huma.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// faultError adapts a stdlib-only fault.Error to huma's StatusError so huma reads the right status.
// It embeds fault.Problem (RFC 9457 JSON tags) so huma can serialize it as flat application/problem+json.
type faultError struct {
	fault.Problem
}

func (e *faultError) Error() string  { return e.Detail }
func (e *faultError) GetStatus() int { return e.Status }

// ContentType serves the body as application/problem+json (ADR-0005).
func (e *faultError) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

//nolint:gochecknoglobals // huma.NewError is process-global, so it is replaced once
var faultErrorsOnce sync.Once

// installFaultErrors overrides huma.NewError (ADR-0005 §4), so the errors huma raises itself (validation,
// parsing, the router's 404/405) render the same fault.Problem body as a handler's fault.Error.
func installFaultErrors() {
	faultErrorsOnce.Do(func() { huma.NewError = newFaultError })
}

// newFaultError builds huma's own errors. A body over MaxBodyBytes is funcd's PayloadTooLarge problem
// (ADR-0148), and a lone input resolver's faultError keeps its own problem; no fault Kind decided the
// others, so their type is RFC 9457's about:blank, and each validation detail joins the detail that
// clients show.
func newFaultError(status int, msg string, errs ...error) huma.StatusError {
	if status == http.StatusRequestEntityTooLarge {
		return &faultError{Problem: fault.ToProblem(fault.PayloadTooLargef("controlplane.body", "%s", msg))}
	}
	var fe *faultError
	if len(errs) == 1 && errors.As(errs[0], &fe) {
		return fe
	}
	var details []string
	for _, err := range errs {
		var d huma.ErrorDetailer
		switch {
		case err == nil:
			continue
		case !errors.As(err, &d):
			details = append(details, err.Error())
		case d.ErrorDetail().Location == "":
			details = append(details, d.ErrorDetail().Message)
		default:
			details = append(details, d.ErrorDetail().Message+" ("+d.ErrorDetail().Location+")")
		}
	}
	detail := msg
	if len(details) > 0 {
		detail += ": " + strings.Join(details, "; ")
	}
	return &faultError{Problem: fault.Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}}
}

// wrapFaultError wraps a fault.Error as a huma StatusError. Exported for testing.
// It converts the *fault.Error to a fault.Problem via fault.ToProblem (the single
// Kind→status map), so the error body is RFC 9457 problem+json.
func wrapFaultError(err error) error {
	if err == nil {
		return nil
	}
	p := fault.ToProblem(err)
	return &faultError{Problem: p}
}
