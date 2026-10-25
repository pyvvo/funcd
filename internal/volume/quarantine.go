package volume

import (
	"context"
	"errors"
	"io"
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

// An entry of the volume quarantine (ADR-0288 Decision 2) is <root>/.quarantine/volume/<ns>/<function>/<volume>/<since>/:
// the leaf renamed to data/, then its meta to meta, and the state file a return writes. A crash between the two
// renames leaves the meta beside no leaf, which the next move puts into the entry. ".quarantine" is no DNS label, so
// the scan skips it; the leaf keeps its quota project, which freeProject skips while a file is charged to it.
const (
	quarantineDir = ".quarantine"
	entryData     = "data"
	entryMeta     = "meta"
	entryState    = "state"
)

// Quarantined is one entry as the disk holds it.
type Quarantined struct {
	ID    ID
	Since time.Time
	// State is the state file's; without one, Moving while the meta has not joined the data, else Kept; "" with
	// neither data nor state, which is no entry: List's callers skip it and the sweep removes it.
	State quarantine.State
	Data  bool // the leaf is there
}

// Quarantine keeps the local volumes a start-up reclaim moves aside instead of releasing them (ADR-0288).
type Quarantine interface {
	// Move renames id's leaf, then its meta, into a new entry dated now and frees its booking; a meta whose leaf a cut
	// move took joins that entry. NotFound with neither; a meta with neither a leaf nor an entry holds no data and goes.
	Move(ctx context.Context, id ID, now time.Time) (since time.Time, err error)
	List(ctx context.Context) ([]Quarantined, error)
	// Mark writes the entry's state, durably.
	Mark(ctx context.Context, id ID, since time.Time, s quarantine.State) error
	// Tar streams the entry's data as Tar streams a leaf.
	Tar(ctx context.Context, id ID, since time.Time) (io.ReadCloser, error)
	// Remove deletes the entry: its state first, so a cut removal leaves a kept copy and never a returning entry
	// without data; then its data and meta.
	Remove(ctx context.Context, id ID, since time.Time) error
}

type volumeQuarantine struct{ m *manager }

func (m *manager) Quarantine() Quarantine { return volumeQuarantine{m} }

func (q volumeQuarantine) idDir(id ID) string {
	return filepath.Join(q.m.o.Root, quarantineDir, "volume", string(id.Namespace), string(id.Function), string(id.Name))
}

func (q volumeQuarantine) entryDir(id ID, since time.Time) string {
	return filepath.Join(q.idDir(id), quarantine.Stamp(since))
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (q volumeQuarantine) Move(_ context.Context, id ID, now time.Time) (time.Time, error) {
	const op = "volume.Quarantine"
	m := q.m
	if err := m.validID(op, id); err != nil {
		return time.Time{}, err
	}
	if id.Worker != "" {
		return time.Time{}, fault.Invalidf(op, "volume %s is an emptyDir: it is released, never quarantined", id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	leaf, meta := m.leaf(id), m.metaPath(id)
	hasLeaf, err := exists(leaf)
	if err != nil {
		return time.Time{}, fault.Wrapf(err, fault.Internal, op, "inspect volume %s", id)
	}
	hasMeta, err := exists(meta)
	if err != nil {
		return time.Time{}, fault.Wrapf(err, fault.Internal, op, "inspect the meta of volume %s", id)
	}
	var dir string
	var since time.Time
	switch {
	case hasLeaf:
		since = now.UTC().Truncate(time.Millisecond)
		dir = q.entryDir(id, since)
		if found, _ := exists(dir); found {
			return time.Time{}, fault.Conflictf(op, "volume %s already has an entry dated %s", id, quarantine.Stamp(since))
		}
		if err := q.rename(op, id, leaf, dir, entryData); err != nil {
			return time.Time{}, err
		}
	case hasMeta:
		entries, err := q.scanID(id)
		if err != nil {
			return time.Time{}, err
		}
		i := slices.IndexFunc(entries, func(e Quarantined) bool { return e.Data && e.State == quarantine.Moving })
		if i < 0 {
			if err := m.release(op, id); err != nil {
				return time.Time{}, err
			}
			return time.Time{}, fault.NotFoundf(op, "volume %s has a meta but no data", id)
		}
		since = entries[i].Since
		dir = q.entryDir(id, since)
	default:
		return time.Time{}, fault.NotFoundf(op, "volume %s has no leaf", id)
	}
	if hasMeta {
		if err := q.rename(op, id, meta, dir, entryMeta); err != nil {
			return time.Time{}, err
		}
	}
	delete(m.bookings, id)
	return since, nil
}

// rename moves src to dir/name, dir made and synced, then syncs both directories.
func (q volumeQuarantine) rename(op string, id ID, src, dir, name string) error {
	var disk durable.Disk
	if err := disk.MkdirAll(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the quarantine entry of volume %s", id)
	}
	if err := disk.Rename(src, filepath.Join(dir, name)); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "move volume %s into the quarantine", id)
	}
	for _, d := range []string{filepath.Dir(src), dir} {
		if err := disk.SyncDir(d); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "sync the move of volume %s", id)
		}
	}
	return nil
}

