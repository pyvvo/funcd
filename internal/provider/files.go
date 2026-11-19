package provider

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// EngineFiles reads and places an engine's files on the host, under its storage directory (ADR-0333 Decision 6):
// every call runs inside an os.Root opened on that directory, a folder is relative to it, and a file is one path
// segment (v1.IsAddonFile). It is workload.AddonFiles for the engines of NewRuntime, which embeds it.
type EngineFiles struct {
	root string
	kind v1.Kind
}

// NewEngineFiles is the file access of kind's engines under the storage root root: <root>/<lower(kind)>/<ns>/<name>.
// root "" is no durable storage, where every call fails Unavailable.
func NewEngineFiles(root string, kind v1.Kind) EngineFiles {
	return EngineFiles{root: root, kind: kind}
}

// copyMode and folderMode are a placed copy's and a created folder's modes: the engine's user alone reads them.
const (
	copyMode   = 0o600
	folderMode = 0o700
)

// Newest opens the newest regular file of dir whose name is a file name, does not end in ~ and has not changed for
// quiet (ADR-0333 Decision 2): NotFound when there is none, or no dir.
func (f EngineFiles) Newest(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string, quiet time.Duration) (string, io.ReadCloser, error) {
	const op = "provider.Newest"
	root, err := f.open(ctx, op, ns, name, dir)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = root.Close() }()
	ents, err := fs.ReadDir(root.FS(), dir)
	if err != nil {
		return "", nil, fault.Wrapf(err, notFoundOr(err), op, "list %s of %s/%s", dir, ns, name)
	}
	var best fs.FileInfo
	now := time.Now()
	for _, e := range ents {
		if !v1.IsAddonFile(e.Name()) || strings.HasSuffix(e.Name(), "~") {
			continue
		}
		fi, err := root.Lstat(filepath.Join(filepath.FromSlash(dir), e.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, fault.Wrapf(err, fault.Internal, op, "inspect %s/%s of %s/%s", dir, e.Name(), ns, name)
		}
		if !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < quiet {
			continue
		}
		if best == nil || fi.ModTime().After(best.ModTime()) || fi.ModTime().Equal(best.ModTime()) && fi.Name() > best.Name() {
			best = fi
		}
	}
	if best == nil {
		return "", nil, fault.NotFoundf(op, "%s of %s/%s holds no file unchanged for %s", dir, ns, name, quiet)
	}
	file, err := root.Open(filepath.Join(filepath.FromSlash(dir), best.Name()))
	if err != nil {
		return "", nil, fault.Wrapf(err, notFoundOr(err), op, "open %s/%s of %s/%s", dir, best.Name(), ns, name)
	}
	if st, err := file.Stat(); err != nil || !os.SameFile(st, best) {
		_ = file.Close()
		return "", nil, fault.Conflictf(op, "%s/%s of %s/%s changed while it was opened", dir, best.Name(), ns, name)
	}
	return best.Name(), file, nil
}

// Put writes r as dir/file, a new file owned by uid and gid, creating dir and its missing parents with that owner; the
// owner is set only when the daemon is root, which a foreign uid needs (ADR-0231 Decision 2). r ends with its own
// context, and a failed write removes what it wrote.
func (f EngineFiles) Put(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir, file string, r io.Reader, uid, gid int) error {
	const op = "provider.Put"
	if !v1.IsAddonFile(file) {
		return fault.Invalidf(op, "%q is not one path segment of letters, digits, dots, underscores and dashes", file)
	}
	root, err := f.open(ctx, op, ns, name, dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	owned := os.Geteuid() == 0
	made := ""
	for _, seg := range strings.Split(dir, "/") {
		made = path.Join(made, seg)
		err := root.Mkdir(filepath.FromSlash(made), folderMode)
		switch {
		case errors.Is(err, fs.ErrExist):
			continue
		case err != nil:
			return fault.Wrapf(err, fault.Internal, op, "create %s of %s/%s", made, ns, name)
		}
		if owned {
			if err := root.Lchown(filepath.FromSlash(made), uid, gid); err != nil {
				return fault.Wrapf(err, fault.Internal, op, "set the owner of %s of %s/%s", made, ns, name)
			}
		}
	}
	target := filepath.Join(filepath.FromSlash(dir), file)
	out, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, copyMode)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s/%s of %s/%s", dir, file, ns, name)
	}
	_, err = io.Copy(out, r)
	if err == nil {
		err = out.Close()
	} else {
		_ = out.Close()
	}
	if err == nil && owned {
		err = root.Lchown(target, uid, gid)
	}
	if err != nil {
		_ = root.Remove(target)
		return fault.Wrapf(err, fault.KindOf(err), op, "write %s/%s of %s/%s", dir, file, ns, name)
	}
	return nil
}

// Clear empties dir, keeping it: nothing to do without it.
func (f EngineFiles) Clear(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, dir string) error {
	const op = "provider.Clear"
	root, err := f.open(ctx, op, ns, name, dir)
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	ents, err := fs.ReadDir(root.FS(), dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "list %s of %s/%s", dir, ns, name)
	}
	for _, e := range ents {
		if err := root.RemoveAll(filepath.Join(filepath.FromSlash(dir), e.Name())); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "remove %s/%s of %s/%s", dir, e.Name(), ns, name)
		}
	}
	return nil
}

// open opens the engine's storage directory as an os.Root, once ctx is live and dir is a folder under it: NotFound
// without the directory.
func (f EngineFiles) open(ctx context.Context, op string, ns v1.NamespaceName, name v1.ObjectName, dir string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" || dir == "." || !fs.ValidPath(dir) {
		return nil, fault.Invalidf(op, "%q is not a folder under the storage of %s/%s", dir, ns, name)
	}
	ref := ProviderRef{Namespace: ns, Name: name}
	if err := ref.Namespace.Validate(); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "namespace")
	}
	if err := ref.Name.Validate(); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "name")
	}
	if f.root == "" {
		return nil, fault.Unavailablef(op, "durable engine storage is not configured: %s/%s has no files", ns, name)
	}
	root, err := os.OpenRoot(storageDir(f.root, f.kind, ref))
	if err != nil {
		return nil, fault.Wrapf(err, notFoundOr(err), op, "open the storage of %s/%s", ns, name)
	}
	return root, nil
}

// notFoundOr is NotFound for a missing file or folder, Internal otherwise.
func notFoundOr(err error) fault.Kind {
	if errors.Is(err, fs.ErrNotExist) {
		return fault.NotFound
	}
	return fault.Internal
}
