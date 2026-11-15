package controlplane

import (
	"context"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ===== Function (namespaced) =====

func registerFunction(api huma.API, h Handlers) {
	writes := functionWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.Function]{
		kind: "Function", kinds: "Functions", path: "functions",
		list: h.ListFunctions, create: h.CreateFunction, get: h.GetFunction,
		replace: h.ReplaceFunction, remove: h.DeleteFunction,
		cases: &kindCases{create: writes, replace: writes, remove: []ErrorCase{problem(fault.Conflict,
			"Another Function's spec.bindings.links names the Function.", "Remove that link first.")}},
	})
}

// functionWrites are the cases a Function's create and replace add to their verb's: the admissions of its links
// (ADR-0064), its bindings (ADR-0248) and its local volumes (ADR-0262).
func functionWrites() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "A link in spec.bindings.links names a Function that does not exist in the namespace.",
			"Apply the target Function first."),
		problem(fault.Invalid, "The links would close a cycle among the namespace's Functions, such as a Function "+
			"that links to itself.", "Remove a link of the cycle."),
		problem(fault.Invalid, "A binding names a store, a Lake, a ConfigMap or a Secret that the spec does not "+
			"declare, such as a KVStore missing from spec.kv.", "Declare it in the spec, or bind one that it declares."),
		problem(fault.Invalid, "The spec sets spec.volumes, and volumes are off on this daemon.",
			"Remove spec.volumes, or ask the operator to turn volumes on."),
		problem(fault.Invalid, "A local volume's size differs from the size it was applied with: a volume is not "+
			"resized.", "Keep the volume's size. A new size needs a new volume, under another name."),
		problem(fault.Conflict, "A local volume does not fit in the capacity that volumes.capacity sets, with the "+
			"volumes reserved already.", "Delete the volumes that are no longer needed, or ask the operator to raise "+
			"volumes.capacity."),
		problem(fault.Conflict, "A local volume does not fit in the namespace's spec.volumeBudget, with the "+
			"namespace's other volumes.", "Delete the namespace's volumes that are no longer needed, or ask an admin "+
			"to raise its spec.volumeBudget."),
	}
}

// ===== Revision (namespaced, read-only) — ADR-0172 =====

func registerRevision(api huma.API, h Handlers) {
	registerNamespacedRead(api, crudRoutes[v1.Revision]{
		kind: "Revision", kinds: "Revisions", path: "revisions",
		list: h.ListRevisions, get: h.GetRevision, cases: &kindCases{},
	})
}

// ===== Service (namespaced) =====

func registerService(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Service]{
		kind: "Service", kinds: "Services", path: "services",
		list: h.ListServices, create: h.CreateService, get: h.GetService,
		replace: h.ReplaceService, remove: h.DeleteService,
	})
}

// ===== EventSource (namespaced) =====

func registerEventSource(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.EventSource]{
		kind: "EventSource", kinds: "EventSources", path: "eventsources",
		list: h.ListEventSources, create: h.CreateEventSource, get: h.GetEventSource,
		replace: h.ReplaceEventSource, remove: h.DeleteEventSource,
	})
}

// ===== ConfigMap (namespaced) =====

func registerConfigMap(api huma.API, h Handlers) {
	// The KVStore marker migration's completion record (refuseMigrationRecord) refuses every write.
	reserved := []ErrorCase{problem(fault.Conflict, "The ConfigMap is kvstore-marker-migration in funcd-system, a "+
		"record that funcd keeps for itself.", "Use another ConfigMap: only funcd writes this one.")}
	registerNamespacedCRUD(api, crudRoutes[v1.ConfigMap]{
		kind: "ConfigMap", kinds: "ConfigMaps", path: "configmaps",
		list: h.ListConfigMaps, create: h.CreateConfigMap, get: h.GetConfigMap,
		replace: h.ReplaceConfigMap, remove: h.DeleteConfigMap,
		cases: &kindCases{create: reserved, replace: reserved, remove: reserved},
	})
}

// ===== Secret (namespaced) =====

