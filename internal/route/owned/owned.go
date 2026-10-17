// Package owned writes the Route a Site or a Function owns for its own edge address (ADR-0241 Decision 5): the
// owner is stamped as its controller, a Route of that name another object controls is left untouched, and Route
// events map back to the owner by controller and by name.
package owned

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

const op = "route.owned"

// ErrNotOwned is a Route of the owner's Route name that this owner, with its current UID, does not control.
var ErrNotOwned = errors.New("route: not controlled by this owner")

const (
	functionRoutePrefix = "fn-"
	maxRouteName        = 63 // a DNS-1123 label
	routeHashLen        = 8  // hex digits of SHA-256(Function name) in a shortened name
)

// FunctionRouteName is the Route a Function owns: fn-<name> while that fits a label, else cut to fit with the first
// hex digits of the name's SHA-256, as revisionName does.
func FunctionRouteName(fn v1.ObjectName) v1.ObjectName {
	full := functionRoutePrefix + string(fn)
	if len(full) <= maxRouteName {
		return v1.ObjectName(full)
	}
	sum := sha256.Sum256([]byte(fn))
	suffix := "-" + hex.EncodeToString(sum[:])[:routeHashLen]
	return v1.ObjectName(full[:maxRouteName-len(suffix)] + suffix)
}

// IsOwned reports whether a Site or a Function controls rt (ADR-0241 Decision 6).
func IsOwned(rt *v1.Route) bool {
	c, ok := v1.ControllerOf(rt.OwnerReferences)
	return ok && (c.Kind == v1.KindSite || c.Kind == v1.KindFunction)
}

// Ensure creates the owner's Route name with spec, or updates the one the owner controls when its spec or group
// drifted. A Route of that name the owner does not control with its current UID is ErrNotOwned.
func Ensure(ctx context.Context, st store.Store, owner v1.Object, name v1.ObjectName, spec v1.RouteSpec) (*v1.Route, error) {
	meta, kind := owner.GetObjectMeta(), owner.GroupVersionKind().Kind
	obj, err := st.Get(ctx, v1.KindRoute.GVK(), meta.Namespace, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		rt := &v1.Route{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRoute.GVK().APIVersion(), Kind: v1.KindRoute}}
		rt.Name, rt.Namespace, rt.ResourceGroup = name, meta.Namespace, meta.ResourceGroup
		rt.OwnerReferences = []v1.OwnerReference{{
			ObjectRef:          v1.ObjectRef{Kind: kind, Namespace: meta.Namespace, Name: meta.Name},
			UID:                meta.UID,
			Controller:         true,
			BlockOwnerDeletion: true,
		}}
		rt.Spec = spec
		created, cerr := st.Create(ctx, rt)
		if cerr != nil {
			return nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "create route %s/%s", meta.Namespace, name)
		}
		return created.(*v1.Route), nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get route %s/%s", meta.Namespace, name)
	}
	rt := obj.(*v1.Route)
	if !v1.ControlledBy(rt.OwnerReferences, kind, meta.UID) {
		return nil, ErrNotOwned
	}
	if reflect.DeepEqual(rt.Spec, spec) && rt.ResourceGroup == meta.ResourceGroup {
		return rt, nil
	}
	rt.Spec, rt.ResourceGroup = spec, meta.ResourceGroup
	updated, uerr := st.Update(ctx, rt)
	if uerr != nil {
		return nil, fault.Wrapf(uerr, fault.KindOf(uerr), op, "update route %s/%s", meta.Namespace, name)
	}
	return updated.(*v1.Route), nil
}

// Remove deletes the owner's Route name when the owner controls it; an absent or foreign Route is left as it is.
func Remove(ctx context.Context, st store.Store, owner v1.Object, name v1.ObjectName) error {
	meta, kind := owner.GetObjectMeta(), owner.GroupVersionKind().Kind
	obj, err := st.Get(ctx, v1.KindRoute.GVK(), meta.Namespace, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil
	case err != nil:
		return fault.Wrapf(err, fault.KindOf(err), op, "get route %s/%s", meta.Namespace, name)
	}
	rt := obj.(*v1.Route)
	if !v1.ControlledBy(rt.OwnerReferences, kind, meta.UID) {
		return nil
	}
	if derr := st.Delete(ctx, v1.KindRoute.GVK(), meta.Namespace, name, rt.ResourceVersion); derr != nil && fault.KindOf(derr) != fault.NotFound {
		return fault.Wrapf(derr, fault.KindOf(derr), op, "delete route %s/%s", meta.Namespace, name)
	}
	return nil
}

// MapOwner maps a Route event to the owners of kind (Site or Function) it may concern: its controller, and the owner
// whose Route name it carries, so a foreign Route's deletion re-runs a stuck owner. A shortened Function Route name
// is matched through a List of the namespace's Functions.
func MapOwner(st store.Store, kind v1.Kind) controller.MapFunc {
	return func(ctx context.Context, obj v1.Object) []controller.Request {
		meta := obj.GetObjectMeta()
		seen := map[v1.ObjectName]bool{}
		var reqs []controller.Request
		add := func(name v1.ObjectName) {
			if name != "" && !seen[name] {
				seen[name] = true
				reqs = append(reqs, controller.Request{GVK: kind.GVK(), Namespace: meta.Namespace, Name: name})
			}
		}
		if c, ok := v1.ControllerOf(meta.OwnerReferences); ok && c.Kind == kind {
			add(c.Name)
		}
		switch kind {
		case v1.KindSite:
			add(meta.Name)
		case v1.KindFunction:
			// A name of the full label length may be a shortened one, which only a List can map back.
			switch name := v1.ObjectName(strings.TrimPrefix(string(meta.Name), functionRoutePrefix)); {
			case !strings.HasPrefix(string(meta.Name), functionRoutePrefix):
			case len(meta.Name) == maxRouteName:
				for _, n := range routeOwners(ctx, st, meta.Namespace, meta.Name) {
					add(n)
				}
			case FunctionRouteName(name) == meta.Name:
				add(name)
			}
		}
		return reqs
	}
}

// routeOwners is every Function of ns whose Route name is route.
func routeOwners(ctx context.Context, st store.Store, ns v1.NamespaceName, route v1.ObjectName) []v1.ObjectName {
	list, err := st.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	if err != nil {
		return nil
	}
	var out []v1.ObjectName
	for _, o := range list.Items {
		if n := o.GetObjectMeta().Name; FunctionRouteName(n) == route {
			out = append(out, n)
		}
	}
	return out
}
