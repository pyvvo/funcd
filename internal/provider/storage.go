package provider

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	containerrt "github.com/pyvvo/funcd/internal/runtime"
)

// Volumes manages the durable directories of one owner kind's engines (ADR-0231). The runtime NewRuntime returns
// implements it; an owner gets it by a type assertion on its provider.Runtime.
type Volumes interface {
	// Reclaim removes ref's directory; Conflict while a container of ref exists; nil when there is none.
	Reclaim(ctx context.Context, ref ProviderRef) error
	// List returns the engines of the runtime's owner kind that have a directory, sorted.
	List(ctx context.Context) ([]ProviderRef, error)
}

var _ Volumes = (*engineRuntime)(nil)

// storageDir is ref's directory under the runtime's root: <root>/<lower(owner kind)>/<ns>/<name>. Both name parts are
// validated DNS labels, so the path cannot leave the root.
func (r *engineRuntime) storageDir(ref ProviderRef) string {
	return filepath.Join(r.kindRoot(), string(ref.Namespace), string(ref.Name))
}

func (r *engineRuntime) kindRoot() string {
	return filepath.Join(r.storageRoot, strings.ToLower(string(r.ownerKind)))
}

// prepareStorage checks a spec with storage and creates or checks its directory before any container exists
// (ADR-0231 Decisions 1, 2 and 5): one replica, a root, an absolute path, valid names, a real directory on the leaf,
// mode 0700, owned by the spec's uid and gid when the daemon is root, refused for a foreign uid otherwise.
func (r *engineRuntime) prepareStorage(spec ProviderSpec) error {
	const op = "provider.Converge"
	st := spec.Storage
	switch {
	case spec.Replicas != 1:
		return fault.Invalidf(op, "an engine with storage runs exactly one replica; got %d", spec.Replicas)
	case r.storageRoot == "":
		return fault.Invalidf(op, "durable engine storage is not configured on this runtime")
	case !filepath.IsAbs(st.Path):
		return fault.Invalidf(op, "storage path %q must be absolute", st.Path)
	}
	if err := spec.Ref.Namespace.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "storage namespace")
	}
	if err := spec.Ref.Name.Validate(); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "storage name")
	}
	root := os.Geteuid() == 0
	if !root && st.UID != os.Geteuid() {
		return fault.Invalidf(op, "storage needs root or the daemon's uid; the engine runs as uid %d", st.UID)
	}
	dir := r.storageDir(spec.Ref)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create the storage parents of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "create the storage of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
		}
	case err != nil:
		return fault.Wrapf(err, fault.Internal, op, "inspect the storage of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
	case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0:
		return fault.Invalidf(op, "the storage of %s/%s is not a directory", spec.Ref.Namespace, spec.Ref.Name)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "set the storage mode of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
	}
	if root {
		if err := os.Lchown(dir, st.UID, st.GID); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "set the storage owner of %s/%s", spec.Ref.Namespace, spec.Ref.Name)
		}
	}
	return nil
}

// storageMounts is the bind mount of the engine's directory, or none without storage.
func (r *engineRuntime) storageMounts(spec ProviderSpec) []containerrt.Mount {
	if spec.Storage == nil {
		return nil
	}
	return []containerrt.Mount{{Source: r.storageDir(spec.Ref), Target: spec.Storage.Path}}
}

// Reclaim removes ref's directory unless a container of ref exists (ADR-0231 Decision 3). The owner serializes it
// with Converge for the same ref.
func (r *engineRuntime) Reclaim(ctx context.Context, ref ProviderRef) error {
	const op = "provider.Reclaim"
	if r.storageRoot == "" {
		return nil
	}
	insts, err := r.namedInstances(ctx, ref)
	if err != nil {
		return err
	}
	if len(insts) > 0 {
		return fault.Conflictf(op, "%s/%s still has %d engine container(s); tear it down first", ref.Namespace, ref.Name, len(insts))
	}
	if err := os.RemoveAll(r.storageDir(ref)); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "remove the storage of %s/%s", ref.Namespace, ref.Name)
	}
	return nil
}

// List returns every engine of the runtime's owner kind that has a directory, sorted.
func (r *engineRuntime) List(_ context.Context) ([]ProviderRef, error) {
	const op = "provider.List"
	if r.storageRoot == "" {
		return nil, nil
	}
	nss, err := os.ReadDir(r.kindRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "list the storage root")
	}
	var out []ProviderRef
	for _, ns := range nss {
		if !ns.IsDir() {
			continue
		}
		names, err := os.ReadDir(filepath.Join(r.kindRoot(), ns.Name()))
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "list the storage of namespace %q", ns.Name())
		}
		for _, n := range names {
			if n.IsDir() {
				out = append(out, ProviderRef{Namespace: v1.NamespaceName(ns.Name()), Name: v1.ObjectName(n.Name())})
			}
		}
	}
	slices.SortFunc(out, func(a, b ProviderRef) int {
		return strings.Compare(string(a.Namespace)+"/"+string(a.Name), string(b.Namespace)+"/"+string(b.Name))
	})
	return out, nil
}
