package workload

import (
	"context"
	"errors"
	"io"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// MemberKind is one kind of backup member (ADR-0311 Decision 1): it writes its members' files under its own directory
// of a copy, names the member of each key there for the listing, and resolves a Restore member's destination. Its
// provider hands it over, so the backup service names no kind.
type MemberKind interface {
	Kind() v1.Kind // the kind its members carry in Member, MemberFile, BackupMember and RestoreMember
	Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error)
	// Parse names the member of an object key's segments under a copy's directory, and its attempt (a Lake's; else 0).
	Parse(seg []string) (m v1.BackupMember, attempt int, ok bool)
	Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) // NotFound if none
}

// Destination is where a Restore loads one member (Decision 3).
type Destination interface {
	Prepare(ctx context.Context) (release func(), err error)         // after the plan, before the first load
	IsEmpty(ctx context.Context) (bool, error)                       // asked only when not in place
	Load(ctx context.Context, f MemberFile, open RecordOpener) error // a refusal wraps ErrNotEmpty or ErrFileCount, writing nothing
	Clear(ctx context.Context) error                                 // after a failed Restore whose load began here
}

// FileWriter writes one file of a copy: its records framed, sealed, hashed and cut in parts.
type FileWriter interface {
	File(ctx context.Context, name string, produce func(emit func(snapshot.Record) error) error) (backup.StoreFile, error)
}

// RecordOpener reads a file of the copy a Restore loads, as Records does.
type RecordOpener func(ctx context.Context, f backup.StoreFile) func() (snapshot.Record, error)

// The failures a Restore names by reason (Decision 5), which the service's reasons cannot be imported here for.
var (
	ErrNotEmpty   = errors.New("the destination holds data")       // reason NotEmpty
	ErrTornObject = errors.New("an object record's frame is torn") // reason Integrity
	ErrFileCount  = errors.New("the member lists another number of files than its kind writes")
)

// marked is err marked with a sentinel for errors.Is; its message and fault kind stay err's.
type marked struct {
	error
	sentinel error
}

func (m marked) Is(target error) bool { return target == m.sentinel }

func (m marked) Unwrap() error { return m.error }

// notEmpty marks a loader's Conflict, its refusal of a destination that holds data, with ErrNotEmpty.
func notEmpty(err error) error {
	if fault.KindOf(err) == fault.Conflict {
		return marked{err, ErrNotEmpty}
	}
	return err
}

// wantFiles is fault.Invalid naming f's member, marked with ErrFileCount, when its manifest entry lists fewer than least or
// more than most files, the range its kind's Write produces. A load asks it first, before it empties or writes anything.
func wantFiles(f MemberFile, least, most int) error {
	n := len(f.Files)
	if n >= least && n <= most {
		return nil
	}
	member := string(f.Name)
	if f.Volume != "" {
		member += "/" + string(f.Volume)
	}
	return marked{fault.Invalidf("backup.Restore", "member %s %s: the manifest lists %d files, and its kind writes %d to %d",
		f.Kind, member, n, least, most), ErrFileCount}
}

// loadObjects puts each object record of f's records through put, in order; a torn frame is ErrTornObject.
func loadObjects(ctx context.Context, f backup.StoreFile, open RecordOpener, put func(key string, body []byte, o blob.PutOptions) error) error {
	next := open(ctx, f)
	for {
		rec, err := next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		ct, md, body, err := DecodeObject(rec.Value)
		if err != nil {
			return marked{err, ErrTornObject}
		}
		if err := put(string(rec.Key), body, blob.PutOptions{ContentType: ct, Metadata: md}); err != nil {
			return err
		}
	}
}

// recordReader reads the values of a file's records in order, as one stream: a database's SQLite file, a volume's tar.
type recordReader struct {
	next func() (snapshot.Record, error)
	cur  []byte
}

func (r *recordReader) Read(p []byte) (int, error) {
	for len(r.cur) == 0 {
		rec, err := r.next()
		if err != nil {
			return 0, err
		}
		r.cur = rec.Value
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}

// none is the release of a Prepare that holds nothing.
func none() {}
