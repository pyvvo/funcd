// Package volume keeps the directories Functions own (ADR-0262): one leaf per volume under a root, with a hard size
// from a Limiter, a booking in one in-process allocator, and a meta file beside each leaf, outside every mount.
package volume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ID names one volume: a Function's local volume, or with Worker set one worker's emptyDir.
type ID struct {
	Namespace v1.NamespaceName
	Function  v1.ObjectName
	Name      v1.ObjectName
	Worker    string // "" for a local volume
}

func (id ID) String() string {
	s := fmt.Sprintf("%s/%s/%s", id.Namespace, id.Function, id.Name)
	if id.Worker != "" {
		s += "@" + id.Worker
	}
	return s
}

// Info is a volume as its meta records it.
type Info struct {
	ID        ID
	Owner     v1.UID // "" ⇒ retained: no Function holds it
	SizeBytes int64
	Deletion  v1.DeletionPolicy
}

// Manager manages the volumes of one daemon (ADR-0262 Contracts).
type Manager interface {
	// Book records sizeBytes for id under the allocator's lock, with no leaf; VolumeCapacity or VolumeBudget
	// (fault.Conflict) when it does not fit, Invalid when id is booked or stored with another size. Idempotent for
	// the same id and size.
	Book(ctx context.Context, id ID, sizeBytes int64) error
	// Reserve creates or checks id's leaf and sets its quota against its booking (booking it first when it has
	// none); StoreNotOwned when the meta names another UID. deletion is recorded for the delete path.
	Reserve(ctx context.Context, id ID, owner v1.UID, sizeBytes int64, deletion v1.DeletionPolicy) (hostPath string, err error)
	// Adopt stamps owner on a retained local volume; "" retains it; Conflict while another live Function holds it.
	Adopt(ctx context.Context, id ID, owner v1.UID) error
	Release(ctx context.Context, id ID) error              // removes the leaf and frees its booking
	Retained(ctx context.Context) ([]ID, error)            // local volumes with no owner
	List(ctx context.Context) ([]Info, error)              // every volume with a leaf, local and emptyDir
	Tar(ctx context.Context, id ID) (io.ReadCloser, error) // the backup member stream
	Untar(ctx context.Context, id ID, r io.Reader) error   // Conflict when the leaf is not empty
	Empty(ctx context.Context, id ID) (bool, error)        // the leaf holds no entry; NotFound with no leaf
	Clear(ctx context.Context, id ID) error                // removes the leaf's entries, after a failed load
}

// Sentinels the reconciler maps to reasons (ADR-0262 Decisions 5 and 7).
var (
	ErrCapacity = errors.New("VolumeCapacity")
	ErrBudget   = errors.New("VolumeBudget")
	ErrNotOwned = errors.New("StoreNotOwned")
)

// Limiter gives a leaf its hard size. A Limiter that does not enforce lets the manager measure instead.
type Limiter interface {
	// Limit sets dir's project id and a hard limit of sizeBytes.
	Limit(dir string, project uint32, sizeBytes int64) error
	// Enforced reports whether Limit is a kernel limit.
	Enforced() bool
}

// measureOnly is the Limiter of funcdctl dev (ADR-0262 Decision 10): no limit, a measurement every interval.
type measureOnly struct{}

func (measureOnly) Limit(string, uint32, int64) error { return nil }
func (measureOnly) Enforced() bool                    { return false }

// MeasureOnly returns the Limiter that only measures.
func MeasureOnly() Limiter { return measureOnly{} }

// Options configure a manager.
type Options struct {
	Root          string // where the leaves and metas live; created 0700
	Limiter       Limiter
	CapacityBytes int64 // 0 ⇒ no capacity check (funcdctl dev)
	// Budget returns a namespace's volume budget in bytes, 0 for none.
	Budget func(ctx context.Context, ns v1.NamespaceName) (int64, error)
	// Holder returns the UID of the stored Function a volume's path names, or false when none is stored.
	Holder func(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) (v1.UID, bool, error)
	// UID and GID own each leaf (the runtime images run as root, the zero value).
	UID, GID int
	// BookingTTL drops a booking no Reserve backed (an apply refused after its admission); default 1 minute.
	BookingTTL time.Duration
	// MeasureInterval paces the measurement of an unenforced Limiter; default 30 s.
	MeasureInterval time.Duration
	Logger          *slog.Logger
	Clock           func() time.Time
}

type booking struct {
	size   int64
	backed bool // a leaf exists for it
	at     time.Time
}

