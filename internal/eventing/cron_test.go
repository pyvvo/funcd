package eventing

import (
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// cronHarness is the ADR-0211 fixture: EventSource default/jobs over one store that outlives every daemon life, and
// a subscriber on the Fanout, the seam a bound Sensor receives through, recording the instant of each event.
type cronHarness struct {
	t     *testing.T
	eng   store.Engine
	st    store.Store
	req   controller.Request
	clk   *clock.Manual
	src   *Source
	fires map[v1.ObjectName][]time.Time
}

func newCronHarness(t *testing.T, events ...v1.TimerEvent) *cronHarness {
	t.Helper()
	eng := memory.New()
	h := &cronHarness{
		t: t, eng: eng, st: store.New(eng), fires: map[v1.ObjectName][]time.Time{},
		req: controller.Request{GVK: v1.KindEventSource.GVK(), Namespace: "default", Name: "jobs"},
	}
	obj, ok := v1.NewObject(v1.KindEventSource)
	require.True(t, ok)
	es := obj.(*v1.EventSource)
	es.Name, es.Namespace, es.ResourceGroup = "jobs", "default", "rg1"
	es.Spec.Timer = &v1.TimerSource{Events: events}
	_, err := h.st.Create(t.Context(), es)
	require.NoError(t, err)
	return h
}

// boot starts a daemon life at start: a new Source on a manual clock and its first Reconcile.
func (h *cronHarness) boot(start string) {
	h.t.Helper()
	h.clk = clock.NewManual(at(h.t, start))
	fan := NewFanout()
	for _, ev := range h.events() {
		fan.Subscribe("default", "jobs", ev.Name, func(context.Context, CloudEvent) {
			h.fires[ev.Name] = append(h.fires[ev.Name], h.clk.Now())
		})
	}
	src, err := NewSource(Deps{Store: h.st, Publisher: fan, Clock: h.clk})
	require.NoError(h.t, err)
	h.src = src
	h.reconcile()
}

func (h *cronHarness) events() []v1.TimerEvent {
	h.t.Helper()
	obj, err := h.st.Get(h.t.Context(), v1.KindEventSource.GVK(), "default", "jobs")
	require.NoError(h.t, err)
	return obj.(*v1.EventSource).Spec.Timer.Events
}

func (h *cronHarness) reconcile() {
	h.t.Helper()
	_, err := h.src.Reconcile(h.t.Context(), h.req)
	require.NoError(h.t, err)
}

// edit changes the stored timer spec through admission and reconciles it.
func (h *cronHarness) edit(apply func(*v1.TimerSource)) {
	h.t.Helper()
	obj, err := h.st.Get(h.t.Context(), v1.KindEventSource.GVK(), "default", "jobs")
	require.NoError(h.t, err)
	es := obj.(*v1.EventSource)
	apply(es.Spec.Timer)
	_, err = h.st.Update(h.t.Context(), es)
	require.NoError(h.t, err)
	h.reconcile()
}

// run ticks the life every 10 seconds up to and including end, publishing each due event.
func (h *cronHarness) run(end string) {
	h.t.Helper()
	for until := at(h.t, end); !h.clk.Now().After(until); h.clk.Advance(10 * time.Second) {
		h.tick()
	}
}

func (h *cronHarness) tick() {
	h.t.Helper()
	for _, k := range h.src.dueTimers(h.clk.Now()) {
		require.NoError(h.t, h.src.Fire(h.t.Context(), k.ns, k.source, k.event))
	}
}

func (h *cronHarness) requireFires(event v1.ObjectName, want ...string) {
	h.t.Helper()
	got := make([]string, 0, len(h.fires[event]))
	for _, f := range h.fires[event] {
		got = append(got, f.UTC().Format(time.RFC3339))
	}
	if want == nil {
		want = []string{}
	}
	require.Equal(h.t, want, got, "fire instants of %q", event)
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return v
}

func cronEvent(name, expr, zone string) v1.TimerEvent {
	return v1.TimerEvent{Name: v1.ObjectName(name), Cron: expr, TimeZone: zone}
}

func TestScenarioCronUTCDefault(t *testing.T) {
	h := newCronHarness(t, cronEvent("quarter", "*/15 * * * *", ""))
	h.boot("2026-10-08T10:07:00Z")
	h.run("2026-10-08T10:50:00Z")
	h.requireFires("quarter", "2026-10-08T10:15:00Z", "2026-10-08T10:30:00Z", "2026-10-08T10:45:00Z")
}

func TestScenarioCronInZone(t *testing.T) {
	h := newCronHarness(t, cronEvent("report", "0 2 * * *", "Europe/Paris"))
	h.boot("2026-10-08T00:30:00Z")
	h.run("2026-10-27T12:00:00Z")
	var want []string
	for d := 9; d <= 25; d++ {
		want = append(want, time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))
	}
	want = append(want, "2026-10-26T01:00:00Z", "2026-10-27T01:00:00Z")
	h.requireFires("report", want...)
}

