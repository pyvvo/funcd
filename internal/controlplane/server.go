package controlplane

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// Deps configures the control-plane API server (ADR-0018, internal component).
type Deps struct {
	Store       store.Store
	Authorizer  auth.Authorizer
	Credentials middleware.CredentialStore
	Logger      *slog.Logger
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
	NewAPI(r, NewStoreHandlers(d.Store, d.Authorizer))
	logger.Info("control-plane API server constructed", "component", "controlplane")
	return r, nil
}
