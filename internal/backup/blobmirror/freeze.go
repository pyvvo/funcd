package blobmirror

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // fileblob records an MD5; the check compares with it
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// checkpointName ends a DuckLake catalog's checkpoint key, <p>/_ducklake/catalog.db (ADR-0086).
const checkpointName = "_ducklake/catalog.db"

// maxRefreezes is the re-freezes of one key or prefix a run accepts; the next fails it.
const maxRefreezes = 3

// frozen is one object of the image: the link (or, live, the store's file), the linked file's size and time, and the
// attributes the store recorded.
type frozen struct {
	key, path, livePath string
	live                bool
	size                int64
	mod                 time.Time
	md5                 []byte
	contentType         string
	meta                map[string]string
	// checked is a checkpoint's MD5, checked at the freeze.
	checked bool
}

// matches checks file against what was frozen: a live file's size and time, then the recorded MD5 unless checked.
// It leaves file at its start.
func (e *frozen) matches(file *os.File) (bool, error) {
	if e.live {
		info, err := file.Stat()
		if err != nil {
			return false, fault.Wrapf(err, fault.Internal, "blobmirror.Run", "stat %q", e.key)
		}
		if info.Size() != e.size || !info.ModTime().Equal(e.mod) {
			return false, nil
		}
	}
	if e.checked || len(e.md5) == 0 {
		return true, nil
	}
	same, err := md5Matches(file, e.md5)
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, "blobmirror.Run", "read %q", e.key)
	}
	_, err = file.Seek(0, io.SeekStart)
	return same, err
}

func md5Matches(r io.Reader, want []byte) (bool, error) {
	h := md5.New() //nolint:gosec // compared with the MD5 fileblob recorded
	if _, err := io.Copy(h, r); err != nil {
		return false, err
	}
	return bytes.Equal(h.Sum(nil), want), nil
}

// freezer builds the image of one run (Decision 5).
type freezer struct {
	m        *mirror
	src      blob.Bucket
	img      map[string]*frozen
	refrozen map[string]int
	catalogs []string
	warned   bool
}

func (f *freezer) close() { _ = f.src.Close() }

// walked is a key of a walk and its data file.
type walked struct{ key, path string }

// freeze empties the image, walks the store, links every key outside a catalog, then each catalog: its checkpoint
// first, then a new walk of its prefix.
func (m *mirror) freeze(ctx context.Context) (*freezer, error) {
	const op = "blobmirror.Run"
	if err := os.RemoveAll(m.cfg.FrozenDir); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "empty the image %s", m.cfg.FrozenDir)
	}
	if err := os.MkdirAll(m.cfg.FrozenDir, 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create the image %s", m.cfg.FrozenDir)
	}
	src, err := gocloud.Open(ctx, gocloud.FileURL(m.cfg.Dir))
	if err != nil {
		return nil, err
	}
	f := &freezer{m: m, src: src, img: map[string]*frozen{}, refrozen: map[string]int{}}
	all, err := f.walk(ctx, "")
	if err != nil {
		f.close()
		return nil, err
	}
	for _, w := range all {
		if p, ok := strings.CutSuffix(w.key, checkpointName); ok && strings.HasSuffix(p, "/") {
			f.catalogs = append(f.catalogs, p)
		}
	}
	for _, w := range all {
		if f.owner(w.key) != "" {
			continue
		}
		if _, err := f.freezeKey(ctx, w.key, w.path, false); err != nil {
			f.close()
			return nil, err
		}
	}
	for _, p := range f.catalogs {
		if err := f.freezePrefix(ctx, p, filepath.Join(m.cfg.Dir, filepath.FromSlash(p+checkpointName))); err != nil {
			f.close()
			return nil, err
		}
	}
	return f, nil
}

