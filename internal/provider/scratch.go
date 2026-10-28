package provider

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/loopfs"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// scratchSuffix ends the file name of an engine's scratch image.
const scratchSuffix = ".img"

// scratchFS makes and removes one scratch filesystem: loopScratch in production, a fake in tests.
type scratchFS interface {
	Make(ctx context.Context, image, dir string, sizeBytes int64) error
	Remove(image, dir string) error
}

// scratchFiles places each engine's scratch (ADR-0294 Decision 6): its image at <root>/<ns>/<name>.img, mounted at
// <mountRoot>/<ns>/<name>.
type scratchFiles struct {
	root, mountRoot string
	fs              scratchFS
}

func (s scratchFiles) enabled() bool { return s.root != "" }

func (s scratchFiles) paths(ref ProviderRef) (image, dir string) {
	return filepath.Join(s.root, string(ref.Namespace), string(ref.Name)+scratchSuffix),
		filepath.Join(s.mountRoot, string(ref.Namespace), string(ref.Name))
}

// mounts is the engine's bind of its scratch, or none when the spec asks none or the runtime has no scratch root.
func (s scratchFiles) mounts(spec ProviderSpec) []containerrt.Mount {
	if spec.Scratch == nil || !s.enabled() {
		return nil
	}
	_, dir := s.paths(spec.Ref)
	return []containerrt.Mount{{Source: dir, Target: spec.Scratch.Path}}
}

// loopScratch is a preallocated ext4 image, loop-mounted (internal/platform/loopfs).
type loopScratch struct{}

// Make removes an earlier image first, so each engine start gets a new, empty filesystem.
func (loopScratch) Make(ctx context.Context, image, dir string, sizeBytes int64) error {
	if err := loopfs.Remove(image, dir); err != nil {
		return err
	}
	if err := loopfs.Create(ctx, image, sizeBytes, ""); err != nil {
		return err
	}
	if err := loopfs.Mount(image, dir); err != nil {
		return errors.Join(err, loopfs.Remove(image, dir))
	}
	return nil
}

func (loopScratch) Remove(image, dir string) error { return loopfs.Remove(image, dir) }

// makeScratch asks the floor for spec's scratch and makes it before the engine is created. The floor's refusal is
// fault.InsufficientStorage.
func (r *engineRuntime) makeScratch(ctx context.Context, spec ProviderSpec) error {
	const op = "provider.Converge"
	if spec.Scratch == nil || !r.scratch.enabled() {
		return nil
	}
	if err := spec.Ref.Namespace.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "scratch namespace")
	}
	if err := spec.Ref.Name.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "scratch name")
	}
	if r.floor != nil {
		if err := r.floor.Check(ctx, spec.Scratch.SizeBytes); err != nil {
			return fault.Wrapf(err, fault.InsufficientStorage, op, "the data filesystem lacks the space for the scratch of %s/%s",
				spec.Ref.Namespace, spec.Ref.Name)
		}
	}
	image, dir := r.scratch.paths(spec.Ref)
	if err := r.scratch.fs.Make(ctx, image, dir, spec.Scratch.SizeBytes); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "make the scratch of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
	}
	return nil
}

// removeScratch removes ref's scratch, when the runtime has a scratch root.
func (r *engineRuntime) removeScratch(ref ProviderRef) error {
	if !r.scratch.enabled() {
		return nil
	}
	image, dir := r.scratch.paths(ref)
	if err := r.scratch.fs.Remove(image, dir); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "provider.removeScratch", "remove the scratch of %s/%s", ref.Namespace, ref.Name)
	}
	return nil
}

// RemoveLeftoverScratch unmounts and removes the scratch images an earlier run left under root, mounted under
// mountRoot (ADR-0294, ADR-0297); one that an engine still holds frees its space once the boot sweep stops that
// engine.
func RemoveLeftoverScratch(root, mountRoot string) error {
	const op = "provider.RemoveLeftoverScratch"
	if root == "" {
		return nil
	}
	images, err := filepath.Glob(filepath.Join(root, "*", "*"+scratchSuffix))
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "list the scratch images under %s", root)
	}
	dirs, err := filepath.Glob(filepath.Join(mountRoot, "*", "*"))
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "list the scratch mounts under %s", mountRoot)
	}
	refs := map[ProviderRef]bool{}
	for _, image := range images {
		refs[refAt(filepath.Dir(image), strings.TrimSuffix(filepath.Base(image), scratchSuffix))] = true
	}
	for _, dir := range dirs {
		refs[refAt(filepath.Dir(dir), filepath.Base(dir))] = true
	}
	files := scratchFiles{root: root, mountRoot: mountRoot}
	var errs []error
	for ref := range refs {
		image, dir := files.paths(ref)
		if err := loopfs.Remove(image, dir); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fault.Wrapf(errors.Join(errs...), fault.Internal, op, "remove the scratch an earlier run left")
	}
	return nil
}

// refAt is the engine whose scratch lives at <nsDir>/<name>.
func refAt(nsDir, name string) ProviderRef {
	return ProviderRef{Namespace: v1.NamespaceName(filepath.Base(nsDir)), Name: v1.ObjectName(name)}
}
