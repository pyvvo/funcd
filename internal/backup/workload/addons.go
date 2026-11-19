package workload

import (
	"context"
	"errors"
	"io"
	"path"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// addonSnapshotQuiet is how long a snapshot stays unchanged before a Backup copies it (ADR-0333 Decision 2): hiqlite
// rewrites a copy's metadata just after naming it.
const addonSnapshotQuiet = 2 * time.Second

// The failures of an add-on object's member that a run names by reason (ADR-0333 Decisions 2 and 3).
var (
	ErrNoSnapshot      = errors.New("the add-on object has no snapshot to copy")               // reason NoSnapshot
	ErrRestoreTimedOut = errors.New("the engine did not take the copy within restore.timeout") // reason RestoreTimedOut
)

// AddonFiles reads and places an add-on engine's files on the host. dir is relative to the Addon's storage.path; every
// call runs inside an os.Root opened on the engine's storage directory, and file is one path segment matching
// ^[A-Za-z0-9][A-Za-z0-9._-]*$. Newest is NotFound when dir holds no quiet file.
type AddonFiles interface {
	Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (file string, r io.ReadCloser, err error)
	Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, r io.Reader, uid, gid int) error
	Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error
}

// Addons is the one MemberKind of every add-on object, with files addon/<kind>/<name>/<file>/. Its Kind() is
// AddonObject and its members carry their add-on's kind: for Addons, Kind() is the kind Serves matches, not ADR-0311's
// "the kind its members carry". A member's file is the newest quiet snapshot its engine wrote under its Addon's
// snapshot.path, copied unopened in records of 8 MiB; a Restore loads it in place only, through the engine's next start
// (ADR-0333 Decisions 2 to 4). files nil is a runtime that reaches no engine's files.
func Addons(st store.Store, files AddonFiles) MemberKind { return addons{st: st, files: files} }

type addons struct {
	st    store.Store
	files AddonFiles
}

// Serves reports whether k serves members of kind: k.Kind() == kind, or, for Addons, any kind funcd does not define.
// Write and a Restore's plan find a member's kind through it.
func Serves(k MemberKind, kind v1.Kind) bool {
	if k.Kind() == kind {
		return true
	}
	_, ok := k.(addons)
	return ok && kind.Validate() != nil
}

func (addons) Kind() v1.Kind { return v1.KindAddonObject }

func (k addons) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	const op = "workload.Write"
	member := string(m.Kind) + "/" + string(m.Name)
	a, err := k.addonOf(ctx, ns, m.Kind, m.Name)
	switch {
	case err != nil:
		return MemberFile{}, err
	case a == nil:
		return MemberFile{}, marked{fault.NotFoundf(op, "member %s: no Addon registers the kind %s, so none declares its snapshot.path",
			member, m.Kind), ErrNoSnapshot}
	case a.Spec.Snapshot == nil:
		return MemberFile{}, marked{fault.NotFoundf(op, "member %s: the add-on %s declares no snapshot.path", member, a.Name), ErrNoSnapshot}
	case k.files == nil:
		return MemberFile{}, fault.Unavailablef(op, "member %s: this server reaches no add-on engine's files", member)
	}
	file, rc, err := k.files.Newest(ctx, ns, m.Name, a.Spec.StorageDir(a.Spec.Snapshot.Path), addonSnapshotQuiet)
	if fault.KindOf(err) == fault.NotFound {
		return MemberFile{}, marked{fault.Wrapf(err, fault.NotFound, op, "member %s: its engine has written no snapshot under %s yet",
			member, a.Spec.Snapshot.Path), ErrNoSnapshot}
	}
	if err != nil {
		return MemberFile{}, fault.Wrapf(err, fault.KindOf(err), op, "member %s: read its snapshot", member)
	}
	defer func() { _ = rc.Close() }()
	if !v1.IsAddonFile(file) {
		return MemberFile{}, fault.Invalidf(op, "member %s: the snapshot %q is not one path segment", member, file)
	}
	f, err := w.File(ctx, "addon/"+member+"/"+file, func(emit func(snapshot.Record) error) error {
		return streamFile(op, "add-on object "+member, io.NopCloser(rc), emit)
	})
	if err != nil {
		return MemberFile{}, err
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Files: []backup.StoreFile{f}}, nil
}

