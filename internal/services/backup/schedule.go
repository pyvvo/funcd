package backup

import (
	"context"
	"reflect"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// ScheduleReconciler is the controller.Reconciler for KindBackupSchedule (Decisions 3, 9).
type ScheduleReconciler struct{ *base }

// Reconcile takes the newest due slot S of a schedule (Decision 3) and writes its status (Decision 9). S is skipped,
// lastScheduleTime kept, while the schedule is paused or its namespace held, so the missed slots collapse into one
// catch-up; while one of its Backups is Pending or Running, S creates none and is taken, so no run overlaps another.
// Else S creates Backup <schedule>-<S in Unix minutes>, a Conflict counting as done, and lastScheduleTime becomes S.
// A deleted schedule leaves its Backups (they outlive it). After a platform restore, until a sync of its target has
// recorded the copies made after the restored point, the schedule takes no slot (ADR-0287 Decision 2).
func (r *ScheduleReconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	const op = "backup.ScheduleReconcile"
	key := v1.ObjectRef{Kind: v1.KindBackupSchedule, Namespace: req.Namespace, Name: req.Name}
	obj, err := r.d.Store.Get(ctx, req.GVK, req.Namespace, req.Name)
	if fault.KindOf(err) == fault.NotFound {
		r.forget(key)
		return controller.Result{}, nil
	}
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get %s/%s", req.Namespace, req.Name)
	}
	s, ok := obj.(*v1.BackupSchedule)
	if !ok {
		return controller.Result{}, fault.Internalf(op, "%s/%s is not a BackupSchedule", req.Namespace, req.Name)
	}
	table, err := timetable(s.Spec)
	if err != nil {
		return controller.Result{}, err
	}
	backups, err := r.backupsOf(ctx, s)
	if err != nil {
		return controller.Result{}, err
	}
	now := r.d.Clock.Now()
	next := s.Status
	next.Conditions = slices.Clone(s.Status.Conditions) // Set writes in place: keep the stored status to compare
	pending := syncPending(s.Status)
	if pending {
		r.startSync(ctx, s, now)
	}
	last := time.Time(s.CreationTime)
	if t := s.Status.LastScheduleTime; t != nil {
		last = time.Time(*t)
	}
	if slot, due := newestDue(table, last, now); due && !pending {
		switch active := activeOf(backups); {
		case s.Spec.Paused, r.heldIn(s.Namespace):
		case active != nil:
			next.LastScheduleTime = ts(slot)
		default:
			b, err := r.create(ctx, s, slot)
			if err != nil {
				return controller.Result{}, err
			}
			if b != nil {
				backups = append(backups, b)
			}
			next.LastScheduleTime = ts(slot)
		}
	}
	r.observe(s, &next, table, backups, now)
	stored := s.Status.RPORisk
	if !reflect.DeepEqual(next, s.Status) {
		s.Status = next
		if _, err := r.d.Store.Update(ctx, s); err != nil {
			if fault.KindOf(err) == fault.Conflict {
				return controller.Result{Requeue: true}, nil
			}
			return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "write the status of %s/%s", s.Namespace, s.Name)
		}
	}
	r.alert(key, s, stored, next.RPORisk)
	r.warnSameProvider(key, s, readyReason(next.Status) == ReasonSameProvider)
	return controller.Result{RequeueAfter: r.wake(table, s, backups, now)}, nil
}

// MapBackup maps a Backup to its schedule, whose status follows its Backups.
func (r *ScheduleReconciler) MapBackup(_ context.Context, obj v1.Object) []controller.Request {
	b, ok := obj.(*v1.Backup)
	if !ok {
		return nil
	}
	return []controller.Request{{GVK: v1.KindBackupSchedule.GVK(), Namespace: b.Namespace, Name: b.Spec.Schedule}}
}

func timetable(s v1.BackupScheduleSpec) (*cron.Timetable, error) {
	loc, err := cron.LoadZone(s.TimeZone)
	if err != nil {
		return nil, err
	}
	return cron.Parse(s.Schedule, loc)
}

