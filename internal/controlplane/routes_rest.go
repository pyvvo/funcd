package controlplane

import (
	"context"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ===== Namespace (cluster-scoped) =====

func registerNamespace(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.Namespace]{
		kind: "Namespace", kinds: "Namespaces", path: "namespaces",
		list: h.ListNamespaces, create: h.CreateNamespace, get: h.GetNamespace,
		replace: h.ReplaceNamespace, remove: h.DeleteNamespace,
		cases: &kindCases{replace: []ErrorCase{problem(fault.Conflict, "The body changes spec.node, and the namespace "+
			"holds objects: their data stays on the node that runs them.", "Keep spec.node. To move a namespace, back its "+
			"stores up, delete its objects, change spec.node, apply the objects again and restore the stores.")}},
	})
}

// ===== ResourceGroup (namespaced) — ADR-0170 =====

// deleteResourceGroupInput is a ResourceGroup delete's input: a namespaced delete's, and force.
type deleteResourceGroupInput struct {
	IfMatchParams
	Namespace v1.NamespaceName `path:"namespace" doc:"The object's namespace."`
	Name      v1.ObjectName    `path:"name" doc:"The object's name."`
	Force     bool             `query:"force" doc:"Delete the members first; each member's protections still apply."`
}

func registerResourceGroup(api huma.API, h Handlers) {
	ops := registerNamespacedCRUD(api, crudRoutes[v1.ResourceGroup]{
		kind: "ResourceGroup", kinds: "ResourceGroups", path: "resourcegroups",
		list: h.ListResourceGroups, create: h.CreateResourceGroup, get: h.GetResourceGroup,
		replace: h.ReplaceResourceGroup,
		cases: &kindCases{remove: []ErrorCase{
			problem(fault.Conflict, "The ResourceGroup still has members: objects of the namespace, other than "+
				"ResourceGroups, whose metadata.resourceGroup names it and that no other object controls.",
				"Delete the members first, or delete the ResourceGroup with force=true, which deletes them."),
			problem(fault.Conflict, "With force=true, funcd refuses the delete of a member, such as a Function that "+
				"a Function outside the group links to; the detail gives that refusal.",
				"Do what the refusal asks, then delete again; the members deleted already stay deleted."),
			blank(http.StatusUnprocessableEntity, "The force parameter is neither true nor false.",
				"Send force=true, or leave it out."),
		}},
	})
	huma.Register(api, ops.remove, func(ctx context.Context, in *deleteResourceGroupInput) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteResourceGroup(ctx, in.Namespace, in.Name, in.Force, in.rv))
	})
}

// ===== Function (namespaced) =====

func registerFunction(api huma.API, h Handlers) {
	writes := functionWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.Function]{
		kind: "Function", kinds: "Functions", path: "functions",
		list: h.ListFunctions, create: h.CreateFunction, get: h.GetFunction,
		replace: h.ReplaceFunction, remove: h.DeleteFunction,
		cases: &kindCases{create: writes, replace: writes, remove: []ErrorCase{problem(fault.Conflict,
			"Another Function's or a Job's spec.bindings.links names the Function.", "Remove that link first.")}},
	})
}

// functionWrites are the cases a Function's create and replace add to their verb's: the admissions of its links
// (ADR-0064), its bindings (ADR-0248) and its local volumes (ADR-0262).
func functionWrites() []ErrorCase {
	return append(slices.Insert(workloadWrites(), 1, problem(fault.Invalid, "The links would close a cycle among the "+
		"namespace's Functions, such as a Function that links to itself.", "Remove a link of the cycle.")),
		hostElsewhere("spec.ingress.host"))
}

// workloadWrites are the cases that a Function's and a Job's create and replace share (ADR-0347 Decision 1): no link
// names a Job, so a Job's links close no cycle.
func workloadWrites() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "A link in spec.bindings.links names a Function that does not exist in the namespace.",
			"Apply the target Function first."),
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
		nodeOffline(),
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
		replace: h.ReplaceEventSource, remove: h.DeleteEventSource, cases: &kindCases{},
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
		replace: h.ReplaceIdentity, remove: h.DeleteIdentity, cases: &kindCases{},
	})
}

// ===== OIDCProvider (namespaced) — ADR-0335, ADR-0336, FEAT-0018 =====