func registerSecret(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Secret]{
		kind: "Secret", kinds: "Secrets", path: "secrets",
		list: h.ListSecrets, create: h.CreateSecret, get: h.GetSecret, replace: h.ReplaceSecret, remove: h.DeleteSecret,
		cases: &kindCases{},
	})
}

// ===== Grant (namespaced) =====

func registerGrant(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Grant]{
		kind: "Grant", kinds: "Grants", path: "grants",
		list: h.ListGrants, create: h.CreateGrant, get: h.GetGrant, replace: h.ReplaceGrant, remove: h.DeleteGrant,
	})
}

// ===== Identity (namespaced) — ADR-0135, FEAT-0008/F100 =====

func registerIdentity(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Identity]{
		kind: "Identity", kinds: "Identities", path: "identities",
		list: h.ListIdentities, create: h.CreateIdentity, get: h.GetIdentity,
		replace: h.ReplaceIdentity, remove: h.DeleteIdentity,
	})
}

// ===== Role + RolesAssignment (namespaced) — ADR-0136, FEAT-0008/F101 =====

func registerRole(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Role]{
		kind: "Role", kinds: "Roles", path: "roles",
		list: h.ListRoles, create: h.CreateRole, get: h.GetRole, replace: h.ReplaceRole, remove: h.DeleteRole,
	})
}

func registerRolesAssignment(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.RolesAssignment]{
		kind: "RolesAssignment", kinds: "RolesAssignments", path: "rolesassignments",
		list: h.ListRolesAssignments, create: h.CreateRolesAssignment, get: h.GetRolesAssignment,
		replace: h.ReplaceRolesAssignment, remove: h.DeleteRolesAssignment,
	})
}

// ===== KVStore (namespaced) — ADR-0072 =====

func registerKVStore(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.KVStore]{
		kind: "KVStore", kinds: "KVStores", path: "kvstores",
		list: h.ListKVStores, create: h.CreateKVStore, get: h.GetKVStore,
		replace: h.ReplaceKVStore, remove: h.DeleteKVStore,
		cases: &kindCases{
			create: []ErrorCase{problem(fault.Invalid,
				"The namespace already holds the most KVStores that kvstore.maxStoresPerNamespace allows.",
				"Delete a KVStore that the namespace no longer needs, or ask the operator to raise the cap.")},
			replace: []ErrorCase{madeByLiveOwner("KVStore"),
				problem(fault.Conflict, "The spec drops a table that a Function's spec.bindings.kv still names.",
					"Remove that binding from the Function first."),
				problem(fault.Conflict, "The spec adds back a table that was dropped, and funcd has not yet "+
					"deleted the dropped table's data.", "Retry after a few seconds.")},
			remove: []ErrorCase{
				problem(fault.Conflict, "A Function's spec.bindings.kv still names the store.",
					"Remove that binding from the Function first."),
				problem(fault.Conflict, "The store still holds keys.", "Delete its keys first.")},
		},
	})
	kv := verbCases{kind: "KVStore", kinds: "KVStores", namespaced: true}
	huma.Register(api, Describe(huma.Operation{
		OperationID: "handoverKVStore", Method: http.MethodPost,
		Path: "/apis/funcd.io/v1alpha1/namespaces/{namespace}/kvstores/{name}/handover", Tags: []string{"KVStore"},
	}, "Hand a kept KVStore over to a Workflow", slices.Concat([]ErrorCase{unauthorized(),
		forbidden("update and delete KVStores and read Workflows in the namespace"),
		problem(fault.NotFound, "No KVStore or no Workflow of those names exists in the namespace.",
			"Check the names and the namespace."),
		badPath(kv.path()), badQuery(),
		problem(fault.Conflict, "The Workflow does not declare the store in its spec.kv.",
			"Declare the store in the Workflow's spec.kv, then retry."),
		madeByLiveOwner("KVStore")}, bodyCases())...,
	), func(ctx context.Context, in *struct {
		Namespace v1.NamespaceName `path:"namespace" doc:"The store's namespace."`
		Name      v1.ObjectName    `path:"name" doc:"The store's name."`
		Body      KVStoreHandover
	}) (*bodyOutput[v1.KVStore], error) {
		return respond(h.HandoverKVStore(ctx, in.Namespace, in.Name, in.Body.Workflow))
	})
}

