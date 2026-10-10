package v1alpha1

import (
	"encoding/json"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
)

// scenario: cron-stored-unchanged (ADR-0211). The bytes are what the TimerEvent before ADR-0211 wrote: decoded and
// encoded again they are equal, so the App reconciler's byte comparison of specs stamps no AppRevision.
func TestScenarioCronStoredUnchanged(t *testing.T) {
	const eventSource = `{"apiVersion":"funcd.io/v1alpha1","kind":"EventSource","metadata":{"name":"jobs","namespace":"default","resourceGroup":"rg1"},"spec":{"timer":{"events":[{"name":"tick","interval":"1m30s"},{"name":"daily","interval":"24h"}]}},"status":{}}`
	const appSpec = `{"version":"1.0.0","functions":[{"ref":"mailer"}],"workflows":[{"name":"todo-plan","steps":[{"name":"due","function":{"image":"oci-layout://todo-due:1"}}],"pooling":{}}],"eventSources":[{"name":"todo-tick","timer":{"events":[{"name":"hourly","interval":"1h"}]}}],"sensors":[{"name":"todo-on-tick","on":[{"name":"tick","source":"todo-tick","event":"hourly"}],"do":[{"name":"plan","on":"tick","workflow":"todo-plan"}]}]}`

	var es EventSource
	require.NoError(t, json.Unmarshal([]byte(eventSource), &es))
	require.NoError(t, es.Validate())
	got, err := json.Marshal(&es)
	require.NoError(t, err)
	require.Equal(t, eventSource, string(got))

	var spec AppSpec
	require.NoError(t, json.Unmarshal([]byte(appSpec), &spec))
	got, err = json.Marshal(spec)
	require.NoError(t, err)
	require.Equal(t, appSpec, string(got))
}

func cronEvent(name, expr, zone string) TimerEvent {
	return TimerEvent{Name: ObjectName(name), Cron: expr, TimeZone: zone}
}

// Decision 4: exactly one of interval and cron per event, timeZone only with cron, and one EventSource may mix both.
func TestTimerEventScheduleValidate(t *testing.T) {
	timer := func(events ...TimerEvent) *EventSource {
		return esWith(EventSourceSpec{Timer: &TimerSource{Events: events}})
	}
	for name, es := range map[string]*EventSource{
		"cron in UTC":    timer(cronEvent("quarter", "*/15 * * * *", "")),
		"cron in a zone": timer(cronEvent("report", "0 2 * * *", "Europe/Paris")),
		"a macro":        timer(cronEvent("nightly", "@daily", "America/New_York")),
		"interval only":  timer(TimerEvent{Name: "tick", Interval: Duration(time.Minute)}),
		"interval and cron events mixed": timer(TimerEvent{Name: "tick", Interval: Duration(time.Minute)},
			cronEvent("report", "0 2 * * *", "Europe/Paris")),
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, es.Validate()) })
	}

	refusals := map[string]struct {
		event TimerEvent
		want  []string
	}{
		"4 fields":                {cronEvent("report", "0 2 * *", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"seconds":                 {cronEvent("report", "0 0 2 * * *", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"@every":                  {cronEvent("report", "@every 5m", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"unknown macro":           {cronEvent("report", "@5minutes", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"weekday name":            {cronEvent("report", "0 2 * * MON", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"n/s":                     {cronEvent("report", "5/10 * * * *", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"minute out of range":     {cronEvent("report", "60 * * * *", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"no calendar date":        {cronEvent("report", "0 0 30 2 *", ""), []string{"spec.timer.events[0].cron", cron.Grammar}},
		"unknown zone":            {cronEvent("report", "0 2 * * *", "Mars/Base"), []string{`spec.timer.events[0].timeZone "Mars/Base"`}},
		"Local zone":              {cronEvent("report", "0 2 * * *", "Local"), []string{`spec.timer.events[0].timeZone "Local"`}},
		"interval and cron":       {TimerEvent{Name: "report", Interval: Duration(time.Hour), Cron: "0 2 * * *"}, []string{`spec.timer.events[0] ("report") must set exactly one schedule (interval or cron), got 2`}},
		"neither":                 {TimerEvent{Name: "report"}, []string{`spec.timer.events[0] ("report") must set exactly one schedule (interval or cron), got 0`}},
		"interval of 0s is unset": {TimerEvent{Name: "report", Interval: 0, TimeZone: "UTC"}, []string{"got 0"}},
		"timeZone with interval":  {TimerEvent{Name: "report", Interval: Duration(time.Hour), TimeZone: "Europe/Paris"}, []string{"spec.timer.events[0].timeZone is set without cron"}},
		"interval out of bounds":  {TimerEvent{Name: "report", Interval: Duration(48 * time.Hour)}, []string{"spec.timer.events[0].interval 48h is out of bounds"}},
	}
	for name, tc := range refusals {
		t.Run(name, func(t *testing.T) {
			err := timer(tc.event).Validate()
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			for _, w := range tc.want {
				require.ErrorContains(t, err, w)
			}
		})
	}

	t.Run("the second event is named by its index", func(t *testing.T) {
		err := timer(TimerEvent{Name: "tick", Interval: Duration(time.Minute)}, cronEvent("report", "0 2 * *", "")).Validate()
		require.ErrorContains(t, err, `spec.timer.events[1].cron "0 2 * *"`)
	})
}

func TestCheckCron(t *testing.T) {
	require.NoError(t, CheckCron("op", "spec.schedule", "@weekly", "spec.timeZone", "Asia/Tokyo"))
	err := CheckCron("op", "spec.schedule", "@sometimes", "spec.timeZone", "")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, `op: spec.schedule "@sometimes" is not a valid schedule`)
	require.ErrorContains(t, err, cron.Grammar)
	err = CheckCron("op", "spec.schedule", "@weekly", "spec.timeZone", "Local")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, `op: spec.timeZone "Local" is not a time zone`)
}

// An App's eventSources gain the fields with no App code change: a cron event validates, and a refusal names the
// entry's path.
func TestAppEventSourceCron(t *testing.T) {
	a := fullApp()
	a.Spec.EventSources[0].Timer.Events = append(a.Spec.EventSources[0].Timer.Events, cronEvent("nightly", "0 2 * * *", "Europe/Paris"))
	require.NoError(t, a.Validate())
	a.Spec.EventSources[0].Timer.Events[1].Cron = "0 2 * *"
	err := a.Validate()
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, `eventSources[0].timer.events[1].cron "0 2 * *"`)
}