func (q volumeQuarantine) List(context.Context) ([]Quarantined, error) {
	const op = "volume.Quarantine"
	base := filepath.Join(q.m.o.Root, quarantineDir, "volume")
	var out []Quarantined
	nss, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the volume quarantine")
	}
	for _, ns := range nss {
		fns, err := os.ReadDir(filepath.Join(base, ns.Name()))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine of namespace %s", ns.Name())
		}
		for _, fn := range fns {
			vols, err := os.ReadDir(filepath.Join(base, ns.Name(), fn.Name()))
			if err != nil {
				return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine of %s/%s", ns.Name(), fn.Name())
			}
			for _, vol := range vols {
				id := ID{Namespace: v1.NamespaceName(ns.Name()), Function: v1.ObjectName(fn.Name()), Name: v1.ObjectName(vol.Name())}
				es, err := q.scanID(id)
				if err != nil {
					return nil, err
				}
				out = append(out, es...)
			}
		}
	}
	return out, nil
}

// scanID reads id's entries, oldest first; a name that is no stamp is no entry.
func (q volumeQuarantine) scanID(id ID) ([]Quarantined, error) {
	const op = "volume.Quarantine"
	stamps, err := os.ReadDir(q.idDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the quarantine of volume %s", id)
	}
	var out []Quarantined
	for _, s := range stamps {
		since, err := quarantine.ParseStamp(s.Name())
		if err != nil || !s.IsDir() {
			continue
		}
		dir := filepath.Join(q.idDir(id), s.Name())
		data, err := exists(filepath.Join(dir, entryData))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "read the entry %s of volume %s", s.Name(), id)
		}
		hasMeta, err := exists(filepath.Join(dir, entryMeta))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "read the entry %s of volume %s", s.Name(), id)
		}
		e := Quarantined{ID: id, Since: since, Data: data}
		b, err := os.ReadFile(filepath.Join(dir, entryState))
		switch {
		case err == nil:
			if e.State, err = quarantine.ParseState(string(b)); err != nil {
				return nil, fault.Wrapf(err, fault.Invalid, op, "the entry %s of volume %s", s.Name(), id)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fault.Wrapf(err, fault.Internal, op, "read the state of entry %s of volume %s", s.Name(), id)
		case !data:
		case !hasMeta:
			e.State = quarantine.Moving
		default:
			e.State = quarantine.Kept
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Quarantined) int { return a.Since.Compare(b.Since) })
	return out, nil
}

func (q volumeQuarantine) Mark(_ context.Context, id ID, since time.Time, s quarantine.State) error {
	const op = "volume.Quarantine"
	if err := q.m.validID(op, id); err != nil {
		return err
	}
	dir := q.entryDir(id, since)
	if found, err := exists(dir); err != nil || !found {
		return fault.NotFoundf(op, "volume %s has no entry dated %s", id, quarantine.Stamp(since))
	}
	return durable.Disk{}.WriteFile(dir, entryState, []byte(string(s)+"\n"), 0o600)
}

func (q volumeQuarantine) Tar(ctx context.Context, id ID, since time.Time) (io.ReadCloser, error) {
	const op = "volume.Quarantine"
	if err := q.m.validID(op, id); err != nil {
		return nil, err
	}
	return tarDir(ctx, op, filepath.Join(q.entryDir(id, since), entryData), id)
}

func (q volumeQuarantine) Remove(_ context.Context, id ID, since time.Time) error {
	const op = "volume.Quarantine"
	if err := q.m.validID(op, id); err != nil {
		return err
	}
	dir := q.entryDir(id, since)
	if err := os.Remove(filepath.Join(dir, entryState)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove the state of volume %s's entry", id)
	}
	var disk durable.Disk
	if err := disk.SyncDir(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "sync volume %s's entry", id)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "remove volume %s's entry", id)
	}
	for d := filepath.Dir(dir); strings.HasPrefix(d, filepath.Join(q.m.o.Root, quarantineDir)+string(filepath.Separator)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}