func registerOIDCProvider(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.OIDCProvider]{
		kind: "OIDCProvider", kinds: "OIDCProviders", path: "oidcproviders",
		list: h.ListOIDCProviders, create: h.CreateOIDCProvider, get: h.GetOIDCProvider,
		replace: h.ReplaceOIDCProvider, remove: h.DeleteOIDCProvider, cases: &kindCases{},
	})
}

// ===== Role + RolesAssignment (namespaced) — ADR-0136, FEAT-0008/F101 =====

func registerRole(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Role]{
		kind: "Role", kinds: "Roles", path: "roles",
		list: h.ListRoles, create: h.CreateRole, get: h.GetRole, replace: h.ReplaceRole, remove: h.DeleteRole,
		cases: &kindCases{},
	})
}

func registerRolesAssignment(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.RolesAssignment]{
		kind: "RolesAssignment", kinds: "RolesAssignments", path: "rolesassignments",
		list: h.ListRolesAssignments, create: h.CreateRolesAssignment, get: h.GetRolesAssignment,
		replace: h.ReplaceRolesAssignment, remove: h.DeleteRolesAssignment, cases: &kindCases{},
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
				problem(fault.Conflict, "The store still holds keys.", "Delete its keys first."), nodeOffline()},
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

// nodeOffline is the 503 of a write whose check reads the state of the worker node that runs the namespace, while that
// node is offline (FEAT-0002 F250): the control plane refuses it rather than skip the check.
func nodeOffline() ErrorCase {
	return problem(fault.Unavailable, "The namespace runs on a worker node that is offline, and the write needs a check "+
		"that reads that node's state.", nodeBack)
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
				problem(fault.Conflict, "A prefix of the Bucket still holds a Lake's catalog.", catalogFix), nodeOffline()},
		},
	})
}

// ===== Lake (namespaced) — ADR-0086 =====

func registerLake(api huma.API, h Handlers) {
	writes := []ErrorCase{problem(fault.Invalid, "A binding names a Bucket, a ConfigMap or a Secret that the spec "+
		"does not declare, such as a Bucket missing from spec.buckets.", "Declare it in the spec, or bind one that it declares."),
		hostElsewhere("spec.ingress.host")}
	registerNamespacedCRUD(api, crudRoutes[v1.Lake]{
		kind: "Lake", kinds: "Lakes", path: "lakes",
		list: h.ListLakes, create: h.CreateLake, get: h.GetLake,
		replace: h.ReplaceLake, remove: h.DeleteLake,
		cases: &kindCases{create: writes, replace: writes},
	})
}

// ===== SQLDatabase (namespaced) — ADR-0232 =====

func registerSQLDatabase(api huma.API, h Handlers) {
	const engineFix = "Retry once the SQLDatabase is Ready and its engine answers without an error."
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
				// The check fails closed when it cannot ask the engine (ADR-0232 Decision 7).
				problem(fault.Conflict, "The database's engine is not Ready and the database has data, so funcd "+
					"cannot check whether it holds a table.", engineFix),
				problem(fault.Conflict, "The database's engine does not answer, so funcd cannot check whether it "+
					"holds a table.", engineFix),
				problem(fault.Conflict, "The database's engine answers with an error, so funcd cannot check whether "+
					"it holds a table.", engineFix), nodeOffline()},
		},
	})
}

// ===== Policy (namespaced) — ADR-0074 =====

func registerPolicy(api huma.API, h Handlers) {
	writes := policyWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.Policy]{
		kind: "Policy", kinds: "Policies", path: "policies",
		list: h.ListPolicies, create: h.CreatePolicy, get: h.GetPolicy, replace: h.ReplacePolicy, remove: h.DeletePolicy,
		cases: &kindCases{create: writes, replace: writes},
	})
}

