package addon

import (
	"context"
	"encoding/json"
	"net/netip"
	"path"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/store"
)

// restoreStep is what a pass does for an in-place Restore of the object (ADR-0333 Decision 4).
type restoreStep int

const (
	// restoreNone starts the engine without restore.env.
	restoreNone restoreStep = iota
	// restoreLoading starts it with restore.env; once that engine is Ready, the copy goes, then the mark turns Loaded.
	restoreLoading
	// restoreLoaded starts it without restore.env; once that engine is Ready, the mark goes.
	restoreLoaded
)

// restoreStep reads o's status.restore, which an in-place Restore set once it placed its copy. A mark whose Restore has
// ended or is gone goes, its copy first, so the engine starts without restore.env under the hold that Restore kept: a
// start after funcd's crash meets hiqlite's crash marker, which a restore start cannot pass. A Loading mark whose copy
// is gone turns Loaded, whatever the engine does then. An Addon that declares no restore leaves a Loading mark for its
// Restore's timeout to end.
func (r *Reconciler) restoreStep(ctx context.Context, a *v1.Addon, o *v1.AddonObject) (restoreStep, error) {
	m := o.Status.Restore
	if m == nil {
		return restoreNone, nil
	}
	ended, err := r.restoreEnded(ctx, o.Namespace, m.Restore)
	if err != nil {
		return restoreNone, err
	}
	switch {
	case ended:
		if err := r.clearCopy(ctx, a, o); err != nil {
			return restoreNone, err
		}
		o.Status.Restore = nil
		return restoreNone, nil
	case m.State == v1.AddonRestoreLoaded:
		return restoreLoaded, nil
	case a.Spec.Restore == nil:
		return restoreNone, nil
	}
	has, err := r.hasCopy(ctx, a, o)
	if err != nil {
		return restoreNone, err
	}
	if !has {
		m.State = v1.AddonRestoreLoaded
		return restoreLoaded, nil
	}
	return restoreLoading, nil
}

// restoreEnded reports whether the Restore name of ns has ended, or is gone.
func (r *Reconciler) restoreEnded(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	obj, err := r.store.Get(ctx, v1.KindRestore.GVK(), ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return true, nil
	}
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "services.addon.restoreEnded", "get Restore %s/%s", ns, name)
	}
	rs, ok := obj.(*v1.Restore)
	return !ok || rs.Ended(), nil
}

// hasCopy reports whether restore.path still holds a copy; without file access it answers yes, so no mark moves on a
// copy nobody saw go.
func (r *Reconciler) hasCopy(ctx context.Context, a *v1.Addon, o *v1.AddonObject) (bool, error) {
	if r.files == nil {
		return true, nil
	}
	_, rc, err := r.files.Newest(ctx, o.Namespace, o.Name, a.Spec.StorageDir(a.Spec.Restore.Path), 0)
	if fault.KindOf(err) == fault.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "services.addon.hasCopy", "look for the copy of %s/%s", o.Namespace, o.Name)
	}
	_ = rc.Close()
	return true, nil
}

// clearCopy empties restore.path; nothing without file access or a restore.path.
func (r *Reconciler) clearCopy(ctx context.Context, a *v1.Addon, o *v1.AddonObject) error {
	if r.files == nil || a.Spec.Restore == nil {
		return nil
	}
	if err := r.files.Clear(ctx, o.Namespace, o.Name, a.Spec.StorageDir(a.Spec.Restore.Path)); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.clearCopy", "remove the copy of %s/%s", o.Namespace, o.Name)
	}
	return nil
}

// restoreEnv renders restore.env over the object's config and restore.file, the copy's path in the engine.
func restoreEnv(a *v1.Addon, o *v1.AddonObject) (map[string]string, error) {
	file, err := json.Marshal(map[string]string{"file": path.Join(a.Spec.Restore.Path, o.Status.Restore.File)})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "services.addon.restoreEnv", "encode restore.file")
	}
	env, err := RenderEnv(a.Spec.Restore.Env, map[string]json.RawMessage{configDoc: configOf(o), restoreDoc: file},
		restoreSchemas(a.Spec.Config))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, "services.addon.restoreEnv", "spec.restore.env")
	}
	return env, nil
}

// MapRestore maps a Restore's change to the objects of its namespace whose status.restore names it, whose copy and mark
// its end clears (ADR-0333 Decision 4).
func (r *Reconciler) MapRestore(ctx context.Context, obj v1.Object) []controller.Request {
	m := obj.GetObjectMeta()
	return r.objects(ctx, m.Namespace, func(o *v1.AddonObject) bool {
		return o.Status.Restore != nil && o.Status.Restore.Restore == m.Name
	})
}

// remember records o's UID and deletion policy, which its deletion reads once it is gone. An object created again under
// the name of one deleted between two passes first has the earlier one's engine stopped and its storage reclaimed, so
// it starts empty (ADR-0333 Decision 5).
func (r *Reconciler) remember(ctx context.Context, o *v1.AddonObject) error {
	ref := provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}
	r.mu.Lock()
	prev, had := r.seen[ref]
	r.mu.Unlock()
	if had && prev.uid != o.UID {
		if err := r.prov.Teardown(ctx, ref); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), "services.addon.remember", "stop the engine of %s/%s", ref.Namespace, ref.Name)
		}
		if err := r.reclaim(ctx, ref, prev.deletion); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.seen[ref] = seenObject{uid: o.UID, deletion: o.Spec.Deletion}
	r.mu.Unlock()
	return nil
}

