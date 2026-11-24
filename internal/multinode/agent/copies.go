package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/store"
)

// writeAttempts bounds a local write that loses to the reconcilers' writes of the same object.
const writeAttempts = 5

// errHeld is a local object that stays until its add-on's Function destroyed what it holds.
var errHeld = errors.New("agent: the object is held until its Function destroyed what it holds")

// upsert writes a control-plane object into the local store as the same object: its uid, generation and creation time are
// the control plane's, and its status is the node's (merged). One created again under its name replaces the local one,
// which goes first with the data it holds; a child the node owns keeps the identity the node gave it. The control plane's
// collector decides when a cluster-scoped object goes, so its copy names no owner.
func (a *Agent) upsert(ctx context.Context, c store.Change) error {
	const op = "agent.upsert"
	obj, ok := v1.NewObject(c.Kind)
	if !ok {
		return fault.Invalidf(op, "unknown kind %q", c.Kind)
	}
	if err := json.Unmarshal(c.Object, obj); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "decode the %s", c.Kind)
	}
	m := obj.GetObjectMeta()
	m.ResourceVersion = ""
	if !c.Kind.Namespaced() {
		m.OwnerReferences = nil
	}
	theirs := c.Kind.Namespaced() && len(m.OwnerReferences) == 0
	ctx = store.AsCopy(ctx)
	for range writeAttempts {
		cur, err := a.cfg.Store.Get(ctx, c.Kind.GVK(), m.Namespace, m.Name)
		if fault.KindOf(err) == fault.NotFound {
			if _, err = a.cfg.Store.Create(ctx, obj); fault.KindOf(err) == fault.Conflict {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		if theirs && cur.GetObjectMeta().UID != m.UID {
			err = a.remove(ctx, cur)
		} else {
			err = a.replace(ctx, obj, cur)
			if fault.KindOf(err) != fault.Conflict {
				return err
			}
		}
		if err != nil && fault.KindOf(err) != fault.Conflict {
			a.resync.Store(true) // the next snapshot brings it again, once the local one is gone
			return err
		}
	}
	return fault.Conflictf(op, "%s %s kept changing", c.Kind, c.Name)
}

// replace writes obj, the control plane's, over cur, its copy.
func (a *Agent) replace(ctx context.Context, obj, cur v1.Object) error {
	next, err := merged(obj, cur)
	if err != nil {
		return err
	}
	m := next.GetObjectMeta()
	m.ResourceVersion = cur.GetObjectMeta().ResourceVersion
	if len(m.OwnerReferences) > 0 {
		m.UID = "" // a child's identity is the node's
	}
	_, err = a.cfg.Store.Update(ctx, next)
	return err
}

// delete follows a Deleted record. An object that the node holds under the name but another uid is not the one deleted.
func (a *Agent) delete(ctx context.Context, c store.Change) error {
	var rec struct {
		Metadata v1.ObjectMeta `json:"metadata"`
	}
	if len(c.Object) > 0 {
		if err := json.Unmarshal(c.Object, &rec); err != nil {
			return fault.Wrapf(err, fault.Invalid, "agent.delete", "decode the %s", c.Kind)
		}
	}
	for range writeAttempts {
		cur, err := a.cfg.Store.Get(ctx, c.Kind.GVK(), c.Namespace, c.Name)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			return nil
		case err != nil:
			return err
		case rec.Metadata.UID != "" && cur.GetObjectMeta().UID != rec.Metadata.UID:
			return nil
		}
		if err = a.remove(ctx, cur); fault.KindOf(err) != fault.Conflict {
			if errors.Is(err, errHeld) {
				return nil
			}
			return err
		}
	}
	return fault.Conflictf("agent.delete", "%s %s kept changing", c.Kind, c.Name)
}

