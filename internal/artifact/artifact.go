// Package artifact is OCI artifact distribution (ADR-0031): how a function source
// bundle reaches the platform. funcdctl pushes a bundle as an OCI artifact to a target
// (a local OCI layout for dev, or a registry for prod) and the platform pulls it by
// digest. It provides the producer side (Push/Pull/Login/Logout) and the consumer side —
// an OrasMaterializer that is a driver of ADR-0030's internal/function.Materializer seam.
//
// The artifact is one digest-addressed OCI artifact: a manifest of artifactType
// application/vnd.funcd.function.artifact.v1 with a single bundle-blob layer. The
// ArtifactRef.Digest is the manifest descriptor digest and is the authority — pulls are
// by digest, an empty digest is rejected, and a mutable tag is only a locator.
package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/function"
)

const (
	// artifactType marks the OCI manifest as a funcd function artifact (not a runnable image).
	artifactType = "application/vnd.funcd.function.artifact.v1"
	// bundleMediaType is the single blob layer: the JS bundle / Python wheel bytes.
	bundleMediaType = "application/vnd.funcd.function.bundle"
	// ociLayoutScheme marks a local OCI layout ref: oci-layout://<dir>[:<tag>].
	ociLayoutScheme = "oci-layout://"
	// contractMediaType is the dedicated contract-blob layer (ADR-0059): the generated I/O JSON
	// Schema(s), statically inspectable from the manifest without pulling the bundle or running code.
	contractMediaType = "application/vnd.funcd.contract.v1+json"
	// contractAnnotation flags the manifest as carrying a contract (value = the contract blob digest).
	contractAnnotation = "dev.funcd.contract.v1"
	// contractDialect is the JSON Schema dialect the generated contracts use (ADR-0058 profile).
	contractDialect = "https://json-schema.org/draft/2020-12/schema"
)

// ContractBlob assembles the contract-blob payload (ADR-0059): {input?, output?, dialect}. input and
// output are the generated JSON Schemas (ADR-0058; either may be nil → that side is omitted). Returns
// (nil, nil) when neither is present, so a contract-less push stays the unchanged ADR-0031 artifact.
func ContractBlob(input, output []byte) ([]byte, error) {
	const op = "artifact.ContractBlob"
	if len(input) == 0 && len(output) == 0 {
		return nil, nil
	}
	payload := struct {
		Input   json.RawMessage `json:"input,omitempty"`
		Output  json.RawMessage `json:"output,omitempty"`
		Dialect string          `json:"dialect"`
	}{Dialect: contractDialect}
	if len(input) > 0 {
		payload.Input = json.RawMessage(input)
	}
	if len(output) > 0 {
		payload.Output = json.RawMessage(output)
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return nil, fault.Internalf(op, "marshal contract blob: %v", err)
	}
	return blob, nil
}

// layerByMediaType returns the first layer with media type mt (false if none) — pull/inspect select
// a layer by what it IS, not by index, so an added contract layer never shifts the bundle (ADR-0059).
func layerByMediaType(layers []ocispec.Descriptor, mt string) (ocispec.Descriptor, bool) {
	for _, l := range layers {
		if l.MediaType == mt {
			return l, true
		}
	}
	return ocispec.Descriptor{}, false
}

