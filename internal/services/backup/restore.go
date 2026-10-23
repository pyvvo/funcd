package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// RestoreReconciler is the controller.Reconciler for KindRestore (Decision 7).
type RestoreReconciler struct{ *base }

// target is one member's destination: a KVStore prefix, a SQLDatabase, or a view of the blob store and whether it is
// a Bucket.
type target struct {
	member  v1.RestoreMember
	file    workload.MemberFile
	kv      string
	sql     v1.ObjectName
	view    blob.Bucket
	bucket  v1.ObjectName
	volFn   v1.ObjectName // with volume: the Function whose local volume volume is loaded (ADR-0262)
	volume  v1.ObjectName
	lost    bool // with volume: its Function's DataMissing names it, so what it holds dates from after the loss (#1206)
	written bool
}

// Reconcile drives a Restore Pending → Running → Succeeded | Failed. It waits for its Backup to succeed; an in-place
// Restore first runs Backup <restore>-before of the Backup's schedule and waits for it (gone ⇒ ScheduleNotFound,
// failed ⇒ ExportFailed, nothing written). The load is a goroutine; a Running Restore this process does not run was
// interrupted. Restores run while the platform is held.
func (r *RestoreReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "backup.RestoreReconcile"
	obj, err := r.d.Store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get %s/%s", req.Namespace, req.Name)
	}
	rs, ok := obj.(*v1.Restore)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "%s/%s is not a Restore", req.Namespace, req.Name)
	}
	now := r.d.Clock.Now()
	switch rs.Status.Phase {
	case v1.RunSucceeded, v1.RunFailed:
		return controller.Result{}, nil
	case v1.RunRunning:
		if r.isActive(refOf(rs)) {
			return controller.Result{}, nil
		}
		return controller.Result{}, r.finish(ctx, rs, now, errors.New("funcd stopped during the restore"), ReasonInterrupted)
	case "":
		rs.Status.Phase = v1.RunPending
		return controller.Result{}, r.write(ctx, rs)
	}
	b, wait, err := r.backupOf(ctx, rs.Namespace, rs.Spec.Backup)
	switch {
	case err != nil:
		return controller.Result{}, r.finish(ctx, rs, now, err, reasonBackupNotReady)
	case wait:
		return controller.Result{}, nil
	}
	if rs.InPlace() {
		if done, err := r.export(ctx, rs, b, now); !done || err != nil {
			return controller.Result{}, err
		}
	}
	if !r.claim(refOf(rs)) {
		return controller.Result{}, nil
	}
	rs.Status.Phase, rs.Status.StartTime = v1.RunRunning, ts(now)
	if err := r.write(ctx, rs); err != nil {
		r.release(refOf(rs))
		return controller.Result{}, err
	}
	r.spawn(func() {
		defer r.release(refOf(rs))
		reason, err := r.load(ctx, rs, b)
		if ctx.Err() != nil {
			return
		}
		if err := r.finish(ctx, rs, r.d.Clock.Now(), err, reason); err != nil {
			r.log.WarnContext(ctx, "workload restore: record the end", "namespace", rs.Namespace, "restore", rs.Name, "error", err)
		}
	})
	return controller.Result{}, nil
}

// MapBackup maps a Backup to the Restores that read it or wait for it as their export.
func (r *RestoreReconciler) MapBackup(ctx context.Context, obj v1.Object) []controller.Request {
	b, ok := obj.(*v1.Backup)
	if !ok {
		return nil
	}
	l, err := r.d.Store.List(ctx, v1.KindRestore.GVK(), store.ListOptions{Namespace: b.Namespace})
	if err != nil {
		return nil
	}
	var out []controller.Request
	for _, o := range l.Items {
		if rs, ok := o.(*v1.Restore); ok && (rs.Spec.Backup == b.Name || rs.Status.Export == b.Name) {
			out = append(out, controller.Request{GVK: v1.KindRestore.GVK(), Namespace: rs.Namespace, Name: rs.Name})
		}
	}
	return out
}

// backupOf reads a Backup: a missing or Failed one is an error, a running one a wait.
func (r *RestoreReconciler) backupOf(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Backup, bool, error) {
	obj, err := r.d.Store.Get(ctx, v1.KindBackup.GVK(), ns, name)
	if err != nil {
		return nil, false, fault.Wrapf(err, fault.KindOf(err), "backup.RestoreReconcile", "Backup %s/%s", ns, name)
	}
	b := obj.(*v1.Backup)
	switch b.Status.Phase {
	case v1.RunSucceeded:
		return b, false, nil
	case v1.RunFailed:
		return nil, false, fault.Conflictf("backup.RestoreReconcile", "Backup %s/%s failed", ns, name)
	}
	return nil, true, nil
}

