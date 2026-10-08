// Package app is the App kind's server side (ADR-0199, F113): the app-parts admission, which admits each declared
// part as a direct write of it would be admitted, and the App reconciler, which writes the parts, reports one status
// and prunes what a new spec drops. The owner GC removes the tree on App delete (internal/gc).
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/revhold"
	"github.com/pyvvo/funcd/internal/store"
)

const op = "app.Reconcile"

// Deps are the reconciler's dependencies; Store and Purger are required.
type Deps struct {
	Store  store.Store
	Purger gc.BucketPurger
	Logger *slog.Logger
}

// Reconciler drives an App to its declared parts (controller.Reconciler). It keeps no state between passes.
type Reconciler struct {
	store  store.Store
	purger gc.BucketPurger
	log    *slog.Logger
}

// NewReconciler builds the App reconciler.
func NewReconciler(d Deps) (*Reconciler, error) {
	const op = "app.NewReconciler"
	if d.Store == nil {
		return nil, fault.Invalidf(op, "store is required")
	}
	if d.Purger == nil {
		return nil, fault.Invalidf(op, "bucket purger is required")
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{store: d.Store, purger: d.Purger, log: log.With("component", "app")}, nil
}

// entry is one section entry: its field path, its kind, the object it names (its name or its ref), and its deletion.
type entry struct {
	path     string
	kind     v1.Kind
	name     v1.ObjectName
	ref      bool
	deletion v1.DeletionPolicy
}

func (e entry) key(ns v1.NamespaceName) v1.ObjectRef {
	return v1.ObjectRef{Kind: e.kind, Namespace: ns, Name: e.name}
}

// entries lists every section entry of a in section order (Decision 2).
func entries(a *v1.App) []entry {
	var out []entry
	add := func(section string, kind v1.Kind, i int, name, ref v1.ObjectName, deletion v1.DeletionPolicy) {
		out = append(out, entry{path: fmt.Sprintf("spec.%s[%d]", section, i), kind: kind, name: cmp.Or(name, ref), ref: ref != "", deletion: deletion})
	}
	s := &a.Spec
	for i, e := range s.KV {
		add("kv", v1.KindKVStore, i, e.Name, e.Ref, e.Deletion)
	}
	for i, e := range s.Buckets {
		add("buckets", v1.KindBucket, i, e.Name, e.Ref, e.Deletion)
	}
	for i, e := range s.Functions {
		add("functions", v1.KindFunction, i, e.Name, e.Ref, "")
	}
	for i, e := range s.Workflows {
		add("workflows", v1.KindWorkflow, i, e.Name, e.Ref, "")
	}
	for i, e := range s.EventSources {
		add("eventSources", v1.KindEventSource, i, e.Name, e.Ref, "")
	}
	for i, e := range s.Sensors {
		add("sensors", v1.KindSensor, i, e.Name, e.Ref, "")
	}
	for i, e := range s.Routes {
		add("routes", v1.KindRoute, i, e.Name, e.Ref, "")
	}
	for i, e := range s.Sites {
		add("sites", v1.KindSite, i, e.Name, e.Ref, "")
	}
	for i, e := range s.Catalogs {
		add("catalogs", v1.KindCatalogService, i, e.Name, e.Ref, "")
	}
	return out
}

// sectionKinds is the kind of every App section: the children of gc.Pairs' App pairs (Decision 7).
func sectionKinds() []v1.Kind {
	var out []v1.Kind
	for _, p := range gc.Pairs() {
		if p.Owner == v1.KindApp {
			out = append(out, p.Child)
		}
	}
	return out
}

func keyOf(o v1.Object) v1.ObjectRef {
	m := o.GetObjectMeta()
	return v1.ObjectRef{Kind: o.GroupVersionKind().Kind, Namespace: m.Namespace, Name: m.Name}
}

// stop is the reason a pass wrote no further part: ChildNotOwned or ChildInvalid (Decision 4).
type stop struct {
	reason string
	part   v1.ObjectRef
	msg    string
}

// Reconcile checks that the App owns every part that exists, writes the parts that are absent or differ, reads each
// part's readiness, prunes the dropped objects it controls once no part is Pending, and writes the App's status.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := r.store.Get(ctx, v1.KindApp.GVK(), req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil // the owner GC collects the tree (Decision 7)
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get app %s/%s", req.Namespace, req.Name)
	}
	a := obj.(*v1.App)
	ents := entries(a)
	objs := make(map[v1.ObjectRef]v1.Object, len(ents))
	halt, err := r.apply(ctx, a, ents, objs)
	if err != nil {
		return controller.Result{}, err
	}
	for _, e := range ents {
		if e.ref {
			if objs[e.key(a.Namespace)], err = r.get(ctx, e.kind, a.Namespace, e.name); err != nil {
				return controller.Result{}, err
			}
		}
	}
	children := readiness(a, ents, objs, halt)
	dropped, err := r.dropped(ctx, a, ents)
	if err != nil {
		return controller.Result{}, err
	}
	var pruning []v1.AppChild
	if halt == nil && !anyPending(children) {
		pruning, err = r.prune(ctx, a, dropped)
		if err != nil {
			return controller.Result{}, err
		}
	} else {
		for _, o := range dropped {
			pruning = append(pruning, pruningChild(o, ""))
		}
	}
	return r.publish(ctx, a, children, pruning, halt)
}