// KVStoreHandover is the handover request body: the Workflow that becomes the store's owner.
type KVStoreHandover struct {
	Workflow v1.ObjectName `json:"workflow" doc:"The Workflow that becomes the store's owner. Its spec.kv declares the store."`
}

// madeByLiveOwner is the 409 of a write to a store that a Function, a Workflow or an App made and that still exists
// (refuseLiveMarked).
func madeByLiveOwner(kind string) ErrorCase { return madeByLive(kind, "Function, Workflow or App") }

// madeByLive is madeByLiveOwner for a kind that makers make, such as a Bucket, which a Lake or a Site makes too.
func madeByLive(kind, makers string) ErrorCase {
	return problem(fault.Conflict, "The "+makers+" that made the "+kind+" still exists, and owns it.",
		"Change the "+kind+" through that owner, or delete the owner first.")
}

// ===== Bucket (namespaced) — ADR-0080 =====

func registerBucket(api huma.API, h Handlers) {
	// A Lake's catalog lives outside the Bucket and keeps its prefix as an object does (ADR-0293).
	const catalogFix = "Once no Lake is located on the prefix, delete the catalog through the S3 gateway, as a " +
		"writer of the prefix: the detail names its S3 bucket and key."
	registerNamespacedCRUD(api, crudRoutes[v1.Bucket]{
		kind: "Bucket", kinds: "Buckets", path: "buckets",
		list: h.ListBuckets, create: h.CreateBucket, get: h.GetBucket, replace: h.ReplaceBucket, remove: h.DeleteBucket,
		cases: &kindCases{
			create: []ErrorCase{problem(fault.Invalid, "The namespace already holds 100 Buckets, the most that funcd "+
				"allows.", "Delete a Bucket that the namespace no longer needs.")},
			replace: []ErrorCase{madeByLive("Bucket", "Function, Workflow, App, Lake or Site"),
				problem(fault.Conflict, "The spec drops a prefix that a Function's spec.bindings.blob still names.",
					"Remove that binding from the Function first."),
				problem(fault.Conflict, "The spec drops a prefix that still holds objects.",
					"Delete the prefix's objects first."),
				problem(fault.Conflict, "The spec drops a prefix that still holds a Lake's catalog.", catalogFix)},
			remove: []ErrorCase{
				problem(fault.Conflict, "A Function's spec.bindings.blob still names the Bucket.",
					"Remove that binding from the Function first."),
				problem(fault.Conflict, "A prefix of the Bucket still holds objects.", "Delete the prefix's objects first."),
				problem(fault.Conflict, "A prefix of the Bucket still holds a Lake's catalog.", catalogFix)},
		},
	})
}

// ===== Lake (namespaced) — ADR-0086 =====

func registerLake(api huma.API, h Handlers) {
	undeclared := []ErrorCase{problem(fault.Invalid, "A binding names a Bucket, a ConfigMap or a Secret that the spec "+
		"does not declare, such as a Bucket missing from spec.buckets.", "Declare it in the spec, or bind one that it declares.")}
	registerNamespacedCRUD(api, crudRoutes[v1.Lake]{
		kind: "Lake", kinds: "Lakes", path: "lakes",
		list: h.ListLakes, create: h.CreateLake, get: h.GetLake,
		replace: h.ReplaceLake, remove: h.DeleteLake,
		cases: &kindCases{create: undeclared, replace: undeclared},
	})
}

// ===== SQLDatabase (namespaced) — ADR-0232 =====

