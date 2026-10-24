// Package durable writes the files a later start of funcd trusts, so a power loss leaves the old content or the new
// one, never an empty or a partial file (#1244): every file and directory is synced before the step that makes it
// visible, and the directory that holds a renamed or created entry is synced after it.
package durable

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Op is one step of a durable write, as Disk.Trace reports it.
type Op string

const (
	SyncFile Op = "sync-file" // a file's data
	SyncDir  Op = "sync-dir"  // a directory's entries
	Rename   Op = "rename"    // an entry moved into place; the path is its new one
)

// Disk does durable writes. Its zero value acts on the file system; Trace, when set, gets each step once it is done,
// so a test can check their order.
type Disk struct {
	Trace func(op Op, path string)
}

func (d Disk) done(op Op, path string) {
	if d.Trace != nil {
		d.Trace(op, path)
	}
}

// WriteFile replaces dir/name with data at mode perm: a temporary file in dir is synced and renamed onto dir/name,
// then dir is synced.
func (d Disk) WriteFile(dir, name string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err == nil {
		d.done(SyncFile, tmp)
		err = d.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return d.SyncDir(dir)
}

// SyncTree syncs every regular file and directory under root, root included, so a rename that publishes root
// publishes their content too.
func (d Disk) SyncTree(root string) error {
	return filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case e.IsDir():
			return d.SyncDir(path)
		case e.Type().IsRegular():
			return d.sync(SyncFile, path)
		}
		return nil
	})
}

// SyncDir syncs dir, so the entries created, renamed or removed in it survive a power loss.
func (d Disk) SyncDir(dir string) error { return d.sync(SyncDir, dir) }

func (d Disk) sync(op Op, path string) error {
	f, err := os.Open(path) //nolint:gosec // the caller's own file or directory
	if err != nil {
		return err
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		return err
	}
	d.done(op, path)
	return nil
}

// Rename moves oldpath to newpath; the caller syncs newpath's directory before it relies on the move.
func (d Disk) Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	d.done(Rename, newpath)
	return nil
}

// MkdirAll makes dir and its missing parents at mode perm, then syncs the parent of each directory it made, outermost
// first.
func (d Disk) MkdirAll(dir string, perm fs.FileMode) error {
	var made []string
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			break
		}
		made = append(made, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	for i := len(made) - 1; i >= 0; i-- {
		if err := d.SyncDir(filepath.Dir(made[i])); err != nil {
			return err
		}
	}
	return nil
}
