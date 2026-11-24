package addon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/platform/pathx"
	"github.com/pyvvo/funcd/internal/services"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// A Function calls an add-on object's engine through funcd (ADR-0338): Calls make the checks both the facet and the
// proxy make before the dial, and the proxy's token names the calling Function.

// Calls resolve a Function's add-on binding to its object's engine (ADR-0338 Decisions 1, 2 and 5).
type Calls struct {
	store store.Store
	authz auth.Authorizer
	held  func(v1.NamespaceName) bool
}

// NewCalls builds the checks over st and authz; held reports whether a namespace is held (ADR-0290), nil when none is.
func NewCalls(st store.Store, authz auth.Authorizer, held func(v1.NamespaceName) bool) *Calls {
	if held == nil {
		held = func(v1.NamespaceName) bool { return false }
	}
	return &Calls{store: st, authz: authz, held: held}
}

var _ local.Addon = (*Calls)(nil)

// Target resolves alias in the caller's bindings, asks Cedar for addon::call, checks the path as the object's edge
// entry does, under its pinned revision's basePath and privatePaths, then, just before the dial, refuses a held
// namespace and an object that is not Ready or has no address: a held object has no engine, so the hold answers first,
// with its Retry-After. No error names the engine's address.
func (c *Calls) Target(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, alias, path string) (local.AddonTarget, error) {
	const op = "services.addon.call"
	o, err := c.bound(ctx, ns, fn, alias)
	if err != nil {
		return local.AddonTarget{}, err
	}
	spec, err := c.served(ctx, o)
	if err != nil {
		return local.AddonTarget{}, err
	}
	clean, ok := pathx.Kept(path, spec.AddonBasePath(), spec.PrivatePaths)
	if !ok {
		return local.AddonTarget{}, local.ErrAddonPathRefused.Errorf(op, "the edge entry of %q does not serve %s", alias, path)
	}
	if c.held(ns) {
		return local.AddonTarget{}, local.ErrAddonHeld.Errorf(op, "namespace %s is held while a Restore loads its data", ns)
	}
	if err := ready(op, alias, o); err != nil {
		return local.AddonTarget{}, err
	}
	return local.AddonTarget{Address: o.Status.Address, Path: clean}, nil
}

// CheckRead is the dependency check of a binding (ADR-0338 Decision 1): the binding, Cedar's addon::call and a Ready
// engine, with no engine call.
func (c *Calls) CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, alias string) error {
	o, err := c.bound(ctx, ns, fn, alias)
	if err != nil {
		return err
	}
	return ready("services.addon.CheckRead", alias, o)
}

// bound is the object that the caller binds under alias, once Cedar permits addon::call on it.
func (c *Calls) bound(ctx context.Context, ns v1.NamespaceName, fn v1.WorkloadRef, alias string) (*v1.AddonObject, error) {
	const op = "services.addon.call"
	bindings, err := services.CallerBindings(ctx, c.store, ns, fn)
	if err != nil {
		return nil, local.ErrAddonNoBinding.Errorf(op, "the %s %s/%s is not stored", fn.Kind, ns, fn.Name)
	}
	var name v1.ObjectName
	for _, b := range bindings.Addon {
		if b.Alias == alias {
			name = b.Object
		}
	}
	if name == "" {
		return nil, local.ErrAddonNoBinding.Errorf(op, "the %s binds no add-on object as %q", fn.Kind, alias)
	}
	if c.authz != nil {
		dec, err := c.authz.Authorize(ctx, auth.Request{
			Identity: auth.Identity{Subject: string(ns) + "/" + string(fn.Name), Principal: services.Principal(ns, fn)},
			Action:   auth.ActionAddonCall,
			Resource: &auth.EntityRef{Type: v1.KindAddonObject, Namespace: ns, Name: name},
		})
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "authorize addon::call on %q", name)
		}
		if !dec.Allowed {
			return nil, local.ErrAddonForbidden.Errorf(op, "the %s may not call %q: %s", fn.Kind, name, dec.Reason)
		}
	}
	got, err := c.store.Get(ctx, v1.KindAddonObject.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, local.ErrAddonEngineUnavailable.Errorf(op, "the add-on object %q does not exist", name)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get add-on object %q", name)
	}
	o, ok := got.(*v1.AddonObject)
	if !ok {
		return nil, fault.Internalf(op, "object %q is not an AddonObject", name)
	}
	return o, nil
}

