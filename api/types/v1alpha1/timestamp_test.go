package v1alpha1

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// stamped is the instant every marshal test uses: one nanosecond short of the next millisecond.
func stamped() time.Time { return time.Date(2026, 10, 7, 20, 3, 35, 965999999, time.UTC) }

func TestNewTimestampTruncatesToUTCMillisecond(t *testing.T) {
	plus2 := time.FixedZone("UTC+2", 2*60*60)
	ts := NewTimestamp(stamped().In(plus2))
	require.Equal(t, time.UTC, time.Time(ts).Location())
	require.True(t, time.Time(ts).Equal(time.Date(2026, 10, 7, 20, 3, 35, 965000000, time.UTC)))
	require.Equal(t, "2026-10-07T20:03:35.965Z", ts.String())
	require.True(t, NewTimestamp(time.Time{}).IsZero())
	require.False(t, ts.IsZero())
}

func TestTimestampStringReappliesTheForm(t *testing.T) {
	raw := Timestamp(stamped().In(time.FixedZone("UTC+2", 2*60*60)))
	require.Equal(t, "2026-10-07T20:03:35.965Z", raw.String())
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	require.Equal(t, `"2026-10-07T20:03:35.965Z"`, string(b))
}

func TestTimestampMarshalJSONYears(t *testing.T) {
	for want, at := range map[string]time.Time{
		`"0000-01-01T00:00:00.000Z"`: time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		`"9999-12-31T23:59:59.999Z"`: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		`"0001-01-01T00:00:00.000Z"`: {},
	} {
		b, err := json.Marshal(Timestamp(at))
		require.NoError(t, err, want)
		require.Equal(t, want, string(b))
		require.Len(t, string(b), 26, "24 characters and two quotes")
	}
	for _, at := range []time.Time{time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)} {
		_, err := json.Marshal(Timestamp(at))
		require.Error(t, err, at)
	}
}

func TestTimestampUnmarshalJSON(t *testing.T) {
	var ts Timestamp
	require.NoError(t, json.Unmarshal([]byte(`"2026-10-07T20:03:35.965Z"`), &ts))
	require.Equal(t, NewTimestamp(stamped()), ts)
	require.Equal(t, time.UTC, time.Time(ts).Location())
	require.NoError(t, json.Unmarshal([]byte(`null`), &ts))
	require.Equal(t, NewTimestamp(stamped()), ts, "null leaves the value unchanged")
	for _, bad := range []string{
		`"2026-10-07T20:03:35Z"`,
		`"2026-10-07T20:03:35.96Z"`,
		`"2026-10-07T20:03:35.965123Z"`,
		`"2026-10-07T22:03:35.965+02:00"`,
		`"2026-10-07T20:03:35.965z"`,
		`"2026-13-07T20:03:35.965Z"`,
		`"2026-10-07T20:03:60.965Z"`,
		`"2026-10-07 20:03:35.965Z"`,
		`1791403415965`,
		`true`,
		`{}`,
		`""`,
	} {
		before := ts
		err := json.Unmarshal([]byte(bad), &ts)
		require.Error(t, err, bad)
		require.Equal(t, fault.Invalid, fault.KindOf(err), bad)
		require.ErrorContains(t, err, "exactly 3 fractional digits", bad)
		require.Equal(t, before, ts, "a refused value leaves the field unchanged: %s", bad)
	}
}

// TimestampPattern and time.Parse(TimestampLayout) agree on every well-formed value; time.Parse additionally refuses
// calendar values the pattern cannot see (month 13, second 60).
func TestTimestampPatternAndParserAgree(t *testing.T) {
	re := regexp.MustCompile(TimestampPattern)
	for _, at := range []time.Time{stamped(), {}, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 999e6, time.UTC)} {
		s := NewTimestamp(at).String()
		require.True(t, re.MatchString(s), s)
		_, err := time.Parse(TimestampLayout, s)
		require.NoError(t, err, s)
	}
	for _, s := range []string{"2026-10-07T20:03:35Z", "2026-10-07T20:03:35.965+02:00", "2026-10-07T20:03:35.9650Z", "26-10-07T20:03:35.965Z"} {
		require.False(t, re.MatchString(s), s)
	}
	for _, s := range []string{"2026-13-07T20:03:35.965Z", "2026-10-07T20:03:60.965Z"} {
		require.True(t, re.MatchString(s), s)
		_, err := time.Parse(TimestampLayout, s)
		require.Error(t, err, s)
	}
}

