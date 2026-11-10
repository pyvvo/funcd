// Package kv is the KV service (ADR-0019/0072/0073): the function-facing Facade (binding-gated,
// <ns>/<store>/<table>/<key> prefixed, results prefix-stripped) over the kvstore.KV port, plus the KV
// services.TypeHandler the Service dispatcher routes type:kv to, and the KindKVStore reconciler.
//
// ADR-0073 makes KV bindings live on the consumer (Function.spec.bindings.kv); ADR-0074 makes authorization
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
	"github.com/pyvvo/funcd/internal/quota"
	"github.com/pyvvo/funcd/internal/services"
)

// FacadeDeps configures the KV facade (the PEP). The BindingResolver (ADR-0073) resolves the
// alias→(store,table) NAMING; the Authorizer (ADR-0074) is the PDP that decides kv::read/kv::write.
type FacadeDeps struct {
	KV         kvstore.KV
	Resolver   BindingResolver
	Authorizer auth.Authorizer
	// Quota decides each Put against its store's quota (ADR-0292); nil is quota.None.
	Quota  quota.Checker
	Logger *slog.Logger
}

// Facade is what a function calls: binding-resolved + PDP-authorized + table-prefixed KV (ADR-0074).
type Facade struct {
	kv         kvstore.KV
	resolver   BindingResolver
	authorizer auth.Authorizer
	quota      quota.Checker
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
	chk := d.Quota
	if chk == nil {
		chk = quota.None
	}
	return &Facade{kv: d.KV, resolver: d.Resolver, authorizer: d.Authorizer, quota: chk, logger: logger.With("component", "services.kv")}, nil
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

// CheckRead resolves the alias and asks the PDP kv::read on its table, as Get does, with no engine call: the
// dependency check of a caller's binding (ADR-0215 Decision 3).
func (f *Facade) CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error {
	_, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVRead)
	return err
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
// forbid(kv::write) unless principal == resource.owner makes this owner-only (ADR-0074). Per-op caps next, then the
// store's quota (ADR-0292), which counts the stored key and the value and credits the value it replaces.
func (f *Facade) Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, value []byte) error {
	return f.put(ctx, ns, fn, alias, key, value, func(k string) (bool, error) { return true, f.kv.Put(ctx, k, value) })
}

// PutIf is Put that stores only while w's condition on the key holds, and that can give the key a TTL. A condition
// that does not hold is a Result that is not Done, charged nothing to the quota.
func (f *Facade) PutIf(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, value []byte, w kvstore.Write) (kvstore.Result, error) {
	c, err := f.conditional("services.kv.put")
	if err == nil {
		err = kvstore.CheckWrite("services.kv.put", w)
	}
	if err != nil {
		return kvstore.Result{}, err
	}
	var res kvstore.Result
	err = f.put(ctx, ns, fn, alias, key, value, func(k string) (bool, error) {
		var err error
		res, err = c.PutIf(ctx, k, value, w)
		return res.Done, err
	})
	return res, err
}

// put authorizes kv::write on the alias's table, checks the per-op caps, admits the write against the store's quota,
// then stores it with store, which reports whether it wrote; the quota settles what it wrote.
func (f *Facade) put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, value []byte, store func(k string) (bool, error)) error {
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
	k := tableKey(ns, b, key)
	settle, err := f.quota.Admit(ctx, quota.Request{
		Subject: quota.Subject{Kind: v1.KindKVStore, Namespace: ns, Name: b.Store},
		Bytes:   int64(len(k) + len(value)),
		Replaces: func(ctx context.Context) (int64, error) {
			old, found, err := f.kv.Get(ctx, k)
			if err != nil || !found {
				return 0, err
			}
			return int64(len(k) + len(old)), nil
		},
	})
	if err != nil {
		return err
	}
	wrote, err := store(k)
	settle(err == nil && wrote)
	return err
}

// Delete removes the alias's key. The PDP authorizes kv::write (owner-only via the built-in forbid).
func (f *Facade) Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) error {
	return f.delete(ctx, ns, fn, alias, key, func(k string) (bool, error) { return true, f.kv.Delete(ctx, k) })
}

// DeleteIf is Delete that deletes only while the key exists at version, as a lease holder releases what it holds.
func (f *Facade) DeleteIf(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key, version string) (kvstore.Result, error) {
	c, err := f.conditional("services.kv.delete")
	if err != nil {
		return kvstore.Result{}, err
	}
	var res kvstore.Result
	err = f.delete(ctx, ns, fn, alias, key, func(k string) (bool, error) {
		var err error
		res, err = c.DeleteIf(ctx, k, version)
		return res.Done, err
	})
	return res, err
}

// delete authorizes kv::write on the alias's table, then deletes with del, which reports whether it deleted.
func (f *Facade) delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string, del func(k string) (bool, error)) error {
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVWrite)
	if err != nil {
		return err
	}
	if err := checkKeyLimit("services.kv.delete", key); err != nil {
		return err
	}
	settle, qerr := f.quota.Admit(ctx, quota.Request{Subject: quota.Subject{Kind: v1.KindKVStore, Namespace: ns, Name: b.Store}, Deletes: true})
	deleted, err := del(tableKey(ns, b, key))
	if qerr == nil {
		settle(err == nil && deleted)
	}
	return err
}

// GetEntry is Get with the key's version and expiry, read under the same kv::read rule.
func (f *Facade) GetEntry(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias, key string) (kvstore.Entry, bool, error) {
	c, err := f.conditional("services.kv.get")
	if err != nil {
		return kvstore.Entry{}, false, err
	}
	b, err := f.resolveAuth(ctx, ns, fn, alias, auth.ActionKVRead)
	if err != nil {
		return kvstore.Entry{}, false, err
	}
	if err := checkKeyLimit("services.kv.get", key); err != nil {
		return kvstore.Entry{}, false, err
	}
	return c.GetEntry(ctx, tableKey(ns, b, key))
}

// conditional is the driver's kvstore.Conditional; both drivers offer it.
func (f *Facade) conditional(op string) (kvstore.Conditional, error) {
	c, ok := f.kv.(kvstore.Conditional)
	if !ok {
		return nil, fault.Unavailablef(op, "the KV driver offers no conditional writes")
	}
	return c, nil
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
