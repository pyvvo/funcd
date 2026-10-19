//go:build dev

package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// applyDesired applies obj, re-applying it on a Conflict. A PUT is an optimistic update against the
// resourceVersion the control plane reads (ADR-0018), and the running controllers write status meanwhile
// (on a --persist restart every resource already exists), so the update can lose that race; obj is the
// whole desired state, so applying it again is safe.
func applyDesired(ctx context.Context, c *sdk.Client, obj v1.Object) error {
	var err error
	for range applyAttempts {
		if _, err = c.Apply(ctx, obj); fault.KindOf(err) != fault.Conflict {
			return err
		}
	}
	return err
}

// stageResources splits the apply of resObjs around the Functions that bind them. Admission refuses to drop a table
// of a KVStore or a prefix of a Bucket while a Function binds it (ADR-0073), and a Function that binds a new one waits
// for it (ADR-0121), so a KVStore or Bucket that drops one its live version holds is applied first with it kept, and
// again as desired (last) once the Functions no longer bind it.
func stageResources(ctx context.Context, op string, c *sdk.Client, resObjs []v1.Object) (first, last []v1.Object, err error) {
	for _, obj := range resObjs {
		kind := obj.GroupVersionKind().Kind
		if kind != v1.KindKVStore && kind != v1.KindBucket {
			first = append(first, obj)
			continue
		}
		live, gerr := c.Get(ctx, kind, devNamespace, obj.GetName())
		if fault.KindOf(gerr) == fault.NotFound {
			first = append(first, obj)
			continue
		}
		if gerr != nil {
			return nil, nil, fault.Wrapf(gerr, fault.KindOf(gerr), op, "read %s %q", kind, obj.GetName())
		}
		kept := obj
		switch want := obj.(type) {
		case *v1.KVStore:
			if l, ok := live.(*v1.KVStore); ok {
				if tables := keepDropped(want.Spec.Tables, l.Spec.Tables, func(t v1.KVTable) string { return t.Name }); tables != nil {
					wide := *want
					wide.Spec.Tables = tables
					kept = &wide
				}
			}
		case *v1.Bucket:
			if l, ok := live.(*v1.Bucket); ok {
				if prefixes := keepDropped(want.Spec.Prefixes, l.Spec.Prefixes, func(p v1.BucketPrefix) string { return p.Name }); prefixes != nil {
					wide := *want
					wide.Spec.Prefixes = prefixes
					kept = &wide
				}
			}
		}
		first = append(first, kept)
		if kept != obj {
			last = append(last, obj)
		}
	}
	return first, last, nil
}

// keepDropped returns want followed by each entry of live whose name want lacks, or nil when want lacks none.
func keepDropped[T v1.KVTable | v1.BucketPrefix](want, live []T, name func(T) string) []T {
	var dropped []T
	for _, l := range live {
		if !slices.ContainsFunc(want, func(w T) bool { return name(w) == name(l) }) {
			dropped = append(dropped, l)
		}
	}
	if dropped == nil {
		return nil
	}
	return append(slices.Clone(want), dropped...)
}

// fileStamp is the size and mtime of the file at path, which an editor's save changes.
func fileStamp(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return infoStamp(fi), nil
}

// infoStamp is the size and mtime of fi.
func infoStamp(fi fs.FileInfo) string {
	return fmt.Sprintf("%d\x00%d", fi.Size(), fi.ModTime().UnixNano())
}

// watchFolder polls the folder until ctx is done (ADR-0247 Decision 8): when a file dev read or a build changes, or a
// *.yaml file is added or removed, it re-reads the folder, re-applies it and prunes what is gone; it also removes the
// old step copies whose step serves its new one (Decision 7). applied is what the boot applied, plus the objects of an
// earlier session it could not delete.
func watchFolder(ctx context.Context, op string, c *sdk.Client, s *devSession, applied []v1.Object, logger *slog.Logger, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(devReloadPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reload(ctx, op, c, &applied, logger); err != nil && ctx.Err() == nil {
				logger.Warn("reload failed", "err", err)
			}
			s.collectOldSteps(ctx, c)
		}
	}
}