// export runs an in-place Restore's Backup <restore>-before of b's schedule and reports it Succeeded; it fails the
// Restore when the schedule is gone (ScheduleNotFound) or the export failed (ExportFailed).
func (r *RestoreReconciler) export(ctx context.Context, rs *v1.Restore, b *v1.Backup, now time.Time) (bool, error) {
	if rs.Status.Export == "" {
		obj, err := r.d.Store.Get(ctx, v1.KindBackupSchedule.GVK(), rs.Namespace, b.Spec.Schedule)
		if fault.KindOf(err) == fault.NotFound {
			return false, r.finish(ctx, rs, now, fault.Wrapf(err, fault.NotFound, "backup.RestoreReconcile", "schedule %q", b.Spec.Schedule), ReasonScheduleNotFound)
		}
		if err != nil {
			return false, err
		}
		s := obj.(*v1.BackupSchedule)
		eo, _ := v1.NewObject(v1.KindBackup)
		ex := eo.(*v1.Backup)
		ex.ObjectMeta = v1.ObjectMeta{Name: v1.RestoreExportName(rs.Name), Namespace: rs.Namespace, ResourceGroup: s.ResourceGroup}
		ex.Spec.Schedule = s.Name
		if _, err := r.d.Store.Create(ctx, ex); err != nil && fault.KindOf(err) != fault.Conflict {
			return false, fault.Wrapf(err, fault.KindOf(err), "backup.RestoreReconcile", "create the export %s", ex.Name)
		}
		rs.Status.Export = ex.Name
		return false, r.write(ctx, rs)
	}
	ex, wait, err := r.backupOf(ctx, rs.Namespace, rs.Status.Export)
	switch {
	case err != nil:
		return false, r.finish(ctx, rs, now, err, ReasonExportFailed)
	case wait:
		return false, nil
	}
	return ex != nil, nil
}

// load reads b with the Restore's credential and identity, checks every part, then loads each member (Decision 7):
// an empty target or, in place, the store emptied under a namespace hold that only a completed load lifts. A
// non-empty target is NotEmpty before any write; a failure empties what it wrote. It returns the failure's reason.
func (r *RestoreReconciler) load(ctx context.Context, rs *v1.Restore, b *v1.Backup) (string, error) {
	key, err := r.staticKey(ctx, rs.Namespace, rs.Spec.Source.CredentialsSecret)
	if err != nil {
		return reasonFailed, err
	}
	env, err := r.d.Secrets.ResolveEnv(ctx, developerFor(rs.Namespace), rs.Namespace, []string{string(rs.Spec.Source.IdentitySecret)})
	if err != nil {
		return reasonFailed, err
	}
	ids, err := envelope.ParseIdentities(fmt.Sprintf("Secret %s/%s key %s", rs.Namespace, rs.Spec.Source.IdentitySecret, keyAgeIdentity),
		strings.NewReader(env[keyAgeIdentity]))
	if err != nil {
		return reasonFailed, err
	}
	src, err := openTarget(ctx, b.Status.Target, key)
	if err != nil {
		return reasonFailed, err
	}
	defer func() { _ = src.Close() }()
	m, err := workload.ReadManifest(ctx, src, b.Status.Path)
	if err != nil {
		return reasonFailed, err
	}
	if err := workload.Integrity(ctx, src, b.Status.Path, m); err != nil {
		return ReasonIntegrity, err
	}
	unseal, err := envelope.Opener(ids)(m.Recipients)
	if err != nil {
		return reasonFailed, err
	}
	targets, reason, err := r.plan(ctx, rs, m)
	if err != nil {
		return reason, err
	}
	var held *hold.Marker
	if rs.InPlace() && r.d.Hold != nil {
		m := hold.Marker{Reason: hold.ReasonRestore, Since: v1.NewTimestamp(r.d.Clock.Now()), Restore: rs.Name}
		if err := r.d.Hold.HoldNamespace(rs.Namespace, m); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				return ReasonNamespaceHeld, err
			}
			return reasonFailed, err
		}
		held = &m
	}
	release, err := r.stopVolumeWorkers(ctx, rs.Namespace, targets)
	if err != nil {
		return reasonFailed, err
	}
	defer release()
	for _, t := range targets {
		t.written = true
		if reason, err := r.fill(ctx, rs.Namespace, t, src, b.Status.Path, unseal); err != nil {
			// a load that refused its target as not empty wrote nothing there: what the target holds is not the
			// Restore's to empty
			t.written = reason != ReasonNotEmpty
			return reason, errors.Join(err, r.empty(ctx, rs.Namespace, targets))
		}
	}
	// Only a completed load lifts the hold: a failure, a stop or a crash leaves it to hold release --namespace once
	// the Restore has ended, whatever the emptying did.
	if held != nil {
		if err := r.d.Hold.ReleaseNamespaceMarker(rs.Namespace, *held); err != nil {
			r.log.WarnContext(ctx, "workload restore: lift the namespace hold", "namespace", rs.Namespace, "error", err)
		}
	}
	return "", nil
}