// owner is the longest catalog prefix holding key, "" when none.
func (f *freezer) owner(key string) string {
	best := ""
	for _, p := range f.catalogs {
		if strings.HasPrefix(key, p) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

func (f *freezer) walk(ctx context.Context, prefix string) ([]walked, error) {
	var out []walked
	err := gocloud.WalkFileKeys(ctx, f.m.cfg.Dir, prefix, func(key, path string) error {
		out = append(out, walked{key, path})
		return nil
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "blobmirror.Run", "walk the blob store under %q", prefix)
	}
	if f.m.hooks.afterWalk != nil {
		f.m.hooks.afterWalk(prefix)
	}
	return out, nil
}

// freezePrefix freezes the catalog at p: its checkpoint (MD5 checked at once), then each key of a new walk of p. A
// walked key gone at its link, or the live checkpoint's MD5 changed by the end of the walk, freezes p again. Without
// a checkpoint, p's keys freeze as any other.
func (f *freezer) freezePrefix(ctx context.Context, p, checkpointPath string) error {
	ck := p + checkpointName
	for {
		for k := range f.img {
			if f.owner(k) == p {
				delete(f.img, k)
			}
		}
		ok, err := f.freezeKey(ctx, ck, checkpointPath, true)
		if err != nil {
			return err
		}
		keys, err := f.walk(ctx, p)
		if err != nil {
			return err
		}
		again := false
		for _, w := range keys {
			if w.key == ck || f.owner(w.key) != p {
				continue
			}
			found, err := f.freezeKey(ctx, w.key, w.path, false)
			if err != nil {
				return err
			}
			if !found && ok {
				again = true
				break
			}
		}
		if !ok {
			return nil
		}
		if !again {
			a, err := f.src.Attributes(ctx, ck)
			again = err != nil || !bytes.Equal(a.MD5, f.img[ck].md5)
		}
		if !again {
			return nil
		}
		if err := f.refreeze(p); err != nil {
			return err
		}
	}
}

// refreeze counts one more freeze of a key or prefix; the fourth fails the run with fault.Conflict.
func (f *freezer) refreeze(name string) error {
	f.refrozen[name]++
	if f.refrozen[name] > maxRefreezes {
		return fault.Conflictf("blobmirror.Run", "%q changed during %d freezes in a row: no generation this run",
			name, f.refrozen[name])
	}
	return nil
}

// freezeKey links key's data file into the image and reads its attributes, again while the attributes do not
// decode or, with checkNow, the linked bytes do not match the recorded MD5. found is false when the key is gone.
func (f *freezer) freezeKey(ctx context.Context, key, path string, checkNow bool) (found bool, err error) {
	for {
		e, err := f.linkOnce(ctx, key, path, checkNow)
		switch {
		case errors.Is(err, errGone):
			delete(f.img, key)
			return false, nil
		case errors.Is(err, errMismatch):
			if err := f.refreeze(key); err != nil {
				return false, err
			}
		case err != nil:
			return false, err
		default:
			f.img[key] = e
			return true, nil
		}
	}
}

var (
	errGone     = errors.New("the key is gone")
	errMismatch = errors.New("the attributes do not match the linked bytes")
)

func (f *freezer) linkOnce(ctx context.Context, key, path string, checkNow bool) (*frozen, error) {
	const op = "blobmirror.Run"
	rel, err := filepath.Rel(f.m.cfg.Dir, path)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "place %q in the image", key)
	}
	e := &frozen{key: key, path: filepath.Join(f.m.cfg.FrozenDir, rel), livePath: path}
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create the image of %q", key)
	}
	if err := os.Remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fault.Wrapf(err, fault.Internal, op, "replace the image of %q", key)
	}
	err = f.m.link(path, e.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, errGone
	case errors.Is(err, syscall.EXDEV):
		e.path, e.live = path, true
		if !f.warned {
			f.warned = true
			f.m.log.WarnContext(ctx, "blob mirror: the store is not on the image's device, so a run copies the live "+
				"files: an object written during the copy is read again", "dir", f.m.cfg.Dir, "image", f.m.cfg.FrozenDir)
		}
	case err != nil:
		return nil, fault.Wrapf(err, fault.Internal, op, "link %q into the image", key)
	}
	if f.m.hooks.afterLink != nil {
		f.m.hooks.afterLink(key)
	}
	info, err := os.Stat(e.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errGone
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "stat the image of %q", key)
	}
	a, err := f.src.Attributes(ctx, key)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil, errGone
	case err != nil:
		return nil, errMismatch
	}
	e.size, e.mod, e.md5, e.contentType, e.meta = info.Size(), info.ModTime(), a.MD5, a.ContentType, a.Metadata
	if checkNow && len(e.md5) > 0 {
		file, err := os.Open(e.path)
		if err != nil {
			return nil, errGone
		}
		same, err := md5Matches(file, e.md5)
		_ = file.Close()
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "read the image of %q", key)
		}
		if !same {
			return nil, errMismatch
		}
		e.checked = true
	}
	return e, nil
}