// apply reads every declared part, stops with ChildNotOwned before any write when one is not this App's, then writes
// each part, in section order, that is absent or whose spec, owner references or resource group differ. A write the
// store refuses stops the pass with ChildInvalid. objs gains each part's stored object, nil when absent.
func (r *Reconciler) apply(ctx context.Context, a *v1.App, ents []entry, objs map[v1.ObjectRef]v1.Object) (*stop, error) {
	parts := make(map[v1.ObjectRef]v1.Object)
	for _, p := range a.Parts() {
		parts[keyOf(p)] = p
	}
	for _, e := range ents {
		if e.ref {
			continue
		}
		k := e.key(a.Namespace)
		cur, err := r.get(ctx, e.kind, a.Namespace, e.name)
		if err != nil {
			return nil, err
		}
		objs[k] = cur
	}
	for _, e := range ents {
		k := e.key(a.Namespace)
		if cur := objs[k]; !e.ref && cur != nil && !owned(a, cur) {
			return &stop{reason: reasonChildNotOwned, part: k, msg: fmt.Sprintf("%s: exists and is not owned by App/%s", partName(k), a.Name)}, nil
		}
	}
	for _, e := range ents {
		if e.ref {
			continue
		}
		k := e.key(a.Namespace)
		desired := parts[k]
		desired.GetObjectMeta().OwnerReferences = ownerRefs(a, e.kind, e.deletion)
		written, err := r.write(ctx, desired, objs[k])
		switch fault.KindOf(err) {
		case "":
			objs[k] = written
		case fault.Invalid, fault.PayloadTooLarge:
			return &stop{reason: reasonChildInvalid, part: k, msg: fmt.Sprintf("%s: %v", partName(k), err)}, nil
		default:
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "write %s of app %s/%s", partName(k), a.Namespace, a.Name)
		}
	}
	return nil, nil
}

// write creates desired, or updates cur with desired's spec, owner references and resource group when one of them
// differs. It never writes a part's status: an update carries cur's.
func (r *Reconciler) write(ctx context.Context, desired, cur v1.Object) (v1.Object, error) {
	if cur == nil {
		return r.store.Create(ctx, desired)
	}
	same, err := sameSpec(desired, cur)
	if err != nil {
		return nil, err
	}
	dm, cm := desired.GetObjectMeta(), cur.GetObjectMeta()
	if same && slices.Equal(dm.OwnerReferences, cm.OwnerReferences) && dm.ResourceGroup == cm.ResourceGroup {
		return cur, nil
	}
	spec(cur).Set(spec(desired))
	cm.OwnerReferences, cm.ResourceGroup = dm.OwnerReferences, dm.ResourceGroup
	return r.store.Update(ctx, cur)
}

