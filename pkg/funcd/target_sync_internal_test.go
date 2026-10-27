package funcd

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// The scenarios of ADR-0287: after a platform restore a schedule records the whole copies of its slots that its
// target holds after the restored lastScheduleTime, and takes no slot before.

// restoredSchedule is a minutely schedule as restore run leaves it (Decision 1): lastScheduleTime last, TargetSynced
// False with reason PlatformRestored. Its zone is UTC: in a fold of Europe/Paris a minutely schedule takes no slot for
// an hour (ADR-0211 Decision 3), and the backup clock starts at the real time.
func (r *wbRig) restoredSchedule(name string, last time.Time, mutate func(*v1.BackupScheduleSpec)) *v1.BackupSchedule {
	s := r.schedule(name, "* * * * *", func(s *v1.BackupScheduleSpec) {
		s.TimeZone = "UTC"
		if mutate != nil {
			mutate(s)
		}
	})
	s.Status.LastScheduleTime = ts(last)
	s.Status.Conditions.Set(v1.Condition{Type: v1.ConditionTargetSynced, Status: v1.ConditionFalse,
		Reason: v1.ReasonPlatformRestored})
	return s
}

func ts(t time.Time) *v1.Timestamp {
	out := v1.NewTimestamp(t)
	return &out
}

func newUIDHex(t *testing.T) string {
	t.Helper()
	var b [16]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return hex.EncodeToString(b[:])
}

// holdData holds orders as restore run does for a namespace with a BackupSchedule (ADR-0222 Decision 8).
func (r *wbRig) holdData() {
	r.t.Helper()
	require.NoError(r.t, r.hold.HoldNamespace("orders", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
}

// waitSynced waits for schedule name's TargetSynced to read status with reason.
func (r *wbRig) waitSynced(name v1.ObjectName, status v1.ConditionStatus, reason string) *v1.BackupSchedule {
	r.t.Helper()
	var s *v1.BackupSchedule
	require.Eventually(r.t, func() bool {
		s = r.getSchedule(name)
		c, ok := s.Status.Conditions.Get(v1.ConditionTargetSynced)
		return ok && c.Status == status && c.Reason == reason
	}, 15*time.Second, 10*time.Millisecond, "TargetSynced of %s reads %s %s\n%s", name, status, reason, r.logs.String())
	return s
}

// minutes is the backup clock's minute, then that many minutes before it.
func (r *wbRig) minutes(before ...int) (time.Time, []time.Time) {
	base := r.clk.Now().Truncate(time.Minute)
	out := make([]time.Time, len(before))
	for i, m := range before {
		out[i] = base.Add(-time.Duration(m) * time.Minute)
	}
	return base, out
}

// scenario: restore-finds-newer-backups — orders-sql (* * * * *) kept backing up on the lost host after the platform
// generation's cut: once restored, each newer whole copy is a Succeeded Backup with reason FoundOnTarget, TargetSynced
// is True, lastScheduleTime is the newest copy's slot, and a Restore naming the newest loads it, the namespace held.
//
// oracle: docs/design/backup/target-sync.fizz#NewerCopiesFound
func TestScenarioRestoreFindsNewerBackups(t *testing.T) {
	t.Parallel()
	var slots []time.Time
	var paths []string
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		_, slots = r.minutes(3, 2, 1)
		r.seed(r.restoredSchedule("orders-sql", slots[0], nil))
		for _, s := range slots[1:] {
			p, _ := r.copyOn(v1.BackupSlotName("orders-sql", s), newUIDHex(t))
			paths = append(paths, p)
		}
		r.holdData()
	}})
	for i, s := range slots[1:] {
		b := r.waitBackup(v1.BackupSlotName("orders-sql", s), v1.RunSucceeded)
		require.Equal(t, v1.ReasonFoundOnTarget, reason(b.Status.Status))
		require.Equal(t, paths[i], b.Status.Path)
		require.Equal(t, r.stub.URL(), b.Status.Target)
		require.True(t, time.Time(*b.Status.StartTime).Equal(s), "startTime is the slot")
		require.True(t, time.Time(*b.Status.ExpireTime).Equal(s.Add(time.Duration(v1.DefaultBackupTTL))), "expireTime is the slot plus the ttl")
		require.Len(t, b.Status.Members, 1)
		require.Equal(t, v1.ObjectName("orders-state"), b.Status.Members[0].Name)
		require.Positive(t, b.Status.Members[0].Bytes)
	}
	s := r.waitSynced("orders-sql", v1.ConditionTrue, "")
	require.True(t, time.Time(*s.Status.LastScheduleTime).Equal(slots[2]))
	require.Contains(t, r.logs.String(), "workload backup found on target")

	newest := v1.BackupSlotName("orders-sql", slots[2])
	r.create(r.dev, r.restoreObj("rs", string(newest), v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", As: "orders-state-r"}))
	r.waitRestore("rs", v1.RunSucceeded)
	require.Equal(t, map[string]string{"t/1": "one"}, r.kv("orders-state-r"))
}

// scenario: incomplete-copy-not-recorded — a newer directory without manifest.yaml (the host died mid-copy) gets no
// record, and lastScheduleTime is the newest whole copy's slot.
//
// oracle: docs/design/backup/target-sync.fizz#NoRecordWithoutCopy
func TestScenarioIncompleteCopyNotRecorded(t *testing.T) {
	t.Parallel()
	var slots []time.Time
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		_, slots = r.minutes(3, 2, 1)
		r.seed(r.restoredSchedule("orders-sql", slots[0], nil))
		r.copyOn(v1.BackupSlotName("orders-sql", slots[1]), newUIDHex(t))
		p, _ := r.copyOn(v1.BackupSlotName("orders-sql", slots[2]), newUIDHex(t))
		require.NoError(t, r.target().Delete(r.ctx, p+"manifest.yaml"))
		r.holdData()
	}})
	s := r.waitSynced("orders-sql", v1.ConditionTrue, "")
	require.True(t, time.Time(*s.Status.LastScheduleTime).Equal(slots[1]))
	require.Equal(t, []string{string(v1.BackupSlotName("orders-sql", slots[1]))}, r.backups())
}