type meta struct {
	Owner     v1.UID            `json:"owner,omitempty"`
	SizeBytes int64             `json:"sizeBytes"`
	Deletion  v1.DeletionPolicy `json:"deletion,omitempty"`
	Project   uint32            `json:"project"`
}

type manager struct {
	o        Options
	mu       sync.Mutex
	bookings map[ID]booking
	next     uint32
}

var workerToken = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// New opens the manager on o.Root and rebuilds the bookings from the metas there (ADR-0262 Decision 5).
func New(o Options) (Manager, error) {
	const op = "volume.New"
	if o.Root == "" || !filepath.IsAbs(o.Root) {
		return nil, fault.Invalidf(op, "the volume root %q must be an absolute path", o.Root)
	}
	if o.Limiter == nil {
		o.Limiter = MeasureOnly()
	}
	if o.BookingTTL <= 0 {
		o.BookingTTL = time.Minute
	}
	if o.MeasureInterval <= 0 {
		o.MeasureInterval = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if err := os.MkdirAll(o.Root, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create the volume root")
	}
	m := &manager{o: o, bookings: map[ID]booking{}, next: 1}
	infos, err := m.scan()
	if err != nil {
		return nil, err
	}
	for _, in := range infos {
		m.bookings[in.ID] = booking{size: in.meta.SizeBytes, backed: true}
		m.next = max(m.next, in.meta.Project+1)
	}
	return m, nil
}

// Run measures every volume each MeasureInterval while the Limiter does not enforce, and logs one warning per
// volume past its size (ADR-0262 Decision 10). It returns when ctx ends.
func Run(ctx context.Context, mgr Manager) {
	m, ok := mgr.(*manager)
	if !ok || m.o.Limiter.Enforced() {
		return
	}
	t := time.NewTicker(m.o.MeasureInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.measure()
		}
	}
}

func (m *manager) measure() {
	infos, err := m.scan()
	if err != nil {
		m.o.Logger.Warn("volume measurement failed", "err", err)
		return
	}
	for _, in := range infos {
		used, err := usage(m.leaf(in.ID))
		if err == nil && used > in.meta.SizeBytes {
			m.o.Logger.Warn("volume past its size", "namespace", in.ID.Namespace, "function", in.ID.Function,
				"volume", in.ID.Name, "worker", in.ID.Worker, "sizeBytes", in.meta.SizeBytes, "usedBytes", used)
		}
	}
}

// usage sums the regular files under dir without following a link.
func usage(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

func (m *manager) validID(op string, id ID) error {
	for _, p := range []string{string(id.Namespace), string(id.Function), string(id.Name)} {
		if !workerToken.MatchString(p) {
			return fault.Invalidf(op, "volume %s: %q is not a DNS-1123 label", id, p)
		}
	}
	if id.Worker != "" && !workerToken.MatchString(id.Worker) {
		return fault.Invalidf(op, "volume %s: worker %q is not a DNS-1123 label", id, id.Worker)
	}
	return nil
}

func (m *manager) fnDir(id ID) string {
	return filepath.Join(m.o.Root, string(id.Namespace), string(id.Function))
}

// leaf is the volume's directory: <ns>/<fn>/<name> for a local volume, <ns>/<fn>/<name>.empty/<worker> for an
// emptyDir. A DNS label holds no dot, so neither collides with another volume's leaf or meta.
func (m *manager) leaf(id ID) string {
	if id.Worker != "" {
		return filepath.Join(m.fnDir(id), string(id.Name)+".empty", id.Worker)
	}
	return filepath.Join(m.fnDir(id), string(id.Name))
}

func (m *manager) metaPath(id ID) string { return m.leaf(id) + ".meta" }

func (m *manager) readMeta(id ID) (meta, bool, error) {
	b, err := os.ReadFile(m.metaPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return meta{}, false, nil
	}
	if err != nil {
		return meta{}, false, fault.Wrapf(err, fault.Internal, "volume.meta", "read the meta of %s", id)
	}
	var md meta
	if err := json.Unmarshal(b, &md); err != nil {
		return meta{}, false, fault.Wrapf(err, fault.Internal, "volume.meta", "decode the meta of %s", id)
	}
	return md, true, nil
}

func (m *manager) writeMeta(id ID, md meta) error {
	b, err := json.Marshal(md)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "volume.meta", "encode the meta of %s", id)
	}
	tmp := m.metaPath(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fault.Wrapf(err, fault.Internal, "volume.meta", "write the meta of %s", id)
	}
	if err := os.Rename(tmp, m.metaPath(id)); err != nil {
		return fault.Wrapf(err, fault.Internal, "volume.meta", "write the meta of %s", id)
	}
	return nil
}

