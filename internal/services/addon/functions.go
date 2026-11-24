package addon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/store"
)

// The Functions of the kinds' images (ADR-0358): funcd creates each from its image, as a Workflow creates its step
// Functions, and takes the schema of a kind's objects from the image's contract.

// ImageReader reads an artifact's runtime and contract from its manifest, never its bundle (Decision 4). An error of any
// kind is tried again.
type ImageReader interface {
	Read(ctx context.Context, image string) (v1.RuntimeName, v1.WorkflowContract, error)
}

// imageReadTimeout bounds the reads of a revision's images in one pass: a registry that hangs keeps the controller's
// worker that long.
const imageReadTimeout = 10 * time.Second

// selfAlias is the alias under which a created Function binds the object it is created for, when that one runs an
// engine (Decision 2).
const selfAlias = "self"

// The reasons an object waits for its kind's image (Decisions 2 and 4).
const (
	reasonArtifactNotReady = "ArtifactNotReady"
	reasonFunctionNotOwned = "FunctionNotOwned"
	reasonSettingsInvalid  = "SettingsInvalid" // the Addon's own, whose constraints do not fit a contract
)

// callInput is what KindSchema reads of a contract's input: a call's shape (ADR-0341 Decision 2, ADR-0345 Decision 2),
// or a union of them discriminated by kind.
type callInput struct {
	Properties struct {
		Kind struct {
			Const string   `json:"const"`
			Enum  []string `json:"enum"`
		} `json:"kind"`
		Objects struct {
			Items struct {
				Properties struct {
					Config json.RawMessage `json:"config"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"objects"`
	} `json:"properties"`
	OneOf         []callInput `json:"oneOf"`
	Discriminator struct {
		PropertyName string `json:"propertyName"`
	} `json:"discriminator"`
}

// names reports whether the input is of calls of kind.
func (c callInput) names(kind string) bool {
	k := c.Properties.Kind
	return v1.SameAddonKind(k.Const, kind) || slices.ContainsFunc(k.Enum, func(e string) bool { return v1.SameAddonKind(e, kind) })
}

// KindSchema is what contract says of one object's config of kind, nil when it says nothing (Decision 3): in its input,
// the schema at objects[].config, or that of the member whose kind is kind when the input is a union discriminated by
// kind.
func KindSchema(contract v1.WorkflowContract, kind string) json.RawMessage {
	var in callInput
	if len(contract.Input) == 0 || json.Unmarshal(contract.Input, &in) != nil {
		return nil
	}
	if len(in.OneOf) > 0 {
		i := slices.IndexFunc(in.OneOf, func(m callInput) bool { return m.names(kind) })
		if i < 0 || in.Discriminator.PropertyName != "kind" {
			return nil
		}
		in = in.OneOf[i]
	}
	return in.Properties.Objects.Items.Properties.Config
}

// OwnFunction is the Function that o names for kind in its spec.functions, "" when it names none.
func OwnFunction(o *v1.AddonObject, kind string) v1.ObjectName {
	for _, f := range o.Spec.Functions {
		if v1.SameAddonKind(f.Kind, kind) {
			return f.Ref
		}
	}
	return ""
}

// functionOf names the Function that gets o's calls of kind, and whether funcd creates it: o's own in spec.functions,
// else the ref that the kind names, else the one from the kind's image (Decisions 1 and 2).
func functionOf(f v1.AddonFunction, kind string, o *v1.AddonObject) (name v1.ObjectName, created bool) {
	if own := OwnFunction(o, kind); own != "" {
		return own, false
	}
	if f.Image == "" {
		return f.Ref, false
	}
	return v1.AddonFunctionName(o.Name, kind), true
}

// ensureFunction creates the Function of kind's image for o, or puts back what differs from it (Decision 2). It returns
// the reason no Function can be there, and why: its image is not read yet, or another object's Function has its name.
func (r *Reconciler) ensureFunction(ctx context.Context, rev *v1.AddonRevision, o *v1.AddonObject, kind, image string) (string, string, error) {
	const op = "services.addon.ensureFunction"
	name := v1.AddonFunctionName(o.Name, kind)
	kc := rev.ContractOf(kind)
	if kc == nil {
		return reasonArtifactNotReady, imageUnread(rev, kind, image), nil
	}
	off := false
	_, digest, _ := strings.Cut(image, "@") // a kind's image is pinned by digest (Decision 1)
	fn := &v1.Function{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: o.Namespace, ResourceGroup: o.ResourceGroup},
		Spec: v1.FunctionSpec{
			Runtime:     kc.Runtime,
			Handler:     v1.MaterializedHandler,
			Image:       image,
			ImageDigest: digest,
			Scaling:     v1.Scaling{IdleTimeout: v1.MaterializedIdleTimeout},
			Ingress:     &v1.Ingress{Enabled: &off},
			Bindings: v1.FunctionBindings{
				Secret: slices.Clone(o.Spec.Bindings.Secret),
				Job:    slices.Clone(o.Spec.Bindings.Job),
			},
		},
	}
	if !v1.SameAddonKind(kind, o.Spec.Kind) { // a settings kind's, whose target runs an engine
		fn.Spec.Bindings.Addon = []v1.FunctionAddon{{Alias: selfAlias, Object: o.Name}}
	}
	_, err := ownedstore.Ensure(ctx, r.store, o, fn, v1.DeletionDelete)
	switch {
	case errors.Is(err, ownedstore.ErrNotOwned):
		return reasonFunctionNotOwned, fmt.Sprintf("the Function %q is not the one that funcd creates for %s %q", name, o.Spec.Kind, o.Name), nil
	case fault.KindOf(err) == fault.Conflict:
		return "", "", nil // written since it was read: the event of that write brings the next pass
	case err != nil:
		return "", "", fault.Wrapf(err, fault.KindOf(err), op, "write the Function %s/%s", o.Namespace, name)
	}
	return "", "", nil
}

// imageUnread says that rev holds no contract of kind's image yet.
func imageUnread(rev *v1.AddonRevision, kind, image string) string {
	return fmt.Sprintf("funcd has not read the image %s of the kind %s in the revision %s yet", image, kind, rev.Name)
}

// ownedFunction is the Function that funcd created for o under name, nil when there is none or it is another's.
func (r *Reconciler) ownedFunction(ctx context.Context, o *v1.AddonObject, name v1.ObjectName) (*v1.Function, error) {
	fn, err := r.function(ctx, o.Namespace, name)
	if err != nil || fn == nil || !ownedstore.Marked(fn.OwnerReferences, o) {
		return nil, err
	}
	return fn, nil
}

// deleteFunction deletes fn as it was read. One gone, or written since, is left to the next pass.
func (r *Reconciler) deleteFunction(ctx context.Context, fn *v1.Function) error {
	err := r.store.Delete(ctx, v1.KindFunction.GVK(), fn.Namespace, fn.Name, fn.ResourceVersion)
	switch fault.KindOf(err) {
	case "", fault.NotFound, fault.Conflict:
		return nil
	}
	return fault.Wrapf(err, fault.KindOf(err), "services.addon.deleteFunction", "delete the Function %s/%s", fn.Namespace, fn.Name)
}

// pruneFunctions deletes each Function that funcd created for o and keep does not name: its kind, its image or its place
// in spec.functions changed (Decision 2).
func (r *Reconciler) pruneFunctions(ctx context.Context, o *v1.AddonObject, keep map[v1.ObjectName]bool) error {
	list, err := r.store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{Namespace: o.Namespace})
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), "services.addon.pruneFunctions", "list the Functions of %s", o.Namespace)
	}
	for _, item := range list.Items {
		fn, ok := item.(*v1.Function)
		if !ok || keep[fn.Name] || !v1.ControlledBy(fn.OwnerReferences, v1.KindAddonObject, o.UID) {
			continue
		}
		if err := r.deleteFunction(ctx, fn); err != nil {
			return err
		}
	}
	return nil
}

