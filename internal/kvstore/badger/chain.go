package badger

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"golang.org/x/mod/semver"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
)

// Point is a restore point (n, m) of the KV chain (ADR-0209 Decision 4); segment 0 is the base alone. Class is the
// base's, "" once it is gone; At is when m's export began; Funcd is the walk's highest stamp. Restorable: base n and
// segment m are complete, the walk reaches m, and no manifest on it has a higher format.
type Point struct {
	Chain, Segment uint64
	Class          backup.Class
	At             time.Time
	Funcd          string
	Restorable     bool
}

// stored is one base or segment the listing shows, with its manifest once read.
type stored struct {
	dir      string
	parts    map[int]bool
	manifest bool
	man      ChainManifest
}

// complete reports the manifest and every part it names.
func (s *stored) complete() bool {
	if s == nil || !s.manifest {
		return false
	}
	for i := range s.man.Store.Parts {
		if !s.parts[i] {
			return false
		}
	}
	return true
}

type chain struct {
	class backup.Class
	base  *stored
	segs  map[uint64]*stored
}

// readChains lists kv/ once and reads every manifest; it needs a credential that reads.
func readChains(ctx context.Context, src blob.Bucket) (map[uint64]*chain, error) {
	items, err := src.List(ctx, KVPrefix)
	if err != nil {
		return nil, err
	}
	chains := map[uint64]*chain{}
	for _, a := range items {
		k, ok := parseKVKey(a.Key)
		if !ok {
			continue
		}
		c := chains[k.chain]
		if c == nil {
			c = &chain{segs: map[uint64]*stored{}}
			chains[k.chain] = c
		}
		var s *stored
		switch {
		case k.base != "" && c.base == nil:
			c.class, c.base = k.base, &stored{dir: baseDir(k.base, k.chain), parts: map[int]bool{}}
			s = c.base
		case k.base != "":
			if k.base != c.class {
				continue
			}
			s = c.base
		default:
			if s = c.segs[k.segment]; s == nil {
				s = &stored{dir: incDir(k.chain, k.segment), parts: map[int]bool{}}
				c.segs[k.segment] = s
			}
		}
		if k.name == manifestName {
			s.manifest = true
		} else if k.part >= 0 {
			s.parts[k.part] = true
		}
	}
	for _, c := range chains {
		for _, s := range append(slices.Collect(maps.Values(c.segs)), c.base) {
			if s == nil || !s.manifest {
				continue
			}
			data, err := src.Get(ctx, s.dir+manifestName)
			if err != nil {
				return nil, err
			}
			if err := yaml.Unmarshal(data, &s.man); err != nil {
				return nil, fault.Wrapf(err, fault.Invalid, "kvbadger.ListPoints", "decode %s%s", s.dir, manifestName)
			}
		}
	}
	return chains, nil
}

// walk returns what point (n, m) loads, in order, and the head it reaches: from the base's to, every complete
// segment up to m whose since is at or below the head, raising the head to its to; a since above the head breaks
// the chain. A missing base or segment, or m beyond the break, is fault.NotFound; a higher format fault.Invalid.
func (c *chain) walk(n, m uint64) (path []*stored, head uint64, funcd string, err error) {
	const op = "kvbadger.RestoreDir"
	if c == nil || !c.base.complete() {
		return nil, 0, "", fault.NotFoundf(op, "the base of KV chain %d is missing or incomplete", n)
	}
	if m > 0 && !c.segs[m].complete() {
		return nil, 0, "", fault.NotFoundf(op, "segment %d of KV chain %d is missing or incomplete", m, n)
	}
	path, head, funcd = []*stored{c.base}, c.base.man.To, c.base.man.Funcd
	for _, i := range slices.Sorted(maps.Keys(c.segs)) {
		s := c.segs[i]
		if i > m {
			break
		}
		if !s.complete() {
			continue
		}
		if s.man.Since > head {
			return nil, 0, "", fault.NotFoundf(op, "KV point %d/%d is unreachable: segment %d starts at version %d, "+
				"above the head %d", n, m, i, s.man.Since, head)
		}
		path, head, funcd = append(path, s), max(head, s.man.To), higher(funcd, s.man.Funcd)
	}
	for _, s := range path {
		if s.man.Format > ChainFormat {
			return nil, 0, "", fault.Invalidf(op, "KV point %d/%d: %s%s has format %d; this funcd reads format %d",
				n, m, s.dir, manifestName, s.man.Format, ChainFormat)
		}
	}
	return path, head, funcd, nil
}

// higher is the semver-higher stamp; one that is not semver orders below every one that is.
func higher(a, b string) string {
	if a == "" || semver.IsValid(b) && (!semver.IsValid(a) || semver.Compare(b, a) > 0) {
		return b
	}
	return a
}

