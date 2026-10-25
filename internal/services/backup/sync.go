package backup

import (
	"context"
	"fmt"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob"
)

// syncPending reports a schedule whose TargetSynced is False: restore run marked it, or its last sync's listing
// failed. It syncs, and takes no slot, until a sync succeeds (ADR-0287 Decision 2).
func syncPending(st v1.BackupScheduleStatus) bool {
	c, ok := st.Conditions.Get(v1.ConditionTargetSynced)
	return ok && c.Status == v1.ConditionFalse
}

// startSync runs one sync of s in the background, at most one per schedule at a time and one per Period after the
// last, since its listing may wait on the target; its status write enqueues the schedule's next pass.
func (r *ScheduleReconciler) startSync(ctx context.Context, s *v1.BackupSchedule, now time.Time) {
	key := v1.ObjectRef{Kind: v1.KindBackupSchedule, Namespace: s.Namespace, Name: s.Name}
	r.mu.Lock()
	tried, ok := r.synced[key]
	r.mu.Unlock()
	if ok && now.Sub(tried) < r.d.Period {
		return
	}
	if !r.claim(key) {
		return
	}
	r.mu.Lock()
	r.synced[key] = now
	r.mu.Unlock()
	snap := *s // the pass goes on writing s's status
	r.spawn(func() {
		defer r.release(key)
		r.sync(ctx, &snap)
	})
}