// newestDue is the newest slot after last that is not after now, and whether one is.
func newestDue(t *cron.Timetable, last, now time.Time) (time.Time, bool) {
	slot := t.Next(last)
	if slot.IsZero() || slot.After(now) {
		return time.Time{}, false
	}
	for n := t.Next(slot); !n.IsZero() && !n.After(now); n = t.Next(slot) {
		slot = n
	}
	return slot, true
}

// backupsOf lists the Backups of s, by name.
func (r *ScheduleReconciler) backupsOf(ctx context.Context, s *v1.BackupSchedule) ([]*v1.Backup, error) {
	l, err := r.d.Store.List(ctx, v1.KindBackup.GVK(), store.ListOptions{Namespace: s.Namespace})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "backup.ScheduleReconcile", "list the Backups of %q", s.Namespace)
	}
	var out []*v1.Backup
	for _, o := range l.Items {
		if b, ok := o.(*v1.Backup); ok && b.Spec.Schedule == s.Name {
			out = append(out, b)
		}
	}
	return out, nil
}

func activeOf(backups []*v1.Backup) *v1.Backup {
	for _, b := range backups {
		if !terminal(b.Status.Phase) {
			return b
		}
	}
	return nil
}

// create makes the Backup of slot; one already there is the slot's (nil).
func (r *ScheduleReconciler) create(ctx context.Context, s *v1.BackupSchedule, slot time.Time) (*v1.Backup, error) {
	obj, _ := v1.NewObject(v1.KindBackup)
	b := obj.(*v1.Backup)
	b.ObjectMeta = v1.ObjectMeta{Name: v1.BackupSlotName(s.Name, slot), Namespace: s.Namespace, ResourceGroup: s.ResourceGroup}
	b.Spec.Schedule = s.Name
	created, err := r.d.Store.Create(ctx, b)
	switch {
	case fault.KindOf(err) == fault.Conflict:
		return nil, nil
	case err != nil:
		return nil, fault.Wrapf(err, fault.KindOf(err), "backup.ScheduleReconcile", "create Backup %s/%s", b.Namespace, b.Name)
	}
	r.log.Info("workload backup scheduled", "namespace", s.Namespace, "schedule", s.Name, "backup", b.Name, "slot", slot)
	return created.(*v1.Backup), nil
}

// observe sets the status Decision 9 derives from the Backups: lastSuccessTime, earliestRestorePoint (the oldest
// stored Succeeded Backup's start), the active Backup, rpoRisk, and Ready, whose reason is SameProvider when the
// target shares the blob store's provider.
func (r *ScheduleReconciler) observe(s *v1.BackupSchedule, st *v1.BackupScheduleStatus, t *cron.Timetable, backups []*v1.Backup, now time.Time) {
	st.LastSuccessTime, st.EarliestRestorePoint, st.Active = nil, nil, ""
	var newest *v1.Backup
	for _, b := range backups {
		switch {
		case b.Status.Phase == v1.RunSucceeded && b.Status.StartTime != nil:
			if newest == nil || time.Time(*b.Status.StartTime).After(time.Time(*newest.Status.StartTime)) {
				newest = b
			}
			if st.EarliestRestorePoint == nil || time.Time(*b.Status.StartTime).Before(time.Time(*st.EarliestRestorePoint)) {
				st.EarliestRestorePoint = b.Status.StartTime
			}
		case !terminal(b.Status.Phase):
			st.Active = b.Name
		}
	}
	since := time.Time(s.CreationTime)
	if newest != nil {
		st.LastSuccessTime, since = newest.Status.CompletionTime, time.Time(*newest.Status.StartTime)
	}
	phases := make(map[v1.ObjectName]v1.RunPhase, len(backups))
	for _, b := range backups {
		phases[b.Name] = b.Status.Phase
	}
	st.RPORisk = RPORisk(t, s.Spec.Paused, since, now, func(slot time.Time) (v1.RunPhase, bool) {
		p, ok := phases[v1.BackupSlotName(s.Name, slot)]
		return p, ok
	})
	reason, message := "", ""
	if o, err := r.overlap(s.Spec.Target.URL); err == nil && o == gocloud.OverlapProvider {
		reason, message = ReasonSameProvider, "the target is on the blob store's provider: one provider failure takes both"
	}
	setReady(&st.Status, s.Generation, true, reason, message)
	st.Phase = v1.PhaseReady
}