// ListPoints lists every point of the kv/ chains, by chain then segment, each with whether it restores; it reads
// every manifest.
func ListPoints(ctx context.Context, src blob.Bucket) ([]Point, error) {
	chains, err := readChains(ctx, src)
	if err != nil {
		return nil, err
	}
	var out []Point
	for _, n := range slices.Sorted(maps.Keys(chains)) {
		c := chains[n]
		ms := []uint64{}
		if c.base.complete() {
			ms = append(ms, 0)
		}
		for _, m := range slices.Sorted(maps.Keys(c.segs)) {
			if c.segs[m].complete() {
				ms = append(ms, m)
			}
		}
		for _, m := range ms {
			s := c.base
			if m > 0 {
				s = c.segs[m]
			}
			p := Point{Chain: n, Segment: m, Class: c.class, At: time.Time(s.man.At), Funcd: s.man.Funcd}
			if _, _, funcd, err := c.walk(n, m); err == nil {
				p.Funcd, p.Restorable = funcd, true
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// RestoreDir restores point p of src into dir (Decision 6). Before any write it refuses a non-empty dir
// (fault.Conflict), a missing base, segment or part (fault.NotFound) and a higher format (fault.Invalid naming both),
// and opens each manifest's recipients with one open call. It then loads the walk, each stored file checked against
// its SHA-256 (fault.Invalid), runs ADR-0195's record pass and stores the 8-byte cursor, so the restored instance's
// first Ship starts a new chain. An error empties dir.
func RestoreDir(ctx context.Context, dir string, src blob.Bucket, p Point, open backup.Opener) (err error) {
	const op = "kvbadger.RestoreDir"
	entries, err := os.ReadDir(dir)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fault.Wrapf(err, fault.Internal, op, "read %s", dir)
	case len(entries) > 0:
		return fault.Conflictf(op, "%s holds %s: a KV restore writes into an empty directory", dir, entries[0].Name())
	}
	chains, err := readChains(ctx, src)
	if err != nil {
		return err
	}
	path, head, _, err := chains[p.Chain].walk(p.Chain, p.Segment)
	if err != nil {
		return err
	}
	unseals := make([]backup.Unseal, len(path))
	for i, s := range path {
		if unseals[i], err = open(s.man.Recipients); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", dir)
	}
	db, err := openDB(dir, true)
	if err != nil {
		return errors.Join(err, emptyDir(dir))
	}
	defer func() {
		err = errors.Join(err, db.Close())
		if err != nil {
			err = errors.Join(err, emptyDir(dir))
		}
	}()
	return loadPath(ctx, db, src, path, unseals, head)
}

// Restore is the seam's restore (ADR-0066): the newest restorable point of an unsealed chain into this instance,
// for tests; a sealed one is fault.Invalid naming RestoreDir, which takes the operator's identities.
func (b *exporter) Restore(ctx context.Context) error {
	const op = "kvbadger.backup.Restore"
	chains, err := readChains(ctx, b.bucket)
	if err != nil {
		return err
	}
	var best *Point
	points, err := ListPoints(ctx, b.bucket)
	if err != nil {
		return err
	}
	for i := range points {
		if points[i].Restorable {
			best = &points[i]
		}
	}
	if best == nil {
		return fault.Invalidf(op, "no restorable KV point")
	}
	path, head, _, err := chains[best.Chain].walk(best.Chain, best.Segment)
	if err != nil {
		return err
	}
	for _, s := range path {
		if len(s.man.Recipients) > 0 {
			return fault.Invalidf(op, "KV point %d/%d is sealed: restore it with RestoreDir and the operator's identities",
				best.Chain, best.Segment)
		}
	}
	return loadPath(ctx, b.db, b.bucket, path, make([]backup.Unseal, len(path)), head)
}

// loadPath loads each stored file of path through its unseal (nil ⇒ as is), then the record pass and the 8-byte
// cursor at head.
func loadPath(ctx context.Context, db *badger.DB, src blob.Bucket, path []*stored, unseals []backup.Unseal, head uint64) error {
	const op = "kvbadger.RestoreDir"
	for i, s := range path {
		r := &partReader{ctx: ctx, bucket: src, prefix: s.dir, parts: s.man.Store.Parts, h: sha256.New()}
		var in io.Reader = r
		if unseals[i] != nil {
			u, err := unseals[i](r)
			if err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "open %s", s.dir)
			}
			in = u
		}
		lerr := db.Load(in, 16)
		if lerr == nil {
			_, lerr = io.Copy(io.Discard, in)
		}
		_, _ = io.Copy(io.Discard, r) // hash what a failed load left unread
		switch {
		case r.err != nil:
			return r.err
		case r.n != s.man.Store.Bytes || hex.EncodeToString(r.h.Sum(nil)) != s.man.Store.SHA256:
			return fault.Invalidf(op, "%s does not match its manifest's size and SHA-256", s.dir)
		case lerr != nil:
			return fault.Wrapf(lerr, fault.Internal, op, "load %s", s.dir)
		}
	}
	if err := (&exporter{db: db}).applyDelRecords(ctx); err != nil {
		return err
	}
	return setCursorRecord(db, binary.BigEndian.AppendUint64(nil, head))
}

// emptyDir removes everything in dir, leaving dir.
func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}

// partReader streams a stored file's parts in order, holding one part at a time, and hashes and counts them.
type partReader struct {
	ctx    context.Context
	bucket blob.Bucket
	prefix string
	parts  int
	idx    int
	cur    []byte
	h      hash.Hash
	n      int64
	err    error
}

func (r *partReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	for len(r.cur) == 0 {
		if r.idx >= r.parts {
			return 0, io.EOF
		}
		name := fmt.Sprintf("%spart-%05d", r.prefix, r.idx)
		data, err := r.bucket.Get(r.ctx, name)
		if err != nil {
			r.err = fault.Wrapf(err, fault.KindOf(err), "kvbadger.RestoreDir", "fetch %q", name)
			return 0, r.err
		}
		r.h.Write(data)
		r.n += int64(len(data))
		r.cur = data
		r.idx++
	}
	n := copy(p, r.cur)
	r.cur = r.cur[n:]
	return n, nil
}