func registerSQLDatabase(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.SQLDatabase]{
		kind: "SQLDatabase", kinds: "SQLDatabases", path: "sqldatabases",
		list: h.ListSQLDatabases, create: h.CreateSQLDatabase, get: h.GetSQLDatabase,
		replace: h.ReplaceSQLDatabase, remove: h.DeleteSQLDatabase,
		cases: &kindCases{
			create: []ErrorCase{problem(fault.Invalid,
				"The namespace already holds the most SQLDatabases that sql.maxDatabasesPerNamespace allows.",
				"Delete a SQLDatabase that the namespace no longer needs, or ask the operator to raise the cap.")},
			replace: []ErrorCase{madeByLiveOwner("SQLDatabase")},
			remove: []ErrorCase{
				problem(fault.Conflict, "A Function's spec.bindings.sql still names the database.",
					"Remove that binding from the Function first."),
				problem(fault.Conflict, "The database still holds a table.", "Drop its tables first."),
				// The check fails closed (ADR-0232 Decision 7).
				problem(fault.Conflict, "funcd cannot ask the database's engine whether it holds a table: the engine "+
					"does not answer, or it is not Ready and the database has data.", "Retry once the SQLDatabase is Ready.")},
		},
	})
}

// ===== Policy (namespaced) — ADR-0074 =====

func registerPolicy(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Policy]{
		kind: "Policy", kinds: "Policies", path: "policies",
		list: h.ListPolicies, create: h.CreatePolicy, get: h.GetPolicy, replace: h.ReplacePolicy, remove: h.DeletePolicy,
	})
}

// ===== EgressPolicy (namespaced) =====

func registerEgressPolicy(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.EgressPolicy]{
		kind: "EgressPolicy", kinds: "EgressPolicies", path: "egresspolicies",
		list: h.ListEgressPolicies, create: h.CreateEgressPolicy, get: h.GetEgressPolicy,
		replace: h.ReplaceEgressPolicy, remove: h.DeleteEgressPolicy,
	})
}

// ===== Invocation (namespaced) =====

func registerInvocation(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Invocation]{
		kind: "Invocation", kinds: "Invocations", path: "invocations",
		list: h.ListInvocations, create: h.CreateInvocation, get: h.GetInvocation,
		replace: h.ReplaceInvocation, remove: h.DeleteInvocation, cases: &kindCases{},
	})
}

// ===== RuntimeClass (cluster-scoped) =====

func registerRuntimeClass(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.RuntimeClass]{
		kind: "RuntimeClass", kinds: "RuntimeClasses", path: "runtimeclasses",
		list: h.ListRuntimeClasses, create: h.CreateRuntimeClass, get: h.GetRuntimeClass,
		replace: h.ReplaceRuntimeClass, remove: h.DeleteRuntimeClass,
	})
}

// ===== WorkerNode (cluster-scoped) =====

func registerWorker(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.WorkerNode]{
		kind: "WorkerNode", kinds: "WorkerNodes", path: "workernodes",
		list: h.ListWorkerNodes, create: h.CreateWorkerNode, get: h.GetWorkerNode,
		replace: h.ReplaceWorkerNode, remove: h.DeleteWorkerNode,
	})
}

// ===== Gateway (cluster-scoped) =====

func registerGateway(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.Gateway]{
		kind: "Gateway", kinds: "Gateways", path: "gateways",
		list: h.ListGateways, create: h.CreateGateway, get: h.GetGateway,
		replace: h.ReplaceGateway, remove: h.DeleteGateway,
	})
}

// ===== Workflow (namespaced) — ADR-0094 =====

func registerWorkflow(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Workflow]{
		kind: "Workflow", kinds: "Workflows", path: "workflows",
		list: h.ListWorkflows, create: h.CreateWorkflow, get: h.GetWorkflow,
		replace: h.ReplaceWorkflow, remove: h.DeleteWorkflow,
	})
}

// ===== WorkflowRun (namespaced) — ADR-0094 =====

func registerWorkflowRun(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.WorkflowRun]{
		kind: "WorkflowRun", kinds: "WorkflowRuns", path: "workflowruns",
		list: h.ListWorkflowRuns, create: h.CreateWorkflowRun, get: h.GetWorkflowRun,
		replace: h.ReplaceWorkflowRun, remove: h.DeleteWorkflowRun,
	})
}