func TestTimestampSchema(t *testing.T) {
	a, b := Timestamp{}.Schema(nil), Timestamp{}.Schema(nil)
	require.NotSame(t, a, b, "huma writes field tags into the schema: each call returns a new one")
	require.Equal(t, huma.TypeString, a.Type)
	require.Equal(t, "date-time", a.Format)
	require.Equal(t, TimestampPattern, a.Pattern)
	require.True(t, strings.Contains(a.PatternDescription, "exactly 3 fractional digits"))
}

// scenario: every-timestamp-utc-millisecond — every api/types instant, stamped at …35.965999999, reads …35.965Z.
func TestEveryTimestampUTCMillisecond(t *testing.T) {
	ts := NewTimestamp(stamped())
	fn := &Function{ObjectMeta: ObjectMeta{CreationTime: ts}}
	fn.Status.Conditions = Conditions{{Type: "Ready", Status: ConditionTrue, LastTransitionTime: ts}}
	fn.Status.DrainingSince = &ts
	inv := &Invocation{ObjectMeta: ObjectMeta{CreationTime: ts}, Status: InvocationStatus{StartTime: ts, EndTime: ts}}
	run := &WorkflowRun{ObjectMeta: ObjectMeta{CreationTime: ts, DeletionTime: &ts}}
	run.Status.Steps = []RunStepStatus{{Name: "a", StartedAt: ts, EndedAt: ts}}
	re := regexp.MustCompile(`"[^"]*":"[0-9]{4}-[^"]*"`)
	for _, obj := range []Object{fn, inv, run} {
		b, err := json.Marshal(obj)
		require.NoError(t, err)
		found := re.FindAllString(string(b), -1)
		require.NotEmpty(t, found)
		for _, kv := range found {
			require.True(t, strings.HasSuffix(kv, `:"2026-10-07T20:03:35.965Z"`), "%s in %s", kv, b)
		}
	}
	b, err := json.Marshal(fn)
	require.NoError(t, err)
	for _, key := range []string{"creationTimestamp", "lastTransitionTime", "drainingSince"} {
		require.Contains(t, string(b), `"`+key+`":"2026-10-07T20:03:35.965Z"`)
	}
}

// scenario: local-clock-stamps-utc — Conditions.Set on a host whose local zone is UTC+2 stamps lastTransitionTime
// in UTC. It swaps time.Local, so it is not parallel.
func TestLocalClockStampsUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("UTC+2", 2*60*60)
	t.Cleanup(func() { time.Local = saved })

	before := time.Now().Truncate(time.Millisecond)
	var cs Conditions
	cs.Set(Condition{Type: "Ready", Status: ConditionFalse})
	after := time.Now()
	lt := time.Time(cs[0].LastTransitionTime)
	require.Equal(t, time.UTC, lt.Location())
	require.False(t, lt.Before(before) || lt.After(after), "%v not in [%v, %v]", lt, before, after)
	b, err := json.Marshal(cs[0])
	require.NoError(t, err)
	require.Regexp(t, `"lastTransitionTime":"[^"]{23}Z"`, string(b))
}

// scenario: zero-time-omitted — an object with no stamped instant marshals without any timestamp key and never as
// 0001-01-01T00:00:00Z.
func TestZeroTimeOmitted(t *testing.T) {
	for _, obj := range []Object{
		&ConfigMap{ObjectMeta: ObjectMeta{Name: "cfg"}},
		&Function{ObjectMeta: ObjectMeta{Name: "fn"}, Status: FunctionStatus{Status: Status{Conditions: Conditions{{Type: "Ready", Status: ConditionTrue}}}}},
		&Invocation{ObjectMeta: ObjectMeta{Name: "inv"}},
		&WorkflowRun{ObjectMeta: ObjectMeta{Name: "run"}, Status: WorkflowRunStatus{Steps: []RunStepStatus{{Name: "a", Phase: StepPending}}}},
	} {
		b, err := json.Marshal(obj)
		require.NoError(t, err)
		for _, key := range []string{"creationTimestamp", "deletionTimestamp", "lastTransitionTime", "drainingSince", "startTime", "endTime", "startedAt", "endedAt", "0001-01-01"} {
			require.NotContains(t, string(b), key, "%T: %s", obj, b)
		}
	}
}