// dropStale removes bookings no Reserve backed within BookingTTL (an apply refused after its admission).
func (m *manager) dropStale() {
	now := m.o.Clock()
	for id, b := range m.bookings {
		if !b.backed && now.Sub(b.at) > m.o.BookingTTL {
			delete(m.bookings, id)
		}
	}
}

// book checks and records a booking; the caller holds m.mu.
func (m *manager) book(ctx context.Context, op string, id ID, size int64) error {
	if size <= 0 {
		return fault.Invalidf(op, "volume %s: size %d must be above 0", id, size)
	}
	m.dropStale()
	if b, ok := m.bookings[id]; ok {
		if b.size != size {
			return fault.Invalidf(op, "volume %s: sizeBytes %d differs from its %d; a volume is not resized", id, size, b.size)
		}
		if !b.backed {
			b.at = m.o.Clock()
			m.bookings[id] = b
		}
		return nil
	}
	var total, inNS int64
	for bid, b := range m.bookings {
		total += b.size
		if bid.Namespace == id.Namespace {
			inNS += b.size
		}
	}
	if c := m.o.CapacityBytes; c > 0 && total+size > c {
		return fault.Wrapf(ErrCapacity, fault.Conflict, op, "volume %s: %d bytes do not fit, %d of volumes.capacityBytes %d are reserved", id, size, total, c)
	}
	if m.o.Budget != nil {
		budget, err := m.o.Budget(ctx, id.Namespace)
		if err != nil {
			return err
		}
		if budget > 0 && inNS+size > budget {
			return fault.Wrapf(ErrBudget, fault.Conflict, op, "volume %s: %d bytes do not fit, %d of namespace %s's volumeBudgetBytes %d are reserved", id, size, inNS, id.Namespace, budget)
		}
	}
	m.bookings[id] = booking{size: size, at: m.o.Clock()}
	return nil
}

func (m *manager) Book(ctx context.Context, id ID, size int64) error {
	const op = "volume.Book"
	if err := m.validID(op, id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.book(ctx, op, id, size)
}

func (m *manager) Reserve(ctx context.Context, id ID, owner v1.UID, size int64, deletion v1.DeletionPolicy) (string, error) {
	const op = "volume.Reserve"
	if err := m.validID(op, id); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	md, found, err := m.readMeta(id)
	if err != nil {
		return "", err
	}
	if found {
		if md.Owner != owner {
			return "", fault.Wrapf(ErrNotOwned, fault.Conflict, op, "volume %s belongs to another Function; bind it with ref", id)
		}
		if md.SizeBytes != size {
			return "", fault.Invalidf(op, "volume %s: sizeBytes %d differs from its %d; a volume is not resized", id, size, md.SizeBytes)
		}
	}
	if err := m.book(ctx, op, id, size); err != nil {
		return "", err
	}
	dir := m.leaf(id)
	if err := m.ensureLeaf(op, id, dir); err != nil {
		return "", err
	}
	if !found {
		md = meta{Owner: owner, SizeBytes: size, Deletion: deletion, Project: m.next}
		m.next++
	} else if md.Deletion != deletion {
		md.Deletion = deletion
	}
	if err := m.o.Limiter.Limit(dir, md.Project, size); err != nil {
		return "", err
	}
	if err := m.writeMeta(id, md); err != nil {
		return "", err
	}
	b := m.bookings[id]
	b.backed = true
	m.bookings[id] = b
	return dir, nil
}

// ensureLeaf creates the leaf and its parents 0700, refuses a symlink or a non-directory, and sets the owner of the
// leaf only (ADR-0262 Decision 4).
func (m *manager) ensureLeaf(op string, id ID, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the parents of volume %s", id)
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "create volume %s", id)
		}
	case err != nil:
		return fault.Wrapf(err, fault.Internal, op, "inspect volume %s", id)
	case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0:
		return fault.Invalidf(op, "volume %s is not a directory", id)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "set the mode of volume %s", id)
	}
	if os.Geteuid() == 0 {
		if err := os.Lchown(dir, m.o.UID, m.o.GID); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "set the owner of volume %s", id)
		}
	}
	return nil
}

