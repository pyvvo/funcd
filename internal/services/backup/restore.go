package backup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// RestoreReconciler is the controller.Reconciler for KindRestore (Decision 7).
type RestoreReconciler struct{ *base }

// planned is one member of a Restore: its file in the manifest, its destination, and whether its load began.
type planned struct {
	file    workload.MemberFile
	dest    workload.Destination
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

// load reads b with the Restore's credential and identity, checks every part, then loads each member through its
// kind's destination (Decision 7, ADR-0311 Decision 3): an empty one or, in place, the store emptied under a namespace
// hold that only a completed load lifts. A destination that is not empty is NotEmpty before any write; a failure
// clears what each load began. It returns the failure's reason.
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
	release, err := prepare(ctx, targets)
	if err != nil {
		return reasonOf(callPrepare, err), err
	}
	defer release()
	open := func(ctx context.Context, f backup.StoreFile) func() (snapshot.Record, error) {
		return workload.Records(ctx, src, b.Status.Path, f, unseal)
	}
	for _, t := range targets {
		t.written = true
		if err := t.dest.Load(ctx, t.file, open); err != nil {
			// a load that refused its destination as not empty wrote nothing there: what it holds is not the
			// Restore's to clear
			reason := reasonOf(callLoad, err)
			t.written = reason != ReasonNotEmpty
			return reason, errors.Join(err, clearBegun(ctx, targets))
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

// prepare calls each destination's Prepare once every member is planned, before the first load (Decision 3), such as
// a volume's stop of its Function's workers; a failed one releases the earlier ones.
func prepare(ctx context.Context, targets []*planned) (release func(), err error) {
	var releases []func()
	release = func() {
		for _, f := range releases {
			f()
		}
	}
	for _, t := range targets {
		f, err := t.dest.Prepare(ctx)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, f)
	}
	return release, nil
}

// plan resolves each member's file in m, its kind and its destination, and checks that a destination not in place is
// empty, all before any write: a missing member or destination is MemberNotFound, a kind no MemberKind serves Failed
// naming it, and a destination that is not empty NotEmpty.
func (r *RestoreReconciler) plan(ctx context.Context, rs *v1.Restore, m workload.Manifest) ([]*planned, string, error) {
	const op = "backup.Restore"
	var out []*planned
	for _, mb := range rs.Spec.Members {
		i := -1
		for j, mf := range m.Members {
			if mf.Kind == mb.Kind && mf.Name == mb.Name && mf.Volume == mb.Volume {
				i = j
			}
		}
		if i < 0 {
			return nil, ReasonMemberNotFound, fault.NotFoundf(op, "the Backup holds no %s/%s%s", mb.Kind, mb.Name, volumeSuffix(mb.Volume))
		}
		k, ok := r.kinds[mb.Kind]
		if !ok {
			return nil, reasonFailed, fault.Invalidf(op, "member %s/%s%s: no member kind serves %s", mb.Kind, mb.Name,
				volumeSuffix(mb.Volume), mb.Kind)
		}
		d, err := k.Destination(ctx, rs.Namespace, mb)
		if err != nil {
			return nil, reasonOf(callDestination, err), err
		}
		if !mb.InPlace {
			empty, err := d.IsEmpty(ctx)
			if err != nil {
				return nil, reasonOf(callIsEmpty, err), err
			}
			if !empty {
				return nil, ReasonNotEmpty, fault.Conflictf(op, "%s/%s is not empty: a Restore loads into an empty one", mb.Kind,
					cmp.Or(mb.As, mb.Name))
			}
		}
		out = append(out, &planned{file: m.Members[i], dest: d})
	}
	return out, "", nil
}

// clearBegun clears every destination whose load began, after a failure.
func clearBegun(ctx context.Context, targets []*planned) error {
	var errs []error
	for _, t := range targets {
		if t.written {
			errs = append(errs, t.dest.Clear(ctx))
		}
	}
	return errors.Join(errs...)
}

// call is a member call whose error reasonOf maps.
type call int

const (
	callDestination call = iota
	callIsEmpty
	callPrepare
	callLoad
)

// reasonOf is the reason of a member call's error (ADR-0311 Decision 5): a Destination's NotFound is MemberNotFound,
// IsEmpty's and Load's ErrEngineNotReady EngineNotReady, Load's ErrNotEmpty NotEmpty and its ErrTornObject Integrity,
// and any other Failed.
func reasonOf(c call, err error) string {
	switch {
	case c == callDestination && fault.KindOf(err) == fault.NotFound:
		return ReasonMemberNotFound
	case c == callLoad && errors.Is(err, workload.ErrNotEmpty):
		return ReasonNotEmpty
	case c == callLoad && errors.Is(err, workload.ErrTornObject):
		return ReasonIntegrity
	case (c == callIsEmpty || c == callLoad) && errors.Is(err, workload.ErrEngineNotReady):
		return ReasonEngineNotReady
	}
	return reasonFailed
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