// policyWrites are the cases a Policy's create and replace add to their verb's: the refusals of the policy-validity
// admission, which holds spec.cedar to the actions and entity types of the registered capabilities (ADR-0116) and to
// the Policy's namespace (ADR-0177).
func policyWrites() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "The text in spec.cedar does not parse as Cedar, or holds no statement.",
			"Correct the text where the detail says it breaks, and keep at least one permit or forbid statement."),
		problem(fault.Invalid, "A statement of spec.cedar does not name exactly one action that funcd knows, or "+
			"names an entity of a type that funcd does not know.",
			"Name exactly one action that funcd knows, such as kv::read, and only entity types that it knows; when the "+
				"detail names an unknown action or entity type, it lists the ones that funcd knows."),
		problem(fault.Invalid, "A statement of spec.cedar names an entity of another namespace: its id does not "+
			"begin with the Policy's namespace and a slash.",
			"Name only entities of the Policy's namespace, or apply the Policy in the namespace of the entities it names."),
	}
}

// ===== Route (namespaced) =====

func registerRoute(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Route]{
		kind: "Route", kinds: "Routes", path: "routes",
		list: h.ListRoutes, create: h.CreateRoute, get: h.GetRoute, replace: h.ReplaceRoute, remove: h.DeleteRoute,
		cases: &kindCases{create: []ErrorCase{hostElsewhere("spec.host")}, replace: []ErrorCase{hostElsewhere("spec.host")}},
	})
}

// ===== EgressPolicy (namespaced) =====

func registerEgressPolicy(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.EgressPolicy]{
		kind: "EgressPolicy", kinds: "EgressPolicies", path: "egresspolicies",
		list: h.ListEgressPolicies, create: h.CreateEgressPolicy, get: h.GetEgressPolicy,
		replace: h.ReplaceEgressPolicy, remove: h.DeleteEgressPolicy, cases: &kindCases{},
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

// ===== APIToken (cluster-scoped, FEAT-0002 F248) =====

func registerAPIToken(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.APIToken]{
		kind: "APIToken", kinds: "APITokens", path: "apitokens",
		list: h.ListAPITokens, create: h.CreateAPIToken, get: h.GetAPIToken,
		replace: h.ReplaceAPIToken, remove: h.DeleteAPIToken, cases: &kindCases{},
	})
}

// ===== WorkerNode (cluster-scoped) =====

func registerWorker(api huma.API, h Handlers) {
	registerClusterCRUD(api, clusterRoutes[v1.WorkerNode]{
		kind: "WorkerNode", kinds: "WorkerNodes", path: "workernodes",
		list: h.ListWorkerNodes, create: h.CreateWorkerNode, get: h.GetWorkerNode,
		replace: h.ReplaceWorkerNode, remove: h.DeleteWorkerNode,
		cases: &kindCases{noSpec: true, remove: []ErrorCase{problem(fault.Conflict, "A Namespace's spec.node names the "+
			"WorkerNode.", "Move the namespaces that the detail names to another node, or delete them, first.")}},
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
	// The binding-declared admission (ADR-0248, ADR-0250) checks an image step's bindings.
	undeclared := []ErrorCase{problem(fault.Invalid, "A binding of an image step names a store, a Lake, a ConfigMap "+
		"or a Secret that neither the Workflow's spec nor its App declares, such as a KVStore missing from spec.kv.",
		"Declare it at the root of the spec, or bind one that it declares.")}
	registerNamespacedCRUD(api, crudRoutes[v1.Workflow]{
		kind: "Workflow", kinds: "Workflows", path: "workflows",
		list: h.ListWorkflows, create: h.CreateWorkflow, get: h.GetWorkflow,
		replace: h.ReplaceWorkflow, remove: h.DeleteWorkflow,
		cases: &kindCases{create: undeclared, replace: undeclared},
	})
}

// ===== WorkflowRun (namespaced) — ADR-0094 =====

func registerWorkflowRun(api huma.API, h Handlers) {
	// The workflowrun-payload and workflowrun-contract admissions check the input of a create and a replace (ADR-0094,
	// ADR-0098); workflowrun-spec-immutable refuses a replace that changes what the run runs, so the shared 409 of a
	// taken name must not offer a replace.
	input := []ErrorCase{
		problem(fault.PayloadTooLarge, "The run input in spec.input is larger than workflow.payloadLimit, 256 KiB by "+
			"default.", "Send a smaller input, and pass large data by reference, such as the key of an object in a Bucket."),
		problem(fault.Invalid, "The run input in spec.input does not match the input schema in the Workflow's "+
			"status.contract.", "Correct the fields that the detail names."),
	}
	fixed := problem(fault.Conflict, "The body changes the run's spec.workflow, spec.input or spec.replay, which are "+
		"fixed once the run is created.", "Start a new run under another name.")
	registerNamespacedCRUD(api, crudRoutes[v1.WorkflowRun]{
		kind: "WorkflowRun", kinds: "WorkflowRuns", path: "workflowruns",
		list: h.ListWorkflowRuns, create: h.CreateWorkflowRun, get: h.GetWorkflowRun,
		replace: h.ReplaceWorkflowRun, remove: h.DeleteWorkflowRun,
		cases: &kindCases{create: input, replace: slices.Concat(input, []ErrorCase{fixed}),
			words: map[sharedCase]wording{nameTaken: {fix: "Start a new run under another name: a run's spec.workflow, " +
				"spec.input and spec.replay are fixed once it is created, so replacing the existing run cannot change them."}}},
	})
}

func registerSensor(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Sensor]{
		kind: "Sensor", kinds: "Sensors", path: "sensors",
		list: h.ListSensors, create: h.CreateSensor, get: h.GetSensor, replace: h.ReplaceSensor, remove: h.DeleteSensor,
		cases: &kindCases{},
	})
}

