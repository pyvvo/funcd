package backup

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// The layout under the target root (Decision 2).
const (
	genPrefix    = "gen/"
	probePrefix  = "probe/"
	manifestName = "manifest.yaml"
	refusedName  = "refused"
	partBytes    = 8 << 20
)

// GenDir is a generation's directory, gen/<class>/<n>-<timeline>/, n in 10 decimal digits.
func GenDir(class Class, n uint64, timeline string) string {
	return fmt.Sprintf("%s%s/%010d-%s/", genPrefix, class, n, timeline)
}

// object is one key under gen/, with the generation its directory names.
type object struct {
	key      string
	class    Class
	n        uint64
	timeline string
	// name is the key below the generation's directory.
	name    string
	modTime time.Time
}

func parseKey(a blob.Attributes) (object, bool) {
	rest, ok := strings.CutPrefix(a.Key, genPrefix)
	if !ok {
		return object{}, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || parts[0] == "" {
		return object{}, false
	}
	num, timeline, ok := strings.Cut(parts[1], "-")
	if !ok || timeline == "" {
		return object{}, false
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return object{}, false
	}
	return object{key: a.Key, class: Class(parts[0]), n: n, timeline: timeline, name: parts[2], modTime: a.ModTime}, true
}

// listObjects lists gen/ once.
func listObjects(ctx context.Context, b blob.Bucket) ([]object, error) {
	items, err := b.List(ctx, genPrefix)
	if err != nil {
		return nil, err
	}
	objs := make([]object, 0, len(items))
	for _, a := range items {
		if o, ok := parseKey(a); ok {
			objs = append(objs, o)
		}
	}
	return objs, nil
}

// List lists gen/ once, a generation per entry, by generation then timeline then class; no lock, no probe.
func List(ctx context.Context, b blob.Bucket) ([]Entry, error) {
	objs, err := listObjects(ctx, b)
	if err != nil {
		return nil, err
	}
	return entries(objs), nil
}

func entries(objs []object) []Entry {
	type id struct {
		class    Class
		n        uint64
		timeline string
	}
	byID := map[id]*Entry{}
	for _, o := range objs {
		k := id{o.class, o.n, o.timeline}
		e, ok := byID[k]
		if !ok {
			e = &Entry{Generation: o.n, Timeline: o.timeline, Class: o.class}
			byID[k] = e
		}
		if o.name == manifestName {
			e.Complete, e.At = true, o.modTime
		}
	}
	out := make([]Entry, 0, len(byID))
	for _, e := range byID {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		return cmp.Or(cmp.Compare(a.Generation, b.Generation), strings.Compare(a.Timeline, b.Timeline),
			strings.Compare(string(a.Class), string(b.Class)))
	})
	return out
}

// fence is a run's second-writer check (Decision 4): its timeline, the generation a restore loaded, and the n it
// writes.
type fence struct {
	timeline string
	parent   *GenRef
	n        uint64
}

// other returns another timeline's key above the run's last, "" when none. The last is the run's highest complete
// n, else the parent's n, else 0; a parent key counts only from the run's first n, its lowest own n, else the n it
// writes: the keys below predate the restore.
func (f fence) other(objs []object) string {
	last, first, complete := uint64(0), f.n, false
	for _, o := range objs {
		if o.timeline != f.timeline {
			continue
		}
		first = min(first, o.n)
		if o.name == manifestName {
			last, complete = max(last, o.n), true
		}
	}
	if !complete && f.parent != nil {
		last = f.parent.Generation
	}
	for _, o := range objs {
		switch {
		case o.timeline == f.timeline:
		case f.parent != nil && o.timeline == f.parent.Timeline && o.n < first:
		case o.n > last:
			return o.key
		}
	}
	return ""
}

// complete reports a complete generation of the run's own timeline.
func (f fence) complete(objs []object) bool {
	return slices.ContainsFunc(objs, func(o object) bool { return o.timeline == f.timeline && o.name == manifestName })
}

