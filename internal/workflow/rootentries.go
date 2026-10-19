package workflow

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

// ownerFor maps a prefix's or a database's owner as ADR-0250 Decision 1 does: a catalog the root declares by name is
// kept as written, a step becomes its Function, <workflow>-<step>.
func ownerFor(wf *v1.Workflow, owner v1.ObjectName) v1.ObjectName {
	if owner == "" {
		return ""
	}
	for _, c := range wf.Spec.CatalogServices {
		if c.Name == owner {
			return owner
		}
	}
	return materializedName(wf, owner)
}

// rootObject is one object a Workflow defines by name at its root (ADR-0250), with the deletion policy it is written
// under.
type rootObject struct {
	field    string
	obj      v1.Object
	deletion v1.DeletionPolicy
}

// rootObjects are the databases, catalogs and ConfigMaps wf defines by name. A database's owner is mapped by ownerFor;
// a catalog and a ConfigMap go with the Workflow (ADR-0213 Decision 2), a ConfigMap stored as v1.AppConfigMapName.
func rootObjects(wf *v1.Workflow) []rootObject {
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: wf.Namespace, ResourceGroup: wf.ResourceGroup}
	}
	tm := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	s := &wf.Spec
	var out []rootObject
	for i, e := range s.SQLDatabases {
		if e.Name != "" {
			spec := e.SQLDatabaseSpec
			spec.Owner = ownerFor(wf, spec.Owner)
			out = append(out, rootObject{fmt.Sprintf("spec.sql[%d]", i),
				&v1.SQLDatabase{TypeMeta: tm(v1.KindSQLDatabase), ObjectMeta: meta(e.Name), Spec: spec}, e.Deletion})
		}
	}
	for i, e := range s.CatalogServices {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.catalogs[%d]", i),
				&v1.CatalogService{TypeMeta: tm(v1.KindCatalogService), ObjectMeta: meta(e.Name), Spec: e.CatalogServiceSpec}, v1.DeletionDelete})
		}
	}
	for i, e := range s.ConfigMaps {
		if e.Name != "" {
			out = append(out, rootObject{fmt.Sprintf("spec.configMaps[%d]", i),
				&v1.ConfigMap{TypeMeta: tm(v1.KindConfigMap), ObjectMeta: meta(v1.AppConfigMapName(e.Name, e.ConfigMapSpec)), Spec: e.ConfigMapSpec},
				v1.DeletionDelete})
		}
	}
	return out
}

// storedConfig points each config binding that names a ConfigMap wf defines at that ConfigMap's stored name
// (ADR-0250 Decision 4).
func storedConfig(wf *v1.Workflow, config []v1.ObjectName) []v1.ObjectName {
	if len(config) == 0 {
		return config
	}
	out := make([]v1.ObjectName, len(config))
	for i, c := range config {
		out[i] = c
		for _, e := range wf.Spec.ConfigMaps {
			if e.Name != "" && e.Name == c {
				out[i] = v1.AppConfigMapName(e.Name, e.ConfigMapSpec)
				break
			}
		}
	}
	return out
}

// checkRoot checks the Secrets wf declares, then writes what it defines at its root, before any step is materialized
// (ADR-0250 Decisions 3 and 5). A declared Secret that is missing or lacks a key stops wf, with no write and no value
// in the status; so does an object of that name without wf's marker (StoreNotOwned).
func (m *Materializer) checkRoot(ctx context.Context, wf *v1.Workflow) error {
	gap, err := ownedstore.CheckSecrets(ctx, m.store, wf.Namespace, wf.Spec.SecretDeclarations)
	if err != nil {
		return err
	}
	if gap != nil {
		return &notOwnedError{reason: gap.Reason(), msg: gap.Message(), poll: true}
	}
	for _, o := range rootObjects(wf) {
		_, eerr := ownedstore.Ensure(ctx, m.store, wf, o.obj, o.deletion)
		if errors.Is(eerr, ownedstore.ErrNotOwned) {
			k, n := o.obj.GroupVersionKind().Kind, o.obj.GetObjectMeta().Name
			return &notOwnedError{reason: "StoreNotOwned", msg: fmt.Sprintf("%s: %s %q exists and is not this Workflow's; bind it with ref", o.field, k, n)}
		}
		if eerr != nil {
			return eerr
		}
	}
	return nil
}

// pruneConfigMaps deletes the ConfigMaps this incarnation controls that its spec no longer defines, once the step
// Functions bind the current ones, unless something still names one (ADR-0213 Decision 5, ADR-0250 Decision 4).
func (m *Materializer) pruneConfigMaps(ctx context.Context, wf *v1.Workflow) error {
	keep := map[v1.ObjectName]bool{}
	for _, o := range rootObjects(wf) {
		if o.obj.GroupVersionKind().Kind == v1.KindConfigMap {
			keep[o.obj.GetObjectMeta().Name] = true
		}
	}
	list, err := m.store.List(ctx, v1.KindConfigMap.GVK(), store.ListOptions{Namespace: wf.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), materializeOp, "list configmaps in %q", wf.Namespace)
	}
	for _, o := range list.Items {
		meta := o.GetObjectMeta()
		if keep[meta.Name] || !v1.ControlledBy(meta.OwnerReferences, v1.KindWorkflow, wf.UID) {
			continue
		}
		_, used, uerr := gc.InUse(ctx, m.store, o, nil)
		if uerr != nil {
			return uerr
		}
		if used {
			continue
		}
		if derr := m.store.Delete(ctx, v1.KindConfigMap.GVK(), meta.Namespace, meta.Name, meta.ResourceVersion); derr != nil && fault.KindOf(derr) != fault.NotFound {
			return fault.Wrapf(derr, fault.KindOf(derr), materializeOp, "delete configmap %q", meta.Name)
		}
	}
	return nil
}
