package restore

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/store"
)

// Point is a parsed restore point (Decision 3): exactly one of Latest, At, Version, Gen and Pin is set. Timeline is
// --timeline: latest, a time and a pin keep only that timeline and those it descends from.
type Point struct {
	Latest   bool
	At       time.Time
	Version  store.Version
	Gen      *backup.GenRef
	Pin      backup.Class
	Timeline string
}

// ParsePoint parses latest, pre-upgrade, verified, an RFC 3339 time, <timeline>/<n> or <timeline>-<n> (a
// resourceVersion); anything else is fault.Invalid.
func ParsePoint(s string) (Point, error) {
	const op = "restore.ParsePoint"
	switch s {
	case "latest":
		return Point{Latest: true}, nil
	case string(backup.PreUpgrade), string(backup.Verified):
		return Point{Pin: backup.Class(s)}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return Point{At: t}, nil
	}
	if timeline, n, ok := strings.Cut(s, "/"); ok {
		num, err := strconv.ParseUint(n, 10, 64)
		if err != nil || !isTimeline(timeline) {
			return Point{}, fault.Invalidf(op, "point %q: want <timeline>/<generation>", s)
		}
		return Point{Gen: &backup.GenRef{Timeline: timeline, Generation: num}}, nil
	}
	if v, err := store.ParseVersion(s); err == nil && v.Timeline != "" {
		return Point{Version: v}, nil
	}
	return Point{}, fault.Invalidf(op, "point %q: want latest, pre-upgrade, verified, an RFC 3339 time, "+
		"<timeline>/<generation> or a resourceVersion <timeline>-<n>", s)
}

func isTimeline(s string) bool {
	v, err := store.ParseVersion(s + "-0")
	return err == nil && v.Timeline == s
}

// Resolve picks the generation p names among gens (Decision 3): none ⇒ fault.NotFound; latest, a time or a pin
// whose candidates come from two timelines neither of which descends from the other ⇒ fault.Conflict naming both.
// A resourceVersion <t>-<n> is the newest complete generation of t below revision n, else t's parent.
func Resolve(gens []Generation, p Point) (Generation, error) {
	const op = "restore.Resolve"
	par := parents(gens)
	switch {
	case p.Gen != nil:
		var found []Generation
		for _, g := range gens {
			if g.Ref() == *p.Gen && g.State != StateIncomplete {
				found = append(found, g)
			}
		}
		if len(found) == 0 {
			return Generation{}, fault.NotFoundf(op, "no complete generation %s/%d", p.Gen.Timeline, p.Gen.Generation)
		}
		return newest(found), nil
	case p.Version.Timeline != "":
		var found []Generation
		for _, g := range gens {
			if g.Manifest.Timeline == p.Version.Timeline && g.State != StateIncomplete && g.Manifest.Revision < p.Version.N {
				found = append(found, g)
			}
		}
		if len(found) > 0 {
			return newest(found), nil
		}
		parent := par[p.Version.Timeline]
		if parent == nil {
			return Generation{}, fault.NotFoundf(op, "no complete generation of timeline %s below %s, and the timeline "+
				"has no parent", p.Version.Timeline, p.Version)
		}
		return Resolve(gens, Point{Gen: parent})
	}
	var found []Generation
	for _, g := range gens {
		switch {
		case g.State != StateComplete && g.State != StateNewer:
		case p.Pin != "" && g.Class != p.Pin:
		case !p.At.IsZero() && time.Time(g.Manifest.At).After(p.At):
		case p.Timeline != "" && !descends(par, p.Timeline, g.Manifest.Timeline):
		default:
			found = append(found, g)
		}
	}
	if len(found) == 0 {
		return Generation{}, fault.NotFoundf(op, "no complete generation matches the point")
	}
	var timelines []string
	for _, g := range found {
		if !slices.Contains(timelines, g.Manifest.Timeline) {
			timelines = append(timelines, g.Manifest.Timeline)
		}
	}
	for i, a := range timelines {
		for _, b := range timelines[i+1:] {
			if !descends(par, a, b) && !descends(par, b, a) {
				return Generation{}, fault.Conflictf(op, "timelines %s and %s both qualify and neither descends from "+
					"the other: name one with --timeline", a, b)
			}
		}
	}
	return newest(found), nil
}

// newest is the generation with the latest at, then the highest number; a ladder or pre-upgrade original before
// its verified copy.
func newest(gens []Generation) Generation {
	return slices.MaxFunc(gens, func(a, b Generation) int {
		return cmp.Or(time.Time(a.Manifest.At).Compare(time.Time(b.Manifest.At)),
			cmp.Compare(a.Manifest.Generation, b.Manifest.Generation),
			cmp.Compare(boolInt(a.Class != backup.Verified), boolInt(b.Class != backup.Verified)))
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
