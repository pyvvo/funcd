package health

import (
	"context"
	"errors"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// ReadChecker resolves a caller's binding alias and authorizes a read on it, with no storage call: the KV, blob and SQL
// Facades' CheckRead.
type ReadChecker interface {
	CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error
}

// CheckerDeps are what the dependency check reads: the metastore that holds the caller, and the kinds of bindings it
// checks in list order, each a provider's own (ADR-0308).
type CheckerDeps struct {
	Store    local.FunctionStore
	Bindings []Binding
}

// Binding is one kind of a Function's bindings the dependency check reads: Aliases lists them in spec order, and Check
// checks one, returning its failure or nil. A kind with no constructor below fills the fields itself.
type Binding struct {
	Kind    string
	Aliases func(fn *v1.Function) []string
	Check   func(ctx context.Context, caller local.Ref, alias string) *local.DependencyReport
}

// KV checks a Function's KV bindings: resolve and read authorization, then the KV probe's last result.
func KV(rc ReadChecker, p *Prober) Binding {
	return readBinding(local.DependencyKV, func(fn *v1.Function) []string {
		return aliases(fn.Spec.Bindings.KV, func(b v1.FunctionKV) string { return b.Alias })
	}, rc, p, TargetKV)
}

// Blob checks a Function's blob bindings as KV checks its KV ones, against the blob probe.
func Blob(rc ReadChecker, p *Prober) Binding {
	return readBinding(local.DependencyBlob, func(fn *v1.Function) []string {
		return aliases(fn.Spec.Bindings.Blob, func(b v1.FunctionBlob) string { return b.Alias })
	}, rc, p, TargetBlob)
}

// SQL checks a Function's SQL bindings: resolve, read authorization and a Ready engine (ADR-0233 Decision 8); it has no
// prober target.
func SQL(rc ReadChecker) Binding {
	return readBinding(local.DependencySQL, func(fn *v1.Function) []string {
		return aliases(fn.Spec.Bindings.SQL, func(b v1.FunctionSQL) string { return b.Alias })
	}, rc, nil, "")
}

// Secret checks a Function's Secret bindings (ADR-0321 Decision 4): each bound Secret's secret::read as the caller. It
// reads no Secret and has no prober target, so a missing Secret passes and the start check answers for it.
func Secret(rc ReadChecker) Binding {
	return readBinding(local.DependencySecret, func(fn *v1.Function) []string {
		out := make([]string, len(fn.Spec.Bindings.Secret))
		for i, n := range fn.Spec.Bindings.Secret {
			out[i] = string(n)
		}
		return out
	}, rc, nil, "")
}

// Links checks a Function's links: the resolver's link-as-grant, link::invoke when authz is set, and that the target
// exists in st and is not Failed.
func Links(r local.Resolver, authz auth.Authorizer, st local.FunctionStore) Binding {
	l := linkCheck{links: r, authz: authz, store: st}
	return Binding{Kind: local.DependencyLink, Aliases: func(fn *v1.Function) []string {
		return aliases(fn.Spec.Bindings.Links, func(b v1.FunctionLink) string { return b.Alias })
	}, Check: l.check}
}

// entry is a binding entry of a Function.
type entry interface {
	v1.FunctionKV | v1.FunctionBlob | v1.FunctionSQL | v1.FunctionLink
}

func aliases[B entry](bs []B, alias func(B) string) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = alias(b)
	}
	return out
}

// NewChecker builds the dependency check of ADR-0215 Decision 3.
func NewChecker(d CheckerDeps) local.DependencyChecker {
	return checker{store: d.Store, bindings: d.Bindings}
}

type checker struct {
	store    local.FunctionStore
	bindings []Binding
}

// Check checks caller's bindings kind by kind, each kind's entries in spec order, and returns the first failure. It
// calls no worker, activator or storage engine and writes nothing. A binding reached after ctx is done is a Timeout.
func (c checker) Check(ctx context.Context, caller local.Ref) *local.DependencyReport {
	obj, err := c.store.Get(ctx, v1.KindFunction.GVK(), caller.Namespace, caller.Function)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil
	case err != nil:
		return &local.DependencyReport{Kind: local.DependencySocket, Reason: reasonOf(ctx, err), Message: messageOf(err)}
	}
	fn, ok := obj.(*v1.Function)
	if !ok {
		return nil
	}
	for _, b := range c.bindings {
		for _, alias := range b.Aliases(fn) {
			if r := timedOut(ctx, b.Kind, alias); r != nil {
				return r
			}
			if r := b.Check(ctx, caller, alias); r != nil {
				return r
			}
		}
	}
	return nil
}

