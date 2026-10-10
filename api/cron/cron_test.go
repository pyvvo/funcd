package cron_test

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := cron.LoadZone(name)
	require.NoError(t, err)
	return loc
}

func at(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return v
}

// fires returns the first n instants of expr in zone after the RFC 3339 time from, in UTC.
func fires(t *testing.T, expr, zoneName, from string, n int) []string {
	t.Helper()
	tt, err := cron.Parse(expr, zone(t, zoneName))
	require.NoError(t, err)
	var out []string
	for next := at(t, from); len(out) < n; {
		next = tt.Next(next)
		require.False(t, next.IsZero())
		out = append(out, next.UTC().Format(time.RFC3339))
	}
	return out
}

func TestParseAccepts(t *testing.T) {
	for expr, want := range map[string][]string{
		"* * * * *":                       {"2026-10-08T10:08:00Z", "2026-10-08T10:09:00Z"},
		"*/15 * * * *":                    {"2026-10-08T10:15:00Z", "2026-10-08T10:30:00Z"},
		"0 2 * * *":                       {"2026-10-09T02:00:00Z", "2026-10-10T02:00:00Z"},
		"5,10-20/5 3 * * *":               {"2026-10-09T03:05:00Z", "2026-10-09T03:10:00Z"},
		"0 0 * * 7":                       {"2026-10-11T00:00:00Z", "2026-10-18T00:00:00Z"},
		"0 0 * * 0":                       {"2026-10-11T00:00:00Z", "2026-10-18T00:00:00Z"},
		"0 0 * * 5-7":                     {"2026-10-09T00:00:00Z", "2026-10-10T00:00:00Z"},
		" 0\t0  1 1 * ":                   {"2027-01-01T00:00:00Z", "2028-01-01T00:00:00Z"},
		"0 0 29 2 *":                      {"2028-02-29T00:00:00Z", "2032-02-29T00:00:00Z"},
		"0 0 31 * *":                      {"2026-10-31T00:00:00Z", "2026-12-31T00:00:00Z"},
		"0 0 1-31 1-12 0-7":               {"2026-10-09T00:00:00Z", "2026-10-10T00:00:00Z"},
		"0-59/30 0-23/12 * * *":           {"2026-10-08T12:00:00Z", "2026-10-08T12:30:00Z"},
		"@yearly":                         {"2027-01-01T00:00:00Z", "2028-01-01T00:00:00Z"},
		"@annually":                       {"2027-01-01T00:00:00Z", "2028-01-01T00:00:00Z"},
		"@monthly":                        {"2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		"@weekly":                         {"2026-10-11T00:00:00Z", "2026-10-18T00:00:00Z"},
		"@daily":                          {"2026-10-09T00:00:00Z", "2026-10-10T00:00:00Z"},
		"@midnight":                       {"2026-10-09T00:00:00Z", "2026-10-10T00:00:00Z"},
		"@hourly":                         {"2026-10-08T11:00:00Z", "2026-10-08T12:00:00Z"},
		"0 0 */9223372036854775807 * *":   {"2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		"1-5/9223372036854775807 * * * *": {"2026-10-08T11:01:00Z", "2026-10-08T12:01:00Z"},
		"0 0 * * 1-7/9223372036854775807": {"2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"},
	} {
		t.Run(expr, func(t *testing.T) {
			require.Equal(t, want, fires(t, expr, "", "2026-10-08T10:07:30Z", 2))
		})
	}
}

// The refusals of scenario cron-invalid-refused and the other forms Decision 1 refuses.
func TestParseRefuses(t *testing.T) {
	for expr, why := range map[string]string{
		"0 2 * *":                      "it has 4 fields",
		"0 0 2 * * *":                  "it has 6 fields",
		"0 0 2 * * * 2027":             "it has 7 fields",
		"":                             "it has 0 fields",
		"@every 5m":                    "not one of the macros",
		"@5minutes":                    "not one of the macros",
		"@always":                      "not one of the macros",
		"@DAILY":                       "not one of the macros",
		"@daily 0":                     "not one of the macros",
		"0 2 * * MON":                  `the day-of-week field "MON"`,
		"0 2 * JAN *":                  `the month field "JAN"`,
		"5/10 * * * *":                 `the minute field "5/10": a step needs * or a range`,
		"60 * * * *":                   "60 is out of range 0-59",
		"0 24 * * *":                   "24 is out of range 0-23",
		"0 0 0 * *":                    "0 is out of range 1-31",
		"0 0 32 * *":                   "32 is out of range 1-31",
		"0 0 * 0 *":                    "0 is out of range 1-12",
		"0 0 * 13 *":                   "13 is out of range 1-12",
		"0 0 * * 8":                    "8 is out of range 0-7",
		"0 0 30 2 *":                   "share no calendar date",
		"0 0 31 4,6,9,11 *":            "share no calendar date",
		"0 0 30-31 2 *":                "share no calendar date",
		"*/0 * * * *":                  `step "0"`,
		"*/ * * * *":                   `step ""`,
		"*/+5 * * * *":                 `step "+5"`,
		"20-10 * * * *":                "range 20-10 is reversed",
		"1- * * * *":                   `"" is not a number`,
		"-1 * * * *":                   `"" is not a number`,
		"+1 * * * *":                   `"+1" is not a number`,
		"1,,2 * * * *":                 `"" is not a number`,
		"? * * * *":                    `"?" is not a number`,
		"0 0 L * *":                    `"L" is not a number`,
		"0 0 15W * *":                  `"15W" is not a number`,
		"0 0 * * 1#2":                  `"1#2" is not a number`,
		"CRON_TZ=UTC 0 2 * * *":        "it has 6 fields",
		"0 0 * * *\n":                  `"*\n" is not a number`,
		"99999999999999999999 * * * *": "is not a number",
	} {
		t.Run(expr, func(t *testing.T) {
			_, err := cron.Parse(expr, nil)
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, why)
			require.ErrorContains(t, err, fmt.Sprintf("%q is not %s", expr, cron.Grammar))
		})
	}
}

func TestLoadZone(t *testing.T) {
	require.Equal(t, time.UTC, zone(t, ""))
	require.Equal(t, "Europe/Paris", zone(t, "Europe/Paris").String())
	for _, name := range []string{"Local", "Mars/Base", "Europe/Paris ", "../etc/passwd"} {
		_, err := cron.LoadZone(name)
		require.Error(t, err, name)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		require.ErrorContains(t, err, fmt.Sprintf("%q", name))
	}
}

// Decision 1's day fields: a field starting with * is unrestricted, any other is restricted, and two restricted
// fields match a day when either does.
func TestDayFieldRule(t *testing.T) {
	require.Equal(t, []string{"2027-02-01T00:00:00Z", "2027-02-08T00:00:00Z", "2027-02-15T00:00:00Z"},
		fires(t, "0 0 30 2 1", "", "2026-10-08T00:00:00Z", 3), "only Mondays in February: there is no 30 February")
	for _, f := range fires(t, "0 0 */2 * 1", "", "2026-10-08T00:00:00Z", 6) {
		d := at(t, f)
		require.Equal(t, time.Monday, d.Weekday(), f)
		require.Equal(t, 1, d.Day()%2, f)
	}
	require.Equal(t, []string{"2026-10-09T00:00:00Z", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"},
		fires(t, "0 0 1-31 * 1", "", "2026-10-08T00:00:00Z", 3), "1-31 is restricted, so OR with Monday: every day")
	require.Equal(t, []string{"2032-02-29T00:00:00Z", "2060-02-29T00:00:00Z"},
		fires(t, "0 0 29 2 */7", "", "2026-10-08T00:00:00Z", 2), "*/7 is unrestricted, so AND: 29 February on a Sunday")
}

// Decision 3: a slot in a gap fires once, at the end of the gap; a slot in a fold fires at its first occurrence.
func TestNextDaylightSaving(t *testing.T) {
	for _, tc := range []struct {
		name, zone, expr, from string
		want                   []string
	}{
		{"New York gap", "America/New_York", "30 2 * * *", "2027-03-13T08:00:00Z",
			[]string{"2027-03-14T07:00:00Z", "2027-03-15T06:30:00Z"}},
		{"New York gap, four slots of the hour once", "America/New_York", "*/15 2 * * *", "2027-03-13T07:50:00Z",
			[]string{"2027-03-14T07:00:00Z", "2027-03-15T06:00:00Z", "2027-03-15T06:15:00Z"}},
		{"New York gap, a slot at the end of the gap fires once", "America/New_York", "0,30 2,3 * * *", "2027-03-14T05:00:00Z",
			[]string{"2027-03-14T07:00:00Z", "2027-03-14T07:30:00Z", "2027-03-15T06:00:00Z"}},
		{"New York fold", "America/New_York", "30 1 * * *", "2026-10-31T12:00:00Z",
			[]string{"2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"}},
		{"New York fold, hourly", "America/New_York", "0 * * * *", "2026-11-01T04:30:00Z",
			[]string{"2026-11-01T05:00:00Z", "2026-11-01T07:00:00Z", "2026-11-01T08:00:00Z"}},
		{"New York fold, from the second pass", "America/New_York", "*/20 1 * * *", "2026-11-01T06:10:00Z",
			[]string{"2026-11-02T06:00:00Z"}},
		{"Paris gap", "Europe/Paris", "30 2 * * *", "2027-03-27T12:00:00Z",
			[]string{"2027-03-28T01:00:00Z", "2027-03-29T00:30:00Z"}},
		{"Paris fold", "Europe/Paris", "30 2 * * *", "2026-10-24T12:00:00Z",
			[]string{"2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"}},
		{"Paris fold, every 30 minutes", "Europe/Paris", "*/30 * * * *", "2026-10-25T00:10:00Z",
			[]string{"2026-10-25T00:30:00Z", "2026-10-25T02:00:00Z", "2026-10-25T02:30:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, fires(t, tc.expr, tc.zone, tc.from, len(tc.want)))
		})
	}
}

// Next returns the instant strictly after its argument, also when the argument is a slot.
func TestNextStrictlyAfter(t *testing.T) {
	require.Equal(t, []string{"2026-10-09T02:00:00Z"}, fires(t, "0 2 * * *", "", "2026-10-08T02:00:00Z", 1))
	require.Equal(t, []string{"2026-10-08T02:01:00Z"}, fires(t, "* * * * *", "", "2026-10-08T02:00:00Z", 1))
}

type item struct {
	text   string
	values []int
}

// fieldGen draws one field as text and its value set, computed here and not by the parser under test.
func fieldGen(lo, hi int) *rapid.Generator[item] {
	one := rapid.Custom(func(rt *rapid.T) item {
		a := rapid.IntRange(lo, hi).Draw(rt, "a")
		b := rapid.IntRange(a, hi).Draw(rt, "b")
		s := rapid.OneOf(rapid.IntRange(1, hi-lo+2), rapid.IntRange(math.MaxInt-hi, math.MaxInt)).Draw(rt, "s")
		span := func(from, to, step int) []int {
			var out []int
			for v := from; v <= to; v++ {
				if (v-from)%step == 0 {
					out = append(out, v)
				}
			}
			return out
		}
		switch rapid.IntRange(0, 5).Draw(rt, "kind") {
		case 0, 1:
			return item{"*", span(lo, hi, 1)}
		case 2:
			return item{fmt.Sprintf("*/%d", s), span(lo, hi, s)}
		case 3:
			return item{fmt.Sprint(a), []int{a}}
		case 4:
			return item{fmt.Sprintf("%d-%d", a, b), span(a, b, 1)}
		default:
			return item{fmt.Sprintf("%d-%d/%d", a, b, s), span(a, b, s)}
		}
	})
	return rapid.Custom(func(rt *rapid.T) item {
		var out item
		for i, it := range rapid.SliceOfN(one, 1, 3).Draw(rt, "items") {
			if i > 0 {
				out.text += ","
			}
			out.text += it.text
			out.values = append(out.values, it.values...)
		}
		return out
	})
}

type oracle struct {
	sets [5]map[int]bool
	or   bool
}

func (o oracle) matches(wall time.Time) bool {
	if !o.sets[0][wall.Minute()] || !o.sets[1][wall.Hour()] || !o.sets[3][int(wall.Month())] {
		return false
	}
	dom, dow := o.sets[2][wall.Day()], o.sets[4][int(wall.Weekday())]
	if o.or {
		return dom || dow
	}
	return dom && dow
}

func wallOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// fires walks every minute instant from a quarter day before after to end, applying Decision 3 instant by
// instant: an instant fires when its local time first reaches a slot, which covers a gap's skipped slots and
// leaves a fold's repeated slots to the first pass.
func (o oracle) fires(loc *time.Location, after, end time.Time) []time.Time {
	start := after.Truncate(time.Minute).Add(-6 * time.Hour)
	high := wallOf(start, loc)
	var out []time.Time
	for t := start.Add(time.Minute); !t.After(end); t = t.Add(time.Minute) {
		wall := wallOf(t, loc)
		if !wall.After(high) {
			continue
		}
		for s := high.Truncate(time.Minute).Add(time.Minute); !s.After(wall); s = s.Add(time.Minute) {
			if o.matches(s) {
				if t.After(after) {
					out = append(out, t)
				}
				break
			}
		}
		high = wall
	}
	return out
}

// Next against the brute-force oracle, in UTC and in both daylight-saving zones, around their changes.
func TestNextMatchesOracle(t *testing.T) {
	zones := []*time.Location{time.UTC, zone(t, "America/New_York"), zone(t, "Europe/Paris")}
	changes := []time.Time{
		at(t, "2026-11-01T06:00:00Z"), at(t, "2027-03-14T07:00:00Z"), at(t, "2026-10-25T01:00:00Z"), at(t, "2027-03-28T01:00:00Z"),
		at(t, "2028-02-28T12:00:00Z"), at(t, "2026-12-31T23:00:00Z"),
	}
	rapid.Check(t, func(rt *rapid.T) {
		fieldItems := []item{
			fieldGen(0, 59).Draw(rt, "minute"),
			fieldGen(0, 23).Draw(rt, "hour"),
			fieldGen(1, 31).Draw(rt, "dom"),
			fieldGen(1, 12).Draw(rt, "month"),
			fieldGen(0, 7).Draw(rt, "dow"),
		}
		var o oracle
		texts := make([]string, len(fieldItems))
		for i, f := range fieldItems {
			texts[i] = f.text
			o.sets[i] = map[int]bool{}
			for _, v := range f.values {
				if i == 4 && v == 7 {
					v = 0
				}
				o.sets[i][v] = true
			}
		}
		o.or = !strings.HasPrefix(texts[2], "*") && !strings.HasPrefix(texts[4], "*")
		expr := strings.Join(texts, " ")
		loc := rapid.SampledFrom(zones).Draw(rt, "zone")
		tt, err := cron.Parse(expr, loc)
		if !o.or && !hasDate(o) {
			require.Error(rt, err, expr)
			return
		}
		require.NoError(rt, err, expr)
		base := rapid.SampledFrom(changes).Draw(rt, "near")
		after := base.Add(time.Duration(rapid.Int64Range(-2*24*3600, 2*24*3600).Draw(rt, "offset")) * time.Second)
		end := after.Add(3 * 24 * time.Hour)
		var got []time.Time
		for next := after; ; {
			next = tt.Next(next)
			require.False(rt, next.IsZero(), expr)
			if next.After(end) {
				break
			}
			got = append(got, next)
		}
		want := o.fires(loc, after, end)
		require.Equal(rt, len(want), len(got), "%s in %s after %s: want %v, got %v", expr, loc, after, want, got)
		for i := range want {
			require.True(rt, want[i].Equal(got[i]), "%s in %s after %s: fire %d want %s, got %s", expr, loc, after, i, want[i], got[i])
		}
	})
}

func hasDate(o oracle) bool {
	days := [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for m := range o.sets[3] {
		for d := range o.sets[2] {
			if d <= days[m] {
				return true
			}
		}
	}
	return false
}
