package controlplane

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

// SpecAPI is the API that specgen documents: the stub handlers, and the stub routes of the operations that need a
// running platform. The committed spec and the user guide's Spec pages come from it (ADR-0005, ADR-0323).
func SpecAPI() huma.API {
	api := NewAPI(chi.NewRouter(), NewStubHandlers())
	RegisterStubLogs(api)            // ADR-0084: the function-logs route
	RegisterStubWorkflowRunLogs(api) // ADR-0106: the run-scoped logs route
	RegisterStubVolumes(api)         // ADR-0262: delete a retained Function volume
	RegisterStubDeadLetters(api)     // ADR-0118: the DLQ read and replay/discard routes
	RegisterStubAppRetry(api)        // ADR-0214: the App retry route
	RegisterStubPlatformBackup(api)  // ADR-0205: the platform backup status route
	RegisterStubHold(api)            // ADR-0206: the hold status and release routes
	return api
}
