package volume

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Tar streams the volume's leaf as a tar, read live and file by file without following a link: the copy is not a
// point in time (ADR-0262 Decision 11).
func (m *manager) Tar(ctx context.Context, id ID) (io.ReadCloser, error) {
	const op = "volume.Tar"
	if err := m.validID(op, id); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(m.leaf(id))
	if err != nil {
		return nil, fault.Wrapf(err, openKind(err), op, "open volume %s", id)
	}
	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = root.Close() }()
		pw.CloseWithError(writeTar(ctx, root, pw))
	}()
	return pr, nil
}

// openKind is NotFound for a volume with no leaf, Internal otherwise.
func openKind(err error) fault.Kind {
	if errors.Is(err, fs.ErrNotExist) {
		return fault.NotFound
	}
	return fault.Internal
}

func writeTar(ctx context.Context, root *os.Root, w io.Writer) error {
	tw := tar.NewWriter(w)
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
			return nil // a FIFO, a socket or a device node is a running process's state, which no restore takes (#1202)
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			if link, err = root.Readlink(p); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		h.Name = p
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if d.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		f, err := root.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.CopyN(tw, f, h.Size)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Untar writes a tar into the volume's leaf only while it is empty (else Conflict). Every entry stays inside the
// leaf: os.Root refuses a path or a link that leaves it.
func (m *manager) Untar(ctx context.Context, id ID, r io.Reader) error {
	const op = "volume.Untar"
	if err := m.validID(op, id); err != nil {
		return err
	}
	dir := m.leaf(id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return fault.Wrapf(err, openKind(err), op, "open volume %s", id)
	}
	if len(ents) > 0 {
		return fault.Conflictf(op, "volume %s is not empty: a restore writes only into an empty volume", id)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "open volume %s", id)
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "read the archive of volume %s", id)
		}
		name := path.Clean(strings.TrimSuffix(h.Name, "/"))
		if name == "." || !fs.ValidPath(name) {
			return fault.Invalidf(op, "volume %s: archive entry %q leaves the volume", id, h.Name)
		}
		if err := extract(root, tr, h, filepath.FromSlash(name)); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "restore %q into volume %s", h.Name, id)
		}
	}
}

func extract(root *os.Root, r io.Reader, h *tar.Header, name string) error {
	mode := fs.FileMode(h.Mode).Perm()
	switch h.Typeflag {
	case tar.TypeDir:
		return root.MkdirAll(name, mode|0o700)
	case tar.TypeSymlink:
		return root.Symlink(h.Linkname, name)
	case tar.TypeReg:
		if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.CopyN(f, r, h.Size); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	default:
		return fault.Invalidf("volume.Untar", "entry %q of type %c is neither a file, a directory nor a link", h.Name, h.Typeflag)
	}
}