// spec is the Spec field of a part: every section kind has one.
func spec(o v1.Object) reflect.Value { return reflect.ValueOf(o).Elem().FieldByName("Spec") }

// sameSpec compares the specs as the store does, by their JSON.
func sameSpec(a, b v1.Object) (bool, error) {
	ja, err := json.Marshal(spec(a).Interface())
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "marshal spec")
	}
	jb, err := json.Marshal(spec(b).Interface())
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "marshal spec")
	}
	return string(ja) == string(jb), nil
}

func (r *Reconciler) get(ctx context.Context, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	obj, err := r.store.Get(ctx, kind.GVK(), ns, name)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get %s/%s in %q", kind, name, ns)
	}
	return obj, nil
}

// controllerRef is the App's controller reference (Decision 7).
func controllerRef(a *v1.App) v1.OwnerReference {
	return v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: v1.KindApp, Namespace: a.Namespace, Name: a.Name},
		UID:                a.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
}

// ownerRefs re-derives a part's references from its deletion and the App's current UID (Decision 7): a store carries
// the marker, and the controller reference only on deletion: delete; any other part carries the controller reference.
func ownerRefs(a *v1.App, kind v1.Kind, deletion v1.DeletionPolicy) []v1.OwnerReference {
	c := controllerRef(a)
	if !isStore(kind) {
		return []v1.OwnerReference{c}
	}
	m := c
	m.Controller, m.BlockOwnerDeletion = false, false
	if deletion == v1.DeletionDelete {
		return []v1.OwnerReference{m, c}
	}
	return []v1.OwnerReference{m}
}

func isStore(kind v1.Kind) bool { return kind == v1.KindKVStore || kind == v1.KindBucket }

// marked reports whether refs hold the marker of this App incarnation (kind, name and UID): a marker never moves to
// another UID (ADR-0178 Decision 4).
func marked(refs []v1.OwnerReference, a *v1.App) bool {
	return slices.ContainsFunc(refs, func(r v1.OwnerReference) bool {
		return !r.Controller && r.Kind == v1.KindApp && r.Name == a.Name && r.UID == a.UID
	})
}

// owned reports whether the App may write obj (Decision 4): a store needs this incarnation's marker; another part a
// controller reference naming the App's kind and name, any UID, so a re-created App takes back its parts.
func owned(a *v1.App, obj v1.Object) bool {
	refs := obj.GetObjectMeta().OwnerReferences
	if isStore(obj.GroupVersionKind().Kind) {
		return marked(refs, a)
	}
	c, ok := v1.ControllerOf(refs)
	return ok && c.Kind == v1.KindApp && c.Name == a.Name
}

// controls reports whether obj's controller reference names this App's UID; a store needs the marker too, as the GC
// requires (Decision 7).
func controls(a *v1.App, obj v1.Object) bool {
	refs := obj.GetObjectMeta().OwnerReferences
	return v1.ControlledBy(refs, v1.KindApp, a.UID) && (!isStore(obj.GroupVersionKind().Kind) || marked(refs, a))
}