func (r *ScheduleReconciler) overlap(target string) (gocloud.Overlap, error) {
	if r.d.StoreURL == "" {
		return gocloud.OverlapNone, nil
	}
	return gocloud.CompareDomains(r.d.StoreURL, target)
}

// RPORisk is Decision 9's rule: not paused, and the slot after since (the newest Succeeded Backup's start, else the
// schedule's creation) has passed with its Backup Failed or absent, or a second slot has passed. phase reads a slot's
// Backup.
func RPORisk(t *cron.Timetable, paused bool, since, now time.Time, phase func(slot time.Time) (v1.RunPhase, bool)) bool {
	if paused {
		return false
	}
	s1 := t.Next(since)
	if s1.IsZero() || s1.After(now) {
		return false
	}
	if s2 := t.Next(s1); !s2.IsZero() && !s2.After(now) {
		return true
	}
	p, ok := phase(s1)
	return !ok || p == v1.RunFailed
}

// alert logs rpoRisk's turning on (Warn) and clearing (Info), and counts the schedule in the gauge.
func (r *ScheduleReconciler) alert(key v1.ObjectRef, s *v1.BackupSchedule, stored, risk bool) {
	if was := r.setRisk(key, stored, risk); was == risk {
		return
	}
	if risk {
		r.log.Warn("workload backup rpo at risk", "namespace", s.Namespace, "schedule", s.Name)
		return
	}
	r.log.Info("workload backup rpo no longer at risk", "namespace", s.Namespace, "schedule", s.Name)
}

// setRisk records key's risk and returns the one before; for a schedule this process has not seen, the one before is
// its stored rpoRisk, so a restart does not warn again.
func (r *ScheduleReconciler) setRisk(key v1.ObjectRef, stored, risk bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	was, seen := r.risk[key]
	if !seen {
		was = stored
	}
	r.risk[key] = risk
	return was
}

// warnSameProvider logs Decision 6's Warn once per process while the schedule's target shares the blob store's
// provider, as cmd/funcd warns at each start about its own targets; a target moved off the provider and back warns
// again.
func (r *ScheduleReconciler) warnSameProvider(key v1.ObjectRef, s *v1.BackupSchedule, same bool) {
	r.mu.Lock()
	warned := r.warned[key]
	if same {
		r.warned[key] = true
	} else {
		delete(r.warned, key)
	}
	r.mu.Unlock()
	if same && !warned {
		r.log.Warn("workload backup target shares the blob store's provider (the same endpoint or device): one "+
			"failure takes both", "namespace", s.Namespace, "schedule", s.Name)
	}
}

// forget drops a deleted schedule from the gauge and the warned set.
func (r *ScheduleReconciler) forget(key v1.ObjectRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.risk, key)
	delete(r.warned, key)
	delete(r.synced, key)
}

// wake is when the schedule needs its next pass: its next slot, or the slot that would turn rpoRisk on, at the latest
// after Period, which also re-checks the namespace hold.
func (r *ScheduleReconciler) wake(t *cron.Timetable, s *v1.BackupSchedule, backups []*v1.Backup, now time.Time) time.Duration {
	at := []time.Time{now.Add(r.d.Period)}
	if n := t.Next(now); !n.IsZero() {
		at = append(at, n)
	}
	since := time.Time(s.CreationTime)
	for _, b := range backups {
		if b.Status.Phase == v1.RunSucceeded && b.Status.StartTime != nil && time.Time(*b.Status.StartTime).After(since) {
			since = time.Time(*b.Status.StartTime)
		}
	}
	if s1 := t.Next(since); s1.After(now) {
		at = append(at, s1)
	} else if s2 := t.Next(s1); s2.After(now) {
		at = append(at, s2)
	}
	return max(slices.MinFunc(at, func(a, b time.Time) int { return a.Compare(b) }).Sub(now), time.Millisecond)
}
