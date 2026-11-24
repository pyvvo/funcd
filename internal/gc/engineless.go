package gc

import (
	"context"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// DeleteAddonObject removes obj at its resourceVersion, or holds it as ADR-0345 Decision 6 says and returns it: an
// object whose status.workload is Engineless and whose spec.deletion is delete gets its metadata.deletionTimestamp set
// in place of its removal, so its Function destroys what it holds first, and one already held is returned as it is.
// The API's DELETE, the owner collector and an App's prune remove an add-on object through it.
func DeleteAddonObject(ctx context.Context, s store.Store, obj *v1.AddonObject) (*v1.AddonObject, error) {
	const op = "gc.DeleteAddonObject"
	m := obj.ObjectMeta
	if obj.Status.Workload != v1.WorkloadEngineless || obj.Spec.Deletion != v1.DeletionDelete {
		return nil, s.Delete(ctx, v1.KindAddonObject.GVK(), m.Namespace, m.Name, m.ResourceVersion)
	}
	if obj.DeletionTime != nil {
		return obj, nil
	}
	held := *obj
	now := v1.NewTimestamp(time.Now())
	held.DeletionTime = &now
	out, err := s.Update(ctx, &held)
	if err != nil {
		return nil, err
	}
	o, ok := out.(*v1.AddonObject)
	if !ok {
		return nil, fault.Internalf(op, "the held %s/%s is a %T", m.Namespace, m.Name, out)
	}
	return o, nil
}

// EnginelessParts are the add-on objects among objs of a kind that runs no engine (ADR-0345 Decision 1): marked so at
// their first pass, or of a kind whose Addon's kind entry is Engineless now, the marked ones and the others an App's
// order waits for alike.
func EnginelessParts(ctx context.Context, s store.Store, objs []v1.Object) ([]*v1.AddonObject, error) {
	var candidates []*v1.AddonObject
	for _, obj := range objs {
		if o, ok := obj.(*v1.AddonObject); ok {
			candidates = append(candidates, o)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	addons, err := s.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "gc.EnginelessParts", "list Addons")
	}
	kinds := map[string]bool{}
	for _, item := range addons.Items {
		if a, ok := item.(*v1.Addon); ok && a.Spec.Engineless() {
			kinds[v1.AddonPlural(a.Spec.Kind())] = true
		}
	}
	var out []*v1.AddonObject
	for _, o := range candidates {
		if o.Status.Workload == v1.WorkloadEngineless || kinds[v1.AddonPlural(o.Spec.Kind)] {
			out = append(out, o)
		}
	}
	return out, nil
}

// enginelessFirst reports whether obj, a part of a deleted App, waits for the App's engineless parts to go first
// (ADR-0345 Decision 8): the App's other parts go only once none of those remains, so the Function and the Job that
// destroy a stack outlive it. The add-on reconciler then runs a pass over the namespace.
func (c *Collector) enginelessFirst(ctx context.Context, obj v1.Object, ref v1.OwnerReference) (bool, error) {
	if ref.Kind != v1.KindApp {
		return false, nil
	}
	if o, ok := obj.(*v1.AddonObject); ok {
		if mine, err := EnginelessParts(ctx, c.store, []v1.Object{o}); err != nil || len(mine) > 0 {
			return false, err
		}
	}
	m := obj.GetObjectMeta()
	list, err := c.store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{Namespace: m.Namespace})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "gc.enginelessFirst", "list the add-on objects of %s", m.Namespace)
	}
	var parts []v1.Object
	for _, item := range list.Items {
		if v1.ControlledBy(item.GetObjectMeta().OwnerReferences, v1.KindApp, ref.UID) {
			parts = append(parts, item)
		}
	}
	left, err := EnginelessParts(ctx, c.store, parts)
	if len(left) > 0 {
		c.log.DebugContext(ctx, "an App's part waits for its engineless parts", "kind", obj.GroupVersionKind().Kind,
			"namespace", m.Namespace, "name", m.Name, "first", left[0].Name)
	}
	return len(left) > 0, err
}
