package provider

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/durable"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
)

// QuarantineDir is the quarantine under a storage root (ADR-0288 Decision 2): no DNS label, so a listing of the
// engines' namespaces skips it.
const QuarantineDir = ".quarantine"

// dirQuarantine is the quarantine of one kind's engine directories: an entry is the directory itself, renamed to
// <root>/<ns>/<name>/<since>, kept as soon as it exists (a rename move writes no state).
type dirQuarantine struct {
	root string
	kind v1.Kind
	dir  func(ProviderRef) string
	busy func(context.Context, ProviderRef) (bool, error)
}

// NewDirQuarantine is the quarantine of kind's engine directories under root, for a Volumes implementation: dir is
// an engine's directory, busy whether an engine of it still runs. root "" is no durable storage: nothing to move.
func NewDirQuarantine(root string, kind v1.Kind, dir func(ProviderRef) string,
	busy func(context.Context, ProviderRef) (bool, error)) quarantine.Store {
	return dirQuarantine{root: root, kind: kind, dir: dir, busy: busy}
}

// Quarantine is the quarantine of the runtime's engine directories, <storage root>/.quarantine/<lower(kind)>
// (ADR-0288); a directory is busy while a container of its engine exists, as for Reclaim.
func (r *engineRuntime) Quarantine() quarantine.Store {
	root := ""
	if r.storageRoot != "" {
		root = filepath.Join(r.storageRoot, QuarantineDir, strings.ToLower(string(r.ownerKind)))
	}
	return NewDirQuarantine(root, r.ownerKind, r.storageDir, func(ctx context.Context, ref ProviderRef) (bool, error) {
		insts, err := r.namedInstances(ctx, ref)
		return len(insts) > 0, err
	})
}

// Move renames ref's directory into a new entry dated now, in one rename: the entry's parents made and synced, then
// both parent directories synced. Conflict while ref's engine runs, as Reclaim; NotFound without a directory.
func (q dirQuarantine) Move(ctx context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	const op = "provider.Quarantine"
	if ref.Kind != q.kind {
		return quarantine.Entry{}, fault.Invalidf(op, "%s: this quarantine keeps %s directories", ref, q.kind)
	}
	pref := ProviderRef{Namespace: ref.Namespace, Name: ref.Name}
	if err := pref.Namespace.Validate(); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Invalid, op, "namespace")
	}
	if err := pref.Name.Validate(); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Invalid, op, "name")
	}
	if q.root == "" {
		return quarantine.Entry{}, fault.NotFoundf(op, "%s: durable engine storage is not configured", ref)
	}
	busy, err := q.busy(ctx, pref)
	if err != nil {
		return quarantine.Entry{}, err
	}
	if busy {
		return quarantine.Entry{}, fault.Conflictf(op, "the engine of %s still runs; tear it down first", ref)
	}
	src := q.dir(pref)
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return quarantine.Entry{}, fault.NotFoundf(op, "%s has no directory", ref)
	} else if err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Internal, op, "inspect the directory of %s", ref)
	}
	since := now.UTC().Truncate(time.Millisecond)
	parent := filepath.Join(q.root, string(ref.Namespace), string(ref.Name))
	dst := filepath.Join(parent, quarantine.Stamp(since))
	var disk durable.Disk
	if err := disk.MkdirAll(parent, 0o700); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Internal, op, "create the quarantine of %s", ref)
	}
	if _, err := os.Lstat(dst); err == nil {
		return quarantine.Entry{}, fault.Conflictf(op, "%s already has an entry dated %s", ref, quarantine.Stamp(since))
	}
	if err := disk.Rename(src, dst); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.Internal, op, "move the directory of %s", ref)
	}
	for _, d := range []string{filepath.Dir(src), parent} {
		if err := disk.SyncDir(d); err != nil {
			return quarantine.Entry{}, fault.Wrapf(err, fault.Internal, op, "sync the move of %s", ref)
		}
	}
	return quarantine.Entry{Ref: q.ref(ref.Namespace, ref.Name), Since: since, State: quarantine.Kept}, nil
}

func (q dirQuarantine) ref(ns v1.NamespaceName, name v1.ObjectName) quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: q.kind, Namespace: ns, Name: name}}
}

// entry is one entry's directory and whether it holds anything.
type entry struct {
	quarantine.Entry
	dir  string
	data bool
}

// scan reads every <ns>/<name>/<since> under the quarantine, sorted; a name that is no stamp is not an entry.
func (q dirQuarantine) scan() ([]entry, error) {
	const op = "provider.Quarantine"
	var out []entry
	if q.root == "" {
		return nil, nil
	}
	nss, err := os.ReadDir(q.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine")
	}
	for _, ns := range nss {
		names, err := os.ReadDir(filepath.Join(q.root, ns.Name()))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine of namespace %q", ns.Name())
		}
		for _, n := range names {
			stamps, err := os.ReadDir(filepath.Join(q.root, ns.Name(), n.Name()))
			if err != nil {
				return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine of %s/%s", ns.Name(), n.Name())
			}
			for _, s := range stamps {
				since, err := quarantine.ParseStamp(s.Name())
				if err != nil || !s.IsDir() {
					continue
				}
				dir := filepath.Join(q.root, ns.Name(), n.Name(), s.Name())
				held, err := os.ReadDir(dir)
				if err != nil {
					return nil, fault.Wrapf(err, fault.Internal, op, "read the entry %s", dir)
				}
				out = append(out, entry{Entry: quarantine.Entry{Ref: q.ref(v1.NamespaceName(ns.Name()), v1.ObjectName(n.Name())),
					Since: since, State: quarantine.Kept}, dir: dir, data: len(held) > 0})
			}
		}
	}
	slices.SortFunc(out, func(a, b entry) int {
		return cmpEntry(a.Entry, b.Entry)
	})
	return out, nil
}

func cmpEntry(a, b quarantine.Entry) int {
	if c := strings.Compare(a.String(), b.String()); c != 0 {
		return c
	}
	return a.Since.Compare(b.Since)
}

// List returns the entries; an empty entry directory holds no data and is skipped.
func (q dirQuarantine) List(context.Context) ([]quarantine.Entry, error) {
	es, err := q.scan()
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range es {
		if e.data {
			out = append(out, e.Entry)
		}
	}
	return out, nil
}

// Expire removes each entry Expired at cutoff and each empty one, then the parents it left empty, and returns the
// first.
func (q dirQuarantine) Expire(_ context.Context, cutoff time.Time) ([]quarantine.Entry, error) {
	const op = "provider.Quarantine"
	es, err := q.scan()
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	for _, e := range es {
		expired := e.Expired(cutoff)
		if !expired && e.data {
			continue
		}
		if err := os.RemoveAll(e.dir); err != nil {
			return out, fault.Wrapf(err, fault.Internal, op, "remove the entry %s", e.dir)
		}
		for d := filepath.Dir(e.dir); d != q.root; d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
		if expired {
			out = append(out, e.Entry)
		}
	}
	return out, nil
}
