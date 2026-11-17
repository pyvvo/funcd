package controlplane

import (
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contractcheck"
)

// SpecAPI is the API that specgen documents: the stub handlers, the stub routes of the operations that need a
// running platform, and the call of a Function, which the gateway serves. The committed spec and the user guide's Spec
// pages come from it (ADR-0005, ADR-0323).
func SpecAPI() huma.API {
	api := NewAPI(chi.NewRouter(), NewStubHandlers())
	RegisterStubLogs(api)            // ADR-0084: the function-logs route
	RegisterStubEngineLogs(api)      // ADR-0342: the engines' logs routes
	RegisterStubWorkflowRunLogs(api) // ADR-0106: the run-scoped logs route
	RegisterStubJobRunLogs(api)      // ADR-0347: a JobRun's logs
	RegisterStubVolumes(api)         // ADR-0262: delete a retained Function volume
	RegisterStubDeadLetters(api)     // ADR-0118: the DLQ read and replay/discard routes
	RegisterStubAppRetry(api)        // ADR-0214: the App retry route
	RegisterStubPlatformBackup(api)  // ADR-0205: the platform backup status route
	RegisterStubHold(api)            // ADR-0206: the hold status and release routes
	RegisterStubJoinToken(api)       // FEAT-0002: a worker node's join token
	addInvokeFunction(api.OpenAPI()) // ADR-0329 Decision 2: the gateway's call of a Function
	return api
}

// addInvokeFunction documents POST /function/{name} on the gateway, whose server is the address server.dataPlaneAddr
// sets (ADR-0329 Decision 2). Only the document holds it: the control plane routes no call of a Function. Its security
// is optional, since the namespace's stance decides whether a call needs a credential (ADR-0113).
func addInvokeFunction(doc *huma.OpenAPI) {
	registry := doc.Components.Schemas
	registry.Schema(reflect.TypeFor[contractcheck.ContractViolation](), true, "")
	anyJSON := map[string]*huma.MediaType{"application/json": {Schema: &huma.Schema{}}}
	op := Describe(huma.Operation{
		OperationID: "invokeFunction", Method: http.MethodPost, Path: "/function/{name}", Tags: []string{string(v1.KindFunction)},
		Description: "A call needs a credential when the Function's spec.ingress.auth.mode, else its namespace's " +
			"spec.edgeDefaults.auth.mode, is authenticated; else it needs none.",
		Servers: []*huma.Server{{URL: "{gateway}", Description: "funcd's gateway, which serves the calls of Functions.",
			Variables: map[string]*huma.ServerVariable{"gateway": {Default: "http://127.0.0.1:8081",
				Description: "The address that server.dataPlaneAddr sets. Its default takes a free port, so set a fixed " +
					"one, such as 127.0.0.1:8081."}}}},
		Parameters: []*huma.Param{
			{Name: "name", In: "path", Required: true, Description: "The Function's name.",
				Schema: registry.Schema(reflect.TypeFor[v1.ObjectName](), true, "")},
			{Name: "X-Funcd-Namespace", In: "header", Description: "The Function's namespace, when the Host header " +
				"names none, as shop.localhost names shop; without either, default.",
				Schema: registry.Schema(reflect.TypeFor[v1.NamespaceName](), true, "")},
		},
		RequestBody: &huma.RequestBody{Content: anyJSON, Description: "The call's input, any JSON value, which the " +
			"handler gets as its event's data. A CloudEvent, an object with specversion or data, passes as it is."},
		Responses: map[string]*huma.Response{"200": {Description: "The handler's result.", Content: anyJSON}},
	}, "Call a Function", invokeCases()...)
	op.Security = append([]map[string][]string{{}}, op.Security...)
	doc.AddOperation(&op)
}

// invokeCases are the answers that funcd itself gives a call of a Function (ADR-0329 Decision 2).
func invokeCases() []ErrorCase {
	violation := huma.DefaultSchemaNamer(reflect.TypeFor[contractcheck.ContractViolation](), "")
	return []ErrorCase{
		problem(fault.Invalid, "The body did not arrive within the gateway's read timeout.",
			"Send the whole request at once, without pausing."),
		problem(fault.Unauthorized, "The namespace's stance asks for a credential, and the call carries none, or one "+
			"that funcd does not know.",
			"Send a credential of funcd's config as a bearer token in the Authorization header, or in X-Api-Key."),
		problem(fault.Unauthorized, "The namespace's stance asks for a credential while server.auth.edge is off, so "+
			"funcd checks none and refuses the call, whatever credential it carries.",
			"Ask funcd's operator to set server.auth.edge to true."),
		problem(fault.Forbidden, "The credential's role or namespaces do not allow it to call Functions in the namespace.",
			"Use a credential of the namespace, such as its developer's."),
		problem(fault.NotFound, "The namespace holds no Function of that name that answers at its own address: it "+
			"does not exist, its ingress is off, or its ingress names a host or a path of its own.",
			"Check the name and the namespace, or call the Function at the address that its ingress names."),
		problem(fault.PayloadTooLarge, "The body is larger than 1 MiB, the most that funcd reads to build the call's event.",
			"Send a smaller body, or pass large data by reference, such as a Bucket key."),
		{Status: http.StatusUnprocessableEntity, Body: violation,
			When: "The event data does not match the Function's input contract; nothing reached its worker.",
			Fix:  "Correct the values that details names."},
		{Status: http.StatusInternalServerError, Body: violation,
			When: "The handler's result does not match the Function's output contract, so funcd sends this instead.",
			Fix:  "Correct the handler, or its output contract."},
		problem(fault.ResourceExhausted, "The call is over the rate that server.limits sets.",
			"Retry after the seconds that the Retry-After header gives."),
		problem(fault.Unavailable, "The gateway already serves as many calls at once as server.limits.maxInFlight allows.",
			"Retry the call later."),
		problem(fault.Unavailable, "The worker failed during the call, before its answer. funcd does not retry the call.",
			"Retry it, if the handler may run twice."),
		problem(fault.Unavailable, "funcd could not read the namespace's settings, so it does not know how to serve the "+
			"call.", "Retry the call later."),
		problem(fault.Unavailable, "funcd cannot read the Function's contract.", "Retry the call later."),
		problem(fault.Unavailable, "The namespace is held for its data, so funcd refuses its calls until the hold is "+
			"released.", "Retry after the seconds that the Retry-After header gives; funcdctl hold release "+
			"--namespace releases the hold."),
		problem(fault.Unavailable, "The namespace is held while a Restore loads it.",
			"Retry after the seconds that the Retry-After header gives, once the Restore has ended."),
		problem(fault.Unavailable, "No worker of the Function became ready within invoke.activationTimeout, 30 s by "+
			"default.", "Retry the call; if it keeps failing, read the Function's status for why its worker is not ready."),
		problem(fault.Unavailable, "The Function is Failed; the detail names the reason, such as ShapeInvalid.",
			"Correct what the Function's status names, such as its artifact, then retry."),
		problem(fault.Unavailable, "funcd is stopping, and the call was waiting for a worker of the Function to "+
			"become ready.", "Retry the call once funcd runs again."),
		problem(fault.DeadlineExceeded, "The worker did not start its answer within the call's deadline: the "+
			"Function's spec.timeout, else invoke.defaultTimeout.", "Make the handler answer sooner, or raise its spec.timeout."),
	}
}
