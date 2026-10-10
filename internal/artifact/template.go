// template.go — the App template artifact (ADR-0218, F121): a template directory pushed unchanged as ONE deterministic
// tar+gzip layer (the ADR-0089 transport, as a site is) under its own artifact type, tagged with the template's
// version. Only funcdctl pushes and pulls it; the server never reads a template.
package artifact

import (
	"context"
	"errors"

	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"

	"github.com/pyvvo/funcd/api/fault"
)

// AppTemplateArtifactType marks an OCI manifest as a funcd App template: one BundleTarMediaType layer holding
// app.yaml, app.lock and resources/*.yaml as git holds them.
const AppTemplateArtifactType = "application/vnd.funcd.app-template.artifact.v1"

// PushTemplate packs dir as it is into one BundleTarMediaType layer under AppTemplateArtifactType and pushes it to
// ref, whose tag must be version. Before any write it refuses a ref without a tag, with a digest, or whose tag is
// not version, and a version tag already at another digest (fault.Conflict); the same digest there writes nothing.
func PushTemplate(ctx context.Context, ref, dir, version string) (digest string, err error) {
	const op = "artifact.PushTemplate"
	tag, err := refTag(op, ref)
	if err != nil {
		return "", err
	}
	switch {
	case tag == "":
		return "", fault.Invalidf(op, "ref %q has no tag: a template is pushed with its version %s as the tag", ref, version)
	case IsDigest(tag):
		return "", fault.Invalidf(op, "ref %q names a digest: a template is pushed with its version %s as the tag", ref, version)
	case registry.Reference{Reference: tag}.ValidateReferenceAsTag() != nil:
		return "", fault.Invalidf(op, "tag %s is not an OCI tag (no +): the template's version %s cannot be one", tag, version)
	case tag != version:
		return "", fault.Invalidf(op, "tag %s is not the template's version %s", tag, version)
	}
	data, err := packDir(op, dir, "")
	if err != nil {
		return "", err
	}
	packed, err := pushTarArtifact(ctx, op, memory.New(), "", AppTemplateArtifactType, data)
	if err != nil {
		return "", err
	}
	digest = packed.Digest.String()
	switch at, err := tagDigest(ctx, op, ref, tag); {
	case err != nil:
		return "", err
	case at == digest:
		return digest, nil
	case at != "":
		return "", fault.Conflictf(op, "tag %s already names %s, not this template's %s: a version is pushed once", tag, at, digest)
	}
	target, _, err := resolveTarget(ctx, ref)
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "resolve target")
	}
	pushed, err := pushTarArtifact(ctx, op, target, tag, AppTemplateArtifactType, data)
	if err != nil {
		return "", err
	}
	if pushed.Digest != packed.Digest {
		return "", fault.Internalf(op, "pushed %s, packed %s", pushed.Digest, packed.Digest)
	}
	return digest, nil
}

// refTag is ref's tag or digest, without opening its target.
func refTag(op, ref string) (string, error) {
	if _, tag, ok := parseLocalRef(ref); ok {
		return tag, nil
	}
	r, err := registry.ParseReference(ref)
	if err != nil {
		return "", fault.Invalidf(op, "parse registry ref %q: %v", ref, err)
	}
	return r.Reference, nil
}

// tagDigest is the digest tag names at ref's target, "" when the tag, or the layout, does not exist.
func tagDigest(ctx context.Context, op, ref, tag string) (string, error) {
	target, _, err := resolveReadTarget(ctx, ref)
	if fault.KindOf(err) == fault.NotFound {
		return "", nil
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), op, "resolve target")
	}
	desc, err := target.Resolve(ctx, tag)
	if errors.Is(err, errdef.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "resolve tag %s", tag)
	}
	return desc.Digest.String(), nil
}

// ResolveTemplate resolves ref (a tag or a digest) to its manifest digest, asserting AppTemplateArtifactType: a
// site or a function artifact ⇒ fault.Invalid; an absent ref ⇒ fault.NotFound.
func ResolveTemplate(ctx context.Context, ref string) (digest string, err error) {
	return resolveTyped(ctx, "artifact.ResolveTemplate", ref, AppTemplateArtifactType, "template")
}

// PullTemplate fetches the template artifact by digest and unpacks its layer into dir, traversal-safely.
func PullTemplate(ctx context.Context, ref, digest, dir string) error {
	return pullTyped(ctx, "artifact.PullTemplate", ref, digest, dir, AppTemplateArtifactType, "template")
}

// ListTags lists every tag of repo, a registry repository or an oci-layout:// directory, through all pages.
func ListTags(ctx context.Context, repo string) ([]string, error) {
	const op = "artifact.ListTags"
	target, _, err := resolveReadTarget(ctx, repo)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "resolve target")
	}
	lister, ok := target.(registry.TagLister)
	if !ok {
		return nil, fault.Internalf(op, "%s cannot list tags", repo)
	}
	tags, err := registry.Tags(ctx, lister)
	if errors.Is(err, errdef.ErrNotFound) {
		return nil, fault.NotFoundf(op, "list the tags of %s: %v", repo, err)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Unavailable, op, "list the tags of %s", repo)
	}
	return tags, nil
}

// ResolveDigest resolves an OCI artifact ref (its tag, or a digest) to the manifest digest (ADR-0035).
func ResolveDigest(ctx context.Context, ref string) (string, error) {
	const op = "artifact.ResolveDigest"
	target, reference, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	if reference == "" {
		return "", fault.Invalidf(op, "ref %q has no tag/digest to resolve", ref)
	}
	desc, rerr := target.Resolve(ctx, reference)
	if rerr != nil {
		return "", fault.NotFoundf(op, "resolve %q: %v", ref, rerr)
	}
	return desc.Digest.String(), nil
}

// TagResolver reads image registries for an App template's lock: it satisfies template.ImageResolver.
type TagResolver struct{}

// Tags lists every tag of repo (ListTags).
func (TagResolver) Tags(ctx context.Context, repo string) ([]string, error) {
	return ListTags(ctx, repo)
}

// Digest resolves ref to its manifest digest (ResolveDigest).
func (TagResolver) Digest(ctx context.Context, ref string) (string, error) {
	return ResolveDigest(ctx, ref)
}
