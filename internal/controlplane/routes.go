package controlplane

import (
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
	registerAPIToken(api, h)
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