// Push packages file as the §1 OCI artifact and pushes it to ref's target (a local OCI
// layout or a registry), returning the manifest descriptor digest. A light pre-flight
// rejects an empty bundle; the authoritative shape-gate is the shim (ADR-0030). When
// contract is non-nil (ADR-0059), it adds a content-addressed contract blob layer +
// the dev.funcd.contract.v1 manifest annotation; nil ⇒ the unchanged ADR-0031 artifact.
func Push(ctx context.Context, ref, file string, contract []byte) (digest string, err error) {
	const op = "artifact.Push"
	data, rerr := os.ReadFile(file) //nolint:gosec // file is a user-supplied CLI argument
	if rerr != nil {
		return "", fault.Invalidf(op, "read bundle %q: %v", file, rerr)
	}
	if len(data) == 0 {
		return "", fault.Invalidf(op, "bundle %q is empty", file)
	}
	target, reference, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}

	layer := content.NewDescriptorFromBytes(bundleMediaType, data)
	layer.Annotations = map[string]string{ocispec.AnnotationTitle: filepath.Base(file)}
	if perr := target.Push(ctx, layer, bytes.NewReader(data)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
		return "", fault.Wrapf(perr, fault.Internal, op, "push bundle blob")
	}
	opts := oras.PackManifestOptions{Layers: []ocispec.Descriptor{layer}}
	if len(contract) > 0 {
		contractLayer := content.NewDescriptorFromBytes(contractMediaType, contract)
		if perr := target.Push(ctx, contractLayer, bytes.NewReader(contract)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
			return "", fault.Wrapf(perr, fault.Internal, op, "push contract blob")
		}
		opts.Layers = append(opts.Layers, contractLayer)
		opts.ManifestAnnotations = map[string]string{contractAnnotation: contractLayer.Digest.String()}
	}
	manifest, merr := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, artifactType, opts)
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

// Pull fetches the artifact named by ref, verifies it against digest (the authority —
// empty digest is rejected), and writes the bundle blob into dir, returning its path.
func Pull(ctx context.Context, ref, digest, dir string) (path string, err error) {
	const op = "artifact.Pull"
	if digest == "" {
		return "", fault.Invalidf(op, "artifact digest is required (the digest is the authority)")
	}
	target, _, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	// Fetch the manifest BY DIGEST so a mutable tag can never swap what was deployed.
	manifestDesc, manifestData, ferr := oras.FetchBytes(ctx, target, digest, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return "", fault.NotFoundf(op, "fetch artifact %s@%s: %v", ref, digest, ferr)
	}
	if manifestDesc.Digest.String() != digest {
		return "", fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", manifestDesc.Digest.String(), digest)
	}
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(manifestData, &manifest); jerr != nil {
		return "", fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	// Select the bundle layer by media type (not Layers[0]) so a contract layer (ADR-0059) never
	// changes which bytes materialize.
	layer, ok := layerByMediaType(manifest.Layers, bundleMediaType)
	if !ok {
		return "", fault.Invalidf(op, "artifact %s has no bundle layer (media type %s)", digest, bundleMediaType)
	}
	blob, berr := content.FetchAll(ctx, target, layer) // verifies the blob against its descriptor digest
	if berr != nil {
		return "", fault.Wrapf(berr, fault.Internal, op, "fetch bundle blob")
	}
	name := layer.Annotations[ocispec.AnnotationTitle]
	if name == "" {
		name = "artifact.bin"
	}
	// Container-readable (0755 dir / 0644 file): in container mode (ADR-0032) the artifact is
	// bind-mounted read-only into a curated image that runs as an unprivileged, non-root user
	// (USER node, uid≠0), which must be able to traverse + read it. The artifact is non-secret
	// read-only function code (secrets are injected separately), so world-readable is correct;
	// process mode (same-user shim) is unaffected. (Surfaced by the ADR-0052 footprint lane.)
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		return "", fault.Wrapf(merr, fault.Internal, op, "create dir")
	}
	path = filepath.Join(dir, name)
	if werr := os.WriteFile(path, blob, 0o644); werr != nil { //nolint:gosec // non-secret RO code; container user must read it
		return "", fault.Wrapf(werr, fault.Internal, op, "write bundle")
	}
	return path, nil
}

// Inspect reads a function's I/O contract straight from its OCI manifest (ADR-0059): it fetches the
// manifest + the small contract blob ONLY — never the bundle layer, never executing code — and
// returns the raw contract JSON ({input?, output?, dialect}). It resolves by digest when one is
// supplied (tamper-evident: the inspected contract == the deployed one); a bare tag is resolved to
// its current manifest. fault.NotFound when the artifact carries no contract.
func Inspect(ctx context.Context, ref, digest string) (contract []byte, err error) {
	const op = "artifact.Inspect"
	target, reference, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return nil, fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	fetchRef := digest
	if fetchRef == "" {
		fetchRef = reference // no digest given → resolve the tag to its current manifest
	}
	if fetchRef == "" {
		return nil, fault.Invalidf(op, "inspect needs a digest or a tag (e.g. <ref>@<digest>)")
	}
	return inspectFrom(ctx, target, fetchRef, digest)
}

