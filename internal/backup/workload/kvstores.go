package workload

import (
	"cmp"
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// KVLoader is the KV driver's per-store load, teardown and listing (ADR-0222 LoadPrefix, ADR-0072 DropPrefix);
// LoadPrefix refuses Conflict when the store holds keys.
type KVLoader interface {
	LoadPrefix(ctx context.Context, prefix string, next func() (snapshot.Record, error)) error
	DropPrefix(prefix string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

// KVStores is the KVStore kind: kv/<store>/ holds the driver's export of the store (ADR-0209 SnapshotPrefix; nil when
// the driver offers none), which a Restore loads with load into the KVStore as, or into the store itself, emptied
// first.
func KVStores(st store.Store, export func(prefix string) snapshot.Source, load KVLoader) MemberKind {
	return kvStores{st: st, export: export, load: load}
}

type kvStores struct {
	st     store.Store
	export func(prefix string) snapshot.Source
	load   KVLoader
}

func (kvStores) Kind() v1.Kind { return v1.KindKVStore }

func (k kvStores) Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error) {
	if k.export == nil {
		return MemberFile{}, fault.Unavailablef("workload.Write", "member %s/%s: the KV driver exports no store", m.Kind, m.Name)
	}
	f, err := w.File(ctx, "kv/"+string(m.Name), func(emit func(snapshot.Record) error) error {
		_, err := k.export(KVPrefix(ns, m.Name)).Snapshot(ctx, emit)
		return err
	})
	if err != nil {
		return MemberFile{}, err
	}
	return MemberFile{Kind: m.Kind, Name: m.Name, Files: []backup.StoreFile{f}}, nil
}

func (kvStores) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 3 || seg[0] != "kv" {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: v1.KindKVStore, Name: v1.ObjectName(seg[1])}, 0, true
}

func (k kvStores) Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) {
	dest := cmp.Or(rm.As, rm.Name)
	if _, err := k.st.Get(ctx, v1.KindKVStore.GVK(), ns, dest); err != nil {
		return nil, err
	}
	return kvDestination{load: k.load, prefix: KVPrefix(ns, dest), inPlace: rm.InPlace}, nil
}

type kvDestination struct {
	load    KVLoader
	prefix  string
	inPlace bool
}

func (kvDestination) Prepare(context.Context) (func(), error) { return none, nil }

func (d kvDestination) IsEmpty(ctx context.Context) (bool, error) {
	if d.load == nil {
		return false, fault.Unavailablef("backup.Restore", "the KV driver loads no store")
	}
	keys, err := d.load.List(ctx, d.prefix)
	return len(keys) == 0, err
}

func (d kvDestination) Load(ctx context.Context, f MemberFile, open RecordOpener) error {
	if d.inPlace {
		if err := d.Clear(ctx); err != nil {
			return err
		}
	}
	if d.load == nil {
		return fault.Unavailablef("backup.Restore", "the KV driver loads no store")
	}
	return notEmpty(d.load.LoadPrefix(ctx, d.prefix, open(ctx, f.Files[0])))
}

func (d kvDestination) Clear(context.Context) error {
	if d.load == nil {
		return nil
	}
	return d.load.DropPrefix(d.prefix)
}