func (m *manager) Adopt(ctx context.Context, id ID, owner v1.UID) error {
	const op = "volume.Adopt"
	if err := m.validID(op, id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	md, found, err := m.readMeta(id)
	if err != nil {
		return err
	}
	if !found {
		return fault.NotFoundf(op, "volume %s does not exist", id)
	}
	if owner != "" && md.Owner != "" && md.Owner != owner {
		live, err := m.live(ctx, id, md.Owner)
		if err != nil {
			return err
		}
		if live {
			return fault.Conflictf(op, "volume %s is held by a live Function", id)
		}
	}
	md.Owner = owner
	return m.writeMeta(id, md)
}

// live reports whether the Function that owned a volume is still stored. A volume's path names its Function, so only
// a stored Function of that name with that UID holds it.
func (m *manager) live(ctx context.Context, id ID, uid v1.UID) (bool, error) {
	if m.o.Holder == nil {
		return true, nil
	}
	cur, ok, err := m.o.Holder(ctx, id.Namespace, id.Function)
	if err != nil {
		return false, err
	}
	return ok && cur == uid, nil
}

func (m *manager) Release(_ context.Context, id ID) error {
	const op = "volume.Release"
	if err := m.validID(op, id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.RemoveAll(m.leaf(id)); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "remove volume %s", id)
	}
	if err := os.Remove(m.metaPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fault.Wrapf(err, fault.Internal, op, "remove the meta of volume %s", id)
	}
	delete(m.bookings, id)
	return nil
}

func (m *manager) Empty(_ context.Context, id ID) (bool, error) {
	const op = "volume.Empty"
	if err := m.validID(op, id); err != nil {
		return false, err
	}
	ents, err := os.ReadDir(m.leaf(id))
	if err != nil {
		return false, fault.Wrapf(err, openKind(err), op, "open volume %s", id)
	}
	return len(ents) == 0, nil
}

func (m *manager) Clear(_ context.Context, id ID) error {
	const op = "volume.Clear"
	if err := m.validID(op, id); err != nil {
		return err
	}
	dir := m.leaf(id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fault.Wrapf(err, openKind(err), op, "open volume %s", id)
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "clear volume %s", id)
		}
	}
	return nil
}

func (m *manager) Retained(context.Context) ([]ID, error) {
	infos, err := m.scan()
	if err != nil {
		return nil, err
	}
	var out []ID
	for _, in := range infos {
		if in.ID.Worker == "" && in.meta.Owner == "" {
			out = append(out, in.ID)
		}
	}
	return out, nil
}

func (m *manager) List(context.Context) ([]Info, error) {
	infos, err := m.scan()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(infos))
	for _, in := range infos {
		out = append(out, Info{ID: in.ID, Owner: in.meta.Owner, SizeBytes: in.meta.SizeBytes, Deletion: in.meta.Deletion})
	}
	return out, nil
}

type scanned struct {
	ID   ID
	meta meta
}

// scan reads every meta under the root, sorted by ID: <ns>/<fn>/<name>.meta and <ns>/<fn>/<name>.empty/<w>.meta.
func (m *manager) scan() ([]scanned, error) {
	const op = "volume.scan"
	var out []scanned
	nss, err := os.ReadDir(m.o.Root)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the volume root")
	}
	for _, ns := range nss {
		if !ns.IsDir() || !workerToken.MatchString(ns.Name()) {
			continue
		}
		fns, err := os.ReadDir(filepath.Join(m.o.Root, ns.Name()))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "list namespace %s", ns.Name())
		}
		for _, fn := range fns {
			if !fn.IsDir() || !workerToken.MatchString(fn.Name()) {
				continue
			}
			base := ID{Namespace: v1.NamespaceName(ns.Name()), Function: v1.ObjectName(fn.Name())}
			ids, err := m.scanFunction(base)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				md, found, err := m.readMeta(id)
				if err != nil {
					return nil, err
				}
				if found {
					out = append(out, scanned{ID: id, meta: md})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b scanned) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	return out, nil
}

func (m *manager) scanFunction(base ID) ([]ID, error) {
	dir := m.fnDir(base)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "volume.scan", "list function %s/%s", base.Namespace, base.Function)
	}
	var ids []ID
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".meta") && workerToken.MatchString(strings.TrimSuffix(name, ".meta")):
			id := base
			id.Name = v1.ObjectName(strings.TrimSuffix(name, ".meta"))
			ids = append(ids, id)
		case e.IsDir() && strings.HasSuffix(name, ".empty") && workerToken.MatchString(strings.TrimSuffix(name, ".empty")):
			ws, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				return nil, fault.Wrapf(err, fault.Internal, "volume.scan", "list emptyDir %s", name)
			}
			for _, w := range ws {
				wn := strings.TrimSuffix(w.Name(), ".meta")
				if strings.HasSuffix(w.Name(), ".meta") && workerToken.MatchString(wn) {
					id := base
					id.Name, id.Worker = v1.ObjectName(strings.TrimSuffix(name, ".empty")), wn
					ids = append(ids, id)
				}
			}
		}
	}
	return ids, nil
}
