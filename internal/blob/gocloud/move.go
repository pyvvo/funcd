package gocloud

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
)

var _ blob.PrefixMover = (*bucket)(nil)

// movePageSize is how many objects a copy move lists at a time.
const movePageSize = 1000

// MovePrefix is blob.PrefixMover (ADR-0288 Decision 2).
func (k *bucket) MovePrefix(ctx context.Context, src, dst string) error {
	const op = "blob.MovePrefix"
	if src == "" || dst == "" || !strings.HasSuffix(src, "/") || !strings.HasSuffix(dst, "/") ||
		strings.HasPrefix(dst, src) || strings.HasPrefix(src, dst) {
		return fault.Invalidf(op, "move %q to %q: want two disjoint prefixes ending in /", src, dst)
	}
	if k.file {
		return k.fileMovePrefix(op, src, dst)
	}
	for {
		items, more, err := k.ListAfter(ctx, src, "", movePageSize)
		if err != nil {
			return err
		}
		for _, it := range items {
			if err := k.b.Copy(ctx, dst+strings.TrimPrefix(it.Key, src), it.Key, nil); err != nil {
				return mapErr(op, it.Key, err)
			}
			if err := k.Delete(ctx, it.Key); err != nil && fault.KindOf(err) != fault.NotFound {
				return err
			}
		}
		if !more {
			return nil
		}
	}
}

// fileMovePrefix renames src's directory onto dst's, after making dst's parents, then syncs both parents: a power
// loss leaves the objects under one prefix or the other. An empty dst directory is replaced; one with files is
// fault.Conflict.
func (k *bucket) fileMovePrefix(op, src, dst string) error {
	from, to := filePath(k.dir, strings.TrimSuffix(src, "/")), filePath(k.dir, strings.TrimSuffix(dst, "/"))
	for _, p := range []string{from, to} {
		if !strings.HasPrefix(p, k.dir+string(os.PathSeparator)) {
			return fault.Invalidf(op, "%q escapes the bucket root", p)
		}
	}
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "stat %q", src)
	}
	if _, err := os.Lstat(to); err == nil {
		// an empty directory, as a Bucket whose objects all went leaves, takes the move; one with files is refused
		if err := os.Remove(to); err != nil {
			return fault.Conflictf(op, "move %q to %q: the destination holds files", src, dst)
		}
	}
	if err := k.disk.MkdirAll(filepath.Dir(to), k.dirMode); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "make the parents of %q", dst)
	}
	if err := k.disk.Rename(from, to); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "move %q to %q", src, dst)
	}
	for _, dir := range []string{filepath.Dir(to), filepath.Dir(from)} {
		if err := k.disk.SyncDir(dir); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "sync %q after the move of %q", dir, src)
		}
	}
	return nil
}
