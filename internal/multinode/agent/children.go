package agent

import (
	"context"
	"encoding/json"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// Prepare makes the node's store a copy of the control plane's before the node's platform runs anything (FEAT-0002
// F251). A store other than the one the saved revision is of is new, or restored from a backup: it takes its namespaces
// whole, with the children the node made that it does not hold, and Prepare waits for the control plane. So does a store
// that followed the control plane before its restore, with the runs it made since. Any other is ready as it is, so a node
// restarts while the control plane is away.
func (a *Agent) Prepare(ctx context.Context) error {
	if err := a.loadMine(ctx); err != nil {
		return err
	}
	backoff, failing := retryMin, false
	for a.timeline == "" && ctx.Err() == nil {
		var err error
		if !a.restored {
			err = a.adoptChildren(ctx)
		}
		if err == nil {
			err = a.syncAll(ctx)
		}
		if err == nil {
			a.restored = false
			return nil
		}
		if !failing && ctx.Err() == nil {
			a.log.WarnContext(ctx, "node agent: the node's store is new or restored, and waits for the control plane's snapshot",
				"error", err)
		}
		failing = true
		sleep(ctx, backoff)
		backoff = min(backoff*2, retryMax)
	}
	return nil
}

// children calls f with each child of the node's namespaces that the control plane holds and the node's store does not.
func (a *Agent) children(ctx context.Context, f func(v1.Object) error) error {
	held, err := a.client().Children(ctx)
	if err != nil {
		return err
	}
	for _, c := range held {
		_, err := a.cfg.Store.Get(ctx, c.Kind.GVK(), c.Namespace, c.Name)
		if err == nil {
			continue
		}
		if fault.KindOf(err) != fault.NotFound {
			return err
		}
		obj, ok := v1.NewObject(c.Kind)
		if !ok {
			return fault.Invalidf("agent.children", "unknown kind %q", c.Kind)
		}
		if err := json.Unmarshal(c.Object, obj); err != nil {
			return fault.Wrapf(err, fault.Invalid, "agent.children", "decode the %s", c.Kind)
		}
		if err := f(obj); err != nil {
			return err
		}
	}
	return nil
}

// adoptChildren takes back, as the same objects, the children that a new or restored store does not hold: Revisions, the
// parts of Apps, and the Secrets of credentials, which no backup of a node holds.
func (a *Agent) adoptChildren(ctx context.Context) error {
	return a.children(ctx, func(obj v1.Object) error {
		obj.GetObjectMeta().ResourceVersion = ""
		_, err := a.cfg.Store.Create(store.AsCopy(ctx), obj)
		return err
	})
}

// dropChildren deletes on the control plane the children the node no longer holds: those whose delete it did not take
// before a restart of the node, or that its restore from a backup brought back. It reports whether it went through.
func (a *Agent) dropChildren(ctx context.Context) bool {
	err := a.children(ctx, func(obj v1.Object) error {
		m, kind := obj.GetObjectMeta(), obj.GroupVersionKind().Kind
		a.sendDelete(ctx, key(kind, m.Namespace, m.Name), nodeapi.ObjectRef{Kind: kind, Namespace: m.Namespace, Name: m.Name, UID: m.UID})
		return nil
	})
	if err != nil && ctx.Err() == nil {
		a.log.WarnContext(ctx, "node agent: compare the children on the control plane", "error", err)
	}
	return err == nil
}