func TestScenarioCronDSTGap(t *testing.T) {
	t.Run("one slot in the gap", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "30 2 * * *", "America/New_York"))
		h.boot("2027-03-13T12:00:00Z")
		h.run("2027-03-15T12:00:00Z")
		h.requireFires("report", "2027-03-14T07:00:00Z", "2027-03-15T06:30:00Z")
	})
	t.Run("the four slots of the hour fire once", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "*/15 2 * * *", "America/New_York"))
		h.boot("2027-03-13T12:00:00Z")
		h.run("2027-03-15T12:00:00Z")
		h.requireFires("report", "2027-03-14T07:00:00Z",
			"2027-03-15T06:00:00Z", "2027-03-15T06:15:00Z", "2027-03-15T06:30:00Z", "2027-03-15T06:45:00Z")
	})
}

func TestScenarioCronDSTFold(t *testing.T) {
	t.Run("a slot in the repeated hour fires at its first occurrence", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "30 1 * * *", "America/New_York"))
		h.boot("2026-11-01T00:00:00Z")
		h.run("2026-11-01T12:00:00Z")
		h.requireFires("report", "2026-11-01T05:30:00Z")
	})
	t.Run("hourly skips the repeated hour", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "0 * * * *", "America/New_York"))
		h.boot("2026-11-01T04:30:00Z")
		h.run("2026-11-01T08:30:00Z")
		h.requireFires("report", "2026-11-01T05:00:00Z", "2026-11-01T07:00:00Z", "2026-11-01T08:00:00Z")
	})
}

func TestScenarioCronNoCatchUp(t *testing.T) {
	t.Run("a slot passed while funcd was down is skipped", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "0 2 * * *", ""))
		h.boot("2026-10-08T01:00:00Z")
		h.run("2026-10-08T01:50:00Z")
		h.boot("2026-10-08T02:10:00Z")
		h.run("2026-10-09T02:30:00Z")
		h.requireFires("report", "2026-10-09T02:00:00Z")
	})
	t.Run("a start on a slot does not fire", func(t *testing.T) {
		h := newCronHarness(t, cronEvent("report", "0 2 * * *", ""))
		h.boot("2026-10-08T02:00:00Z")
		h.run("2026-10-09T02:30:00Z")
		h.requireFires("report", "2026-10-09T02:00:00Z")
	})
}

// Decision 6: slots passed between two ticks, as in a slow publish or a forward clock jump, fire once, not in a burst.
func TestCronSkipsMissedSlots(t *testing.T) {
	h := newCronHarness(t, cronEvent("quarter", "*/15 * * * *", ""))
	h.boot("2026-10-08T10:07:00Z")
	h.clk.Advance(time.Hour)
	h.run("2026-10-08T11:20:00Z")
	h.requireFires("quarter", "2026-10-08T11:07:00Z", "2026-10-08T11:15:00Z")
}