// ===== Job (namespaced) — ADR-0347 =====

func registerJob(api huma.API, h Handlers) {
	writes := workloadWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.Job]{
		kind: "Job", kinds: "Jobs", path: "jobs",
		list: h.ListJobs, create: h.CreateJob, get: h.GetJob, replace: h.ReplaceJob, remove: h.DeleteJob,
		cases: &kindCases{create: writes, replace: writes},
	})
}

// ===== JobRun (namespaced) — ADR-0347 =====

func registerJobRun(api huma.API, h Handlers) {
	// The jobrun-payload and jobrun-contract admissions check a create's input; jobrun-spec refuses a replace that
	// changes spec.job or spec.input, or turns spec.cancel back (ADR-0347 Decision 3).
	input := []ErrorCase{
		problem(fault.PayloadTooLarge, "The run input in spec.input is larger than job.payloadLimit, 256 KiB by default.",
			"Send a smaller input, and pass large data by reference, such as the key of an object in a Bucket."),
		blank(http.StatusUnprocessableEntity, "The run input in spec.input breaks the input schema in the Job's "+
			"status.contract; the detail names each value that breaks it.", "Correct the values that the detail names."),
	}
	fixed := problem(fault.Invalid, "The body changes the run's spec.job or spec.input, which are fixed once the run is "+
		"created, or turns spec.cancel back to false.", "Keep spec.job and spec.input, and start a new run under another "+
		"name to run other input. A cancelled run stays cancelled.")
	registerNamespacedCRUD(api, crudRoutes[v1.JobRun]{
		kind: "JobRun", kinds: "JobRuns", path: "jobruns",
		list: h.ListJobRuns, create: h.CreateJobRun, get: h.GetJobRun, replace: h.ReplaceJobRun, remove: h.DeleteJobRun,
		cases: &kindCases{create: input, replace: []ErrorCase{fixed},
			words: map[sharedCase]wording{nameTaken: {fix: "Start a new run under another name: a run's spec.job and " +
				"spec.input are fixed once it is created, so replacing the existing run cannot change them."}}},
	})
}

// ===== Site (namespaced) — ADR-0139, FEAT-0003/F103 =====

func registerSite(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.Site]{
		kind: "Site", kinds: "Sites", path: "sites",
		list: h.ListSites, create: h.CreateSite, get: h.GetSite, replace: h.ReplaceSite, remove: h.DeleteSite,
		cases: &kindCases{create: []ErrorCase{hostElsewhere("spec.ingress.host")}, replace: []ErrorCase{
			problem(fault.Invalid, "The spec changes spec.bindings.blob[0].prefix, which holds the Site's bundles and is "+
				"never removed from the Bucket.", "Keep the prefix. A new prefix needs a new Site, under another name."),
			hostElsewhere("spec.ingress.host")}},
	})
}

// ===== App (namespaced) — ADR-0199, FEAT-0010/F113 =====

