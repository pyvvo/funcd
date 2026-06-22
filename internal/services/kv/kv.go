// Package kv is the KV service (ADR-0019/0072): the function-facing Facade (grant-gated,
// <namespace>/<store>/<key> prefixed, results prefix-stripped) over the kvstore.KV port, plus the
// KV services.TypeHandler the Service dispatcher routes type:kv to, and the KindKVStore reconciler.
//
// ADR-0072 makes KV a declarative, owned resource: the facade resolves a caller's (function, binding)
// to a Grant (default-deny) — this Grant gate REPLACES the per-call KindService PDP check the facade
// did under ADR-0019. It enforces mode (rw for put/del), per-op caps, and a store-scoped key prefix.
package kv

import (
	"context"
	"log/slog"
	"strings"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/kvstore"
	"github.com/green-0-rabbit/funcd/internal/services"
)

// FacadeDeps configures the KV facade (the PEP). The Binder (ADR-0072) is the grant gate; it replaces
// the per-call KindService PDP check (ADR-0019) — control-plane CRUD of KVStore/Grant is still PDP-gated.
type FacadeDeps struct {
	KV     kvstore.KV
	Binder Binder
	Logger *slog.Logger
}

// Facade is what a function calls: grant-gated + namespace/store-prefixed KV (ADR-0072).
type Facade struct {
	kv     kvstore.KV
	binder Binder
	logger *slog.Logger
}

// NewFacade builds the KV facade. KV + Binder are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	if d.KV == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "kv is required")
	}
	if d.Binder == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "binder is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{kv: d.KV, binder: d.Binder, logger: logger.With("component", "services.kv")}, nil
}

// storeKey is the on-disk key for a (resolved store, key): "<ns>/<store>/<key>" (ADR-0072) — the key
// is namespaced by the GRANTED store, not the raw binding alias.
func storeKey(ns v1.NamespaceName, store v1.ObjectName, key string) string {
	return string(ns) + "/" + string(store) + "/" + key
}

func storePrefix(ns v1.NamespaceName, store v1.ObjectName) string {
	return string(ns) + "/" + string(store) + "/"
}

// resolveRead resolves a Grant for a read (get/list): any mode is accepted.
func (f *Facade) resolveRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string) (Binding, error) {
	return f.binder.Resolve(ctx, ns, fn, binding)
}

// resolveWrite resolves a Grant for a write (put/del): the mode must be rw, else Forbidden.
func (f *Facade) resolveWrite(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding string) (Binding, error) {
	b, err := f.binder.Resolve(ctx, ns, fn, binding)
	if err != nil {
		return Binding{}, err
	}
	if b.Mode != v1.KVModeRW {
		return Binding{}, fault.Forbiddenf("services.kv.write", "binding %q is read-only (ro Grant); writes require rw", binding)
	}
	return b, nil
}

// Get returns the value for the binding's key, gated by the caller's Grant (any mode).
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) ([]byte, bool, error) {
	b, err := f.resolveRead(ctx, ns, fn, binding)
	if err != nil {
		return nil, false, err
	}
	return f.kv.Get(ctx, storeKey(ns, b.Store, key))
}

// Put stores value under the binding's key (requires an rw Grant; per-op caps enforced first).
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, value []byte) error {
	b, err := f.resolveWrite(ctx, ns, fn, binding)
	if err != nil {
		return err
	}
	if int64(len(value)) > b.MaxValueBytes {
		return fault.Invalidf("services.kv.put", "value (%d bytes) exceeds the store cap (%d bytes)", len(value), b.MaxValueBytes)
	}
	if len(key) > b.MaxKeyBytes {
		return fault.Invalidf("services.kv.put", "key (%d bytes) exceeds the store cap (%d bytes)", len(key), b.MaxKeyBytes)
	}
	return f.kv.Put(ctx, storeKey(ns, b.Store, key), value)
}

// Delete removes the binding's key (requires an rw Grant).
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error {
	b, err := f.resolveWrite(ctx, ns, fn, binding)
	if err != nil {
		return err
	}
	return f.kv.Delete(ctx, storeKey(ns, b.Store, key))
}

// List returns the binding's keys under prefix (gated by the caller's Grant, any mode), with the
// <ns>/<store>/ store prefix stripped so the caller only sees its own key space.
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, prefix string) ([]string, error) {
	b, err := f.resolveRead(ctx, ns, fn, binding)
	if err != nil {
		return nil, err
	}
	sp := storePrefix(ns, b.Store)
	keys, err := f.kv.List(ctx, sp+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, sp)
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