// addonOf is the Addon of the add-on object ns/name of kind: NotFound when no such object exists, nil when no Addon
// registers kind. An object belongs to the Addon that registers its kind, whatever that Addon's name (ADR-0332
// Decision 2).
func (k addons) addonOf(ctx context.Context, ns v1.NamespaceName, kind v1.Kind, name v1.ObjectName) (*v1.Addon, error) {
	const op = "workload.Addons"
	obj, err := k.st.Get(ctx, v1.KindAddonObject.GVK(), ns, name)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "%s %s/%s", kind, ns, name)
	}
	if o, ok := obj.(*v1.AddonObject); !ok || o.Spec.Kind != string(kind) {
		return nil, fault.NotFoundf(op, "no %s %s/%s: the add-on object %s is of another kind", kind, ns, name, name)
	}
	l, err := k.st.List(ctx, v1.KindAddon.GVK(), store.ListOptions{})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list Addons")
	}
	for _, o := range l.Items {
		if a, ok := o.(*v1.Addon); ok && a.Spec.Kind == string(kind) {
			return a, nil
		}
	}
	return nil, nil
}

func (addons) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 5 || seg[0] != "addon" || !v1.IsAddonKind(seg[1]) {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.Kind(seg[1]), Name: v1.ObjectName(seg[2])}, 0, true
}

// Destination is the add-on object rm names, of rm's kind, loaded in place: MemberNotFound without one. Its Addon
// declares where the copy goes and how long the Restore waits for the engine to take it.
func (k addons) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	const op = "backup.Restore"
	if !rm.InPlace {
		return nil, fault.Invalidf(op, "member %s/%s: an add-on object restores in place only", rm.Kind, rm.Name)
	}
	a, err := k.addonOf(ctx, ns, rm.Kind, rm.Name)
	switch {
	case err != nil:
		return nil, err
	case a == nil:
		return nil, fault.Unavailablef(op, "member %s/%s: no Addon registers the kind %s", rm.Kind, rm.Name, rm.Kind)
	case a.Spec.Restore == nil:
		return nil, fault.Invalidf(op, "member %s/%s: the add-on %s declares no restore.path", rm.Kind, rm.Name, a.Name)
	case k.files == nil:
		return nil, fault.Unavailablef(op, "member %s/%s: this server reaches no add-on engine's files", rm.Kind, rm.Name)
	case restoreOf(ctx) == "":
		return nil, fault.Internalf(op, "member %s/%s: no Restore names the load, which its object's status.restore records",
			rm.Kind, rm.Name)
	}
	obj, err := k.st.Get(ctx, v1.KindAddonObject.GVK(), ns, rm.Name)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "%s %s/%s", rm.Kind, ns, rm.Name)
	}
	d := addonDestination{st: k.st, files: k.files, ns: ns, name: rm.Name, uid: obj.GetObjectMeta().UID,
		dir: a.Spec.StorageDir(a.Spec.Restore.Path), timeout: a.Spec.Restore.EffectiveTimeout(), restore: restoreOf(ctx)}
	if s := a.Spec.Storage; s != nil {
		d.owner, d.group = s.UID, s.GID
	}
	return d, nil
}

// addonDestination is an add-on object a Restore loads in place: its copy goes into restore.path for the engine's next
// start, under the namespace hold the Restore took (ADR-0333 Decision 3).
type addonDestination struct {
	st           store.Store
	files        AddonFiles
	ns           v1.NamespaceName
	name         v1.ObjectName
	uid          v1.UID // the object's, which a Restore that outlives it fails on
	dir          string // restore.path, relative to storage.path
	owner, group int    // the storage's uid and gid
	timeout      time.Duration
	restore      v1.ObjectName // the Restore that places the copy
}

func (addonDestination) Prepare(context.Context) (func(), error) { return none, nil }

// IsEmpty is never asked: an add-on object restores in place only.
func (addonDestination) IsEmpty(context.Context) (bool, error) { return false, nil }

// Load empties restore.path, writes the copy there, owned by the storage's uid and gid, and sets status.restore to the
// Restore, the copy's file and Loading. It then waits until status.restore is gone, which the add-on reconciler does
// once the engine took the copy and started again without restore.env: past restore.timeout it fails
// ErrRestoreTimedOut, and the hold stays (ADR-0333 Decisions 3 and 4).
func (d addonDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	const op = "backup.Restore"
	if err := wantFiles(f, 1, 1); err != nil {
		return err
	}
	file := path.Base(f.Files[0].Name)
	if !v1.IsAddonFile(file) {
		return fault.Invalidf(op, "member %s/%s: the copy %q is not one path segment", f.Kind, f.Name, file)
	}
	if err := d.files.Clear(ctx, d.ns, d.name, d.dir); err != nil {
		return err
	}
	if err := d.files.Put(ctx, d.ns, d.name, d.dir, file, &recordReader{next: open(ctx, f.Files[0])}, d.owner, d.group); err != nil {
		return err
	}
	if err := d.mark(ctx, &v1.AddonRestoreState{Restore: d.restore, File: file, State: v1.AddonRestoreLoading}); err != nil {
		return err
	}
	return d.wait(ctx)
}