// served is the spec o's edge entry follows: the revision it pins (ADR-0339 Decision 2), else its Addon's.
func (c *Calls) served(ctx context.Context, o *v1.AddonObject) (v1.AddonSpec, error) {
	const op = "services.addon.call"
	if o.Status.AddonRevision != "" {
		obj, err := c.store.Get(ctx, v1.KindAddonRevision.GVK(), "", o.Status.AddonRevision)
		if err == nil {
			if rev, ok := obj.(*v1.AddonRevision); ok {
				return rev.Spec.Spec, nil
			}
		}
	}
	list, err := c.store.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return v1.AddonSpec{}, fault.Wrapf(err, fault.KindOf(err), op, "list Addons")
	}
	for _, obj := range list.Items {
		if a, ok := obj.(*v1.Addon); ok && v1.SameAddonKind(a.Spec.Kind, o.Spec.Kind) {
			return a.Spec, nil
		}
	}
	return v1.AddonSpec{}, local.ErrAddonEngineUnavailable.Errorf(op, "no Addon registers the kind %q", o.Spec.Kind)
}

// ready refuses an object that is not Ready or has no engine address.
func ready(op, alias string, o *v1.AddonObject) error {
	if c, ok := o.Status.Conditions.Get(condReady); !ok || c.Status != v1.ConditionTrue || o.Status.Address == "" {
		return local.ErrAddonEngineUnavailable.Errorf(op, "the add-on object of %q is not Ready", alias)
	}
	return nil
}

// tokenLabel separates the proxy's token key from the node master's other uses, the lake token's among them, so
// neither proxy verifies the other's token (ADR-0338 Decision 3).
const tokenLabel = "funcd add-on proxy token v1"

// tokenKey derives the proxy's HMAC key from the node master.
func tokenKey(master []byte) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(tokenLabel))
	return m.Sum(nil)
}

// Token is a workload's token at the add-on proxy, FUNCD_ADDON_<ALIAS>_TOKEN: its kind, namespace and name,
// "<kind>/<ns>/<name>", so a Job's token is never a same-named Function's (ADR-0351 Decision 4), and an HMAC of them
// under a key derived from the node master. It is deterministic, as the lake token is, so it is the same across
// restarts; rotating the master rotates it.
func Token(master []byte, ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) string {
	subject := string(kind) + "/" + string(ns) + "/" + string(name)
	m := hmac.New(sha256.New, tokenKey(master))
	m.Write([]byte(subject))
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(subject)) + "." + enc.EncodeToString(m.Sum(nil))
}

// TokenCaller is the Function or the Job a proxy token names, read back by its kind, ok false for any other value; a
// Job's ref carries no run, which the proxy does not need.
func TokenCaller(master []byte, token string) (local.Ref, bool) {
	enc := base64.RawURLEncoding
	head, sig, found := strings.Cut(token, ".")
	if !found {
		return local.Ref{}, false
	}
	subject, err := enc.DecodeString(head)
	if err != nil {
		return local.Ref{}, false
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return local.Ref{}, false
	}
	m := hmac.New(sha256.New, tokenKey(master))
	m.Write(subject)
	if !hmac.Equal(got, m.Sum(nil)) {
		return local.Ref{}, false
	}
	parts := strings.Split(string(subject), "/")
	if len(parts) != 3 || v1.NamespaceName(parts[1]).Validate() != nil || v1.ObjectName(parts[2]).Validate() != nil {
		return local.Ref{}, false
	}
	kind := v1.Kind(parts[0])
	if kind != v1.KindFunction && kind != v1.KindJob {
		return local.Ref{}, false
	}
	return local.Ref{Namespace: v1.NamespaceName(parts[1]), Kind: kind, Name: v1.ObjectName(parts[2])}, true
}
