// Package cron parses 5-field cron expressions in an IANA time zone and computes their next slot under one
// daylight-saving rule, for every funcd consumer of a cron schedule (ADR-0211). It imports the standard library and
// api/fault only, and not time/tzdata: a program that needs zones without host files imports that itself.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// Grammar describes the accepted expressions in words; every parse error quotes it.
const Grammar = "a cron expression: 5 fields (minute hour day-of-month month day-of-week) of *, n, a-b, */s or " +
	"a-b/s in comma lists, or @yearly, @annually, @monthly, @weekly, @daily, @midnight or @hourly"

// macro returns the 5 fields a macro stands for.
func macro(name string) (string, bool) {
	switch name {
	case "@yearly", "@annually":
		return "0 0 1 1 *", true
	case "@monthly":
		return "0 0 1 * *", true
	case "@weekly":
		return "0 0 * * 0", true
	case "@daily", "@midnight":
		return "0 0 * * *", true
	case "@hourly":
		return "0 * * * *", true
	}
	return "", false
}

type field struct {
	name     string
	min, max int
}

const (
	minuteField = iota
	hourField
	domField
	monthField
	dowField
)

// maxScanDays bounds Next's day walk by one Gregorian cycle: the calendar, weekdays included, repeats every 400
// years, and within a cycle every date falls on each weekday, so an accepted expression matches a day in it.
const maxScanDays = 146097

// Timetable is a parsed expression in a zone (Decision 1). Immutable and safe for concurrent use.
type Timetable struct {
	sets [5]uint64
	or   bool
	loc  *time.Location
}

// LoadZone resolves an IANA name: "" ⇒ time.UTC; "Local" or an unknown name ⇒ fault.Invalid naming it.
func LoadZone(name string) (*time.Location, error) {
	const op = "cron.LoadZone"
	if name == "" {
		return time.UTC, nil
	}
	if name == "Local" {
		return nil, fault.Invalidf(op, "time zone %q names the host's zone: want an IANA name such as Europe/Paris", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fault.Invalidf(op, "time zone %q is not a known IANA name", name)
	}
	return loc, nil
}

// Parse parses expr in loc (nil ⇒ UTC): fault.Invalid naming expr, the failing field and Grammar, also when the
// expression matches no calendar date.
func Parse(expr string, loc *time.Location) (*Timetable, error) {
	if loc == nil {
		loc = time.UTC
	}
	parts := strings.FieldsFunc(expr, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(parts) > 0 && strings.HasPrefix(parts[0], "@") {
		m, ok := macro(parts[0])
		if !ok || len(parts) != 1 {
			return nil, refuse(expr, "it is not one of the macros")
		}
		parts = strings.Fields(m)
	}
	fields := [...]field{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day-of-month", 1, 31},
		{"month", 1, 12},
		{"day-of-week", 0, 7},
	}
	if len(parts) != len(fields) {
		return nil, refuse(expr, "it has %d fields", len(parts))
	}
	t := &Timetable{loc: loc}
	for i, f := range fields {
		set, err := parseField(parts[i], f, func(format string, a ...any) error {
			return refuse(expr, "the %s field %q: %s", f.name, parts[i], fmt.Sprintf(format, a...))
		})
		if err != nil {
			return nil, err
		}
		t.sets[i] = set
	}
	if t.sets[dowField]&(1<<7) != 0 {
		t.sets[dowField] = t.sets[dowField]&^(1<<7) | 1
	}
	domRestricted := !strings.HasPrefix(parts[domField], "*")
	dowRestricted := !strings.HasPrefix(parts[dowField], "*")
	t.or = domRestricted && dowRestricted
	if !t.or && !t.hasDate() {
		return nil, refuse(expr, "the day-of-month field %q and the month field %q share no calendar date", parts[domField], parts[monthField])
	}
	return t, nil
}

func refuse(expr, format string, a ...any) error {
	return fault.Invalidf("cron.Parse", "%q is not %s: %s", expr, Grammar, fmt.Sprintf(format, a...))
}

// parseField returns the bit set of one field: a comma list of *, n or a-b, where * and a-b may take a step /s.
// bad builds the refusal naming the field.
func parseField(text string, f field, bad func(format string, a ...any) error) (uint64, error) {
	var set uint64
	for item := range strings.SplitSeq(text, ",") {
		rng, stepText, stepped := strings.Cut(item, "/")
		lo, hi := f.min, f.max
		switch a, b, isRange := strings.Cut(rng, "-"); {
		case rng == "*":
		case isRange:
			var err error
			if lo, err = number(a, f, bad); err != nil {
				return 0, err
			}
			if hi, err = number(b, f, bad); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, bad("range %d-%d is reversed", lo, hi)
			}
		case stepped:
			return 0, bad("a step needs * or a range, got %q", item)
		default:
			n, err := number(rng, f, bad)
			if err != nil {
				return 0, err
			}
			lo, hi = n, n
		}
		step := 1
		if stepped {
			s, err := strconv.Atoi(stepText)
			if err != nil || s < 1 || !digits(stepText) {
				return 0, bad("step %q is not a whole number of at least 1", stepText)
			}
			step = s
		}
		for v := lo; ; v += step {
			set |= 1 << v
			if step > hi-v {
				break
			}
		}
	}
	return set, nil
}

func number(s string, f field, bad func(format string, a ...any) error) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || !digits(s) {
		return 0, bad("%q is not a number", s)
	}
	if n < f.min || n > f.max {
		return 0, bad("%d is out of range %d-%d", n, f.min, f.max)
	}
	return n, nil
}

