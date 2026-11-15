package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// RegisterRoutes registers every kind's CRUD operation on api, dispatching to h.
func RegisterRoutes(api huma.API, h Handlers) {
	registerNamespace(api, h)
	registerResourceGroup(api, h)
	registerFunction(api, h)
	registerRevision(api, h)
	registerRoute(api, h)
	registerService(api, h)
	registerEventSource(api, h)
	registerConfigMap(api, h)
	registerSecret(api, h)
	registerGrant(api, h)
	registerEgressPolicy(api, h)
	registerInvocation(api, h)
	registerRuntimeClass(api, h)
	registerWorker(api, h)
	registerGateway(api, h)
	registerKVStore(api, h)
	registerBucket(api, h)
	registerLake(api, h)
	registerSQLDatabase(api, h)
	registerIdentity(api, h)
	registerRole(api, h)
	registerRolesAssignment(api, h)
	registerSite(api, h)
	registerApp(api, h)
	registerAppRevision(api, h)
	registerWorkloadBackup(api, h)
	registerPolicy(api, h)
	registerWorkflow(api, h)
	registerWorkflowRun(api, h)
	registerSensor(api, h)
}

// ---- shared input types ----

// namespacedGet is input for get-by-name on a namespaced resource.
type namespacedGet struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
}

// namespacedDelete is input for delete on a namespaced resource.
type namespacedDelete struct {
	IfMatchParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
}

// namespacedList is input for list in a namespace.
type namespacedList struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The namespace whose objects the answer lists."`
}

// clusterScopedGet is input for get-by-name on a cluster-scoped resource.
type clusterScopedGet struct {
	Name v1.ObjectName `path:"name" doc:"The object's name."`
}

// clusterScopedDelete is input for delete on a cluster-scoped resource.
type clusterScopedDelete struct {
	IfMatchParams
	Name v1.ObjectName `path:"name" doc:"The object's name."`
}

// ===== Route (namespaced) =====

type createRouteInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Route
}
type routeOutput struct {
	Body v1.Route
}
type listRouteOutput struct {
	Body []v1.Route
}

func registerRoute(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/routes"

	huma.Register(api, huma.Operation{
		OperationID: "listRoutes", Method: http.MethodGet, Path: base,
		Tags: []string{"Route"},
	}, func(ctx context.Context, in *namespacedList) (*listRouteOutput, error) {
		rts, err := h.ListRoutes(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listRouteOutput{Body: rts}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createRoute", Method: http.MethodPost, Path: base,
		Tags: []string{"Route"},
	}, func(ctx context.Context, in *createRouteInput) (*routeOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		rt, err := h.CreateRoute(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &routeOutput{Body: rt}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getRoute", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Route"},
	}, func(ctx context.Context, in *namespacedGet) (*routeOutput, error) {
		rt, err := h.GetRoute(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &routeOutput{Body: rt}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceRoute", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Route"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Route
	}) (*routeOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		rt, err := h.ReplaceRoute(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &routeOutput{Body: rt}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteRoute", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Route"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteRoute(ctx, in.Namespace, in.Name, in.rv))
	})
}