// dropped lists the objects this App controls that no entry names, in gc.Pairs' order: users before stores.
func (r *Reconciler) dropped(ctx context.Context, a *v1.App, ents []entry) ([]v1.Object, error) {
	named := make(map[v1.ObjectRef]bool, len(ents))
	for _, e := range ents {
		named[e.key(a.Namespace)] = true
	}
	var out []v1.Object
	for _, kind := range sectionKinds() {
		res, err := r.store.List(ctx, kind.GVK(), store.ListOptions{Namespace: a.Namespace})
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %s in %q", kind, a.Namespace)
		}
		for _, o := range res.Items {
			if !named[keyOf(o)] && controls(a, o) {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// prune deletes each dropped object at its resourceVersion, a Bucket through gc.DeleteBucket (Decision 6). It keeps a
// Function an open run holds (RunHeld) and then, counting kept objects as not deleted, anything an object it does not
// delete still uses (InUse). It returns the objects left, as Pruning children.
func (r *Reconciler) prune(ctx context.Context, a *v1.App, dropped []v1.Object) ([]v1.AppChild, error) {
	kept := make(map[v1.ObjectRef]string)
	var holds revhold.Holds
	for _, o := range dropped {
		if _, ok := o.(*v1.Function); !ok {
			continue
		}
		if holds == nil {
			var err error
			if holds, err = revhold.Held(ctx, r.store, a.Namespace); err != nil {
				return nil, err
			}
		}
		if m := o.GetObjectMeta(); holds.Function(m.Name, m.UID) {
			kept[keyOf(o)] = reasonRunHeld
		}
	}
	deleting := func(u v1.Object) bool {
		k := keyOf(u)
		_, keep := kept[k]
		return !keep && slices.ContainsFunc(dropped, func(o v1.Object) bool { return keyOf(o) == k })
	}
	for changed := true; changed; {
		changed = false
		for _, o := range dropped {
			if _, keep := kept[keyOf(o)]; keep {
				continue
			}
			user, used, err := gc.InUse(ctx, r.store, o, deleting)
			if err != nil {
				return nil, err
			}
			if used {
				kept[keyOf(o)] = fmt.Sprintf("%s: %s", reasonInUse, partName(user))
				changed = true
			}
		}
	}
	var out []v1.AppChild
	for _, o := range dropped {
		k := keyOf(o)
		if reason, keep := kept[k]; keep {
			out = append(out, pruningChild(o, reason))
			continue
		}
		err := r.remove(ctx, o)
		switch fault.KindOf(err) {
		case "", fault.NotFound:
			r.log.InfoContext(ctx, "pruned", "kind", k.Kind, "namespace", k.Namespace, "name", k.Name, "app", a.Name)
		case fault.Conflict:
			out = append(out, pruningChild(o, "")) // changed since it was listed: judged again on the requeue
		default:
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "prune %s of app %s/%s", partName(k), a.Namespace, a.Name)
		}
	}
	return out, nil
}

func (r *Reconciler) remove(ctx context.Context, o v1.Object) error {
	if b, ok := o.(*v1.Bucket); ok {
		return gc.DeleteBucket(ctx, r.store, r.purger, b)
	}
	m := o.GetObjectMeta()
	return r.store.Delete(ctx, o.GroupVersionKind(), m.Namespace, m.Name, m.ResourceVersion)
}

func pruningChild(o v1.Object, reason string) v1.AppChild {
	return v1.AppChild{Kind: o.GroupVersionKind().Kind, Name: o.GetObjectMeta().Name, State: v1.AppChildPruning, Reason: reason}
}

// MapPart is the controller.MapFunc of every section kind: it requeues each App whose controller reference or marker
// obj carries, and each App of obj's namespace with a ref entry naming obj (Decision 4).
func (r *Reconciler) MapPart(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	var reqs []controller.Request
	add := func(name v1.ObjectName) {
		req := controller.Request{GVK: v1.KindApp.GVK(), Namespace: m.Namespace, Name: name}
		if !slices.Contains(reqs, req) {
			reqs = append(reqs, req)
		}
	}
	for _, ref := range m.OwnerReferences {
		if ref.Kind == v1.KindApp {
			add(ref.Name)
		}
	}
	res, err := r.store.List(ctx, v1.KindApp.GVK(), store.ListOptions{Namespace: m.Namespace})
	if err != nil {
		r.log.WarnContext(ctx, "list apps of a changed part", "namespace", string(m.Namespace), "name", string(m.Name), "error", err)
		return reqs
	}
	k := keyOf(obj)
	for _, o := range res.Items {
		if a, ok := o.(*v1.App); ok && slices.Contains(a.Refs(), k) {
			add(a.Name)
		}
	}
	return reqs
}
