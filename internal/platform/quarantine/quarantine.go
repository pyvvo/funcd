// Package quarantine is the data no object names that a start-up reclaim moves aside instead of deleting (ADR-0288):
// an entry per moved object, dated by its move, and the port each store's quarantine implements. Each store keeps its
// entries beside its data: the KV substrate's keys, the blob store's Buckets, the SQL directories, the Function
// volumes.
package quarantine

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// State is an entry's state (Decision 2). A copy move writes Moving before its first copy and Kept after it deleted
// the source; a rename move writes none; a return writes Returning first. A missing state reads Kept.
type State string

// The states of Decision 2.
const (
	Moving    State = "moving"
	Kept      State = "kept"
	Returning State = "returning"
)

// ParseState reads a stored state: empty, the missing state, is Kept.
func ParseState(s string) (State, error) {
	switch st := State(strings.TrimSpace(s)); st {
	case "":
		return Kept, nil
	case Moving, Kept, Returning:
		return st, nil
	default:
		return "", fault.Invalidf("quarantine.ParseState", "state %q: want %s, %s or %s", s, Moving, Kept, Returning)
	}
}

// Ref names the object an entry holds the data of: a volume's Function, with Volume its volume, else "".
type Ref struct {
	v1.ObjectRef
	Volume v1.ObjectName
}

// String is <kind> <ns>/<name>, a volume's with /<volume>.
func (r Ref) String() string {
	s := string(r.Kind) + " " + string(r.Namespace) + "/" + string(r.Name)
	if r.Volume != "" {
		s += "/" + string(r.Volume)
	}
	return s
}

// Entry is one object's quarantined data: Since is when its move began, to the ms.
type Entry struct {
	Ref
	Since time.Time
	State State
}

// Until is when the entry expires: Since plus the period (Decision 4).
func (e Entry) Until(period time.Duration) time.Time { return e.Since.Add(period) }

// Expired reports whether the sweep deletes the entry at cutoff, now less the period: only a Kept entry that old
// (Decisions 3 and 5).
func (e Entry) Expired(cutoff time.Time) bool { return e.State == Kept && e.Since.Before(cutoff) }

// Store is one kind's quarantine.
type Store interface {
	// Move moves ref's data into a new entry dated now, or resumes ref's Moving entry.
	Move(ctx context.Context, ref Ref, now time.Time) (Entry, error)
	// List returns the entries, an entry with neither data nor state skipped.
	List(ctx context.Context) ([]Entry, error)
	// Expire deletes each entry Expired at cutoff, and each entry with neither data nor state, and returns the first.
	Expire(ctx context.Context, cutoff time.Time) ([]Entry, error)
}

// Returner is a Store whose entries `funcdctl quarantine return` brings back: KVStore, Bucket and Function volume.
// Return moves ref's Kept entry dated since, the newest when since is zero, back into the data of ref's object:
// fault.NotFound without such an entry, fault.Conflict when that data is not empty. A Returning entry resumes, its
// emptiness check skipped. That the object exists is the caller's check (Decision 6).
type Returner interface {
	Return(ctx context.Context, ref Ref, since time.Time) error
}

const stampLayout = "20060102T150405.000Z"

// Stamp is t in UTC, to the ms: an entry's <since> in its key or path (Decision 2).
func Stamp(t time.Time) string { return t.UTC().Format(stampLayout) }

// ParseStamp reads a Stamp.
func ParseStamp(s string) (time.Time, error) {
	t, err := time.Parse(stampLayout, s)
	if err != nil {
		return time.Time{}, fault.Invalidf("quarantine.ParseStamp", "since %q: want %s", s, stampLayout)
	}
	return t, nil
}

// Pick is the entry Return takes among ref's entries: the one dated since, else the newest. Returning wins, as a
// cut return resumes; fault.NotFound when none.
func Pick(entries []Entry, ref Ref, since time.Time) (Entry, error) {
	var found []Entry
	for _, e := range entries {
		if e.Ref == ref && (since.IsZero() || e.Since.Equal(since)) && e.State != Moving {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		return Entry{}, fault.NotFoundf("quarantine.Return", "no quarantined data of %s", ref)
	}
	slices.SortFunc(found, func(a, b Entry) int { return a.Since.Compare(b.Since) })
	for _, e := range found {
		if e.State == Returning {
			return e, nil
		}
	}
	return found[len(found)-1], nil
}
