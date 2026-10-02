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
	layer := content.NewDescriptorFromBytes(BundleTarMediaType, data)
	if perr := target.Push(ctx, layer, bytes.NewReader(data)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
		return "", fault.Wrapf(perr, fault.Internal, op, "push site layer")
	}
	manifest, merr := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, SiteArtifactType,
		reproducible(oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}}))
	if merr != nil {
		return "", fault.Wrapf(merr, fault.Internal, op, "pack manifest")
	}
	if reference != "" {
		if terr := target.Tag(ctx, manifest, reference); terr != nil {
			return "", fault.Wrapf(terr, fault.Internal, op, "tag manifest")
		}
	}
	return manifest.Digest.String(), nil
}

// ResolveSite resolves ref (a tag or a digest) to its manifest digest, asserting SiteArtifactType.
// A function artifact ⇒ fault.Invalid; an absent ref ⇒ fault.NotFound.
func ResolveSite(ctx context.Context, ref string) (digest string, err error) {
	const op = "artifact.ResolveSite"
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
	if aerr := assertSiteManifest(op, manifestDesc, manifestData); aerr != nil {
		return "", aerr
	}
	return manifestDesc.Digest.String(), nil
}

// PullSite fetches the digest-pinned site artifact and untars its bundle layer into dir using the
// existing traversal-safe untar (a "../"/absolute/symlink-escape entry is refused). The manifest is
// fetched BY DIGEST so a moved tag can never change what materializes.
func PullSite(ctx context.Context, ref, digest, dir string) error {
	const op = "artifact.PullSite"
	if digest == "" {
		return fault.Invalidf(op, "site digest is required (the digest is the authority)")
	}
	target, _, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	manifestDesc, manifestData, ferr := oras.FetchBytes(ctx, target, digest, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return fault.NotFoundf(op, "fetch site %s@%s: %v", ref, digest, ferr)
	}
	if manifestDesc.Digest.String() != digest {
		return fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", manifestDesc.Digest.String(), digest)
	}
	var manifest ocispec.Manifest
	if err := assertSiteManifest(op, manifestDesc, manifestData); err != nil {
		return err
	}
	if jerr := json.Unmarshal(manifestData, &manifest); jerr != nil {
		return fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	layer, ok := layerByMediaType(manifest.Layers, BundleTarMediaType)
	if !ok {
		return fault.Invalidf(op, "site artifact %s has no bundle layer (media type %s)", digest, BundleTarMediaType)
	}
	if merr := os.MkdirAll(dir, 0o750); merr != nil {
		return fault.Wrapf(merr, fault.Internal, op, "create site dir")
	}
	rc, ferr := target.Fetch(ctx, layer)
	if ferr != nil {
		return fault.Wrapf(ferr, fault.Internal, op, "fetch site layer")
	}
	defer func() { _ = rc.Close() }()
	vr := content.NewVerifyReader(rc, layer)
	if uerr := untarBundle(op, vr, dir); uerr != nil {
		return uerr
	}
	if _, derr := io.Copy(io.Discard, vr); derr != nil {
		return fault.Wrapf(derr, fault.Internal, op, "drain site layer")
	}
	if verr := vr.Verify(); verr != nil {
		return fault.Wrapf(verr, fault.Internal, op, "site layer digest mismatch")
	}
	return nil
}

// assertSiteManifest rejects a manifest that is not a funcd site artifact: a function artifact (or any
// other artifactType) is fault.Invalid, so a Site can never materialize a function bundle.
func assertSiteManifest(op string, desc ocispec.Descriptor, data []byte) error {
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(data, &manifest); jerr != nil {
		return fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	if manifest.ArtifactType != SiteArtifactType {
		return fault.Invalidf(op, "artifact %s is %q, not a site artifact (%s)", desc.Digest, manifest.ArtifactType, SiteArtifactType)
	}
	return nil
}