// remove deletes the local object cur at its version. An add-on object that holds something outside funcd is held
// instead, as the API's delete holds it, and its Function's answer removes it (ADR-0345 Decision 6). A copy of a retired
// AddonRevision is marked deleted, and the add-on reconciler removes it once no object of the node pins it.
func (a *Agent) remove(ctx context.Context, cur v1.Object) error {
	switch o := cur.(type) {
	case *v1.AddonObject:
		held, err := gc.DeleteAddonObject(ctx, a.cfg.Store, o)
		if err == nil && held != nil {
			return errHeld
		}
		return gone(err)
	case *v1.AddonRevision:
		if o.DeletionTime != nil {
			return nil
		}
		now := v1.NewTimestamp(time.Now())
		o.DeletionTime = &now
		_, err := a.cfg.Store.Update(ctx, o)
		return gone(err)
	}
	m := cur.GetObjectMeta()
	return gone(a.cfg.Store.Delete(ctx, cur.GroupVersionKind(), m.Namespace, m.Name, m.ResourceVersion))
}

// gone is err, but nil for an object that is gone already.
func gone(err error) error {
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	return err
}

// pruneClusterKinds removes the copies of the cluster-scoped kinds the node follows that the control plane no longer
// holds, so what it revoked or retired while the node was behind goes on the node too.
func (a *Agent) pruneClusterKinds(ctx context.Context, keep map[string]bool) {
	for _, k := range multinode.NodeKinds() {
		a.pruneKind(ctx, k, "", keep)
	}
}

// prune removes the local objects of ns the control plane does not hold (not in keep). The node's own children stay: its
// reconcilers own them.
func (a *Agent) prune(ctx context.Context, ns v1.NamespaceName, keep map[string]bool) {
	for _, k := range v1.AllKinds() {
		if k.Namespaced() {
			a.pruneKind(ctx, k, ns, keep)
		}
	}
}

func (a *Agent) pruneKind(ctx context.Context, k v1.Kind, ns v1.NamespaceName, keep map[string]bool) {
	res, err := a.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		a.log.WarnContext(ctx, "node agent: list for a prune", "kind", k, "namespace", ns, "error", err)
		return
	}
	for _, obj := range res.Items {
		m := obj.GetObjectMeta()
		if len(m.OwnerReferences) > 0 || keep[key(k, ns, m.Name)] {
			continue
		}
		if err := a.remove(ctx, obj); err != nil && !errors.Is(err, errHeld) {
			a.log.WarnContext(ctx, "node agent: prune", "kind", k, "namespace", ns, "name", m.Name, "error", err)
		}
	}
}

func key(k v1.Kind, ns v1.NamespaceName, name v1.ObjectName) string {
	return string(k) + "/" + string(ns) + "/" + string(name)
}

// merged is obj, the control plane's, with cur's status, the node's, and the decisions the control plane records there.
// The status of an API token, an Addon or an AddonRevision is the control plane's: a token's hash and expiry, and the
// revision an Addon's new objects pin.
func merged(obj, cur v1.Object) (v1.Object, error) {
	switch obj.GroupVersionKind().Kind {
	case v1.KindAPIToken, v1.KindAddon, v1.KindAddonRevision:
		return obj, nil
	}
	next, err := keepStatus(obj, cur)
	if err != nil {
		return nil, err
	}
	multinode.KeepDecisions(next, obj)
	multinode.KeepPause(next, cur)
	return next, nil
}

// keepStatus is obj with cur's status.
func keepStatus(obj, cur v1.Object) (v1.Object, error) {
	const op = "agent.keepStatus"
	curRaw, err := json.Marshal(cur)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	var curMap map[string]json.RawMessage
	if err := json.Unmarshal(curRaw, &curMap); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	if st, ok := curMap["status"]; ok {
		m["status"] = st
	} else {
		delete(m, "status")
	}
	if raw, err = json.Marshal(m); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "marshal")
	}
	next, _ := v1.NewObject(obj.GroupVersionKind().Kind)
	if err := json.Unmarshal(raw, next); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "unmarshal")
	}
	return next, nil
}