// Clear removes the copy, then the status.restore this Restore set.
func (d addonDestination) Clear(ctx context.Context) error {
	if err := d.files.Clear(ctx, d.ns, d.name, d.dir); err != nil {
		return err
	}
	return d.mark(ctx, nil)
}

// markAttempts bounds the rereads of a status.restore write that loses to the reconciler's writes of the same object.
const markAttempts = 10

// mark writes the object's status.restore: to = nil removes only the one this Restore set, which an object deleted or
// created again no longer holds.
func (d addonDestination) mark(ctx context.Context, to *v1.AddonRestoreState) error {
	const op = "backup.Restore"
	for range markAttempts {
		o, err := d.object(ctx)
		if to == nil && fault.KindOf(err) == fault.Conflict {
			return nil
		}
		if err != nil {
			return err
		}
		if to == nil && (o.Status.Restore == nil || o.Status.Restore.Restore != d.restore) {
			return nil
		}
		o.Status.Restore = to
		if _, err = d.st.Update(ctx, o); fault.KindOf(err) != fault.Conflict {
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "write status.restore of %s/%s", d.ns, d.name)
			}
			return nil
		}
	}
	return fault.Conflictf(op, "write status.restore of %s/%s: kept conflicting", d.ns, d.name)
}

// object reads the object the Restore planned: one deleted or created again since fails it.
func (d addonDestination) object(ctx context.Context) (*v1.AddonObject, error) {
	const op = "backup.Restore"
	obj, err := d.st.Get(ctx, v1.KindAddonObject.GVK(), d.ns, d.name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, fault.Conflictf(op, "the add-on object %s/%s was deleted during the restore", d.ns, d.name)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get the add-on object %s/%s", d.ns, d.name)
	}
	o, ok := obj.(*v1.AddonObject)
	if !ok || o.UID != d.uid {
		return nil, fault.Conflictf(op, "the add-on object %s/%s was created again during the restore", d.ns, d.name)
	}
	return o, nil
}

// wait watches the object until its status.restore is no longer this Restore's, at most the timeout: a stop's
// cancellation returns its error, so the Restore stays Running for the next start to end.
func (d addonDestination) wait(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, d.timeout, ErrRestoreTimedOut)
	defer cancel()
	for {
		w, err := d.st.Watch(ctx, v1.KindAddonObject.GVK(), store.WatchOptions{Namespace: d.ns})
		if err != nil {
			return d.ended(ctx, err)
		}
		done, err := d.loaded(ctx)
		for !done && err == nil {
			ev, open := <-w.ResultChan()
			if !open {
				break
			}
			if ev.Object.GetName() == d.name {
				done, err = d.loaded(ctx)
			}
		}
		w.Stop()
		if done || err != nil {
			return d.ended(ctx, err)
		}
		if ctx.Err() != nil {
			return d.ended(ctx, ctx.Err())
		}
	}
}

// loaded reports whether the object's status.restore is no longer this Restore's.
func (d addonDestination) loaded(ctx context.Context) (bool, error) {
	o, err := d.object(ctx)
	if err != nil {
		return false, err
	}
	return o.Status.Restore == nil || o.Status.Restore.Restore != d.restore, nil
}

// ended is the wait's end: past the timeout, ErrRestoreTimedOut naming it; else err.
func (d addonDestination) ended(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), ErrRestoreTimedOut) {
		return marked{fault.Unavailablef("backup.Restore", "%s/%s: its engine did not take the copy and start again without restore.env within %s",
			d.ns, d.name, d.timeout), ErrRestoreTimedOut}
	}
	return err
}

type restoreKey struct{}

// WithRestore gives a Restore's member calls the Restore's name, which an add-on object's status.restore records.
func WithRestore(ctx context.Context, name v1.ObjectName) context.Context {
	return context.WithValue(ctx, restoreKey{}, name)
}

// restoreOf is the Restore WithRestore names, "" without one.
func restoreOf(ctx context.Context) v1.ObjectName {
	name, _ := ctx.Value(restoreKey{}).(v1.ObjectName)
	return name
}