// scenario: gone-backup-stays-gone — a record deleted before the cut, one deleted after the sync and an expired newer
// copy have no record, also after a second sync and once the slots resume.
//
// oracle: docs/design/backup/target-sync.fizz#GoneStaysGone
func TestScenarioGoneBackupStaysGone(t *testing.T) {
	t.Parallel()
	var base time.Time
	var slots []time.Time
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		base, slots = r.minutes(12, 10, 5, 1)
		r.seed(r.restoredSchedule("orders-sql", slots[1], func(s *v1.BackupScheduleSpec) { s.TTL = v1.Duration(2 * time.Minute) }))
		for _, s := range []time.Time{slots[0], slots[2], slots[3]} {
			r.copyOn(v1.BackupSlotName("orders-sql", s), newUIDHex(t))
		}
		r.holdData()
	}})
	fresh := v1.BackupSlotName("orders-sql", slots[3])
	c, _ := r.waitSynced("orders-sql", v1.ConditionTrue, "").Status.Conditions.Get(v1.ConditionTargetSynced)
	require.Equal(t, "found 1 Backups, newest "+string(fresh), c.Message, "the expired copy is not recorded, even briefly")
	require.Equal(t, []string{string(fresh)}, r.backups(), "the older and the expired copies get no record")

	require.NoError(t, r.dev.Delete(r.ctx, v1.KindBackup, "orders", fresh))
	require.Eventually(t, func() bool {
		s := r.getSchedule("orders-sql")
		s.Status.Conditions.Set(v1.Condition{Type: v1.ConditionTargetSynced, Status: v1.ConditionFalse, Reason: v1.ReasonPlatformRestored})
		_, err := r.st.Update(r.ctx, s)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "mark the schedule again")
	r.clk.Advance(wbPeriod)
	r.waitSynced("orders-sql", v1.ConditionTrue, "")
	require.Empty(t, r.backups(), "a second sync brings no deleted record back")

	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	next := v1.BackupSlotName("orders-sql", base)
	r.waitBackup(next, v1.RunSucceeded)
	require.Equal(t, []string{string(next)}, r.backups(), "the slots resume after the newest copy's")
}

// scenario: only-own-slots — a target shared by nightly and nightly-1 that also holds a Backup by hand and an export:
// nightly's sync records its own slot copies only.
func TestScenarioOnlyOwnSlots(t *testing.T) {
	t.Parallel()
	var slots []time.Time
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		_, slots = r.minutes(3, 1)
		r.seed(r.restoredSchedule("nightly", slots[0], nil), r.schedule("nightly-1", "0 3 * * *", nil))
		for _, name := range []v1.ObjectName{v1.BackupSlotName("nightly", slots[1]), v1.BackupSlotName("nightly-1", slots[1]),
			"nightly-by-hand", v1.RestoreExportName("nightly-rewind")} {
			r.copyOn(name, newUIDHex(t))
		}
		r.holdData()
	}})
	r.waitSynced("nightly", v1.ConditionTrue, "")
	require.Equal(t, []string{string(v1.BackupSlotName("nightly", slots[1]))}, r.backups())
}