// readImages reads the runtime and the contract of each image of rev's kinds that rev's status lacks, once, and stores
// what it read (Decision 4). It returns rev as stored, and what it could not read, by kind.
func (r *Reconciler) readImages(ctx context.Context, rev *v1.AddonRevision) (*v1.AddonRevision, map[string]error, error) {
	const op = "services.addon.readImages"
	images, unread, read := rev.Spec.Spec.KindImages(), map[string]error{}, *rev
	read.Status.Contracts = slices.Clone(rev.Status.Contracts)
	rctx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()
	for kind, image := range images {
		if rev.ContractOf(kind) != nil {
			continue
		}
		kc, err := r.readImage(rctx, kind, image)
		if err != nil {
			unread[kind] = err
			continue
		}
		read.Status.Contracts = append(read.Status.Contracts, kc)
	}
	if len(read.Status.Contracts) == len(rev.Status.Contracts) {
		return rev, unread, nil
	}
	slices.SortFunc(read.Status.Contracts, func(x, y v1.AddonKindContract) int { return strings.Compare(x.Kind, y.Kind) })
	out, err := r.store.Update(ctx, &read)
	if err != nil {
		return rev, unread, fault.Wrapf(err, fault.KindOf(err), op, "store what the images of AddonRevision %s say", rev.Name)
	}
	stored, ok := out.(*v1.AddonRevision)
	if !ok {
		return rev, unread, fault.Internalf(op, "the stored %s is a %T", rev.Name, out)
	}
	return stored, unread, nil
}

// readImage reads one image.
func (r *Reconciler) readImage(ctx context.Context, kind, image string) (v1.AddonKindContract, error) {
	if r.images == nil {
		return v1.AddonKindContract{}, fault.Unavailablef("services.addon.readImage", "this server reads no images")
	}
	rt, c, err := r.images.Read(ctx, image)
	if err != nil {
		return v1.AddonKindContract{}, err
	}
	return v1.AddonKindContract{Kind: kind, Runtime: rt, Contract: c}, nil
}

// revision is the AddonRevision name, nil when there is none.
func (r *Reconciler) revision(ctx context.Context, name v1.ObjectName) (*v1.AddonRevision, error) {
	const op = "services.addon.revision"
	if name == "" {
		return nil, nil
	}
	obj, err := r.store.Get(ctx, v1.KindAddonRevision.GVK(), "", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get AddonRevision %s", name)
	}
	rev, ok := obj.(*v1.AddonRevision)
	if !ok {
		return nil, fault.Internalf(op, "object %s is not an AddonRevision", name)
	}
	return rev, nil
}

// MapAddonRevision maps a revision's change to the objects of its Addon's kinds: an image read since ends an
// ArtifactNotReady wait (Decision 4).
func (r *Reconciler) MapAddonRevision(ctx context.Context, obj v1.Object) []controller.Request {
	rev, ok := obj.(*v1.AddonRevision)
	if !ok {
		return nil
	}
	kinds := rev.Spec.Spec.Kinds()
	return r.objects(ctx, "", func(o *v1.AddonObject) bool {
		return slices.ContainsFunc(kinds, func(k string) bool { return v1.SameAddonKind(o.Spec.Kind, k) })
	})
}