// changed reports whether a file the folder was read from, its listing, or a build changed since it was read.
func (s *devSession) changed() bool {
	for path, stamp := range s.folder.stamps {
		if strings.HasSuffix(path, string(filepath.Separator)) {
			files, err := filepath.Glob(filepath.Join(path, "*.yaml"))
			if err != nil || listingStamp(slices.Sorted(slices.Values(files))) != stamp {
				return true
			}
			continue
		}
		if cur, err := fileStamp(path); err != nil || cur != stamp {
			return true
		}
	}
	for _, fn := range s.folder.functions {
		if fp, err := fn.build.fingerprint(s.stateDirs); err == nil && fp != s.seen[fn.fn.Name] {
			return true
		}
	}
	for _, st := range s.folder.steps {
		if fp, err := st.build.fingerprint(s.stateDirs); err == nil &&
			filepath.Base(s.stepDirs[st.name]) != string(st.name)+"-"+strings.TrimPrefix(fp, "sha256:")[:16] {
			return true
		}
	}
	return false
}

// reload re-reads the folder when it changed, re-applies it as the boot did and prunes what *applied holds that the
// folder no longer does. A folder that does not load is reported once and retried on the next change; an apply that
// lost a race with a status write (Conflict) is retried on the next poll.
func (s *devSession) reload(ctx context.Context, op string, c *sdk.Client, applied *[]v1.Object, logger *slog.Logger) error {
	if !s.changed() {
		return nil
	}
	folder, err := loadDevFolder(op, s.folder.dir)
	if err != nil {
		s.folder.stamps = s.currentStamps()
		return err
	}
	secrets, serr := folder.declaredSecrets(op)
	if serr != nil {
		s.folder.stamps = folder.stamps
		return serr
	}
	desired, perr := s.prepare(op, &devInstance{}, folder, secrets, logger)
	if perr != nil {
		s.folder.stamps = folder.stamps
		return perr
	}
	if aerr := applyOrdered(ctx, op, c, desired); aerr != nil {
		if fault.KindOf(aerr) != fault.Conflict {
			s.folder = folder
		}
		*applied = pruneRemoved(ctx, c, *applied, desired, false, logger)
		return aerr
	}
	s.folder = folder
	*applied = pruneRemoved(ctx, c, *applied, desired, true, logger)
	return nil
}

// currentStamps re-stamps every file the folder was read from, so a folder that fails to load is reported once.
func (s *devSession) currentStamps() map[string]string {
	out := make(map[string]string, len(s.folder.stamps))
	for path := range s.folder.stamps {
		if strings.HasSuffix(path, string(filepath.Separator)) {
			files, _ := filepath.Glob(filepath.Join(path, "*.yaml"))
			out[path] = listingStamp(slices.Sorted(slices.Values(files)))
			continue
		}
		out[path], _ = fileStamp(path)
	}
	return out
}

// pruneRemoved deletes, last applied first, each resource of prev that next no longer holds (ADR-0247 Decision 8: the
// folder is the session's desired state) and returns what the session keeps: next, then each removed resource it
// did not delete. del is false when the reload failed to apply, as a Function may still bind a removed resource. A
// KVStore, Bucket or SQLDatabase that still holds data refuses the delete (ADR-0073, ADR-0232), so a reload never drops
// data: it stays with a warning, and the next reload retries it.
func pruneRemoved(ctx context.Context, c *sdk.Client, prev, next []v1.Object, del bool, logger *slog.Logger) []v1.Object {
	key := func(o v1.Object) string { return string(o.GroupVersionKind().Kind) + "/" + string(o.GetName()) }
	want := make(map[string]bool, len(next))
	for _, o := range next {
		want[key(o)] = true
	}
	var kept []v1.Object
	for i := len(prev) - 1; i >= 0; i-- {
		o := prev[i]
		if want[key(o)] {
			continue
		}
		if del {
			err := c.Delete(ctx, o.GroupVersionKind().Kind, devNamespace, o.GetName())
			if err == nil || fault.KindOf(err) == fault.NotFound {
				continue
			}
			logger.Warn("a resource removed from the folder stays until it can be deleted",
				"kind", o.GroupVersionKind().Kind, "name", o.GetName(), "err", err)
		}
		kept = append(kept, o)
	}
	slices.Reverse(kept)
	return append(next, kept...)
}