// scenario: unreachable-target-retried — while the target refuses the listing, TargetSynced is False, ListFailed, and
// the schedule takes no slot, even with its namespace released; once the target answers, the next sync records the copy
// and the slots resume with the slot due meanwhile (one catch-up).
//
// oracle: docs/design/backup/target-sync.fizz#SlotsAfterSync
func TestScenarioUnreachableTargetRetried(t *testing.T) {
	t.Parallel()
	var base time.Time
	var slots []time.Time
	var failing atomic.Bool
	failing.Store(true)
	r := newWBRig(t, wbOptions{setup: func(r *wbRig) {
		base, slots = r.minutes(3, 1)
		r.seed(r.restoredSchedule("orders-sql", slots[0], nil))
		r.copyOn(v1.BackupSlotName("orders-sql", slots[1]), newUIDHex(t))
		r.stub.Set(func(s *s3stub.Stub) { s.FailList = func(string) bool { return failing.Load() } })
		r.holdData()
	}})
	r.waitSynced("orders-sql", v1.ConditionFalse, v1.ReasonListFailed)
	require.NoError(t, r.admin.ReleaseNamespaceHold(r.ctx, []string{"orders"}))
	r.advanceTo(base.Add(time.Minute))
	r.still(func() bool { return len(r.backups()) == 0 }, "no slot while the sync is pending")

	failing.Store(false)
	r.clk.Advance(wbPeriod)
	s := r.waitSynced("orders-sql", v1.ConditionTrue, "")
	require.False(t, time.Time(*s.Status.LastScheduleTime).Before(slots[1]), "the slot logic resumes after the copy's slot")
	r.waitBackup(v1.BackupSlotName("orders-sql", slots[1]), v1.RunSucceeded)
	r.waitBackup(v1.BackupSlotName("orders-sql", base.Add(time.Minute)), v1.RunSucceeded)
}

// scenario: finished-run-kept — a Backup Running when funcd stopped whose manifest is on the target is Succeeded,
// FoundOnTarget, its startTime and expireTime kept, also when the target answers only after the first listing; a
// Pending one whose copy is whole is recorded without a new copy, its startTime its creation.
//
// oracle: docs/design/backup/target-sync.fizz#FinishedRunKept
func TestScenarioFinishedRunKept(t *testing.T) {
	t.Parallel()
	finishedRunKept(t, time.Time{})
}

// Issue #1303: the scenario dated its Backups from a fixed day, so once the real time passed their expireTime, the
// reconciler deleted queued as soon as the sync recorded it. Run on a backup clock years later, it must still hold.
func TestIssue1303_FinishedRunKeptOnAnyDate(t *testing.T) {
	t.Parallel()
	finishedRunKept(t, time.Date(2031, 3, 1, 12, 0, 0, 0, time.UTC))
}

// finishedRunKept runs the scenario on a backup clock that starts at now, the real time when zero.
func finishedRunKept(t *testing.T, now time.Time) {
	var killed, queued *v1.Backup
	var failing atomic.Bool
	var killedLists atomic.Int32
	failing.Store(true)
	var start time.Time
	r := newWBRig(t, wbOptions{now: now, setup: func(r *wbRig) {
		_, slots := r.minutes(3)
		start = slots[0] // on the backup clock, so the Backups' expireTime stays ahead of it (#1303)
		r.seed(r.restoredSchedule("orders-sql", slots[0], nil))
		for _, name := range []string{"killed", "queued"} {
			created, err := r.st.Create(r.ctx, r.backupObj(name, "orders-sql"))
			require.NoError(t, err)
			b := created.(*v1.Backup)
			r.copyOn(b.Name, string(b.UID))
			if name == "killed" {
				b.Status = v1.BackupStatus{Phase: v1.RunRunning, StartTime: ts(start), ExpireTime: ts(start.Add(time.Hour))}
				killed = b
			} else {
				b.Status = v1.BackupStatus{Phase: v1.RunPending, ExpireTime: ts(start.Add(time.Hour))}
				queued = b
			}
			_, err = r.st.Update(r.ctx, b)
			require.NoError(t, err)
		}
		r.stub.Set(func(s *s3stub.Stub) {
			s.FailList = func(prefix string) bool {
				if strings.Contains(prefix, "/killed-") {
					killedLists.Add(1)
				}
				return failing.Load() && (strings.Contains(prefix, "/killed-") || strings.Contains(prefix, "/orders-sql-"))
			}
		})
	}})
	q := r.waitBackup(queued.Name, v1.RunSucceeded)
	require.Equal(t, v1.ReasonFoundOnTarget, reason(q.Status.Status))
	require.Equal(t, queued.CreationTime, *q.Status.StartTime)
	require.True(t, time.Time(*q.Status.ExpireTime).Equal(start.Add(time.Hour)))
	first := killedLists.Load()
	require.Eventually(t, func() bool { return killedLists.Load() > first+1 }, 15*time.Second, 10*time.Millisecond,
		"a second listing of the killed run's directory, after the first one failed")
	b, _ := r.getBackup(killed.Name)
	require.Equal(t, v1.RunRunning, b.Status.Phase, "a failed listing while the sync is pending keeps the record Running")

	failing.Store(false)
	b = r.waitBackup(killed.Name, v1.RunSucceeded)
	require.Equal(t, v1.ReasonFoundOnTarget, reason(b.Status.Status))
	require.True(t, time.Time(*b.Status.StartTime).Equal(start))
	require.True(t, time.Time(*b.Status.ExpireTime).Equal(start.Add(time.Hour)))
	require.Equal(t, "orders/killed-"+string(killed.UID)+"/", b.Status.Path)
	require.NotNil(t, b.Status.CompletionTime)
}