// stopVolumeWorkers stops the workers of each Function whose volume the load fills until release, so none runs while
// its volume is emptied and loaded (ADR-0262 Decisions 11 and 12, #1206).
func (r *RestoreReconciler) stopVolumeWorkers(ctx context.Context, ns v1.NamespaceName, targets []*target) (release func(), err error) {
	var releases []func()
	release = func() {
		for _, f := range releases {
			f()
		}
	}
	stopped := map[v1.ObjectName]bool{}
	for _, t := range targets {
		if t.volume == "" || r.d.Volumes == nil || stopped[t.volFn] {
			continue
		}
		stopped[t.volFn] = true
		f, err := r.d.Volumes.StopWorkers(ctx, ns, t.volFn)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, f)
	}
	return release, nil
}

// plan resolves each member's file in m and its destination; a missing member or destination is MemberNotFound and a
// destination that is not empty NotEmpty.
func (r *RestoreReconciler) plan(ctx context.Context, rs *v1.Restore, m workload.Manifest) ([]*target, string, error) {
	const op = "backup.Restore"
	ns := rs.Namespace
	var out []*target
	for _, mb := range rs.Spec.Members {
		t := &target{member: mb}
		i := -1
		for j, mf := range m.Members {
			if mf.Kind == mb.Kind && mf.Name == mb.Name && mf.Volume == mb.Volume {
				i = j
			}
		}
		if i < 0 {
			return nil, ReasonMemberNotFound, fault.NotFoundf(op, "the Backup holds no %s/%s%s", mb.Kind, mb.Name, volumeSuffix(mb.Volume))
		}
		t.file = m.Members[i]
		dest := mb.Name
		if mb.As != "" {
			dest = mb.As
		}
		var err error
		switch {
		case mb.Kind == v1.KindKVStore:
			_, err = r.d.Store.Get(ctx, v1.KindKVStore.GVK(), ns, dest)
			t.kv = workload.KVPrefix(ns, dest)
		case mb.Kind == v1.KindBucket:
			_, err = r.d.Store.Get(ctx, v1.KindBucket.GVK(), ns, dest)
			t.bucket, t.view = dest, blob.Prefixed(r.d.Sources.Blob, r.d.Sources.BucketPrefix(ns, dest))
		case mb.Kind == v1.KindSQLDatabase:
			_, err = r.d.Store.Get(ctx, v1.KindSQLDatabase.GVK(), ns, dest)
			t.sql = dest
		case mb.Kind == v1.KindFunction:
			var obj v1.Object
			if obj, err = r.d.Store.Get(ctx, v1.KindFunction.GVK(), ns, dest); err == nil {
				f, _ := obj.(*v1.Function)
				if f == nil || !slices.ContainsFunc(f.Spec.Volumes, func(v v1.VolumeEntry) bool { return v.Type == v1.VolumeLocal && v.VolumeName() == mb.Volume }) {
					err = fault.NotFoundf(op, "function %s declares no local volume %s", dest, mb.Volume)
				}
			}
			if err == nil && r.d.Volumes != nil {
				t.lost, err = r.d.Volumes.Lost(ctx, ns, dest, mb.Volume)
			}
			t.volFn, t.volume = dest, mb.Volume
		case mb.AsLocation != nil:
			_, err = r.d.Store.Get(ctx, v1.KindBucket.GVK(), ns, mb.AsLocation.Bucket)
			t.view = blob.Prefixed(r.d.Sources.Blob, r.d.Sources.BucketPrefix(ns, mb.AsLocation.Bucket)+mb.AsLocation.Prefix+"/")
		default:
			var obj v1.Object
			if obj, err = r.d.Store.Get(ctx, v1.KindLake.GVK(), ns, mb.Name); err == nil {
				c := obj.(*v1.Lake).Spec.Location
				t.view = blob.Prefixed(r.d.Sources.Blob, r.d.Sources.BucketPrefix(ns, c.Bucket)+c.Prefix+"/")
			}
		}
		if fault.KindOf(err) == fault.NotFound {
			return nil, ReasonMemberNotFound, err
		}
		if err != nil {
			return nil, reasonFailed, err
		}
		if !mb.InPlace {
			empty, err := r.isEmpty(ctx, ns, t)
			if err != nil {
				return nil, failReason(err), err
			}
			if !empty {
				return nil, ReasonNotEmpty, fault.Conflictf(op, "%s/%s is not empty: a Restore loads into an empty one", mb.Kind, dest)
			}
		}
		out = append(out, t)
	}
	return out, "", nil
}