func registerSensor(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Sensor]{
		kind: "Sensor", kinds: "Sensors", path: "sensors",
		list: h.ListSensors, create: h.CreateSensor, get: h.GetSensor, replace: h.ReplaceSensor, remove: h.DeleteSensor,
	})
}

// ===== Site (namespaced) — ADR-0139, FEAT-0003/F103 =====

func registerSite(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Site]{
		kind: "Site", kinds: "Sites", path: "sites",
		list: h.ListSites, create: h.CreateSite, get: h.GetSite, replace: h.ReplaceSite, remove: h.DeleteSite,
	})
}

// ===== App (namespaced) — ADR-0199, FEAT-0010/F113 =====

func registerApp(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.App]{
		kind: "App", kinds: "Apps", path: "apps",
		list: h.ListApps, create: h.CreateApp, get: h.GetApp, replace: h.ReplaceApp, remove: h.DeleteApp,
	})
}

// ===== AppRevision (namespaced, read-only) — ADR-0200, FEAT-0010/F114 =====

func registerAppRevision(api huma.API, h Handlers) {
	registerNamespacedRead(api, crudRoutes[v1.AppRevision]{
		kind: "AppRevision", kinds: "AppRevisions", path: "apprevisions",
		list: h.ListAppRevisions, get: h.GetAppRevision,
	})
}

// ===== BackupSchedule, Backup, Restore (namespaced) — ADR-0222, FEAT-0009/F111 =====

func registerWorkloadBackup(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.BackupSchedule]{
		kind: "BackupSchedule", kinds: "BackupSchedules", path: "backupschedules",
		list: h.ListBackupSchedules, create: h.CreateBackupSchedule, get: h.GetBackupSchedule,
		replace: h.ReplaceBackupSchedule, remove: h.DeleteBackupSchedule,
	})
	registerNamespacedCRUD(api, crudRoutes[v1.Backup]{
		kind: "Backup", kinds: "Backups", path: "backups",
		list: h.ListBackups, create: h.CreateBackup, get: h.GetBackup, replace: h.ReplaceBackup, remove: h.DeleteBackup,
	})
	registerNamespacedCRUD(api, crudRoutes[v1.Restore]{
		kind: "Restore", kinds: "Restores", path: "restores",
		list: h.ListRestores, create: h.CreateRestore, get: h.GetRestore, replace: h.ReplaceRestore, remove: h.DeleteRestore,
	})
}

// crudRoutes is the REST surface of one namespaced kind under
// /apis/funcd.io/v1alpha1/namespaces/{namespace}/<path>, registered by registerNamespacedCRUD, or by
// registerNamespacedRead for a read-only kind, which leaves create, replace and remove nil.
type crudRoutes[T crudKind] struct {
	kind, kinds, path string
	list              func(context.Context, v1.NamespaceName) ([]T, error)
	create            func(context.Context, T) (T, error)
	get               func(context.Context, v1.NamespaceName, v1.ObjectName) (T, error)
	replace           func(context.Context, v1.NamespaceName, v1.ObjectName, T) (T, error)
	remove            func(context.Context, v1.NamespaceName, v1.ObjectName, string) error
	cases             *kindCases // nil until the kind's operations are described (ADR-0329)
}

type namespacedBodyInput[T crudKind] struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Body      T
}

type namespacedNameBodyInput[T crudKind] struct {
	IfMatchParams
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
	Body      T
}

type bodyOutput[T crudKind] struct{ Body T }

type listOutput[T crudKind] struct{ Body []T }

func respond[T crudKind](v T, err error) (*bodyOutput[T], error) {
	if err != nil {
		return nil, wrapFaultError(err)
	}
	return &bodyOutput[T]{Body: v}, nil
}

