// Package kv is the KV service (ADR-0019/0072/0073): the function-facing Facade (binding-gated,
// <ns>/<store>/<table>/<key> prefixed, results prefix-stripped) over the kvstore.KV port, plus the KV
// services.TypeHandler the Service dispatcher routes type:kv to, and the KindKVStore reconciler.
//
// ADR-0073 makes KV bindings live on the consumer (Function.spec.kv); ADR-0074 makes authorization
// the PDP's job: the facade resolves a caller's (function, alias) to a (store, table) — that is
// NAMING — then asks the PDP (auth.Authorizer) a per-object question: kv::read (get/list) or
// kv::write (put/del) on the KVTable. Reads are DEFAULT-DENY (need a permitting Policy); writes are
// governed by the built-in forbid(kv::write) unless principal == resource.owner (single-writer). A
// PDP deny maps to fault.Forbidden. Per-op caps + a table-scoped prefix still apply.
package kv

import (
	"context"
	"log/slog"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/services"
)

// FacadeDeps configures the KV facade (the PEP). The BindingResolver (ADR-0073) resolves the
// alias→(store,table) NAMING; the Authorizer (ADR-0074) is the PDP that decides kv::read/kv::write.
type FacadeDeps struct {
	KV         kvstore.KV
	Resolver   BindingResolver
	Authorizer auth.Authorizer
	Logger     *slog.Logger
}

// Facade is what a function calls: binding-resolved + PDP-authorized + table-prefixed KV (ADR-0074).
type Facade struct {
	kv         kvstore.KV
	resolver   BindingResolver
	authorizer auth.Authorizer
	logger     *slog.Logger
}

// NewFacade builds the KV facade. KV, Resolver, and Authorizer are required.
func NewFacade(d FacadeDeps) (*Facade, error) {
	if d.KV == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "kv is required")
	}
	if d.Resolver == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "resolver is required")
	}
	if d.Authorizer == nil {
		return nil, fault.Invalidf("services.kv.NewFacade", "authorizer is required")
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Facade{kv: d.KV, resolver: d.Resolver, authorizer: d.Authorizer, logger: logger.With("component", "services.kv")}, nil
}

// tableKey is the on-disk key for a resolved (store, table, key): "<ns>/<store>/<table>/<key>" (ADR-0073)
// — a table is a sub-domain of the store, so keys are namespaced by both.
func tableKey(ns v1.NamespaceName, b Binding, key string) string {
	return tablePrefix(ns, b) + key
}

func tablePrefix(ns v1.NamespaceName, b Binding) string {
	return string(ns) + "/" + string(b.Store) + "/" + b.Table + "/"
}

// authorize asks the PDP a per-object question for the resolved binding (ADR-0074): action kv::read
// (get/list) or kv::write (put/del) on the KVTable (a sub-resource of the KVStore). The principal is
// the connection-scoped caller Function; a deny (default-deny for reads; the built-in owner-forbid
// for writes) maps to fault.Forbidden.
func (f *Facade) authorize(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, action auth.Action, b Binding) error {
	const op = "services.kv.authorize"
	principal := &auth.EntityRef{Type: v1.KindFunction, Namespace: ns, Name: fn}
	resource := &auth.EntityRef{Type: v1.KindKVStore, Namespace: ns, Name: b.Store, Path: b.Table}
	dec, err := f.authorizer.Authorize(ctx, auth.Request{
		Identity: auth.Identity{Subject: string(ns) + "/" + string(fn), Principal: principal},
		Action:   action,
		Resource: resource,
	})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "authorize %s on table %q/%q", action, b.Store, b.Table)
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "function %q is not authorized to %s table %q (store %q): %s", fn, action, b.Table, b.Store, dec.Reason)
	}
	return nil
}

// resolveAuth resolves a binding (naming) then authorizes the action against the table via the PDP
// (ADR-0074), returning the binding on allow.
func (f *Facade) resolveAuth(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string, action auth.Action) (Binding, error) {
	b, err := f.resolver.Resolve(ctx, ns, fn, alias)
	if err != nil {
		return Binding{}, err
	}
	if err := f.authorize(ctx, ns, fn, action, b); err != nil {
		return Binding{}, err
	}
	return b, nil
}

// Get returns the value for the alias's key. Reads are DEFAULT-DENY: the PDP must permit kv::read
// (a permitting Policy) on the resolved table (ADR-0074).
func (f *Facade) Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) ([]byte, bool, error) {
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVRead)
	if err != nil {
		return nil, false, err
	}
	if err := checkKeyLimit("services.kv.get", key); err != nil {
		return nil, false, err
	}
	return f.kv.Get(ctx, tableKey(ns, b, key))
}

// Put stores value under the alias's key. The PDP authorizes kv::write — the built-in
// forbid(kv::write) unless principal == resource.owner makes this owner-only (ADR-0074). Per-op caps next.
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, value []byte) error {
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVWrite)
	if err != nil {
		return err
	}
	if int64(len(value)) > b.MaxValueBytes {
		return fault.PayloadTooLargef("services.kv.put", "value (%d bytes) exceeds the store cap (%d bytes)", len(value), b.MaxValueBytes)
	}
	if len(key) > b.MaxKeyBytes {
		return fault.PayloadTooLargef("services.kv.put", "key (%d bytes) exceeds the store cap (%d bytes)", len(key), b.MaxKeyBytes)
	}
	return f.kv.Put(ctx, tableKey(ns, b, key), value)
}

// Delete removes the alias's key. The PDP authorizes kv::write (owner-only via the built-in forbid).
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) error {
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVWrite)
	if err != nil {
		return err
	}
	if err := checkKeyLimit("services.kv.delete", key); err != nil {
		return err
	}
	return f.kv.Delete(ctx, tableKey(ns, b, key))
}

// checkKeyLimit refuses a key no store can hold (ADR-0148). Get and Delete check this limit, not the
// store's maxKeyBytes, so lowering that cap never strands a stored key.
func checkKeyLimit(op, key string) error {
	if len(key) > v1.MaxKeyBytesLimit {
		return fault.PayloadTooLargef(op, "key (%d bytes) exceeds the largest storable key (%d bytes)", len(key), v1.MaxKeyBytesLimit)
	}
	return nil
}

// List returns the alias's keys under prefix. Like Get, listing is DEFAULT-DENY: the PDP must permit
// kv::read on the resolved table (ADR-0074). The <ns>/<store>/<table>/ prefix is stripped so the
// caller only sees its own key space.
func (f *Facade) List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, prefix string) ([]string, error) {
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVRead)
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