// isEmpty reports a target with no data; a SQLDatabase also needs no Function binding it, which closes the race
// between the check and the load (ADR-0234 Decision 3), and a Ready engine to answer.
func (r *RestoreReconciler) isEmpty(ctx context.Context, ns v1.NamespaceName, t *target) (bool, error) {
	if t.volume != "" {
		if r.d.Volumes == nil {
			return false, fault.Unavailablef("backup.Restore", "volumes are off: no volume loads")
		}
		if t.lost {
			return true, nil
		}
		return r.d.Volumes.IsEmpty(ctx, ns, t.volFn, t.volume)
	}
	if t.sql != "" {
		if r.d.SQL == nil {
			return false, fault.Unavailablef("backup.Restore", "no SQL engine loads a database")
		}
		fns, err := r.d.Store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
		if err != nil {
			return false, err
		}
		for _, o := range fns.Items {
			if fn, ok := o.(*v1.Function); ok && slices.ContainsFunc(fn.Spec.Bindings.SQL, func(b v1.FunctionSQL) bool { return b.Database == t.sql }) {
				return false, nil
			}
		}
		has, err := r.d.SQL.HasTables(ctx, ns, t.sql)
		return !has, err
	}
	if t.kv != "" {
		if r.d.KV == nil {
			return false, fault.Unavailablef("backup.Restore", "the KV driver loads no store")
		}
		keys, err := r.d.KV.List(ctx, t.kv)
		return len(keys) == 0, err
	}
	items, _, err := t.view.ListAfter(ctx, "", "", 1)
	return len(items) == 0, err
}

// fill empties an in-place target, then loads its member: a KVStore by LoadPrefix, a SQLDatabase by streaming its
// file's records to the engine's load, which replaces all its data, objects by Put, a lake's files before its
// catalog.db.
func (r *RestoreReconciler) fill(ctx context.Context, ns v1.NamespaceName, t *target, src blob.Bucket, path string, unseal backup.Unseal) (string, error) {
	if t.volume != "" {
		return r.fillVolume(ctx, ns, t, src, path, unseal)
	}
	if t.sql != "" {
		if r.d.SQL == nil {
			return reasonFailed, fault.Unavailablef("backup.Restore", "no SQL engine loads a database")
		}
		if err := r.d.SQL.Load(ctx, ns, t.sql, &recordReader{next: workload.Records(ctx, src, path, t.file.Files[0], unseal)}); err != nil {
			return failReason(err), err
		}
		return "", nil
	}
	if t.member.InPlace {
		if err := r.emptyOne(ctx, ns, t); err != nil {
			return reasonFailed, err
		}
	}
	if t.kv != "" {
		if r.d.KV == nil {
			return reasonFailed, fault.Unavailablef("backup.Restore", "the KV driver loads no store")
		}
		err := r.d.KV.LoadPrefix(ctx, t.kv, workload.Records(ctx, src, path, t.file.Files[0], unseal))
		if fault.KindOf(err) == fault.Conflict {
			return ReasonNotEmpty, err
		}
		if err != nil {
			return reasonFailed, err
		}
		return "", nil
	}
	for _, f := range t.file.Files {
		next := workload.Records(ctx, src, path, f, unseal)
		for {
			rec, err := next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return reasonFailed, err
			}
			ct, md, body, err := workload.DecodeObject(rec.Value)
			if err != nil {
				return ReasonIntegrity, err
			}
			if err := t.view.Put(ctx, string(rec.Key), body, blob.PutOptions{ContentType: ct, Metadata: md}); err != nil {
				return reasonFailed, err
			}
		}
	}
	return "", nil
}

