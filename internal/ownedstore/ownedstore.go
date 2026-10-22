// Package ownedstore writes the stores a resource declares under ADR-0178's marker rule, for any owner kind
// (ADR-0248 Decision 3): a store an owner makes carries the owner's non-controller marker, plus a cascading
// controller reference under deletion: delete, and an existing store without this owner's current-UID marker is
// refused, never taken over.
package ownedstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

const op = "ownedstore.Ensure"

// ErrNotOwned is a store that exists without this owner's current-UID marker.
var ErrNotOwned = errors.New("ownedstore: the store exists and is not this owner's")

// IsMarker reports whether r has the shape of a store marker: a non-controller reference naming a kind that stamps one,
// a Workflow (ADR-0178), an App (ADR-0199 Decision 8), a Function or Site (ADR-0248), or a Lake (ADR-0251).
func IsMarker(r v1.OwnerReference) bool {
	if r.Controller {
		return false
	}
	switch r.Kind {
	case v1.KindWorkflow, v1.KindApp, v1.KindFunction, v1.KindSite, v1.KindLake:
		return true
	}
	return false
}

// Refs are the references a store owner stamps: its marker and, under deletion: delete, its cascading controller
// reference.
func Refs(owner v1.Object, deletion v1.DeletionPolicy) []v1.OwnerReference {
	meta := owner.GetObjectMeta()
	c := v1.OwnerReference{
		ObjectRef:          v1.ObjectRef{Kind: owner.GroupVersionKind().Kind, Namespace: meta.Namespace, Name: meta.Name},
		UID:                meta.UID,
		Controller:         true,
		BlockOwnerDeletion: true,
	}
	m := c
	m.Controller, m.BlockOwnerDeletion = false, false
	if deletion == v1.DeletionDelete {
		return []v1.OwnerReference{m, c}
	}
	return []v1.OwnerReference{m}
}

// Marked reports whether refs hold owner's marker: its kind, name and current UID. A marker never moves to another
// UID (ADR-0178 Decision 4).
func Marked(refs []v1.OwnerReference, owner v1.Object) bool {
	meta, kind := owner.GetObjectMeta(), owner.GroupVersionKind().Kind
	for _, r := range refs {
		if IsMarker(r) && r.Kind == kind && r.Name == meta.Name && r.UID == meta.UID {
			return true
		}
	}
	return false
}

// Ensure creates desired with owner's references, or updates the store of that name that carries owner's marker with
// desired's spec, references and resource group when one differs, keeping its status, and returns the store's UID. A store without
// the marker is ErrNotOwned and is not written.
func Ensure(ctx context.Context, st store.Store, owner, desired v1.Object, deletion v1.DeletionPolicy) (v1.UID, error) {
	dm, gvk := desired.GetObjectMeta(), desired.GroupVersionKind()
	dm.OwnerReferences = Refs(owner, deletion)
	cur, err := st.Get(ctx, gvk, dm.Namespace, dm.Name)
	if fault.KindOf(err) == fault.NotFound {
		out, cerr := st.Create(ctx, desired)
		if cerr != nil {
			return "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "create %s %q", gvk.Kind, dm.Name)
		}
		return out.GetObjectMeta().UID, nil
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "get %s %q", gvk.Kind, dm.Name)
	}
	cm := cur.GetObjectMeta()
	if !Marked(cm.OwnerReferences, owner) {
		return "", ErrNotOwned
	}
	if reflect.DeepEqual(spec(cur).Interface(), spec(desired).Interface()) &&
		reflect.DeepEqual(cm.OwnerReferences, dm.OwnerReferences) && cm.ResourceGroup == dm.ResourceGroup {
		return cm.UID, nil
	}
	spec(cur).Set(spec(desired))
	cm.OwnerReferences, cm.ResourceGroup = dm.OwnerReferences, dm.ResourceGroup
	if _, uerr := st.Update(ctx, cur); uerr != nil {
		return "", fault.Wrapf(uerr, fault.KindOf(uerr), op, "update %s %q", gvk.Kind, dm.Name)
	}
	return cm.UID, nil
}

// spec is the Spec field of a typed object (every store kind has one).
func spec(obj v1.Object) reflect.Value { return reflect.ValueOf(obj).Elem().FieldByName("Spec") }

// MapOwner maps a store event to the owners of kind whose marker the store carries, so a change to a store re-runs
// the reconciler of the resource that declares it.
func MapOwner(_ store.Store, kind v1.Kind) controller.MapFunc {
	return func(_ context.Context, obj v1.Object) []controller.Request {
		meta := obj.GetObjectMeta()
		var reqs []controller.Request
		for _, r := range meta.OwnerReferences {
			if IsMarker(r) && r.Kind == kind {
				reqs = append(reqs, controller.Request{GVK: kind.GVK(), Namespace: meta.Namespace, Name: r.Name})
			}
		}
		return reqs
	}
}

// The reasons a declared Secret stops its declarer (ADR-0213 Decision 8).
const (
	ReasonSecretNotFound   = "SecretNotFound"
	ReasonSecretKeyMissing = "SecretKeyMissing"
)

// SecretGap is the first declared Secret that is not complete: the declaration's index and name, and the first declared
// key it lacks, or no key when the Secret does not exist. It holds no Secret value.
type SecretGap struct {
	Index int
	Name  v1.ObjectName
	Key   string
}

// Reason is SecretNotFound or SecretKeyMissing.
func (g SecretGap) Reason() string {
	if g.Key == "" {
		return ReasonSecretNotFound
	}
	return ReasonSecretKeyMissing
}

// Message is the gap as a resource that declares its Secrets in spec.secrets reports it.
func (g SecretGap) Message() string {
	if g.Key == "" {
		return fmt.Sprintf("spec.secrets[%d]: Secret %q does not exist in the namespace; waiting", g.Index, g.Name)
	}
	return fmt.Sprintf("spec.secrets[%d]: Secret %q lacks the declared key %s; waiting", g.Index, g.Name, g.Key)
}

// CheckSecrets gets each Secret decls declares in ns, in declaration order, and returns the first gap, nil when every
// one exists with every declared key (ADR-0213 Decision 8). It tests key membership only and only reads.
func CheckSecrets(ctx context.Context, st store.Store, ns v1.NamespaceName, decls []v1.SecretDeclaration) (*SecretGap, error) {
	const op = "ownedstore.CheckSecrets"
	for i, d := range decls {
		obj, err := st.Get(ctx, v1.KindSecret.GVK(), ns, d.Name)
		if fault.KindOf(err) == fault.NotFound {
			return &SecretGap{Index: i, Name: d.Name}, nil
		}
		if err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "get secret %q", d.Name)
		}
		sec, ok := obj.(*v1.Secret)
		if !ok {
			return nil, fault.Internalf(op, "object %s/%s is not a Secret", ns, d.Name)
		}
		for _, key := range d.Keys {
			if _, has := sec.Spec.Data[key]; !has {
				return &SecretGap{Index: i, Name: d.Name, Key: key}, nil
			}
		}
	}
	return nil, nil
}
