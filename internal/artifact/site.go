// site.go — the static-site bundle artifact (ADR-0139, F103): a prebuilt web app pushed as ONE
// deterministic tar+gzip layer (the ADR-0089 transport, reused) under its own artifact type. A site
// has no I/O contract and no runtime, so the manifest carries no contract blob and no runtime
// annotation; the distinct type keeps a site from ever being pulled as a function artifact.
package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"

	"github.com/pyvvo/funcd/api/fault"
)

// SiteArtifactType marks an OCI manifest as a funcd static-site bundle: one BundleTarMediaType layer,
// no contract blob and no runtime annotation (a site has no I/O contract). A distinct type keeps a site
// from being pulled by the function materializer.
const SiteArtifactType = "application/vnd.funcd.site.artifact.v1"

// PushSite packs dir as the deterministic tar+gzip bundle layer (ADR-0089) under SiteArtifactType and
// pushes it to ref's target, returning the manifest digest. Empty dir ⇒ fault.Invalid. It packs
// WITHOUT an entry-file gate (a site's index is a reconcile-time spec.index, unknown at push).
func PushSite(ctx context.Context, ref, dir string) (digest string, err error) {
	const op = "artifact.PushSite"
	info, serr := os.Stat(dir)
	if serr != nil || !info.IsDir() {
		return "", fault.Invalidf(op, "site path %q is not a directory", dir)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		return "", fault.Wrapf(rerr, fault.Internal, op, "read site dir %q", dir)
	}
	if len(entries) == 0 {
		return "", fault.Invalidf(op, "site dir %q is empty", dir)
	}
	data, perr := packDir(op, dir, "")
	if perr != nil {
		return "", perr
	}
	target, reference, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	manifest, perr := pushTarArtifact(ctx, op, target, reference, SiteArtifactType, data)
	if perr != nil {
		return "", perr
	}
	return manifest.Digest.String(), nil
}

// pushTarArtifact pushes data as one BundleTarMediaType layer under artifactType with the reproducible manifest
// (ADR-0089) and tags it reference unless that is "". The same data always yields the same manifest digest.
func pushTarArtifact(ctx context.Context, op string, target oras.Target, reference, artifactType string, data []byte) (ocispec.Descriptor, error) {
	layer := content.NewDescriptorFromBytes(BundleTarMediaType, data)
	if perr := target.Push(ctx, layer, bytes.NewReader(data)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, fault.Wrapf(perr, fault.Internal, op, "push layer")
	}
	manifest, merr := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, artifactType,
		reproducible(oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}}))
	if merr != nil {
		return ocispec.Descriptor{}, fault.Wrapf(merr, fault.Internal, op, "pack manifest")
	}
	if reference != "" {
		if terr := target.Tag(ctx, manifest, reference); terr != nil {
			return ocispec.Descriptor{}, fault.Wrapf(terr, fault.Internal, op, "tag manifest")
		}
	}
	return manifest, nil
}

// ResolveSite resolves ref (a tag or a digest) to its manifest digest, asserting SiteArtifactType.
// A function artifact ⇒ fault.Invalid; an absent ref ⇒ fault.NotFound.
func ResolveSite(ctx context.Context, ref string) (digest string, err error) {
	return resolveTyped(ctx, "artifact.ResolveSite", ref, SiteArtifactType, "site")
}

// PullSite fetches the digest-pinned site artifact and untars its bundle layer into dir using the
// existing traversal-safe untar (a "../"/absolute/symlink-escape entry is refused). The manifest is
// fetched BY DIGEST so a moved tag can never change what materializes.
func PullSite(ctx context.Context, ref, digest, dir string) error {
	return pullTyped(ctx, "artifact.PullSite", ref, digest, dir, SiteArtifactType, "site")
}

// resolveTyped resolves ref (a tag or a digest) to its manifest digest, asserting artifactType, which a what
// artifact has: another type ⇒ fault.Invalid; an absent ref ⇒ fault.NotFound.
func resolveTyped(ctx context.Context, op, ref, artifactType, what string) (digest string, err error) {
	target, reference, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	if reference == "" {
		return "", fault.Invalidf(op, "ref %q has no tag/digest to resolve", ref)
	}
	manifestDesc, manifestData, ferr := oras.FetchBytes(ctx, target, reference, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return "", fault.NotFoundf(op, "resolve %q: %v", ref, ferr)
	}
	if aerr := assertArtifactType(op, manifestDesc, manifestData, artifactType, what); aerr != nil {
		return "", aerr
	}
	return manifestDesc.Digest.String(), nil
}

// pullTyped fetches the digest-pinned artifact of artifactType, which a what artifact has, and untars its bundle
// layer into dir traversal-safely. The manifest is fetched BY DIGEST so a moved tag can never change what lands.
func pullTyped(ctx context.Context, op, ref, digest, dir, artifactType, what string) error {
	if digest == "" {
		return fault.Invalidf(op, "%s digest is required (the digest is the authority)", what)
	}
	target, _, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	manifestDesc, manifestData, ferr := oras.FetchBytes(ctx, target, digest, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return fault.NotFoundf(op, "fetch %s %s@%s: %v", what, ref, digest, ferr)
	}
	if manifestDesc.Digest.String() != digest {
		return fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", manifestDesc.Digest.String(), digest)
	}
	var manifest ocispec.Manifest
	if err := assertArtifactType(op, manifestDesc, manifestData, artifactType, what); err != nil {
		return err
	}
	if jerr := json.Unmarshal(manifestData, &manifest); jerr != nil {
		return fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	layer, ok := layerByMediaType(manifest.Layers, BundleTarMediaType)
	if !ok {
		return fault.Invalidf(op, "%s artifact %s has no bundle layer (media type %s)", what, digest, BundleTarMediaType)
	}
	if merr := os.MkdirAll(dir, 0o750); merr != nil {
		return fault.Wrapf(merr, fault.Internal, op, "create %s dir", what)
	}
	rc, ferr := target.Fetch(ctx, layer)
	if ferr != nil {
		return fault.Wrapf(ferr, fault.Internal, op, "fetch %s layer", what)
	}
	defer func() { _ = rc.Close() }()
	vr := content.NewVerifyReader(rc, layer)
	if uerr := untarBundle(op, vr, dir); uerr != nil {
		return uerr
	}
	if _, derr := io.Copy(io.Discard, vr); derr != nil {
		return fault.Wrapf(derr, fault.Internal, op, "drain %s layer", what)
	}
	if verr := vr.Verify(); verr != nil {
		return fault.Wrapf(verr, fault.Internal, op, "%s layer digest mismatch", what)
	}
	return nil
}

// assertArtifactType rejects a manifest whose artifactType is not want, the type of a what artifact: a function
// artifact (or any other artifactType) is fault.Invalid, so a Site never materializes a function bundle.
func assertArtifactType(op string, desc ocispec.Descriptor, data []byte, want, what string) error {
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(data, &manifest); jerr != nil {
		return fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	if manifest.ArtifactType != want {
		return fault.Invalidf(op, "artifact %s is %q, not a %s artifact (%s)", desc.Digest, manifest.ArtifactType, what, want)
	}
	return nil
}