// inspectFrom is Inspect's core over an already-resolved target (the white-box seam the
// inspect-without-pull invariant test drives with a counting target). It fetches the manifest +
// contract blob ONLY; wantDigest (if non-empty) pins the manifest.
func inspectFrom(ctx context.Context, target oras.ReadOnlyTarget, fetchRef, wantDigest string) ([]byte, error) {
	const op = "artifact.Inspect"
	manifestDesc, manifestData, ferr := oras.FetchBytes(ctx, target, fetchRef, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return nil, fault.NotFoundf(op, "fetch artifact %s: %v", fetchRef, ferr)
	}
	if wantDigest != "" && manifestDesc.Digest.String() != wantDigest {
		return nil, fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", manifestDesc.Digest.String(), wantDigest)
	}
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(manifestData, &manifest); jerr != nil {
		return nil, fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	contractLayer, ok := layerByMediaType(manifest.Layers, contractMediaType)
	if !ok {
		return nil, fault.NotFoundf(op, "artifact %s carries no contract", fetchRef)
	}
	// Fetch ONLY the contract blob — the bundle layer is never fetched (the static-inspection invariant).
	blob, berr := content.FetchAll(ctx, target, contractLayer) // verifies the blob against its descriptor digest
	if berr != nil {
		return nil, fault.Wrapf(berr, fault.Internal, op, "fetch contract blob")
	}
	return blob, nil
}

// Login stores registry credentials via oras-go's credential store (the local OCI layout
// needs no login). It verifies the credential against the registry.
func Login(ctx context.Context, registry, user, pass string) error {
	const op = "artifact.Login"
	store, serr := credentials.NewStoreFromDocker(credentials.StoreOptions{AllowPlaintextPut: true})
	if serr != nil {
		return fault.Wrapf(serr, fault.Internal, op, "open credential store")
	}
	reg, rerr := remote.NewRegistry(registry)
	if rerr != nil {
		return fault.Invalidf(op, "registry %q: %v", registry, rerr)
	}
	if lerr := credentials.Login(ctx, store, reg, auth.Credential{Username: user, Password: pass}); lerr != nil {
		return fault.Wrapf(lerr, fault.Unavailable, op, "login to %s", registry)
	}
	return nil
}

// Logout removes stored credentials for a registry.
func Logout(ctx context.Context, registry string) error {
	const op = "artifact.Logout"
	store, serr := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	if serr != nil {
		return fault.Wrapf(serr, fault.Internal, op, "open credential store")
	}
	if lerr := credentials.Logout(ctx, store, registry); lerr != nil {
		return fault.Wrapf(lerr, fault.Internal, op, "logout of %s", registry)
	}
	return nil
}

// resolveTarget maps a ref to an oras Target + the tag/digest reference to use for
// tag/fetch ops. oci-layout://<dir>[:<tag>] → a local OCI layout; otherwise a registry.
func resolveTarget(_ context.Context, ref string) (oras.Target, string, error) {
	const op = "artifact.resolveTarget"
	if ref == "" {
		return nil, "", fault.Invalidf(op, "artifact ref is required")
	}
	if dir, tag, ok := parseLocalRef(ref); ok {
		if dir == "" {
			return nil, "", fault.Invalidf(op, "local layout ref %q has no directory", ref)
		}
		if merr := os.MkdirAll(dir, 0o750); merr != nil {
			return nil, "", fault.Wrapf(merr, fault.Internal, op, "create layout dir")
		}
		store, serr := oci.New(dir)
		if serr != nil {
			return nil, "", fault.Wrapf(serr, fault.Internal, op, "open OCI layout %q", dir)
		}
		return store, tag, nil
	}
	repo, rerr := remote.NewRepository(ref)
	if rerr != nil {
		return nil, "", fault.Invalidf(op, "parse registry ref %q: %v", ref, rerr)
	}
	if credStore, cerr := credentials.NewStoreFromDocker(credentials.StoreOptions{}); cerr == nil {
		repo.Client = &auth.Client{Client: auth.DefaultClient.Client, Cache: auth.NewCache(), Credential: credentials.Credential(credStore)}
	}
	return repo, repo.Reference.Reference, nil
}

// parseLocalRef splits oci-layout://<dir>[:<tag>] into its directory + optional tag. A
// tag is the suffix after the last ':' that contains no path separator.
func parseLocalRef(ref string) (dir, tag string, ok bool) {
	if !strings.HasPrefix(ref, ociLayoutScheme) {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, ociLayoutScheme)
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		if cand := rest[i+1:]; cand != "" && !strings.ContainsAny(cand, `/\`) {
			return rest[:i], cand, true
		}
	}
	return rest, "", true
}

// OrasMaterializer is a DRIVER of ADR-0030's internal/function.Materializer: it pulls a
// Function's artifact by digest into a per-digest cache dir (immutable) and returns the
// local path the shim reads. The local-file driver (ADR-0030) stays the no-dep test stand-in.
type OrasMaterializer struct {
	artifactDir string // root for the per-digest artifact cache
}

// NewOrasMaterializer builds the OCI-backed Materializer caching under artifactDir.
func NewOrasMaterializer(artifactDir string) *OrasMaterializer {
	return &OrasMaterializer{artifactDir: artifactDir}
}

// Materialize resolves fn.spec.artifact.uri → target, pulls by fn.spec.artifact.digest
// (the authority; empty → fault.Invalid), and returns the cached local path.
func (m *OrasMaterializer) Materialize(ctx context.Context, fn *v1.Function) (string, error) {
	const op = "artifact.OrasMaterializer.Materialize"
	ref := fn.Spec.Artifact.URI
	digest := fn.Spec.Artifact.Digest
	if ref == "" {
		return "", fault.Invalidf(op, "function %s/%s has no spec.artifact.uri", fn.Namespace, fn.Name)
	}
	if digest == "" {
		return "", fault.Invalidf(op, "function %s/%s has no spec.artifact.digest (the digest is the authority)", fn.Namespace, fn.Name)
	}
	cacheDir := filepath.Join(m.artifactDir, sanitizeDigest(digest))
	if entries, derr := os.ReadDir(cacheDir); derr == nil && len(entries) > 0 {
		return filepath.Join(cacheDir, entries[0].Name()), nil // cached (immutable per digest)
	}
	path, perr := Pull(ctx, ref, digest, cacheDir)
	if perr != nil {
		return "", fault.Wrapf(perr, fault.KindOf(perr), op, "materialize %s/%s", fn.Namespace, fn.Name)
	}
	return path, nil
}

// Resolve resolves an OCI artifact ref (its tag) to the manifest digest (ADR-0035), so the
// reconciler can pin it into the immutable Revision without the user typing it. It is the
// ArtifactResolver seam the Function reconciler calls at Revision-stamp time.
func (m *OrasMaterializer) Resolve(ctx context.Context, uri string) (string, error) {
	const op = "artifact.OrasMaterializer.Resolve"
	target, ref, terr := resolveTarget(ctx, uri)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	if ref == "" {
		return "", fault.Invalidf(op, "ref %q has no tag/digest to resolve", uri)
	}
	desc, rerr := target.Resolve(ctx, ref)
	if rerr != nil {
		return "", fault.NotFoundf(op, "resolve %q: %v", uri, rerr)
	}
	return desc.Digest.String(), nil
}

var _ function.ArtifactResolver = (*OrasMaterializer)(nil) // implements the ADR-0035 resolver seam

// sanitizeDigest turns "sha256:abcd…" into a filesystem-safe cache key.
func sanitizeDigest(digest string) string {
	return strings.ReplaceAll(digest, ":", "-")
}

var _ function.Materializer = (*OrasMaterializer)(nil) // implements the ADR-0030 seam
