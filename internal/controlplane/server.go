package controlplane

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/store"
)

// Deps configures the control-plane API server (ADR-0018, internal component).
type Deps struct {
	Store       store.Store
	Authorizer  auth.Authorizer
	Credentials middleware.CredentialStore
	Logger      *slog.Logger
	// Admissions are extra admissions registered on the write-path pipeline (ADR-0063), appended
	// after the built-in validate admission. ADR-0064 passes the link admissions here. Optional.
	Admissions []admission.Admission
	// Logs is the function-log reader (ADR-0084). Optional; when set, NewServer registers the
	// namespaced GET …/functions/{name}/logs route (authorized get/Function per caller).
	Logs LogQuerier
	// RunLogs is the run-scoped log querier (ADR-0106). Optional; when set, NewServer registers
	// GET …/workflowruns/{name}/logs (authorized get/WorkflowRun; resolves status.traceId).
	RunLogs WorkflowRunLogQuerier
	// DeadLetters is the eventing DLQ store (ADR-0118). Optional; when set (with Replayer), NewServer
	// registers the DLQ read + replay/discard routes under …/namespaces/{ns}/deadletters.
	DeadLetters deadletter.Store
	// Replayer performs the imperative DLQ replay (ADR-0118 §4). Required alongside DeadLetters.
	Replayer Replayer
}

// NewServer builds the authenticated, authorized, store-backed control-plane API
// (ADR-0018): a chi router with the authn middleware mounted, serving the huma
// operations (ADR-0005) against the store-backed Handlers. The spec/docs endpoints
// stay public; everything under /apis requires a valid token/API key.
func NewServer(d Deps) (http.Handler, error) {
	const op = "controlplane.NewServer"
	if d.Store == nil {
		return nil, fault.Invalidf(op, "store is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf(op, "authorizer is required")
	}
	if d.Credentials == nil {
		return nil, fault.Invalidf(op, "credentials are required")
	}

	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := chi.NewRouter()
	r.Use(middleware.Authn(d.Credentials))
	admissions := append([]admission.Admission{admission.NewValidateAdmission()}, d.Admissions...)
	api := NewAPI(r, NewStoreHandlers(d.Store, d.Authorizer, admission.NewPipeline(admissions...)))
	if d.Logs != nil { // ADR-0084: the function-log read route, tenant-scoped by the same RBAC PEP
		RegisterLogs(api, d.Logs, d.Authorizer)
	}
	if d.RunLogs != nil { // ADR-0106: the run-scoped log read route (resolves status.traceId), same RBAC PEP
		RegisterWorkflowRunLogs(api, d.RunLogs, d.Authorizer)
	}
	if d.DeadLetters != nil && d.Replayer != nil { // ADR-0118: the DLQ read + replay/discard surface
		RegisterDeadLetters(api, d.DeadLetters, d.Replayer, d.Authorizer)
	}
	logger.Info("control-plane API server constructed", "component", "controlplane")
	return r, nil
}
