package eventing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// timerHarness drives EventSource team-a/clock through daemon lives over one store that survives every restart
// (ADR-0182). fires holds each event's fire times as offsets from t0, the stored creationTimestamp.
type timerHarness struct {
	t     *testing.T
	st    store.Store
	t0    time.Time
	fires map[v1.ObjectName][]time.Duration
}

// specEdit changes the stored timer spec at offset at of a life, before that minute's tick.
type specEdit struct {
	at    time.Duration
	apply func(*v1.TimerSource)
}

// newTimerHarness creates team-a/clock with its timer event daily at 24h.
func newTimerHarness(t *testing.T) *timerHarness {
	t.Helper()
	st := store.New(memory.New())
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = "clock", "team-a", "rg1"
	es.Spec.Timer = &v1.TimerSource{Events: []v1.TimerEvent{{Name: "daily", Interval: v1.Duration(24 * time.Hour)}}}
	_, err := st.Create(t.Context(), es)
	require.NoError(t, err)
	got, err := st.Get(t.Context(), v1.KindEventSource.GVK(), "team-a", "clock")
	require.NoError(t, err)
	t0 := got.(*v1.EventSource).CreationTime
	require.False(t, t0.IsZero())
	return &timerHarness{t: t, st: st, t0: t0, fires: map[v1.ObjectName][]time.Duration{}}
}

// live runs one daemon life over [t0+from, t0+to): a new Source on a manual clock, one Reconcile at boot, then a
// dueTimers tick every minute, each due event published through Fire.
func (h *timerHarness) live(from, to time.Duration, edits ...specEdit) {
	h.t.Helper()
	ctx := h.t.Context()
	req := controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "team-a", Name: "clock"}
	clk := clock.NewManual(h.t0.Add(from))
	src, err := NewSource(Deps{Store: h.st, Publisher: &capturePub{}, Clock: clk})
	require.NoError(h.t, err)
	_, err = src.Reconcile(ctx, req)
	require.NoError(h.t, err)
	for now := clk.Now(); now.Before(h.t0.Add(to)); now = clk.Now() {
		for _, e := range edits {
			if now.Equal(h.t0.Add(e.at)) {
				h.edit(e.apply)
				_, err = src.Reconcile(ctx, req)
				require.NoError(h.t, err)
			}
		}
		for _, k := range src.dueTimers(now) {
			require.NoError(h.t, src.Fire(ctx, k.ns, k.source, k.event))
			h.fires[k.event] = append(h.fires[k.event], now.Sub(h.t0))
		}
		clk.Advance(time.Minute)
	}
}

func (h *timerHarness) edit(apply func(*v1.TimerSource)) {
	h.t.Helper()
	obj, err := h.st.Get(h.t.Context(), v1.KindEventSource.GVK(), "team-a", "clock")
	require.NoError(h.t, err)
	es := obj.(*v1.EventSource)
	apply(es.Spec.Timer)
	_, err = h.st.Update(h.t.Context(), es)
	require.NoError(h.t, err)
}

func (h *timerHarness) requireFires(event v1.ObjectName, want ...time.Duration) {
	h.t.Helper()
	require.Equal(h.t, want, h.fires[event], "fire times of %q as offsets from creationTimestamp", event)
}

func TestIssue713_TimerScheduleSurvivesRestart(t *testing.T) {
	h := newTimerHarness(t)
	for i := range 3 {
		h.live(time.Duration(i)*23*time.Hour, time.Duration(i+1)*23*time.Hour)
	}
	h.requireFires("daily", 24*time.Hour, 48*time.Hour)
}

func TestScenarioUninterruptedLifeUnchanged(t *testing.T) {
	h := newTimerHarness(t)
	h.live(0, 69*time.Hour)
	h.requireFires("daily", 24*time.Hour, 48*time.Hour)
}

func TestScenarioMissedFireSkipped(t *testing.T) {
	h := newTimerHarness(t)
	h.live(0, 20*time.Hour)
	h.live(30*time.Hour, 50*time.Hour)
	h.requireFires("daily", 48*time.Hour)
}

func TestScenarioAddedEventOnCreationGrid(t *testing.T) {
	h := newTimerHarness(t)
	h.live(0, 73*time.Hour, specEdit{at: 30 * time.Hour, apply: func(ts *v1.TimerSource) {
		ts.Events = append(ts.Events, v1.TimerEvent{Name: "other", Interval: v1.Duration(24 * time.Hour)})
	}})
	h.requireFires("other", 48*time.Hour, 72*time.Hour)
	h.requireFires("daily", 24*time.Hour, 48*time.Hour, 72*time.Hour)
}

func TestScenarioIntervalChangeOnCreationGrid(t *testing.T) {
	h := newTimerHarness(t)
	h.live(0, 49*time.Hour, specEdit{at: 25 * time.Hour, apply: func(ts *v1.TimerSource) {
		ts.Events[0].Interval = v1.Duration(6 * time.Hour)
	}})
	h.requireFires("daily", 24*time.Hour, 30*time.Hour, 36*time.Hour, 42*time.Hour, 48*time.Hour)
}

func TestScenarioClockBehindCreation(t *testing.T) {
	h := newTimerHarness(t)
	h.live(-30*time.Hour, 42*time.Hour)
	h.requireFires("daily", -24*time.Hour, 0, 24*time.Hour)
}

func TestGridFloor(t *testing.T) {
	created := time.Date(2026, 10, 5, 9, 30, 0, 123, time.UTC)
	const day = 24 * time.Hour
	for _, tc := range []struct {
		name    string
		created time.Time
		now     time.Time
		want    time.Time
	}{
		{name: "now equal to created", created: created, now: created, want: created},
		{name: "now on a later grid point", created: created, now: created.Add(3 * day), want: created.Add(3 * day)},
		{name: "now between grid points", created: created, now: created.Add(2*day + 5*time.Hour), want: created.Add(2 * day)},
		{name: "now before created by a non-multiple", created: created, now: created.Add(-30 * time.Hour), want: created.Add(-2 * day)},
		{name: "now before created by an exact multiple", created: created, now: created.Add(-2 * day), want: created.Add(-2 * day)},
		{name: "zero created", now: created, want: created},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, gridFloor(tc.created, tc.now, day))
		})
	}
}