// readBinding checks one kind's bindings by their resolve and read authorization, then the storage probe's last result
// for t; a nil prober, or a target it does not probe, is healthy.
func readBinding(kind string, list func(*v1.Function) []string, rc ReadChecker, p *Prober, t Target) Binding {
	return Binding{Kind: kind, Aliases: list, Check: func(ctx context.Context, caller local.Ref, alias string) *local.DependencyReport {
		if err := rc.CheckRead(ctx, caller.Namespace, caller.Function, alias); err != nil {
			return &local.DependencyReport{Kind: kind, Binding: alias, Reason: reasonOf(ctx, err), Message: messageOf(err)}
		}
		if res := p.Result(t); !res.Healthy {
			return &local.DependencyReport{Kind: kind, Binding: alias, Reason: local.ReasonStorageUnreachable, Message: res.Message}
		}
		return nil
	}}
}

// linkCheck checks one link. An Idle, Deploying, Degraded or never-booted target passes, so mutual links never block
// each other.
type linkCheck struct {
	links local.Resolver
	authz auth.Authorizer
	store local.FunctionStore
}

func (c linkCheck) check(ctx context.Context, caller local.Ref, alias string) *local.DependencyReport {
	const op = "health.link"
	report := func(err error) *local.DependencyReport {
		return &local.DependencyReport{Kind: local.DependencyLink, Binding: alias, Reason: reasonOf(ctx, err), Message: messageOf(err)}
	}
	target, _, _, err := c.links.Resolve(ctx, caller, alias)
	if err != nil {
		return report(err)
	}
	if c.authz != nil {
		dec, err := c.authz.Authorize(ctx, auth.Request{
			Identity: auth.Identity{
				Subject:   caller.String(),
				Principal: &auth.EntityRef{Type: v1.KindFunction, Namespace: caller.Namespace, Name: caller.Function},
			},
			Action:   auth.ActionLinkInvoke,
			Resource: &auth.EntityRef{Type: v1.KindFunction, Namespace: target.Namespace, Name: target.Function},
		})
		if err != nil {
			return report(err)
		}
		if !dec.Allowed {
			return report(fault.Forbiddenf(op, "caller %s is not authorized to invoke %s: %s", caller, target, dec.Reason))
		}
	}
	obj, err := c.store.Get(ctx, v1.KindFunction.GVK(), target.Namespace, target.Function)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return report(fault.NotFoundf(op, "link target %s does not exist", target))
		}
		return report(err)
	}
	if fn, ok := obj.(*v1.Function); ok && fn.Status.Phase == v1.PhaseFailed {
		return &local.DependencyReport{Kind: local.DependencyLink, Binding: alias, Reason: local.ReasonNotReady,
			Message: "link target " + target.String() + " is Failed"}
	}
	return nil
}

// timedOut is the Timeout report of a binding reached once ctx, bounded by local.DependencyCheckBudget, is done.
func timedOut(ctx context.Context, kind, alias string) *local.DependencyReport {
	if ctx.Err() == nil {
		return nil
	}
	return &local.DependencyReport{Kind: kind, Binding: alias, Reason: local.ReasonTimeout,
		Message: "the dependency check did not reach this binding within " + local.DependencyCheckBudget.String()}
}

// reasonOf maps a resolve, authorization or metastore error to a report reason.
func reasonOf(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return local.ReasonTimeout
	case errors.Is(err, local.ErrEngineNotReady):
		return local.ReasonEngineNotReady
	case fault.KindOf(err) == fault.Forbidden:
		return local.ReasonForbidden
	case fault.KindOf(err) == fault.NotFound:
		return local.ReasonNotFound
	}
	return local.ReasonUnreachable
}

// messageOf is err's text without the op of its outermost fault.Error.
func messageOf(err error) string {
	var fe *fault.Error
	if errors.As(err, &fe) {
		return strings.TrimPrefix(err.Error(), fe.Op+": ")
	}
	return err.Error()
}
