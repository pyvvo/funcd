package backup

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/cron"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// TestRPORiskRule (Decision 9): a daily 03:00 schedule whose newest success started Monday 03:00 is at risk once
// Tuesday 03:00 has passed with its Backup Failed or absent, not while it is Running, and always once Wednesday 03:00
// has passed; never while paused.
func TestRPORiskRule(t *testing.T) {
	t.Parallel()
	paris, err := cron.LoadZone("Europe/Paris")
	require.NoError(t, err)
	table, err := cron.Parse("0 3 * * *", paris)
	require.NoError(t, err)
	monday := time.Date(2026, 10, 5, 3, 0, 0, 0, paris)
	tuesday := monday.AddDate(0, 0, 1)
	slot := func(p v1.RunPhase, ok bool) func(time.Time) (v1.RunPhase, bool) {
		return func(s time.Time) (v1.RunPhase, bool) {
			require.True(t, s.Equal(tuesday), "the slot after the newest success is read, not %s", s)
			return p, ok
		}
	}
	for name, c := range map[string]struct {
		now    time.Time
		paused bool
		phase  func(time.Time) (v1.RunPhase, bool)
		want   bool
	}{
		"before the next slot":  {tuesday.Add(-time.Minute), false, slot("", false), false},
		"next slot absent":      {tuesday.Add(time.Minute), false, slot("", false), true},
		"next slot failed":      {tuesday.Add(time.Minute), false, slot(v1.RunFailed, true), true},
		"next slot running":     {tuesday.Add(time.Minute), false, slot(v1.RunRunning, true), false},
		"second slot passed":    {tuesday.AddDate(0, 0, 1).Add(time.Minute), false, func(time.Time) (v1.RunPhase, bool) { return v1.RunRunning, true }, true},
		"paused":                {tuesday.AddDate(0, 0, 2), true, slot("", false), false},
		"next slot not yet run": {tuesday, false, slot(v1.RunPending, true), false},
	} {
		require.Equal(t, c.want, RPORisk(table, c.paused, monday, c.now, c.phase), name)
	}
}

// newestDue takes the newest due slot: three missed slots collapse into one.
func TestNewestDueCollapses(t *testing.T) {
	t.Parallel()
	table, err := cron.Parse("0 * * * *", time.UTC)
	require.NoError(t, err)
	last := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	got, due := newestDue(table, last, last.Add(3*time.Hour+30*time.Minute))
	require.True(t, due)
	require.Equal(t, last.Add(3*time.Hour), got)
	_, due = newestDue(table, last, last.Add(59*time.Minute))
	require.False(t, due)
}

// noSecrets resolves no Secret: a schedule's pass reads none.
type noSecrets struct{}

func (noSecrets) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return nil, nil
}

// A deleted schedule leaves the set of schedules warned about as on the blob store's provider (Decision 6), so one
// re-created under its name warns again; a pass that keeps the target does not.
func TestSameProviderWarnForgetsADeletedSchedule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const endpoint = "region=us-east-1&endpoint=https://s3.example.com"
	st := store.New(memory.New())
	var logs bytes.Buffer
	rs, err := New(Deps{Store: st, Secrets: noSecrets{}, StoreURL: "s3://store?" + endpoint,
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	require.NoError(t, err)
	req := controller.Request{GVK: v1.KindBackupSchedule.GVK(), Namespace: "orders", Name: "beside"}
	create := func() {
		_, err := st.Create(ctx, &v1.BackupSchedule{
			TypeMeta:   v1.TypeMeta{APIVersion: req.GVK.APIVersion(), Kind: v1.KindBackupSchedule},
			ObjectMeta: v1.ObjectMeta{Namespace: req.Namespace, ResourceGroup: "rg1", Name: req.Name},
			Spec: v1.BackupScheduleSpec{Schedule: "0 3 * * *", Scope: v1.BackupScope{KVStores: []v1.ObjectName{"orders-state"}},
				Target:     v1.BackupTarget{URL: "s3://backups?" + endpoint, CredentialsSecret: "backup-writer"},
				Encryption: v1.BackupEncryption{Recipients: []string{"age1owner", "age1recovery"}}},
		})
		require.NoError(t, err)
	}
	pass := func() {
		_, err := rs.Schedules.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	warns := func() int {
		return strings.Count(logs.String(), "workload backup target shares the blob store's provider")
	}

	create()
	pass()
	pass()
	require.Equal(t, 1, warns(), "a pass that keeps the target warns no more")
	require.NoError(t, st.Delete(ctx, req.GVK, req.Namespace, req.Name, ""))
	pass()
	create()
	pass()
	require.Equal(t, 2, warns(), "the schedule re-created under the name warns again")
}