func TestScenarioCronChangeReseeds(t *testing.T) {
	key := eventKey{ns: "default", source: "jobs", event: "report"}
	// Each case boots at 10:07 with the hourly cron entry (next 11:00), lets the clock pass 11:00 with no tick,
	// then edits the spec: a kept entry is still due at 11:00:30, a reseeded one waits for its next slot.
	for _, tc := range []struct {
		name   string
		apply  func(*v1.TimerSource)
		dueNow bool
		next   string
	}{
		{"same cron and zone keep the next fire", func(ts *v1.TimerSource) {
			ts.Events = append(ts.Events, cronEvent("other", "@daily", ""))
		}, true, ""},
		{"a changed expression reseeds from now", func(ts *v1.TimerSource) { ts.Events[0].Cron = "0 */1 * * *" }, false, "2026-10-08T12:00:00Z"},
		{"a changed zone reseeds from now", func(ts *v1.TimerSource) { ts.Events[0].TimeZone = "Europe/Paris" }, false, "2026-10-08T12:00:00Z"},
		{"a switch to interval reseeds on the creation grid", func(ts *v1.TimerSource) {
			ts.Events[0] = v1.TimerEvent{Name: "report", Interval: v1.Duration(time.Hour)}
		}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newCronHarness(t, cronEvent("report", "0 * * * *", ""))
			h.boot("2026-10-08T10:07:00Z")
			require.Equal(t, at(t, "2026-10-08T11:00:00Z"), h.src.timers[key].next.UTC())
			h.clk.Advance(53*time.Minute + 30*time.Second)
			h.edit(tc.apply)
			due := h.src.dueTimers(h.clk.Now())
			require.Equal(t, tc.dueNow, len(due) == 1 && due[0] == key, "due at 11:00:30: %v", due)
			if tc.next != "" {
				require.Equal(t, at(t, tc.next), h.src.timers[key].next.UTC())
			}
		})
	}
	t.Run("a switch from interval to cron reseeds from now", func(t *testing.T) {
		h := newCronHarness(t, v1.TimerEvent{Name: "report", Interval: v1.Duration(time.Hour)})
		h.boot("2026-10-08T10:07:00Z")
		require.Nil(t, h.src.timers[key].table)
		h.edit(func(ts *v1.TimerSource) { ts.Events[0] = cronEvent("report", "30 * * * *", "") })
		require.NotNil(t, h.src.timers[key].table)
		require.Equal(t, at(t, "2026-10-08T10:30:00Z"), h.src.timers[key].next.UTC())
	})
}

// Decision 6: a cron that does not parse, which admission makes unreachable, keeps the event's previous entry while
// the other events register, and Reconcile returns the error so the controller retries it.
func TestCronParseErrorKeepsPreviousEntry(t *testing.T) {
	h := newCronHarness(t, cronEvent("report", "0 2 * * *", ""), v1.TimerEvent{Name: "tick", Interval: v1.Duration(time.Minute)})
	h.boot("2026-10-08T10:00:00Z")
	key := eventKey{ns: "default", source: "jobs", event: "report"}
	prev := h.src.timers[key]
	require.Equal(t, 2, h.src.ActiveTimers())

	rawEdit(t, h, `"cron":"0 2 * * *"`, `"cron":"0 2 * *"`)
	rawEdit(t, h, `{"name":"tick","interval":"1m"}`, `{"name":"tick","interval":"1m"},{"name":"added","cron":"@hourly"}`)
	_, err := h.src.Reconcile(t.Context(), h.req)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "default/jobs/report")
	require.Same(t, prev, h.src.timers[key])
	require.Equal(t, 3, h.src.ActiveTimers())
}

// rawEdit replaces old by repl in the stored bytes of default/jobs, bypassing admission.
func rawEdit(t *testing.T, h *cronHarness, old, repl string) {
	t.Helper()
	bucket := v1.KindEventSource.GVK().String()
	require.NoError(t, h.eng.Update(t.Context(), func(tx store.Txn) error {
		raw, found, err := tx.Get(bucket, "default/jobs")
		require.NoError(t, err)
		require.True(t, found)
		require.Contains(t, string(raw), old)
		return tx.Put(bucket, "default/jobs", []byte(strings.Replace(string(raw), old, repl, 1)))
	}))
}