// fillVolume loads a Function's local volume: only into an empty one, in place too (ADR-0262 Decision 11), but for a
// lost one, which is emptied first, since what it holds dates from after the loss (#1206).
func (r *RestoreReconciler) fillVolume(ctx context.Context, ns v1.NamespaceName, t *target, src blob.Bucket, path string, unseal backup.Unseal) (string, error) {
	if r.d.Volumes == nil {
		return reasonFailed, fault.Unavailablef("backup.Restore", "volumes are off: no volume loads")
	}
	if t.lost {
		if err := r.d.Volumes.Clear(ctx, ns, t.volFn, t.volume); err != nil {
			return reasonFailed, err
		}
	}
	err := r.d.Volumes.Load(ctx, ns, t.volFn, t.volume, &recordReader{next: workload.Records(ctx, src, path, t.file.Files[0], unseal)})
	if fault.KindOf(err) == fault.Conflict {
		return ReasonNotEmpty, err
	}
	if err != nil {
		return reasonFailed, err
	}
	return "", nil
}

// empty empties every target a load began, after a failure.
func (r *RestoreReconciler) empty(ctx context.Context, ns v1.NamespaceName, targets []*target) error {
	var errs []error
	for _, t := range targets {
		if t.written {
			errs = append(errs, r.emptyOne(ctx, ns, t))
		}
	}
	return errors.Join(errs...)
}

// emptyOne empties a target: a KVStore by DropPrefix, a SQLDatabase by its engine, a Bucket by BucketPurger.Purge, a
// lake prefix key by key.
func (r *RestoreReconciler) emptyOne(ctx context.Context, ns v1.NamespaceName, t *target) error {
	switch {
	case t.volume != "":
		if r.d.Volumes == nil {
			return nil
		}
		return r.d.Volumes.Clear(ctx, ns, t.volFn, t.volume)
	case t.sql != "":
		if r.d.SQL == nil {
			return nil
		}
		return r.d.SQL.Empty(ctx, ns, t.sql)
	case t.kv != "":
		if r.d.KV == nil {
			return nil
		}
		return r.d.KV.DropPrefix(t.kv)
	case t.bucket != "" && r.d.Purger != nil:
		return r.d.Purger.Purge(ctx, ns, t.bucket)
	}
	for {
		items, more, err := t.view.ListAfter(ctx, "", "", 1000)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := t.view.Delete(ctx, it.Key); err != nil && fault.KindOf(err) != fault.NotFound {
				return err
			}
		}
		if !more || len(items) == 0 {
			return nil
		}
	}
}

// failReason is a load's reason: EngineNotReady for a database whose engine is not Ready, else Failed.
func failReason(err error) string {
	if errors.Is(err, workload.ErrEngineNotReady) {
		return ReasonEngineNotReady
	}
	return reasonFailed
}

// recordReader reads the values of a file's records in order, as one stream: a database's SQLite file.
type recordReader struct {
	next func() (snapshot.Record, error)
	cur  []byte
}

func (r *recordReader) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		rec, err := r.next()
		if err != nil {
			return 0, err
		}
		r.cur = rec.Value
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}

// finish writes the end of the Restore: Succeeded, or Failed with reason and err's message.
func (r *RestoreReconciler) finish(ctx context.Context, rs *v1.Restore, at time.Time, err error, reason string) error {
	rs.Status.CompletionTime = ts(at)
	if err != nil {
		rs.Status.Phase = v1.RunFailed
		setReady(&rs.Status.Status, rs.Generation, false, reason, err.Error())
		r.log.WarnContext(ctx, "workload restore failed", "namespace", rs.Namespace, "restore", rs.Name, "reason", reason, "error", err)
	} else {
		rs.Status.Phase = v1.RunSucceeded
		setReady(&rs.Status.Status, rs.Generation, true, "", "")
		r.log.InfoContext(ctx, "workload restore succeeded", "namespace", rs.Namespace, "restore", rs.Name)
	}
	return r.write(ctx, rs)
}

// write writes rs's status onto the stored Restore.
func (r *RestoreReconciler) write(ctx context.Context, rs *v1.Restore) error {
	return writeStatus(ctx, r.d.Store, v1.KindRestore, rs.Namespace, rs.Name, rs.UID, func(cur v1.Object) {
		cur.(*v1.Restore).Status = rs.Status
	})
}

// volumeSuffix names a Function member's volume in a message.
func volumeSuffix(v v1.ObjectName) string {
	if v == "" {
		return ""
	}
	return " volume " + string(v)
}
