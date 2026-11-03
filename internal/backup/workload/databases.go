package workload

import (
	"cmp"
	"context"
	"io"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// SQLLoader copies, loads, empties and inspects a database through its engine (ADR-0234). Source streams its SQLite
// file: it fails ErrEngineNotReady for a database with no Ready engine and returns the stream's error, not io.EOF,
// when the engine reports one at its end. Load replaces all its data. HasTables counts tables, views, indexes and
// triggers.
type SQLLoader interface {
	Source(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (io.ReadCloser, error)
	Load(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, file io.Reader) error
	Empty(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
	HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
}

// Databases is the SQLDatabase kind (ADR-0234): sql/<name>/ holds its SQLite file in records of 8 MiB keyed 000001,
// 000002, …, which a Restore loads through the engine of the database as, or of the database itself. load nil is no
// SQL engine wired.
func Databases(st store.Store, load SQLLoader) MemberKind { return databases{st: st, load: load} }

type databases struct {
	st   store.Store
	load SQLLoader
}

func (databases) Kind() v1.Kind { return v1.KindSQLDatabase }

func (k databases) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	const op = "workload.Write"
	if k.load == nil {
		return MemberFile{}, fault.Unavailablef(op, "member %s/%s: no SQL engine is wired", m.Kind, m.Name)
	}
	f, err := w.File(ctx, "sql/"+string(m.Name), func(emit func(snapshot.Record) error) error {
		rc, err := k.load.Source(ctx, ns, m.Name)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "database %s", m.Name)
		}
		return streamFile(op, "database "+string(m.Name), rc, emit)
	})
	if err != nil {
		return MemberFile{}, err
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Files: []backup.StoreFile{f}}, nil
}

func (databases) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 3 || seg[0] != "sql" {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.KindSQLDatabase, Name: v1.ObjectName(seg[1])}, 0, true
}

func (k databases) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	dest := cmp.Or(rm.As, rm.Name)
	if _, err := k.st.Get(ctx, v1.KindSQLDatabase.GVK(), ns, dest); err != nil {
		return nil, err
	}
	return dbDestination{st: k.st, load: k.load, ns: ns, name: dest}, nil
}

type dbDestination struct {
	st   store.Store
	load SQLLoader
	ns   v1.NamespaceName
	name v1.ObjectName
}

func (dbDestination) Prepare(context.Context) (func(), error) { return none, nil }

// IsEmpty reports a database with no table, view, index or trigger and no Function binding it, which closes the race
// between the check and the load (ADR-0234 Decision 3), and a Ready engine to answer.
func (d dbDestination) IsEmpty(ctx context.Context) (bool, error) {
	if d.load == nil {
		return false, fault.Unavailablef("backup.Restore", "no SQL engine loads a database")
	}
	fns, err := d.st.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: d.ns})
	if err != nil {
		return false, err
	}
	for _, o := range fns.Items {
		if fn, ok := o.(*v1.Function); ok && slices.ContainsFunc(fn.Spec.Bindings.SQL, func(b v1.FunctionSQL) bool { return b.Database == d.name }) {
			return false, nil
		}
	}
	has, err := d.load.HasTables(ctx, d.ns, d.name)
	return !has, err
}

// Load streams the file's records to the engine's load, which replaces all the database's data.
func (d dbDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if d.load == nil {
		return fault.Unavailablef("backup.Restore", "no SQL engine loads a database")
	}
	return d.load.Load(ctx, d.ns, d.name, &recordReader{next: open(ctx, f.Files[0])})
}

func (d dbDestination) Clear(ctx context.Context) error {
	if d.load == nil {
		return nil
	}
	return d.load.Empty(ctx, d.ns, d.name)
}