func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// hasDate reports whether some month of the month set has a day of the day-of-month set, 29 February included
// (the AND mode's check; in the OR mode every month contains each weekday).
func (t *Timetable) hasDate() bool {
	daysIn := [...]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	for m := 1; m <= 12; m++ {
		if t.sets[monthField]&(1<<m) == 0 {
			continue
		}
		if t.sets[domField]&(1<<(daysIn[m]+1)-1) != 0 {
			return true
		}
	}
	return false
}

// matchesDay reports whether the calendar date of d (a UTC wall date) is a day of the expression.
func (t *Timetable) matchesDay(d time.Time) bool {
	if t.sets[monthField]&(1<<int(d.Month())) == 0 {
		return false
	}
	dom := t.sets[domField]&(1<<d.Day()) != 0
	dow := t.sets[dowField]&(1<<int(d.Weekday())) != 0
	if t.or {
		return dom || dow
	}
	return dom && dow
}

// Next returns the earliest slot instant strictly after after (Decision 3). It never returns the zero Time for a
// Timetable from Parse.
//
// A slot maps to the first instant whose local time is not earlier than the slot: the slot itself, its first
// occurrence in a fold, the end of the gap in a gap. That map never decreases with the slot, and a slot no later
// than after's local time maps to no later than after, so the answer is the first later slot whose instant is
// after after.
func (t *Timetable) Next(after time.Time) time.Time {
	local := after.In(t.loc)
	y, mo, d := local.Date()
	nowMinute := local.Hour()*60 + local.Minute()
	day := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	for i := range maxScanDays {
		if i > 0 {
			day = day.Add(24 * time.Hour)
		}
		if !t.matchesDay(day) {
			continue
		}
		for h := range 24 {
			if t.sets[hourField]&(1<<h) == 0 {
				continue
			}
			for m := range 60 {
				if t.sets[minuteField]&(1<<m) == 0 || (i == 0 && h*60+m <= nowMinute) {
					continue
				}
				if at := t.instant(day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)); at.After(after) {
					return at
				}
			}
		}
	}
	return time.Time{}
}

// instant maps a wall time, given as a UTC time with the same fields, to the first instant whose local time in the
// zone is not earlier than it. It walks the zone's periods by ZoneBounds from a day and a quarter before, earlier
// than any instant with that local time, and never relies on time.Date's choice in a gap or a fold.
func (t *Timetable) instant(wall time.Time) time.Time {
	at := wall.Add(-30 * time.Hour).In(t.loc)
	for {
		_, offset := at.Zone()
		start, end := at.ZoneBounds()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if !start.IsZero() && candidate.Before(start) {
			return start.In(t.loc)
		}
		if end.IsZero() || candidate.Before(end) {
			return candidate.In(t.loc)
		}
		at = end.In(t.loc)
	}
}
