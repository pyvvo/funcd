// Package kv is the KV service (ADR-0019/0072/0073): the function-facing Facade (binding-gated,
// <ns>/<store>/<table>/<key> prefixed, results prefix-stripped) over the kvstore.KV port, plus the KV
// services.TypeHandler the Service dispatcher routes type:kv to, and the KindKVStore reconciler.
//
// ADR-0073 makes KV bindings live on the consumer (Function.spec.kv) and authorization the PDP's job:
// the facade resolves a caller's (function, alias) to a (store, table) via the caller's spec.kv
// (default-deny). Reads are coarse-allowed for any bound same-namespace caller; writes (put/del) require
// the caller to be the table's owner (single-writer per table). Per-op caps + a table-scoped prefix apply.
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

// FacadeDeps configures the KV facade (the PEP). The BindingResolver (ADR-0073) is the default-deny
// binding gate over the caller's Function.spec.kv; it replaces the ADR-0072 grant Binder.
type FacadeDeps struct {
	KV       kvstore.KV
	Resolver BindingResolver
	Logger   *slog.Logger
}

// Facade is what a function calls: binding-gated + namespace/store/table-prefixed KV (ADR-0073).
type Facade struct {
	kv       kvstore.KV
	resolver BindingResolver
	logger   *slog.Logger
}

// NewFacade builds the KV facade. KV + Resolver are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	if d.KV == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "kv is required")
	}
	if d.Resolver == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "resolver is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{kv: d.KV, resolver: d.Resolver, logger: logger.With("component", "services.kv")}, nil
}

// tableKey is the on-disk key for a resolved (store, table, key): "<ns>/<store>/<table>/<key>" (ADR-0073)
// — a table is a sub-domain of the store, so keys are namespaced by both.
func tableKey(ns v1.NamespaceName, b Binding, key string) string {
	return tablePrefix(ns, b) + key
}

func tablePrefix(ns v1.NamespaceName, b Binding) string {
	return string(ns) + "/" + string(b.Store) + "/" + b.Table + "/"
}

// resolveWrite resolves a binding for a write (put/del): the caller must be the table's owner, else
// Forbidden (single-writer per table — ADR-0073).
func (f *Facade) resolveWrite(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) (Binding, error) {
	b, err := f.resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return Binding{}, err
	}
	if b.Owner != fn {
		return Binding{}, fault.Forbiddenf("services.kv.write", "function %q is not the owner of table %q (store %q); writes are owner-only", fn, b.Table, b.Store)
	}
	return b, nil
}

// Get returns the value for the alias's key, gated by the caller's binding (any bound caller may read).
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) ([]byte, bool, error) {
	b, err := f.resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return nil, false, err
	}
	return f.kv.Get(ctx, tableKey(ns, b, key))
}

// Put stores value under the alias's key (requires the caller to be the table owner; per-op caps first).
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, value []byte) error {
	b, err := f.resolveWrite(ctx, ns, fn, alias)
	if err != nil {
		return err
	}
	if int64(len(value)) > b.MaxValueBytes {
		return fault.Invalidf("services.kv.put", "value (%d bytes) exceeds the store cap (%d bytes)", len(value), b.MaxValueBytes)
	}
	if len(key) > b.MaxKeyBytes {
		return fault.Invalidf("services.kv.put", "key (%d bytes) exceeds the store cap (%d bytes)", len(key), b.MaxKeyBytes)
	}
	return f.kv.Put(ctx, tableKey(ns, b, key), value)
}

// Delete removes the alias's key (requires the caller to be the table owner).
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) error {
	b, err := f.resolveWrite(ctx, ns, fn, alias)
	if err != nil {
		return err
	}
	return f.kv.Delete(ctx, tableKey(ns, b, key))
}

// List returns the alias's keys under prefix (gated by the caller's binding, any bound caller), with the
// <ns>/<store>/<table>/ table prefix stripped so the caller only sees its own key space.
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, prefix string) ([]string, error) {
	b, err := f.resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return nil, err
	}
	tp := tablePrefix(ns, b)
	keys, err := f.kv.List(ctx, tp+prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = strings.TrimPrefix(k, tp)
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
