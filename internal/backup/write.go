package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
)

// Write runs snapshot.Cut once, then lists gen/ (n = 1 + the highest n of any key), puts each store's parts in cut
// order, lists again and puts the manifest; every put goes IfNotExist unless Conditional is false, and an error ends
// the run (Decision 2). The cut is spooled first: the keys name the timeline the cut returns.
func (t *target) Write(ctx context.Context, events, meta, runs snapshot.Source, opts WriteOptions) (Manifest, error) {
	const op = "backup.Write"
	if opts.Pin != "" && opts.Pin != PreUpgrade {
		return Manifest{}, fault.Invalidf(op, "pin %q: Write puts the %q pin or none", opts.Pin, PreUpgrade)
	}
	t.run.Lock()
	defer t.run.Unlock()
	if err := t.Ready(ctx); err != nil {
		return Manifest{}, err
	}
	at := t.clock.Now()
	stores, rv, err := t.cut(ctx, events, meta, runs, opts.Seal)
	if err != nil {
		return Manifest{}, err
	}
	defer stores.remove()
	v, err := store.ParseVersion(rv)
	if err != nil {
		return Manifest{}, err
	}
	if v.Timeline == "" {
		return Manifest{}, fault.Internalf(op, "the metastore version %q names no timeline", rv)
	}

	objs, err := listObjects(ctx, t.b)
	if err != nil {
		return Manifest{}, err
	}
	n := uint64(1)
	for _, o := range objs {
		n = max(n, o.n+1)
	}
	f := fence{timeline: v.Timeline, parent: opts.Parent, n: n}
	if key := f.other(objs); key != "" {
		return Manifest{}, t.refuse(ctx, f, objs, key)
	}
	class := opts.Pin
	if class == "" {
		class = ClassFor(entries(objs), at, t.cfg.Retention)
	}
	dir := genDir(class, n, v.Timeline)
	put := map[string]bool{}
	files := make([]StoreFile, 0, len(stores))
	buf := make([]byte, partBytes)
	for _, s := range stores {
		parts, err := t.putParts(ctx, dir+s.name+"/", s, buf, put)
		if err != nil {
			return Manifest{}, err
		}
		files = append(files, StoreFile{Name: s.name, Parts: parts, Bytes: s.bytes, SHA256: hex.EncodeToString(s.sum)})
	}

	if objs, err = listObjects(ctx, t.b); err != nil {
		return Manifest{}, err
	}
	key := f.other(objs)
	if key == "" {
		key = f.atN(objs, put)
	}
	if key != "" {
		return Manifest{}, fault.Conflictf(op, "%starget: %q is another writer's, put during this run; no manifest written", t.prefix, key)
	}
	m := Manifest{
		Format: Format, Generation: n, At: v1alpha1.NewTimestamp(at), Funcd: version.Version,
		Timeline: v.Timeline, Revision: v.N, Parent: opts.Parent, Stores: files, Keys: opts.Keys,
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return Manifest{}, fault.Wrapf(err, fault.Internal, op, "encode the manifest")
	}
	if err := t.put(ctx, dir+manifestName, data); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// refuse ends a run refused at the first listing. A run with a complete generation of its own puts the empty
// refused mark at its n, so the other writer sees it and stops in turn (Decision 4).
func (t *target) refuse(ctx context.Context, f fence, objs []object, key string) error {
	err := fault.Conflictf("backup.Write", "%starget: %q is another writer's; no generation is written until the "+
		"operator stops that writer and moves the target's prefix or deletes its keys", t.prefix, key)
	if !f.complete(objs) {
		return err
	}
	if perr := t.put(ctx, genDir(Hourly, f.n, f.timeline)+refusedName, nil); perr != nil {
		return errors.Join(err, perr)
	}
	return err
}

func (t *target) put(ctx context.Context, key string, data []byte) error {
	return t.b.Put(ctx, key, data, blob.PutOptions{IfNotExist: t.Conditional()})
}

// putParts puts a store file in parts of up to 8 MiB, part-00000 first; an empty file is one empty part.
func (t *target) putParts(ctx context.Context, dir string, s *spool, buf []byte, put map[string]bool) (int, error) {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return 0, fault.Wrapf(err, fault.Internal, "backup.Write", "read the %s spool", s.name)
	}
	for parts := 0; ; parts++ {
		k, err := io.ReadFull(s.file, buf)
		if errors.Is(err, io.EOF) && parts > 0 {
			return parts, nil
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, fault.Wrapf(err, fault.Internal, "backup.Write", "read the %s spool", s.name)
		}
		key := fmt.Sprintf("%spart-%05d", dir, parts)
		if perr := t.put(ctx, key, buf[:k]); perr != nil {
			return 0, perr
		}
		put[key] = true
		if err != nil {
			return parts + 1, nil
		}
	}
}

// spool is one store's file, sealed and framed into a temp file while the cut reads, with its size and SHA-256.
type spool struct {
	name  string
	file  *os.File
	bytes int64
	sum   []byte
}

type spools []*spool

func (s spools) remove() {
	for _, sp := range s {
		_ = sp.file.Close()
		_ = os.Remove(sp.file.Name())
	}
}

// tagged is a store's Source with its place in the cut, which Cut hands back with each record.
type tagged struct {
	snapshot.Source
	i int
}

// cut spools the three stores beside the data directory (DataDir, else the temp directory) and returns the
// metastore version.
func (t *target) cut(ctx context.Context, events, meta, runs snapshot.Source, seal Seal) (spools, string, error) {
	const op = "backup.Write"
	var out spools
	var ws []*storeWriter
	for _, name := range []string{"events", "metastore", "runs"} {
		f, err := os.CreateTemp(t.cfg.DataDir, "backup-"+name+"-*")
		if err != nil {
			out.remove()
			return nil, "", fault.Wrapf(err, fault.Internal, op, "spool the %s store", name)
		}
		out = append(out, &spool{name: name, file: f})
		w, err := newStoreWriter(f, seal)
		if err != nil {
			out.remove()
			return nil, "", fault.Wrapf(err, fault.KindOf(err), op, "seal the %s store", name)
		}
		ws = append(ws, w)
	}
	rv, err := snapshot.Cut(ctx, &tagged{events, 0}, &tagged{meta, 1}, &tagged{runs, 2},
		func(src snapshot.Source, r snapshot.Record) error { return writeRecord(ws[src.(*tagged).i].buf, r) })
	for i, w := range ws {
		err = errors.Join(err, w.close())
		out[i].bytes, out[i].sum = w.n, w.h.Sum(nil)
	}
	if err != nil {
		out.remove()
		return nil, "", err
	}
	return out, rv, nil
}

// storeWriter frames records into a buffer, through the seal, into the spool, its hash and its count.
type storeWriter struct {
	buf    *bufio.Writer
	sealed io.WriteCloser
	h      hash.Hash
	n      int64
}

func newStoreWriter(f *os.File, seal Seal) (*storeWriter, error) {
	w := &storeWriter{h: sha256.New()}
	dst := io.MultiWriter(f, w.h, (*counter)(&w.n))
	w.sealed = nopCloser{dst}
	if seal != nil {
		sealed, err := seal(dst)
		if err != nil {
			return nil, err
		}
		w.sealed = sealed
	}
	w.buf = bufio.NewWriterSize(w.sealed, 64<<10)
	return w, nil
}

func (w *storeWriter) close() error {
	return errors.Join(w.buf.Flush(), w.sealed.Close())
}

type counter int64

func (c *counter) Write(p []byte) (int, error) {
	*c += counter(len(p))
	return len(p), nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
