//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"errors"
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
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// applyDesired applies obj, re-applying it on a Conflict. A PUT is an optimistic update against the
// resourceVersion the control plane reads (ADR-0018), and the running controllers write status meanwhile
// (on a --persist restart every resource already exists), so the update can lose that race; obj is the
// whole desired state, so applying it again is safe.
func applyDesired(ctx context.Context, c *sdk.Client, obj v1.Object) error {
	var err error
	for range devApplyAttempts {
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

// devHandler is one from-source function's hot-reload state (ADR-0125 boot sequence, "watch files, re-apply on
// change"): its plan (the manifest is re-read on an edit), the bundle file its worker runs (the entry itself in
// place, a private copy when isolated), and the fingerprint of the files last acted on.
type devHandler struct {
	pf     plannedFunc
	bundle string
	seen   string
}

// fingerprint digests the size and mtime of every file whose edit reloads the function: the manifest, the handler
// entry and, in place, every file under the bundle root except dot-entries (the delivered contract, .git, .venv),
// node_modules, __pycache__ and the durable state dirs. The Function carries it as spec.imageDigest, so an edit
// rolls out a new revision whose worker loads the edited code, then drains the old one (ADR-0143) — a long-lived
// worker never re-imports a module. A missing manifest or entry (an editor's save swaps the file) is an error,
// retried on the next poll.
func (h *devHandler) fingerprint(stateDirs []string) (string, error) {
	sum := sha256.New()
	add := func(p, stamp string) {
		_, _ = fmt.Fprintf(sum, "%s\x00%s\n", p, stamp)
	}
	for _, p := range []string{h.pf.manifestPath, filepath.Join(h.pf.srcDir, h.pf.entry)} {
		stamp, err := fileStamp(p)
		if err != nil {
			return "", err
		}
		add(p, stamp)
	}
	if !h.pf.isolate {
		root, err := filepath.Abs(h.pf.srcDir)
		if err != nil {
			return "", err
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil //nolint:nilerr // an entry removed mid-walk changes the fingerprint on the next poll
			}
			name := d.Name()
			ignored := p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__")
			if ignored || d.IsDir() && slices.Contains(stateDirs, p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if fi, ierr := d.Info(); ierr == nil && fi.Mode().IsRegular() {
				add(p, infoStamp(fi))
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sha256:%x", sum.Sum(nil)), nil
}

// watchHandlers polls every function's files, and the workflow file of a workflow run (wf, nil otherwise), for an
// edit until ctx is done (ADR-0125, hot-reload on change). applied is the set of resources the boot applied, plus
// the objects of an earlier session it could not delete.
func watchHandlers(ctx context.Context, op string, c *sdk.Client, hs []*devHandler, wf *devWorkflow, applied []v1.Object, stateDirs []string, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(devReloadPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := reloadChanged(ctx, op, c, hs, &applied, stateDirs)
			if wf != nil {
				var werr error
				hs, werr = wf.reapply(ctx, op, c, hs)
				err = errors.Join(err, werr)
			}
			if err != nil && ctx.Err() == nil {
				slog.Default().Warn("hot-reload failed", "err", err)
			}
		}
	}
}

// reloadChanged re-applies every function whose files changed since the last poll, as bootDev applied them: it
// re-reads each edited manifest, re-synthesizes and re-applies the resources of the whole set (they are shared
// across functions), then re-delivers each edited bundle and contract and re-applies its Function, then drops the
// tables and prefixes no Function binds any more (stageResources) and deletes the resources of *applied that the set
// no longer holds. A failed reload is reported once and retried on the next edit; an apply that lost a race with a
// concurrent status write (Conflict) is re-applied in place, then on the next poll once those attempts run out.
func reloadChanged(ctx context.Context, op string, c *sdk.Client, hs []*devHandler, applied *[]v1.Object, stateDirs []string) error {
	var changed []*devHandler
	var errs []error
	for _, h := range hs {
		fp, ferr := h.fingerprint(stateDirs)
		if ferr != nil || fp == h.seen {
			continue
		}
		h.seen = fp
		m, lerr := loadManifestAt(op, h.pf.manifestPath)
		if lerr != nil {
			errs = append(errs, fault.Wrapf(lerr, fault.KindOf(lerr), op, "reload %s", h.pf.name))
			continue
		}
		if m.Main != h.pf.m.Main || m.Dev.Backends != h.pf.m.Dev.Backends || m.Dev.Node != h.pf.m.Dev.Node || m.Dev.Python != h.pf.m.Dev.Python {
			slog.Default().Warn("restart funcdctl dev to apply a changed main, dev.backends, dev.node or dev.python", "function", h.pf.name)
		}
		h.pf.m = m
		changed = append(changed, h)
	}
	if len(changed) == 0 {
		return errors.Join(errs...)
	}
	loadErrs := len(errs)
	pfs := make([]plannedFunc, 0, len(hs))
	for _, h := range hs {
		pfs = append(pfs, h.pf)
	}
	resObjs, serr := synthesizeResources(op, pfs)
	if serr != nil {
		return errors.Join(append(errs, serr)...)
	}
	firstRes, lastRes, gerr := stageResources(ctx, op, c, resObjs)
	if gerr != nil {
		return errors.Join(append(errs, gerr)...)
	}
	for _, obj := range firstRes {
		if aerr := applyDesired(ctx, c, obj); aerr != nil {
			errs = append(errs, fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply %s %q", obj.GroupVersionKind().Kind, obj.GetName()))
			if fault.KindOf(aerr) == fault.Conflict {
				for _, h := range changed {
					h.seen = ""
				}
				return errors.Join(errs...)
			}
		}
	}
	for _, h := range changed {
		contractBlob, cerr := artifact.ContractBlob(h.pf.m.Contract.Input, h.pf.m.Contract.Output)
		if cerr != nil {
			errs = append(errs, fault.Wrapf(cerr, fault.KindOf(cerr), op, "build contract for %s", h.pf.name))
			continue
		}
		if derr := deliverBundle(op, h.pf, filepath.Dir(h.bundle), contractBlob); derr != nil {
			errs = append(errs, derr)
			continue
		}
		fn := synthesizeFunction(h.pf, h.bundle)
		fn.Spec.ImageDigest = h.seen
		if aerr := applyDesired(ctx, c, fn); aerr != nil {
			errs = append(errs, fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply Function %q", fn.Name))
			if fault.KindOf(aerr) == fault.Conflict {
				h.seen = ""
			}
		}
	}
	if len(errs) == loadErrs {
		for _, obj := range lastRes {
			if aerr := applyDesired(ctx, c, obj); aerr != nil {
				errs = append(errs, fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply %s %q", obj.GroupVersionKind().Kind, obj.GetName()))
			}
		}
	}
	*applied = pruneRemoved(ctx, c, *applied, resObjs, len(errs) == loadErrs)
	return errors.Join(errs...)
}

// devWorkflow is the Workflow of a `funcdctl dev workflow.yaml` run: its file, the object bootDev applies, the stamp
// of the file version last acted on, and the step Functions and Workflow the session applied, which a reload prunes.
type devWorkflow struct {
	path    string
	obj     *v1.Workflow
	seen    string
	applied []v1.Object
}

// fileStamp is the size and mtime of the file at path, which an editor's save changes.
func fileStamp(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return infoStamp(fi), nil
}

// infoStamp is the size and mtime of fi: the stamp of a function's files in its fingerprint and of the workflow file.
func infoStamp(fi fs.FileInfo) string {
	return fmt.Sprintf("%d\x00%d", fi.Size(), fi.ModTime().UnixNano())
}

// reapply re-applies the Workflow when its file changed since the last poll, resolved as the boot resolved it
// (ADR-0125, "watch files, re-apply on change"), then deletes the Workflow of a previous name and the Function of
// each step the file no longer holds, and returns the handlers of the steps it still holds. A step whose function is
// not running needs a restart, which it warns about, as for a changed main. A failed reload is reported once and
// retried on the next edit; an apply that keeps losing a race with a concurrent status write (Conflict) is retried
// on the next poll.
func (w *devWorkflow) reapply(ctx context.Context, op string, c *sdk.Client, hs []*devHandler) ([]*devHandler, error) {
	stamp, serr := fileStamp(w.path)
	if serr != nil || stamp == w.seen {
		return hs, nil
	}
	w.seen = stamp
	wf, isWorkflow, derr := detectWorkflow(op, w.path)
	if derr != nil {
		return hs, derr
	}
	if !isWorkflow {
		return hs, fault.Invalidf(op, "reload %q: the file no longer holds a Workflow", w.path)
	}
	pfs, rerr := resolveWorkflowPlan(op, w.path, wf)
	if rerr != nil {
		return hs, rerr
	}
	for _, pf := range pfs {
		if !slices.ContainsFunc(hs, func(h *devHandler) bool { return h.pf.name == pf.name }) {
			slog.Default().Warn("restart funcdctl dev to run a new workflow step function", "function", pf.name)
		}
	}
	if aerr := applyDesired(ctx, c, wf); aerr != nil {
		if fault.KindOf(aerr) == fault.Conflict {
			w.seen = ""
		}
		return hs, fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply Workflow %q", wf.Name)
	}
	hs = slices.DeleteFunc(hs, func(h *devHandler) bool {
		return !slices.ContainsFunc(pfs, func(pf plannedFunc) bool { return pf.name == h.pf.name })
	})
	next := make([]v1.Object, 0, len(hs)+1)
	for _, h := range hs {
		next = append(next, synthesizeFunction(h.pf, h.bundle))
	}
	w.applied = pruneRemoved(ctx, c, w.applied, append(next, wf), true)
	return hs, nil
}

// pruneRemoved deletes, last applied first, each resource of prev that next no longer holds (ADR-0125: the
// manifests are the session's desired state) and returns what the session keeps: next, then each removed resource it
// did not delete. del is false when the reload failed to apply, as a Function may still bind a removed resource. A
// KVStore or Bucket that still holds data refuses the delete (ADR-0073), so a reload never drops data: it stays with
// a warning, and the next reload retries it.
func pruneRemoved(ctx context.Context, c *sdk.Client, prev, next []v1.Object, del bool) []v1.Object {
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
			slog.Default().Warn("a resource removed from the manifests stays until it can be deleted",
				"kind", o.GroupVersionKind().Kind, "name", o.GetName(), "err", err)
		}
		kept = append(kept, o)
	}
	slices.Reverse(kept)
	return append(next, kept...)
}
