// Package kv is the KV service (ADR-0019): the function-facing Facade (PDP-authorized,
// <namespace>/<binding>/<key> prefixed, results prefix-stripped) over the kvstore.KV
// port, plus the KV services.TypeHandler the Service dispatcher routes type:kv to. It is
// the first instance of the service facade pattern (blob/secrets copy this shape).
package kv

import (
	"context"
	"log/slog"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
	"github.com/green-0-rabbit/funcd/internal/services"
)

// FacadeDeps configures the KV facade (the PEP).
type FacadeDeps struct {
	KV         kvstore.KV
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Facade is what a function calls: PDP-authorized + namespace/binding-prefixed KV.
type Facade struct {
	kv     kvstore.KV
	authz  auth.Authorizer
	logger *slog.Logger
}

// NewFacade builds the KV facade. KV + Authorizer are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	if d.KV == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "kv is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "authorizer is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{kv: d.KV, authz: d.Authorizer, logger: logger.With("component", "services.kv")}, nil
}

func (f *Facade) authorize(ctx context.Context, id auth.Identity, verb auth.Verb, ns v1.NamespaceName) error {
	dec, err := f.authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: v1.KindService, Namespace: ns})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "services.kv.authz", "authorize")
	}
	if !dec.Allowed {
		return fault.Forbiddenf("services.kv.authz", "kv %s in %q denied: %s", verb, ns, dec.Reason)
	}
	return nil
}

func tenantKey(ns v1.NamespaceName, binding, key string) string {
	return string(ns) + "/" + binding + "/" + key
}

// Get returns the value for the binding's key (PDP-authorized).
func (f *Facade) Get(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) ([]byte, bool, error) {
	if err := f.authorize(ctx, id, auth.VerbGet, ns); err != nil {
		return nil, false, err
	}
	return f.kv.Get(ctx, tenantKey(ns, binding, key))
}

// Put stores value under the binding's key (PDP-authorized).
func (f *Facade) Put(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string, value []byte) error {
	if err := f.authorize(ctx, id, auth.VerbUpdate, ns); err != nil {
		return err
	}
	return f.kv.Put(ctx, tenantKey(ns, binding, key), value)
}

// Delete removes the binding's key (PDP-authorized).
func (f *Facade) Delete(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, key string) error {
	if err := f.authorize(ctx, id, auth.VerbDelete, ns); err != nil {
		return err
	}
	return f.kv.Delete(ctx, tenantKey(ns, binding, key))
}

// List returns the binding's keys under prefix, with the <ns>/<binding>/ tenant prefix
// stripped so the caller only sees its own key space.
func (f *Facade) List(ctx context.Context, id auth.Identity, ns v1.NamespaceName, binding, prefix string) ([]string, error) {
	if err := f.authorize(ctx, id, auth.VerbList, ns); err != nil {
		return nil, err
	}
	tenantPrefix := string(ns) + "/" + binding + "/"
	keys, err := f.kv.List(ctx, tenantPrefix+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, tenantPrefix)
	}
	return out, nil
}

// --- the Service dispatcher's KV TypeHandler ---

type handler struct{}

// NewHandler returns the KV services.TypeHandler (Type() == ServiceTypeKV).
func NewHandler() services.TypeHandler { return handler{} }

func (handler) Type() v1.ServiceType { return v1.ServiceTypeKV }

// Reconcile validates the KV binding spec; the in-memory driver needs no external
// provisioning, so a valid binding is immediately Ready (the dispatcher writes status).
func (handler) Reconcile(_ context.Context, svc *v1.Service) (controller.Result, error) {
	if svc.Spec.KV == nil || svc.Spec.KV.Binding == "" {
		return controller.Result{}, fault.Invalidf("services.kv.Reconcile", "kv service %q requires spec.kv.binding", svc.Name)
	}
	return controller.Result{}, nil
}
