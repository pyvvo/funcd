package backup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/cron"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