// atN returns a key at the run's n that the run did not put, "" when none.
func (f fence) atN(objs []object, put map[string]bool) string {
	for _, o := range objs {
		if o.n == f.n && !put[o.key] {
			return o.key
		}
	}
	return ""
}

// ClassFor is a ladder run's class (Decision 6): weekly when weekly is kept and no complete weekly manifest is in
// now's ISO week (UTC); else daily when daily is kept and no complete daily or weekly one is in now's UTC day; else
// hourly. Pins never count.
func ClassFor(entries []Entry, now time.Time, r Retention) Class {
	now = now.UTC()
	year, week := now.ISOWeek()
	day := now.Truncate(24 * time.Hour)
	var thisWeek, today bool
	for _, e := range entries {
		if !e.Complete || (e.Class != Weekly && e.Class != Daily) {
			continue
		}
		at := e.At.UTC()
		if y, w := at.ISOWeek(); e.Class == Weekly && y == year && w == week {
			thisWeek = true
		}
		if at.Truncate(24 * time.Hour).Equal(day) {
			today = true
		}
	}
	switch {
	case r.Weekly > 0 && !thisWeek:
		return Weekly
	case r.Daily > 0 && !today:
		return Daily
	default:
		return Hourly
	}
}

// LifecycleRules is the expiry in days the operator sets per prefix (Decision 6): a class unused, or Verified 0,
// has none.
func LifecycleRules(r Retention) map[string]int {
	rules := map[string]int{
		genPrefix + string(Hourly) + "/": (r.Hourly + 23) / 24,
		probePrefix:                      1,
	}
	if r.Daily > 0 {
		rules[genPrefix+string(Daily)+"/"] = r.Daily
	}
	if r.Weekly > 0 {
		rules[genPrefix+string(Weekly)+"/"] = 7 * r.Weekly
	}
	if r.Verified > 0 {
		rules[genPrefix+string(Verified)+"/"] = r.Verified
	}
	return rules
}

// writeRecord frames r as uvarint(len key) ‖ key ‖ uvarint(len value) ‖ value (Decision 2).
func writeRecord(w io.Writer, r snapshot.Record) error {
	var hdr [binary.MaxVarintLen64]byte
	for _, field := range [][]byte{r.Key, r.Value} {
		if _, err := w.Write(binary.AppendUvarint(hdr[:0], uint64(len(field)))); err != nil {
			return err
		}
		if _, err := w.Write(field); err != nil {
			return err
		}
	}
	return nil
}

// Records reads the framing of Decision 2: io.EOF at the end, a torn record fault.Invalid.
func Records(r io.Reader) func() (snapshot.Record, error) {
	br := bufio.NewReader(r)
	return func() (snapshot.Record, error) {
		key, err := readField(br, true)
		if err != nil {
			return snapshot.Record{}, err
		}
		value, err := readField(br, false)
		if err != nil {
			return snapshot.Record{}, err
		}
		return snapshot.Record{Key: key, Value: value}, nil
	}
}

// readField reads one length-prefixed field; io.EOF before a record's first byte is the end.
func readField(br *bufio.Reader, first bool) ([]byte, error) {
	const op = "backup.Records"
	n, err := binary.ReadUvarint(br)
	switch {
	case first && errors.Is(err, io.EOF):
		return nil, io.EOF
	case err != nil:
		return nil, fault.Wrapf(err, fault.Invalid, op, "torn record length")
	case n > math.MaxInt64:
		return nil, fault.Invalidf(op, "record length %d is out of range", n)
	}
	var buf bytes.Buffer
	if got, err := io.CopyN(&buf, br, int64(n)); got < int64(n) {
		return nil, fault.Wrapf(cmp.Or(err, io.ErrUnexpectedEOF), fault.Invalid, op, "torn record: %d of %d bytes", got, n)
	}
	return buf.Bytes(), nil
}
