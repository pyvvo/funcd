package lake

import (
	"context"
	"errors"
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/store"
)

// reasonStoreNotOwned holds a Lake whose root names a Bucket or ConfigMap of another owner (ADR-0251
// Decision 3).
const reasonStoreNotOwned = "StoreNotOwned"

// definedObject is one object a standalone Lake defines by name at its root, with the deletion policy it is
// written under.
type definedObject struct {
	field    string
	obj      v1.Object
	deletion v1.DeletionPolicy
}

// definedObjects are the Buckets and ConfigMaps cs defines by name (ADR-0251 Decisions 1 and 4); a defined ConfigMap is
// stored under v1.AppConfigMapName with cs's controller reference. A ref entry names an object cs never writes.
func definedObjects(cs *v1.Lake) []definedObject {
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: cs.Namespace, ResourceGroup: cs.ResourceGroup}
	}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	var out []definedObject
	for i, e := range cs.Spec.Buckets {
		if e.Name != "" {
			out = append(out, definedObject{fmt.Sprintf("spec.buckets[%d]", i),
				&v1.Bucket{TypeMeta: tm(v1.KindBucket), ObjectMeta: meta(e.Name), Spec: e.BucketSpec}, e.Deletion})
		}
	}
	for i, e := range cs.Spec.ConfigMaps {
		if e.Name != "" {
			out = append(out, definedObject{fmt.Sprintf("spec.configMaps[%d]", i),
				&v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap), ObjectMeta: meta(v1.AppConfigMapName(e.Name, e.ConfigMapSpec)), Spec: e.ConfigMapSpec},
				v1.DeletionDelete})
		}
	}
	return out
}

// ensureRootEntries checks the Secrets cs declares, then writes what it defines at its root, before resolveBucketRefs
// (ADR-0251 Decisions 3 and 5). It returns the reason and message of the first entry that holds cs: a declared Secret
// that is missing or lacks a key, so nothing is written while one is incomplete, or an object of that name without
// cs's marker (StoreNotOwned, never written); "" when none does.
func (r *Reconciler) ensureRootEntries(ctx context.Context, cs *v1.Lake) (reason, message string, err error) {
	gap, err := ownedstore.CheckSecrets(ctx, r.store, cs.Namespace, cs.Spec.SecretDeclarations)
	if err != nil {
		return "", "", err
	}
	if gap != nil {
		return gap.Reason(), gap.Message(), nil
	}
	for _, o := range definedObjects(cs) {
		_, eerr := ownedstore.Ensure(ctx, r.store, cs, o.obj, o.deletion)
		if errors.Is(eerr, ownedstore.ErrNotOwned) {
			m, k := o.obj.GetObjectMeta(), o.obj.GroupVersionKind().Kind
			return reasonStoreNotOwned, fmt.Sprintf("%s: %s %q exists and is not this Lake's; bind it with ref", o.field, k, m.Name), nil
		}
		if eerr != nil {
			return "", "", eerr
		}
	}
	return "", "", nil
}

// pruneConfigMaps deletes the ConfigMaps cs controls that its spec no longer defines, once its engine runs on the
// current spec, unless another object still names one (ADR-0213 Decision 5, ADR-0251 Decision 4).
func (r *Reconciler) pruneConfigMaps(ctx context.Context, cs *v1.Lake) error {
	const op = "services.lake.pruneConfigMaps"
	keep := map[v1.ObjectName]bool{}
	for _, o := range definedObjects(cs) {
		if o.obj.GroupVersionKind().Kind == v1.KindConfigMap {
			keep[o.obj.GetObjectMeta().Name] = true
		}
	}
	list, err := r.store.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{Namespace: cs.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "list configmaps in %q", cs.Namespace)
	}
	for _, o := range list.Items {
		m := o.GetObjectMeta()
		if keep[m.Name] || !v1.ControlledBy(m.OwnerReferences, v1.KindLake, cs.UID) {
			continue
		}
		_, used, uerr := gc.InUse(ctx, r.store, o, nil)
		if uerr != nil {
			return uerr
		}
		if used {
			continue
		}
		if derr := r.store.Delete(ctx, v1.KindConfigMap.GVK(), m.Namespace, m.Name, m.ResourceVersion); derr != nil && fault.KindOf(derr) != fault.NotFound {
			return fault.Wrapf(derr, fault.KindOf(derr), op, "delete configmap %q", m.Name)
		}
	}
	return nil
}