func registerNamespacedCRUD[T crudKind, PT objectPtr[T]](api huma.API, r crudRoutes[T]) {
	ops := registerNamespacedRead(api, r)
	huma.Register(api, ops.create,
		func(ctx context.Context, in *namespacedBodyInput[T]) (*bodyOutput[T], error) {
			if err := matchPathNamespace(in.Namespace, PT(&in.Body).GetObjectMeta()); err != nil {
				return nil, wrapFaultError(err)
			}
			return respond(r.create(withDryRun(ctx, in.DryRun), in.Body))
		})
	huma.Register(api, ops.replace,
		func(ctx context.Context, in *namespacedNameBodyInput[T]) (*bodyOutput[T], error) {
			if err := withReplaceVersion(in.IfMatchParams, PT(&in.Body).GetObjectMeta()); err != nil {
				return nil, wrapFaultError(err)
			}
			return respond(r.replace(withDryRun(ctx, in.DryRun), in.Namespace, in.Name, in.Body))
		})
	huma.Register(api, ops.remove,
		func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
			return nil, wrapFaultError(r.remove(ctx, in.Namespace, in.Name, in.rv))
		})
}

// registerNamespacedRead registers r's list and get only, and returns all five of its operations. A read-only kind
// (ADR-0172 Decision 2, ADR-0200 Decision 2) stops here: its POST, PUT and DELETE answer 405 with Allow: GET.
func registerNamespacedRead[T crudKind](api huma.API, r crudRoutes[T]) restOperations {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/" + r.path
	ops := restOperationsOf(r.kind, r.kinds, base, base+"/{name}", true, r.cases)
	huma.Register(api, ops.list,
		func(ctx context.Context, in *namespacedList) (*listOutput[T], error) {
			items, err := r.list(ctx, in.Namespace)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &listOutput[T]{Body: items}, nil
		})
	huma.Register(api, ops.get,
		func(ctx context.Context, in *namespacedGet) (*bodyOutput[T], error) {
			return respond(r.get(ctx, in.Namespace, in.Name))
		})
	return ops
}

// clusterRoutes is the REST surface of one cluster-scoped kind under /apis/funcd.io/v1alpha1/<path>, registered by
// registerClusterCRUD. Its inputs carry no namespace: huma reads the path parameters from each handler's input type.
type clusterRoutes[T crudKind] struct {
	kind, kinds, path string
	list              func(context.Context) ([]T, error)
	create            func(context.Context, T) (T, error)
	get               func(context.Context, v1.ObjectName) (T, error)
	replace           func(context.Context, v1.ObjectName, T) (T, error)
	remove            func(context.Context, v1.ObjectName, string) error
	cases             *kindCases // nil until the kind's operations are described (ADR-0329)
}

type clusterBodyInput[T crudKind] struct {
	DryRunParams
	Body T
}

type clusterNameBodyInput[T crudKind] struct {
	IfMatchParams
	DryRunParams
	Name v1.ObjectName `path:"name"`
	Body T
}

func registerClusterCRUD[T crudKind, PT objectPtr[T]](api huma.API, r clusterRoutes[T]) {
	base := "/apis/funcd.io/v1alpha1/" + r.path
	ops := restOperationsOf(r.kind, r.kinds, base, base+"/{name}", false, r.cases)
	huma.Register(api, ops.list,
		func(ctx context.Context, _ *struct{}) (*listOutput[T], error) {
			items, err := r.list(ctx)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &listOutput[T]{Body: items}, nil
		})
	huma.Register(api, ops.create,
		func(ctx context.Context, in *clusterBodyInput[T]) (*bodyOutput[T], error) {
			return respond(r.create(withDryRun(ctx, in.DryRun), in.Body))
		})
	huma.Register(api, ops.get,
		func(ctx context.Context, in *clusterScopedGet) (*bodyOutput[T], error) {
			return respond(r.get(ctx, in.Name))
		})
	huma.Register(api, ops.replace,
		func(ctx context.Context, in *clusterNameBodyInput[T]) (*bodyOutput[T], error) {
			if err := withReplaceVersion(in.IfMatchParams, PT(&in.Body).GetObjectMeta()); err != nil {
				return nil, wrapFaultError(err)
			}
			return respond(r.replace(withDryRun(ctx, in.DryRun), in.Name, in.Body))
		})
	huma.Register(api, ops.remove,
		func(ctx context.Context, in *clusterScopedDelete) (*struct{}, error) {
			return nil, wrapFaultError(r.remove(ctx, in.Name, in.rv))
		})
}
