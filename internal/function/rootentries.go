package function

import (
	"context"
	"errors"
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/revhold"
	"github.com/pyvvo/funcd/internal/store"
)

// reasonStoreNotOwned stops a Function whose root names a store of another owner (ADR-0248 Decision 3); a declared
// Secret stops it with ownedstore's reasons (Decision 6).
const reasonStoreNotOwned = "StoreNotOwned"

// rootObject is one object a Function defines by name at its root, with the deletion policy it is written under.
type rootObject struct {
	field    string
	obj      v1.Object
	deletion v1.DeletionPolicy
}

// rootObjects are the stores, lakes and ConfigMaps fn defines by name (ADR-0248 Decision 1). A lake and a
// ConfigMap go with the Function, as an App's do (ADR-0213 Decision 2); a defined ConfigMap is stored under
// v1.AppConfigMapName (Decision 5). A ref entry names an object the Function never writes.
func rootObjects(fn *v1.Function) []rootObject {
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: fn.Namespace, ResourceGroup: fn.ResourceGroup}
	}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	s := &fn.Spec
	var out []rootObject
	for i, e := range s.KVStores {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.kv[%d]", i),
				&v1.KVStore{TypeMeta: tm(v1.KindKVStore), ObjectMeta: meta(e.Name), Spec: e.KVStoreSpec}, e.Deletion})
		}
	}
	for i, e := range s.Buckets {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.buckets[%d]", i),
				&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta(e.Name), Spec: e.BucketSpec}, e.Deletion})
		}
	}
	for i, e := range s.SQLDatabases {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.sql[%d]", i),
				&v1.SQLDatabase{TypeMeta: tm(v1.KindSQLDatabase), ObjectMeta: meta(e.Name), Spec: e.SQLDatabaseSpec}, e.Deletion})
		}
	}
	for i, e := range s.Lakes {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.lakes[%d]", i),
				&v1.Lake{TypeMeta: tm(v1.KindLake), ObjectMeta: meta(e.Name), Spec: e.LakeSpec}, v1.DeletionDelete})
		}
	}
	for i, e := range s.ConfigMaps {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.configMaps[%d]", i),
				&v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap), ObjectMeta: meta(v1.AppConfigMapName(e.Name, e.ConfigMapSpec)), Spec: e.ConfigMapSpec}, v1.DeletionDelete})
		}
	}
	return out
}

// ensureRootEntries checks the Secrets fn declares, then writes what it defines at its root, before any binding
// resolves (ADR-0248 Decisions 3, 5 and 6). It returns the gate of the first entry that stops fn: a declared Secret
// that is missing or lacks a key, which no value of enters the message, so nothing is written while one is
// incomplete; or an object of that name without fn's marker (StoreNotOwned, never written).
func (r *Reconciler) ensureRootEntries(ctx context.Context, fn *v1.Function) (*gateFailure, error) {
	gap, err := ownedstore.CheckSecrets(ctx, r.store, fn.Namespace, fn.Spec.SecretDeclarations)
	if err != nil {
		return nil, err
	}
	if gap != nil {
		msg := gap.Message()
		return &gateFailure{reason: gap.Reason(), message: msg, readyMessage: msg, phase: v1.PhasePending, requeue: r.referentPoll}, nil
	}
	for _, o := range rootObjects(fn) {
		_, err := ownedstore.Ensure(ctx, r.store, fn, o.obj, o.deletion)
		if errors.Is(err, ownedstore.ErrNotOwned) {
			m, k := o.obj.GetObjectMeta(), o.obj.GroupVersionKind().Kind
			msg := fmt.Sprintf("%s: %s %q exists and is not this Function's; bind it with ref", o.field, k, m.Name)
			return &gateFailure{reason: reasonStoreNotOwned, message: msg, readyMessage: msg, phase: v1.PhasePending, requeue: r.referentPoll}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// pruneConfigMaps deletes the ConfigMaps fn controls that its spec no longer defines, once fn serves its current
// spec, unless another object still names one or an open run holds fn (ADR-0213 Decision 5, ADR-0248 Decision 5).
func (r *Reconciler) pruneConfigMaps(ctx context.Context, fn *v1.Function) error {
	const op = "function.pruneConfigMaps"
	keep := map[v1.ObjectName]bool{}
	for _, o := range rootObjects(fn) {
		if o.obj.GroupVersionKind().Kind == v1.KindConfigMap {
			keep[o.obj.GetObjectMeta().Name] = true
		}
	}
	list, err := r.store.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{Namespace: fn.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list configmaps in %q", fn.Namespace)
	}
	var stale []v1.Object
	for _, o := range list.Items {
		m := o.GetObjectMeta()
		if !keep[m.Name] && v1.ControlledBy(m.OwnerReferences, v1.KindFunction, fn.UID) {
			stale = append(stale, o)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	holds, err := revhold.Held(ctx, r.store, fn.Namespace)
	if err != nil || holds.Function(fn.Name, fn.UID) {
		return err
	}
	for _, o := range stale {
		if _, used, uerr := gc.InUse(ctx, r.store, o, nil); uerr != nil || used {
			if uerr != nil {
				return uerr
			}
			continue
		}
		m := o.GetObjectMeta()
		if derr := r.store.Delete(ctx, v1.KindConfigMap.GVK(), m.Namespace, m.Name, m.ResourceVersion); derr != nil && fault.KindOf(derr) != fault.NotFound {
			return fault.Wrapf(derr, fault.KindOf(derr), op, "delete configmap %q", m.Name)
		}
	}
	return nil
}
