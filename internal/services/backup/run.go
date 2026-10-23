package backup

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/controller"
)

// BackupReconciler is the controller.Reconciler for KindBackup (Decision 4).
type BackupReconciler struct{ *base }

// Reconcile drives a Backup Pending → Running → Succeeded | Failed, one attempt. A new Backup gets its expireTime
// (creation + its ttl or its schedule's); a Pending one waits while the platform is held, but for a Restore's export;
// a Running one this process does not run was interrupted (Interrupted); a finished one is deleted at its expireTime.
// The run itself, with the platform's rights, is a goroutine whose end writes the record.
func (r *BackupReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "backup.BackupReconcile"
	obj, err := r.d.Store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get %s/%s", req.Namespace, req.Name)
	}
	b, ok := obj.(*v1.Backup)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "%s/%s is not a Backup", req.Namespace, req.Name)
	}
	now := r.d.Clock.Now()
	switch b.Status.Phase {
	case v1.RunSucceeded, v1.RunFailed:
		return r.expire(ctx, b, now)
	case v1.RunRunning:
		if r.isActive(refOf(b)) {
			return controller.Result{}, nil
		}
		return controller.Result{}, r.finish(ctx, b, now, errors.New("funcd stopped during the run"), ReasonInterrupted)
	case "":
		ttl, err := r.ttl(ctx, b)
		if err != nil {
			return controller.Result{}, err
		}
		b.Status.Phase, b.Status.ExpireTime = v1.RunPending, ts(time.Time(b.CreationTime).Add(time.Duration(ttl)))
		return controller.Result{}, r.write(ctx, b)
	}
	if r.held() && !r.isExport(ctx, b) {
		return controller.Result{RequeueAfter: r.d.Period}, nil
	}
	if !r.claim(refOf(b)) {
		return controller.Result{}, nil
	}
	b.Status.Phase, b.Status.StartTime = v1.RunRunning, ts(now)
	if err := r.write(ctx, b); err != nil {
		r.release(refOf(b))
		return controller.Result{}, err
	}
	r.spawn(func() { r.run(ctx, b, now) })
	return controller.Result{}, nil
}

// ttl is the Backup's spec.ttl, else its schedule's, else DefaultBackupTTL without the schedule.
func (r *BackupReconciler) ttl(ctx context.Context, b *v1.Backup) (v1.Duration, error) {
	if b.Spec.TTL > 0 {
		return b.Spec.TTL, nil
	}
	s, err := r.schedule(ctx, b.Namespace, b.Spec.Schedule)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return v1.DefaultBackupTTL, nil
	case err != nil:
		return 0, err
	}
	return s.Spec.EffectiveTTL(), nil
}

func (r *BackupReconciler) schedule(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.BackupSchedule, error) {
	obj, err := r.d.Store.Get(ctx, v1.KindBackupSchedule.GVK(), ns, name)
	if err != nil {
		return nil, err
	}
	return obj.(*v1.BackupSchedule), nil
}

// isExport reports a Restore's export, <restore>-before named by its status.export (Decision 7).
func (r *BackupReconciler) isExport(ctx context.Context, b *v1.Backup) bool {
	name, ok := strings.CutSuffix(string(b.Name), "-before")
	if !ok {
		return false
	}
	obj, err := r.d.Store.Get(ctx, v1.KindRestore.GVK(), b.Namespace, v1.ObjectName(name))
	rs, isRestore := obj.(*v1.Restore)
	return err == nil && isRestore && rs.Status.Export == b.Name
}

// run copies the schedule's members (Decisions 4 to 6) and records the end, unless funcd's shutdown ended it: that
// Backup stays Running, and the next start fails it Interrupted.
func (r *BackupReconciler) run(ctx context.Context, b *v1.Backup, start time.Time) {
	defer r.release(refOf(b))
	reason, err := r.copy(ctx, b)
	if ctx.Err() != nil {
		return
	}
	r.count(ctx, r.d.Clock.Now().Sub(start), err)
	if err := r.finish(ctx, b, r.d.Clock.Now(), err, reason); err != nil {
		r.log.WarnContext(ctx, "workload backup: record the end", "namespace", b.Namespace, "backup", b.Name, "error", err)
	}
}