// sync records the whole copies of s's slots after its lastScheduleTime L that its target holds (ADR-0287 Decisions 3,
// 4): a Succeeded Backup each, oldest first, a name already stored left as it is, an expired copy left out. Then L
// becomes the newest whole copy's slot and TargetSynced True, in one status write. A failed listing turns TargetSynced
// False, ListFailed; a failed create leaves the status as it was, so the next sync lists the same copies again.
func (r *ScheduleReconciler) sync(ctx context.Context, s *v1.BackupSchedule) {
	last := time.Time(s.CreationTime)
	if t := s.Status.LastScheduleTime; t != nil {
		last = time.Time(*t)
	}
	var copies []workload.Copy
	err := r.onTarget(ctx, s, func(target blob.Bucket) (err error) {
		copies, err = workload.SlotCopies(ctx, target, s.Namespace, s.Name, last)
		return err
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.writeSync(ctx, s, last, v1.ConditionFalse, v1.ReasonListFailed, err.Error())
		return
	}
	now, ttl := r.d.Clock.Now(), time.Duration(s.Spec.EffectiveTTL())
	newest, found := last, 0
	var newestName v1.ObjectName
	for _, c := range copies {
		if !c.Whole {
			continue
		}
		slot, _ := v1.ParseBackupSlotName(s.Name, c.Name)
		if slot.After(newest) {
			newest, newestName = slot, c.Name
		}
		exp := slot.Add(ttl)
		if !exp.After(now) {
			continue
		}
		obj, _ := v1.NewObject(v1.KindBackup)
		b := obj.(*v1.Backup)
		b.ObjectMeta = v1.ObjectMeta{Name: c.Name, Namespace: s.Namespace, ResourceGroup: s.ResourceGroup}
		b.Spec.Schedule = s.Name
		b.Status.StartTime, b.Status.ExpireTime = ts(slot), ts(exp)
		foundOnTarget(&b.Status, 1, s.Spec.Target.URL, c)
		switch _, err := r.d.Store.Create(ctx, b); {
		case fault.KindOf(err) == fault.Conflict:
			continue
		case err != nil:
			r.log.WarnContext(ctx, "workload backup sync: record a copy", "namespace", s.Namespace, "schedule", s.Name,
				"backup", c.Name, "error", err)
			return
		}
		found++
		r.log.InfoContext(ctx, "workload backup found on target", "namespace", s.Namespace, "schedule", s.Name, "backup", c.Name)
	}
	message := "found no copy after the restored point"
	if newestName != "" {
		message = fmt.Sprintf("found %d Backups, newest %s", found, newestName)
	}
	r.writeSync(ctx, s, newest, v1.ConditionTrue, "", message)
}

// onTarget runs f on s's target, opened with the schedule's credential (put and list).
func (b *base) onTarget(ctx context.Context, s *v1.BackupSchedule, f func(blob.Bucket) error) error {
	key, err := b.staticKey(ctx, s.Namespace, s.Spec.Target.CredentialsSecret)
	if err != nil {
		return err
	}
	target, err := openTarget(ctx, s.Spec.Target.URL, key)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	return f(target)
}

// writeSync writes TargetSynced and, when later, lastScheduleTime last onto the stored schedule; turning False logs
// a Warn.
func (r *ScheduleReconciler) writeSync(ctx context.Context, s *v1.BackupSchedule, last time.Time, status v1.ConditionStatus,
	reason, message string) {
	was, _ := s.Status.Conditions.Get(v1.ConditionTargetSynced)
	err := writeStatus(ctx, r.d.Store, v1.KindBackupSchedule, s.Namespace, s.Name, s.UID, func(cur v1.Object) {
		cs := cur.(*v1.BackupSchedule)
		if t := cs.Status.LastScheduleTime; t == nil || last.After(time.Time(*t)) {
			cs.Status.LastScheduleTime = ts(last)
		}
		cs.Status.Conditions.Set(v1.Condition{Type: v1.ConditionTargetSynced, Status: status, ObservedGeneration: cs.Generation,
			Reason: reason, Message: message})
	})
	if err != nil {
		r.log.WarnContext(ctx, "workload backup sync: write the schedule's status", "namespace", s.Namespace,
			"schedule", s.Name, "error", err)
		return
	}
	if status == v1.ConditionFalse && was.Reason != reason {
		r.log.WarnContext(ctx, "workload backup target not listed", "namespace", s.Namespace, "schedule", s.Name,
			"error", message)
	}
}

// recovery is what a listing of a Backup's own directory found (ADR-0287 Decision 5).
type recovery int

const (
	// notRecovered: no whole copy, or no schedule or credential to look with; the record goes on as before.
	notRecovered recovery = iota
	// recovered: the copy is whole, and the record is Succeeded.
	recovered
	// unlisted: the listing failed while the schedule's sync is pending; the next pass looks again.
	unlisted
)

// recoverCopy lists b's own directory on its schedule's target: with its manifest there, b becomes Succeeded, reason
// FoundOnTarget, its startTime start (its creation time when nil) and its expireTime kept.
func (r *BackupReconciler) recoverCopy(ctx context.Context, b *v1.Backup, start *v1.Timestamp) (recovery, error) {
	s, err := r.schedule(ctx, b.Namespace, b.Spec.Schedule)
	if err != nil {
		return notRecovered, nil
	}
	var c workload.Copy
	err = r.onTarget(ctx, s, func(target blob.Bucket) (err error) {
		c, err = workload.ListCopy(ctx, target, copyPath(b))
		return err
	})
	switch {
	case err != nil && syncPending(s.Status):
		return unlisted, nil
	case err != nil || !c.Whole:
		return notRecovered, nil
	}
	if start == nil {
		start = &b.CreationTime
	}
	b.Status.StartTime = start
	foundOnTarget(&b.Status, b.Generation, s.Spec.Target.URL, c)
	if err := r.write(ctx, b); err != nil {
		return notRecovered, err
	}
	r.log.InfoContext(ctx, "workload backup found on target", "namespace", b.Namespace, "schedule", s.Name, "backup", b.Name)
	return recovered, nil
}

// copyPath is b's directory on its target, <ns>/<backup>-<uid>/ (ADR-0222 Decision 4).
func copyPath(b *v1.Backup) string {
	return string(b.Namespace) + "/" + string(b.Name) + "-" + string(b.UID) + "/"
}

// foundOnTarget records c, a whole copy on url, in st (ADR-0287 Decisions 4, 5).
func foundOnTarget(st *v1.BackupStatus, gen int64, url string, c workload.Copy) {
	st.Phase, st.Target, st.Path, st.Members = v1.RunSucceeded, url, c.Path, c.Members
	st.CompletionTime = ts(c.Completed)
	setReady(&st.Status, gen, true, v1.ReasonFoundOnTarget, "its copy is whole on the target")
}