// deletionOf is the deletion policy remember recorded of ref: retain when unknown, as after a restart.
func (r *Reconciler) deletionOf(ref provider.ProviderRef) v1.DeletionPolicy {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[ref].deletion
}

// forget drops what remember recorded of ref, once its deletion is done.
func (r *Reconciler) forget(ref provider.ProviderRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.seen, ref)
}

// reclaim removes ref's storage under deletion delete, else moves it into the quarantine in one rename, once its engine
// is gone (ADR-0333 Decision 5); nothing while the platform is held (ADR-0231 Decision 4).
func (r *Reconciler) reclaim(ctx context.Context, ref provider.ProviderRef, deletion v1.DeletionPolicy) error {
	const op = "services.addon.reclaim"
	if r.volumes == nil || r.gate.Held() {
		return nil
	}
	if deletion == v1.DeletionDelete {
		if err := r.volumes.Reclaim(ctx, ref); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "remove the storage of %s/%s", ref.Namespace, ref.Name)
		}
		return nil
	}
	return r.quarantineMove(ctx, ref, r.now(), "moved the storage of a deleted add-on object into the quarantine")
}

// quarantineMove moves ref's storage into a quarantine entry dated now and logs msg; nothing without storage.
func (r *Reconciler) quarantineMove(ctx context.Context, ref provider.ProviderRef, now time.Time, msg string) error {
	e, err := r.volumes.Quarantine().Move(ctx, quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindAddonObject,
		Namespace: ref.Namespace, Name: ref.Name}}, now)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil
	case err != nil:
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.quarantine", "quarantine the storage of %s/%s", ref.Namespace, ref.Name)
	}
	r.logger.WarnContext(ctx, msg, "namespace", ref.Namespace, "name", ref.Name, "until", e.Until(r.quarantine))
	return nil
}

// ReclaimOrphans moves into the quarantine, before the controller runs, the storage of every add-on object no longer
// stored, whatever its deletion was: no start-up path deletes data (ADR-0288 Decision 1, ADR-0333 Decision 5). Each
// entry is dated now, the start's quarantine clock. A no-op while the platform is held.
func (r *Reconciler) ReclaimOrphans(ctx context.Context, now time.Time) error {
	const op = "services.addon.ReclaimOrphans"
	if r.gate.Held() {
		return nil
	}
	orphans, err := r.Orphans(ctx)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		ref := provider.ProviderRef{Namespace: o.Namespace, Name: o.Name}
		if err := r.prov.Teardown(ctx, ref); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "stop the engine of %s/%s", ref.Namespace, ref.Name)
		}
		if err := r.quarantineMove(ctx, ref, now, "moved the data of an AddonObject with no object into the quarantine"); err != nil {
			return err
		}
	}
	return nil
}

// Orphans are the add-on objects whose storage has no object, which hold status lists while held (ADR-0231 Decision 4)
// and a start not held quarantines.
func (r *Reconciler) Orphans(ctx context.Context) ([]v1.ObjectRef, error) {
	const op = "services.addon.Orphans"
	if r.volumes == nil {
		return nil, nil
	}
	refs, err := r.volumes.List(ctx)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the add-on objects' storage")
	}
	var out []v1.ObjectRef
	for _, ref := range refs {
		_, err := r.store.Get(ctx, v1.KindAddonObject.GVK(), ref.Namespace, ref.Name)
		switch {
		case fault.KindOf(err) == fault.NotFound:
			out = append(out, v1.ObjectRef{Kind: v1.KindAddonObject, Namespace: ref.Namespace, Name: ref.Name})
		case err != nil:
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "get the add-on object %s/%s", ref.Namespace, ref.Name)
		}
	}
	return out, nil
}

// Quarantine is the quarantine of the add-on objects' storage (ADR-0288, ADR-0333 Decision 5): hold status lists it and
// the sweep expires it; an entry comes back by hand. nil without storage.
func (r *Reconciler) Quarantine() quarantine.Store {
	if r.volumes == nil {
		return nil
	}
	return r.volumes.Quarantine()
}

// Engines tells an add-on engine's address from the objects' status.address, each engine's own (ADR-0332 Decision 3):
// an engine on a loopback address, as funcdctl dev publishes one, is that address and port; any other is its whole
// address, which only its engine holds.
type Engines struct{ Store store.Store }

// Engine reports whether addr is an add-on engine's address.
func (e Engines) Engine(ctx context.Context, addr netip.AddrPort) (bool, error) {
	l, err := e.Store.List(ctx, v1.KindAddonObject.GVK(), store.ListOptions{})
	if err != nil {
		return false, fault.Wrapf(err, fault.KindOf(err), "services.addon.Engines", "list the add-on objects")
	}
	addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
	for _, obj := range l.Items {
		o, ok := obj.(*v1.AddonObject)
		if !ok {
			continue
		}
		at, err := netip.ParseAddrPort(o.Status.Address)
		if err != nil {
			continue
		}
		at = netip.AddrPortFrom(at.Addr().Unmap(), at.Port())
		if at == addr || !at.Addr().IsLoopback() && at.Addr() == addr.Addr() {
			return true, nil
		}
	}
	return false, nil
}