func registerApp(api huma.API, h Handlers) {
	writes := appWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.App]{
		kind: "App", kinds: "Apps", path: "apps",
		list: h.ListApps, create: h.CreateApp, get: h.GetApp, replace: h.ReplaceApp, remove: h.DeleteApp,
		cases: &kindCases{create: writes, replace: slices.Concat(writes, []ErrorCase{
			problem(fault.Conflict, "The new spec.version is outside the range that another App, once started, "+
				"requires of the App in its spec.requires.", "Widen that App's range first, or keep a version within it."),
		}), remove: []ErrorCase{problem(fault.Conflict, "Another App's spec.requires names the App.",
			"Remove that requirement first.")}},
	})
}

// appWrites are the cases an App's create and replace add to their verb's: the rules of the app-parts admission
// (ADR-0199 Decision 3, ADR-0213 Decision 7), which runs each part through the admissions of its kind, and the cycle of
// app-requires (ADR-0219 Decision 7). A create meets the ref rule too: the admissions run before the store refuses a
// taken name, and the rule matches the controller by kind and name.
func appWrites() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "A Function, a Lake or a Workflow's image step of the App binds a Secret that "+
			"spec.secrets does not declare.", "Declare the Secret in spec.secrets, or remove it from the bindings."),
		problem(fault.Invalid, "A ref entry names an object that the App of this name controls, such as a part that "+
			"an earlier spec declared by name.", "Declare it by name instead; to keep a store as a ref, apply the App "+
			"with the store's deletion set to retain first."),
		problem(fault.Invalid, "The App's spec.requires would close a cycle among the namespace's Apps, such as an "+
			"App that requires itself.", "Remove a requirement of the cycle."),
		problem(fault.Invalid, "A part breaks a rule that a write of its kind is held to, such as a Function's link "+
			"to a Function that neither the App nor the namespace holds; the detail names the part's entry, such as "+
			"spec.functions[0].", "Correct that entry as the detail says."),
		problem(fault.Conflict, "A part conflicts with what the namespace or funcd holds, as a write of its kind "+
			"would, such as a Function's local volume that does not fit in volumes.capacity; the detail names the "+
			"part's entry.", "Resolve what the detail names, then apply the App again."),
	}
}

// ===== AppRevision (namespaced, read-only) — ADR-0200, FEAT-0010/F114 =====

func registerAppRevision(api huma.API, h Handlers) {
	registerNamespacedRead(api, crudRoutes[v1.AppRevision]{
		kind: "AppRevision", kinds: "AppRevisions", path: "apprevisions",
		list: h.ListAppRevisions, get: h.GetAppRevision, cases: &kindCases{},
	})
}

// ===== BackupSchedule, Backup, Restore (namespaced) — ADR-0222, FEAT-0009/F111 =====

func registerWorkloadBackup(api huma.API, h Handlers) {
	schedule := backupScheduleWrites()
	registerNamespacedCRUD(api, crudRoutes[v1.BackupSchedule]{
		kind: "BackupSchedule", kinds: "BackupSchedules", path: "backupschedules",
		list: h.ListBackupSchedules, create: h.CreateBackupSchedule, get: h.GetBackupSchedule,
		replace: h.ReplaceBackupSchedule, remove: h.DeleteBackupSchedule,
		cases: &kindCases{create: schedule, replace: schedule},
	})
	registerNamespacedCRUD(api, crudRoutes[v1.Backup]{
		kind: "Backup", kinds: "Backups", path: "backups",
		list: h.ListBackups, create: h.CreateBackup, get: h.GetBackup, replace: h.ReplaceBackup, remove: h.DeleteBackup,
		cases: &kindCases{
			create: []ErrorCase{problem(fault.Invalid, "spec.ttl is longer than the ttl of the schedule that "+
				"spec.schedule names.", "Set spec.ttl to at most the schedule's ttl, or leave it out to take the schedule's.")},
			replace: []ErrorCase{backupSpecFixed("Backup")},
		},
	})
	registerNamespacedCRUD(api, crudRoutes[v1.Restore]{
		kind: "Restore", kinds: "Restores", path: "restores",
		list: h.ListRestores, create: h.CreateRestore, get: h.GetRestore, replace: h.ReplaceRestore, remove: h.DeleteRestore,
		cases: &kindCases{create: restoreInPlaceCreates(), replace: []ErrorCase{backupSpecFixed("Restore")},
			remove: []ErrorCase{problem(fault.Conflict, "The Restore loads a member in place and has not ended.",
				"Delete it once it is Succeeded or Failed. "+restoreStuck)}},
	})
}

