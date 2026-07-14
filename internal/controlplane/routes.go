package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
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
	registerCatalogService(api, h)
	registerIdentity(api, h)
	registerRole(api, h)
	registerRolesAssignment(api, h)
	registerPolicy(api, h)
	registerWorkflow(api, h)
	registerWorkflowRun(api, h)
	registerSensor(api, h)
}

// ---- shared input types ----

// namespacedGet is input for get-by-name on a namespaced resource.
type namespacedGet struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
}

// namespacedDelete is input for delete on a namespaced resource.
type namespacedDelete struct {
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
}

// namespacedList is input for list in a namespace.
type namespacedList struct {
	Namespace v1.NamespaceName `path:"namespace"`
}

// clusterScopedGet is input for get-by-name on a cluster-scoped resource.
type clusterScopedGet struct {
	Name v1.ObjectName `path:"name"`
}

// clusterScopedDelete is input for delete on a cluster-scoped resource.
type clusterScopedDelete struct {
	Name v1.ObjectName `path:"name"`
}

// ===== Namespace (cluster-scoped) =====

type createNamespaceInput struct {
	Body v1.Namespace
}
type listNamespaceOutput struct {
	Body []v1.Namespace
}
type namespaceOutput struct {
	Body v1.Namespace
}

func registerNamespace(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces"

	huma.Register(api, huma.Operation{
		OperationID: "listNamespaces", Method: http.MethodGet, Path: base,
		Tags: []string{"Namespace"},
	}, func(ctx context.Context, _ *struct{}) (*listNamespaceOutput, error) {
		ns, err := h.ListNamespaces(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listNamespaceOutput{Body: ns}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createNamespace", Method: http.MethodPost, Path: base,
		Tags: []string{"Namespace"},
	}, func(ctx context.Context, in *createNamespaceInput) (*namespaceOutput, error) {
		ns, err := h.CreateNamespace(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &namespaceOutput{Body: ns}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getNamespace", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Namespace"},
	}, func(ctx context.Context, in *clusterScopedGet) (*namespaceOutput, error) {
		ns, err := h.GetNamespace(ctx, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &namespaceOutput{Body: ns}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceNamespace", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Namespace"},
	}, func(ctx context.Context, in *struct {
		Name v1.ObjectName `path:"name"`
		Body v1.Namespace
	}) (*namespaceOutput, error) {
		ns, err := h.ReplaceNamespace(ctx, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &namespaceOutput{Body: ns}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteNamespace", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Namespace"},
	}, func(ctx context.Context, in *clusterScopedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteNamespace(ctx, in.Name))
	})
}

// ===== ResourceGroup (namespaced) =====

type createResourceGroupInput struct {
	Body v1.ResourceGroup
}
type resourceGroupOutput struct {
	Body v1.ResourceGroup
}
type listResourceGroupOutput struct {
	Body []v1.ResourceGroup
}

func registerResourceGroup(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/resourcegroups"

	huma.Register(api, huma.Operation{
		OperationID: "listResourceGroups", Method: http.MethodGet, Path: base,
		Tags: []string{"ResourceGroup"},
	}, func(ctx context.Context, in *namespacedList) (*listResourceGroupOutput, error) {
		rgs, err := h.ListResourceGroups(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listResourceGroupOutput{Body: rgs}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createResourceGroup", Method: http.MethodPost, Path: base,
		Tags: []string{"ResourceGroup"},
	}, func(ctx context.Context, in *createResourceGroupInput) (*resourceGroupOutput, error) {
		rg, err := h.CreateResourceGroup(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &resourceGroupOutput{Body: rg}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getResourceGroup", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"ResourceGroup"},
	}, func(ctx context.Context, in *namespacedGet) (*resourceGroupOutput, error) {
		rg, err := h.GetResourceGroup(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &resourceGroupOutput{Body: rg}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceResourceGroup", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"ResourceGroup"},
	}, func(ctx context.Context, in *struct {
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.ResourceGroup
	}) (*resourceGroupOutput, error) {
		rg, err := h.ReplaceResourceGroup(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &resourceGroupOutput{Body: rg}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteResourceGroup", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"ResourceGroup"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteResourceGroup(ctx, in.Namespace, in.Name))
	})
}

// ===== Function (namespaced) =====

type createFunctionInput struct {
	Body v1.Function
}
type functionOutput struct {
	Body v1.Function
}
type listFunctionOutput struct {
	Body []v1.Function
}

func registerFunction(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/functions"

	huma.Register(api, huma.Operation{
		OperationID: "listFunctions", Method: http.MethodGet, Path: base,
		Tags: []string{"Function"},
	}, func(ctx context.Context, in *namespacedList) (*listFunctionOutput, error) {
		fns, err := h.ListFunctions(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listFunctionOutput{Body: fns}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createFunction", Method: http.MethodPost, Path: base,
		Tags: []string{"Function"},
	}, func(ctx context.Context, in *createFunctionInput) (*functionOutput, error) {
		fn, err := h.CreateFunction(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &functionOutput{Body: fn}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getFunction", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Function"},
	}, func(ctx context.Context, in *namespacedGet) (*functionOutput, error) {
		fn, err := h.GetFunction(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &functionOutput{Body: fn}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceFunction", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Function"},
	}, func(ctx context.Context, in *struct {
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Function
	}) (*functionOutput, error) {
		fn, err := h.ReplaceFunction(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &functionOutput{Body: fn}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteFunction", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Function"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteFunction(ctx, in.Namespace, in.Name))
	})
}

// ===== Revision (namespaced) =====

type createRevisionInput struct {
	Body v1.Revision
}
type revisionOutput struct {
	Body v1.Revision
}
type listRevisionOutput struct {
	Body []v1.Revision
}

func registerRevision(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/revisions"

	huma.Register(api, huma.Operation{
		OperationID: "listRevisions", Method: http.MethodGet, Path: base,
		Tags: []string{"Revision"},
	}, func(ctx context.Context, in *namespacedList) (*listRevisionOutput, error) {
		revs, err := h.ListRevisions(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listRevisionOutput{Body: revs}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createRevision", Method: http.MethodPost, Path: base,
		Tags: []string{"Revision"},
	}, func(ctx context.Context, in *createRevisionInput) (*revisionOutput, error) {
		rev, err := h.CreateRevision(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &revisionOutput{Body: rev}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getRevision", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Revision"},
	}, func(ctx context.Context, in *namespacedGet) (*revisionOutput, error) {
		rev, err := h.GetRevision(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &revisionOutput{Body: rev}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceRevision", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Revision"},
	}, func(ctx context.Context, in *struct {
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Revision
	}) (*revisionOutput, error) {
		rev, err := h.ReplaceRevision(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &revisionOutput{Body: rev}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteRevision", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Revision"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteRevision(ctx, in.Namespace, in.Name))
	})
}

// ===== Route (namespaced) =====

type createRouteInput struct {
	Body v1.Route
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
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Route
	}) (*routeOutput, error) {
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
		return nil, wrapFaultError(h.DeleteRoute(ctx, in.Namespace, in.Name))
	})
}