// copy resolves the schedule, its credential, members and recipients, then writes under
// <target>/<ns>/<backup>-<uid>/; it returns the failure's reason.
func (r *BackupReconciler) copy(ctx context.Context, b *v1.Backup) (string, error) {
	s, err := r.schedule(ctx, b.Namespace, b.Spec.Schedule)
	if fault.KindOf(err) == fault.NotFound {
		return ReasonScheduleNotFound, fault.Wrapf(err, fault.NotFound, "backup.run", "schedule %q", b.Spec.Schedule)
	}
	if err != nil {
		return reasonFailed, err
	}
	key, err := r.staticKey(ctx, b.Namespace, s.Spec.Target.CredentialsSecret)
	if err != nil {
		return reasonFailed, err
	}
	members, err := workload.Resolve(ctx, r.d.Store, b.Namespace, s.Spec.Scope)
	if fault.KindOf(err) == fault.NotFound {
		return ReasonMemberNotFound, err
	}
	if err != nil {
		return reasonFailed, err
	}
	sealer, err := envelope.Parse(s.Spec.Encryption.Recipients)
	if err != nil {
		return reasonFailed, err
	}
	target, err := openTarget(ctx, s.Spec.Target.URL, key)
	if err != nil {
		return reasonFailed, err
	}
	defer func() { _ = target.Close() }()
	path := string(b.Namespace) + "/" + string(b.Name) + "-" + string(b.UID) + "/"
	b.Status.Target, b.Status.Path, b.Status.Recipients = s.Spec.Target.URL, path, sealer.Keys().Recipients
	m, err := workload.Write(ctx, b.Namespace, members, r.d.Sources, workload.Options{
		Target: target, Path: path, Seal: sealer.Seal(), Recipients: sealer.Keys().Recipients,
	})
	switch {
	case errors.Is(err, workload.ErrTargetUnconditional):
		return ReasonTargetUnconditional, err
	case errors.Is(err, workload.ErrLakeUnstable):
		return ReasonLakeUnstable, err
	case errors.Is(err, workload.ErrEngineNotReady):
		return ReasonEngineNotReady, err
	case err != nil:
		return reasonFailed, err
	}
	b.Status.Members = nil
	for _, mf := range m.Members {
		bm := v1.BackupMember{Kind: mf.Kind, Name: mf.Name, Volume: mf.Volume}
		for _, f := range mf.Files {
			bm.Bytes += f.Bytes
		}
		b.Status.Members = append(b.Status.Members, bm)
	}
	return "", nil
}

// finish writes the end of b's run: Succeeded, or Failed with reason and err's message.
func (r *BackupReconciler) finish(ctx context.Context, b *v1.Backup, at time.Time, err error, reason string) error {
	b.Status.CompletionTime = ts(at)
	if err != nil {
		b.Status.Phase = v1.RunFailed
		setReady(&b.Status.Status, b.Generation, false, reason, err.Error())
		r.log.WarnContext(ctx, "workload backup failed", "namespace", b.Namespace, "backup", b.Name, "reason", reason, "error", err)
	} else {
		b.Status.Phase = v1.RunSucceeded
		setReady(&b.Status.Status, b.Generation, true, "", "")
	}
	return r.write(ctx, b)
}

// write writes b's status onto the stored Backup.
func (r *BackupReconciler) write(ctx context.Context, b *v1.Backup) error {
	return writeStatus(ctx, r.d.Store, v1.KindBackup, b.Namespace, b.Name, b.UID, func(cur v1.Object) {
		cur.(*v1.Backup).Status = b.Status
	})
}

// expire deletes a finished Backup at its expireTime, its record only: the objects go by the owner's lifecycle rule.
func (r *BackupReconciler) expire(ctx context.Context, b *v1.Backup, now time.Time) (controller.Result, error) {
	exp := b.Status.ExpireTime
	if exp == nil {
		return controller.Result{}, nil
	}
	if wait := time.Time(*exp).Sub(now); wait > 0 {
		return controller.Result{RequeueAfter: min(wait, r.d.Period)}, nil
	}
	switch err := r.d.Store.Delete(ctx, v1.KindBackup.GVK(), b.Namespace, b.Name, b.ResourceVersion); fault.KindOf(err) {
	case fault.Conflict:
		return controller.Result{Requeue: true}, nil
	case fault.NotFound:
		return controller.Result{}, nil
	default:
		if err != nil {
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), "backup.BackupReconcile", "delete expired %s/%s", b.Namespace, b.Name)
		}
	}
	r.log.InfoContext(ctx, "workload backup expired", "namespace", b.Namespace, "backup", b.Name)
	return controller.Result{}, nil
}