// backupScheduleWrites are the cases a BackupSchedule's create and replace add to their verb's: its recipients and its
// target, which the backup-schedule admission checks (ADR-0222 Decision 6).
func backupScheduleWrites() []ErrorCase {
	return []ErrorCase{
		problem(fault.Invalid, "An entry of spec.encryption.recipients is not an age recipient, or the recipients mix "+
			"X25519 and hybrid keys.", "List at least two distinct age recipients of one family: X25519 keys, which "+
			"start with age1, or hybrid keys, which start with age1pq1."),
		problem(fault.Invalid, "spec.target.url names the bucket of blob.target, the funcd server's own blob store, "+
			"so one failure of that store would take the copies with it.", "Name another bucket, best one of another "+
			"S3 provider."),
	}
}

// backupSpecFixed is the 400 of a replace that changes the spec of a Backup or a Restore, which is fixed at creation
// (ADR-0222 Decision 1).
func backupSpecFixed(kind string) ErrorCase {
	return problem(fault.Invalid, "The spec differs from the stored "+kind+"'s: the spec of "+an(kind)+" does not change.",
		"Create another "+kind+" instead.")
}

// restoreStuck is the way out of an in-place Restore that waits in Pending for good, which no cancel ends (ADR-0228
// Decision 2.4 and its Open questions).
const restoreStuck = "A Restore stuck in Pending fails once the Backup that it waits for is deleted."

// restoreInPlaceCreates are the refusals of a Restore with a member in place (ADR-0228 Decision 2.4): it needs update
// on the Namespace, one runs at a time in a namespace, and none starts while an earlier load's hold stands.
func restoreInPlaceCreates() []ErrorCase {
	return []ErrorCase{
		problem(fault.Forbidden, "A member sets inPlace, and the credential's role does not allow it to update the "+
			"Namespace, as only an admin's does.", "Use an admin's credential, or load the member into another, empty "+
			"store with as."),
		problem(fault.Conflict, "A member sets inPlace, and another in-place Restore of the namespace has not ended.",
			"Create the Restore once the other one is Succeeded or Failed. "+restoreStuck),
		problem(fault.Conflict, "A member sets inPlace, and the namespace is still held by an earlier in-place Restore "+
			"whose load did not complete.", "Check the namespace's stores, release the hold with funcdctl hold release "+
			"--namespace, then create the Restore again."),
		nodeOffline(),
	}
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

// registerNamespacedCRUD registers r's five operations and returns them. A kind whose delete takes more than a
// version, such as a ResourceGroup's force, leaves remove nil and registers its own delete on the returned operation.
func registerNamespacedCRUD[T crudKind, PT objectPtr[T]](api huma.API, r crudRoutes[T]) restOperations {
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
	if r.remove == nil {
		return ops
	}
	huma.Register(api, ops.remove,
		func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
			return nil, wrapFaultError(r.remove(ctx, in.Namespace, in.Name, in.rv))
		})
	return ops
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

// registerClusterRead registers r's list and get only, and returns all five of its operations. A read-only kind
// (ADR-0339 Decision 1, as ADR-0172 Decision 2) stops here: its POST, PUT and DELETE answer 405 with Allow: GET.
func registerClusterRead[T crudKind](api huma.API, r clusterRoutes[T]) restOperations {
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
	huma.Register(api, ops.get,
		func(ctx context.Context, in *clusterScopedGet) (*bodyOutput[T], error) {
			return respond(r.get(ctx, in.Name))
		})
	return ops
}

// clusterRoutes is the REST surface of one cluster-scoped kind under /apis/funcd.io/v1alpha1/<path>, registered by
// registerClusterCRUD, or by registerClusterRead for a read-only kind. Its inputs carry no namespace: huma reads the
// path parameters from each handler's input type.
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
	Name v1.ObjectName `path:"name" doc:"The object's name."`
	Body T
}

func registerClusterCRUD[T crudKind, PT objectPtr[T]](api huma.API, r clusterRoutes[T]) {
	ops := registerClusterRead(api, r)
	huma.Register(api, ops.create,
		func(ctx context.Context, in *clusterBodyInput[T]) (*bodyOutput[T], error) {
			return respond(r.create(withDryRun(ctx, in.DryRun), in.Body))
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
