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

// ReadChecker resolves a caller's binding alias and authorizes a read on it, with no storage call: the KV and blob
// Facades' CheckRead.
type ReadChecker interface {
	CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error
}

// CheckerDeps are what the dependency check reads: the metastore, the binding Facades' resolve and read
// authorization, the link resolver, the PDP and the prober's memory. A nil KV, Blob or Links skips that kind.
type CheckerDeps struct {
	Store  local.FunctionStore
	KV     ReadChecker
	Blob   ReadChecker
	Links  local.Resolver
	Authz  auth.Authorizer
	Health *Prober
}

// NewChecker builds the dependency check of ADR-0215 Decision 3.
func NewChecker(d CheckerDeps) local.DependencyChecker { return checker{d} }

type checker struct{ d CheckerDeps }

// Check checks caller's spec.kv, then spec.blob, then spec.links entries in order and returns the first failure. It
// calls no worker, activator or storage engine and writes nothing. A binding reached after ctx is done is a Timeout.
func (c checker) Check(ctx context.Context, caller local.Ref) *local.DependencyReport {
	obj, err := c.d.Store.Get(ctx, v1.KindFunction.GVK(), caller.Namespace, caller.Function)
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
	if c.d.KV != nil {
		for _, b := range fn.Spec.KV {
			if r := c.read(ctx, caller, local.DependencyKV, b.Alias, c.d.KV, TargetKV); r != nil {
				return r
			}
		}
	}
	if c.d.Blob != nil {
		for _, b := range fn.Spec.Blob {
			if r := c.read(ctx, caller, local.DependencyBlob, b.Alias, c.d.Blob, TargetBlob); r != nil {
				return r
			}
		}
	}
	if c.d.Links != nil {
		for _, l := range fn.Spec.Links {
			if r := c.link(ctx, caller, l.Alias); r != nil {
				return r
			}
		}
	}
	return nil
}

// read checks one KV or blob binding: its resolve and read authorization, then the storage probe's last result.
func (c checker) read(ctx context.Context, caller local.Ref, kind, alias string, rc ReadChecker, t Target) *local.DependencyReport {
	if r := timedOut(ctx, kind, alias); r != nil {
		return r
	}
	if err := rc.CheckRead(ctx, caller.Namespace, caller.Function, alias); err != nil {
		return &local.DependencyReport{Kind: kind, Binding: alias, Reason: reasonOf(ctx, err), Message: messageOf(err)}
	}
	if res := c.d.Health.Result(t); !res.Healthy {
		return &local.DependencyReport{Kind: kind, Binding: alias, Reason: local.ReasonStorageUnreachable, Message: res.Message}
	}
	return nil
}

// link checks one link: the resolver's link-as-grant, link::invoke, and that its target exists and is not Failed. An
// Idle, Deploying, Degraded or never-booted target passes, so mutual links never block each other.
func (c checker) link(ctx context.Context, caller local.Ref, alias string) *local.DependencyReport {
	const op = "health.link"
	report := func(err error) *local.DependencyReport {
		return &local.DependencyReport{Kind: local.DependencyLink, Binding: alias, Reason: reasonOf(ctx, err), Message: messageOf(err)}
	}
	if r := timedOut(ctx, local.DependencyLink, alias); r != nil {
		return r
	}
	target, _, err := c.d.Links.Resolve(ctx, caller, alias)
	if err != nil {
		return report(err)
	}
	if c.d.Authz != nil {
		dec, err := c.d.Authz.Authorize(ctx, auth.Request{
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
	obj, err := c.d.Store.Get(ctx, v1.KindFunction.GVK(), target.Namespace, target.Function)
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
