// Package artifact is OCI artifact distribution (ADR-0031): how a function source
// bundle reaches the platform. funcdctl pushes a bundle as an OCI artifact to a target
// (a local OCI layout for dev, or a registry for prod) and the platform pulls it by
// digest. It provides the producer side (Push/Pull/Login/Logout) and the consumer side —
// an OrasMaterializer that is a driver of ADR-0030's internal/function.Materializer seam.
//
// The artifact is one digest-addressed OCI artifact: a manifest of artifactType
// application/vnd.funcd.function.artifact.v1 with a single bundle-blob layer. The
// imageDigest is the manifest descriptor digest and is the authority — pulls are
// by digest, an empty digest is rejected, and a mutable tag is only a locator.
package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/platform/httpx"
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
	// runtimeAnnotation records the function's runtime class on the manifest (ADR-0094): a
	// self-describing artifact so the workflow materializer reads the runtime from the manifest
	// alone — never pulling the bundle or running code. Empty ⇒ no runtime is asserted.
	runtimeAnnotation = "dev.funcd.runtime.v1"
	// contractDialect is the JSON Schema dialect the generated contracts use (ADR-0058 profile).
	contractDialect = "https://json-schema.org/draft/2020-12/schema"
)

// VoidSchema is the canonical void side (ADR-0090): a side that carries no meaningful payload is the
// explicit JSON Schema {"type":"null"} — never an omission. Both sides are always present in the blob;
// a void side declares itself with this schema, and its validator is compiled from it like any other.
const VoidSchema = `{"type":"null"}`

// ContractBlob assembles the mandatory {dialect, input, output} contract blob (ADR-0090, supersedes
// ADR-0059's optional form). BOTH input and output must be present and non-empty — a void side is
// VoidSchema, never nil. Returns fault.Invalid if either is missing; there is no contract-less
// artifact. The marshaled JSON always serializes both fields (no omitempty).
func ContractBlob(input, output []byte) ([]byte, error) {
	const op = "artifact.ContractBlob"
	if len(input) == 0 {
		return nil, fault.Invalidf(op, "every function must declare an input contract (a void side is %s)", VoidSchema)
	}
	if len(output) == 0 {
		return nil, fault.Invalidf(op, "every function must declare an output contract (a void side is %s)", VoidSchema)
	}
	payload := struct {
		Input   json.RawMessage `json:"input"`
		Output  json.RawMessage `json:"output"`
		Dialect string          `json:"dialect"`
	}{
		Input:   json.RawMessage(input),
		Output:  json.RawMessage(output),
		Dialect: contractDialect,
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

// reproducible returns opts with the manifest creation time fixed to the bundle's zero time. oras stamps the wall clock
// when none is given, so identical content pushed in different seconds got different digests (#237); a fixed value
// keeps ADR-0089's same tree, same digest at the manifest too.
func reproducible(opts oras.PackManifestOptions) oras.PackManifestOptions {
	annotations := make(map[string]string, len(opts.ManifestAnnotations)+1)
	maps.Copy(annotations, opts.ManifestAnnotations)
	annotations[ocispec.AnnotationCreated] = zeroTime.Format(time.RFC3339)
	opts.ManifestAnnotations = annotations
	return opts
}

// Push packages file as the §1 OCI artifact and pushes it to ref's target (a local OCI
// layout or a registry), returning the manifest descriptor digest. A light pre-flight
// rejects an empty bundle and a dotfile name (Pull refuses it as a title); the authoritative
// shape-gate is the shim (ADR-0030). When
// contract is non-nil (ADR-0059), it adds a content-addressed contract blob layer +
// the dev.funcd.contract.v1 manifest annotation; nil ⇒ the unchanged ADR-0031 artifact. A non-empty
// platform records PlatformAnnotation (ADR-0145); "" records none.
func Push(ctx context.Context, ref, file string, contract []byte, runtime string, platform v1.OCIPlatform) (digest string, err error) {
	const op = "artifact.Push"
	name := filepath.Base(file)
	if !plainFileName(name) {
		return "", fault.Invalidf(op, "bundle %q: a file name starting with \".\" is reserved for cache metadata", file)
	}
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
	layer.Annotations = map[string]string{ocispec.AnnotationTitle: name}
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
	if runtime != "" {
		if opts.ManifestAnnotations == nil {
			opts.ManifestAnnotations = map[string]string{}
		}
		opts.ManifestAnnotations[runtimeAnnotation] = runtime
	}
	if platform != "" {
		if opts.ManifestAnnotations == nil {
			opts.ManifestAnnotations = map[string]string{}
		}
		opts.ManifestAnnotations[PlatformAnnotation] = string(platform)
	}
	manifest, merr := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, artifactType, reproducible(opts))
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

// plainFileName reports whether name, a single-file artifact's title, is a bare file name: not a
// path, which would leave the destination dir, and not a dotfile, which would land on a cache
// metadata sidecar (.funcd-entry, .funcd-contract.json) the resolver trusts.
func plainFileName(name string) bool {
	slash := filepath.ToSlash(name)
	return !strings.Contains(slash, "/") && !strings.HasPrefix(slash, ".")
}

// Pull fetches the artifact named by ref, verifies it against digest (the authority —
// empty digest is rejected), and writes the bundle blob into dir, returning its path. When digest
// names an OCI image index (ADR-0145), the manifest for node ("" ⇒ the daemon's own platform) is used.
func Pull(ctx context.Context, ref, digest, dir string, node v1.OCIPlatform) (path string, err error) {
	const op = "artifact.Pull"
	if digest == "" {
		return "", fault.Invalidf(op, "artifact digest is required (the digest is the authority)")
	}
	target, _, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	// Fetch the manifest BY DIGEST so a mutable tag can never swap what was deployed.
	_, manifest, ferr := fetchManifest(ctx, op, target, digest, digest, nodeOrHost(node))
	if ferr != nil {
		return "", ferr
	}
	// A multi-file bundle (ADR-0089) is a BundleTarMediaType layer: untar it into dir (traversal-safe)
	// and return dir/<entry>. This is selected before the single-blob layer so a bundle artifact takes
	// the tar path; a single-blob artifact keeps the ADR-0031 behavior below unchanged.
	if bundleLayer, ok := layerByMediaType(manifest.Layers, BundleTarMediaType); ok {
		entryPath, berr := pullBundle(ctx, op, target, bundleLayer, dir)
		if berr != nil {
			return "", berr
		}
		// ADR-0123: deliver the digest-pinned ADR-0059 contract blob into the bundle dir so the shim
		// compiles the exact bytes `inspect` advertises (advertised == enforced). The bundle already
		// carries __funcd_contract.json in its tar; overwriting it with the promoted blob keeps a
		// single source of truth and never leaves a contracted function un-validated (no fail-open).
		if cerr := deliverContract(ctx, op, target, &manifest, dir, bundleContractFile); cerr != nil {
			return "", cerr
		}
		return entryPath, nil
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
	// The title is publisher-controlled: the digest authenticates the bytes, not the name.
	if !plainFileName(name) {
		return "", fault.Invalidf(op, "bundle title annotation %q is not a plain file name", name)
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
	// ADR-0123: deliver the digest-pinned ADR-0059 contract blob as a DOTFILE sidecar beside the
	// single-file handler so the shim compiles the exact advertised bytes (advertised == enforced,
	// no fail-open). The dotfile name is skipped by the FUNCD_ARTIFACT resolver (below), so it can
	// never displace the handler as the lone entry.
	if cerr := deliverContract(ctx, op, target, &manifest, dir, singleFileContractSidecar); cerr != nil {
		return "", cerr
	}
	return path, nil
}

// deliverContract fetches the manifest's ADR-0059 contract blob (by its content-addressed layer,
// digest-verified) and writes it into dir under filename — the ADR-0123 schema delivery. A manifest
// with no contract layer is a no-op (a legacy contract-less ADR-0031 artifact); a contracted one
// always lands its schema so the worker never serves un-validated (fail-closed is the shim's job
// when the file is absent, this guarantees it is present for every contracted function).
func deliverContract(ctx context.Context, op string, target oras.ReadOnlyTarget, manifest *ocispec.Manifest, dir, filename string) error {
	layer, ok := layerByMediaType(manifest.Layers, contractMediaType)
	if !ok {
		return nil // contract-less artifact (legacy ADR-0031) — nothing to deliver
	}
	blob, berr := content.FetchAll(ctx, target, layer) // verifies the blob against its descriptor digest
	if berr != nil {
		return fault.Wrapf(berr, fault.Internal, op, "fetch contract blob for delivery")
	}
	if werr := os.WriteFile(filepath.Join(dir, filename), blob, 0o644); werr != nil { //nolint:gosec // non-secret contract schema; the container user must read it
		return fault.Wrapf(werr, fault.Internal, op, "write contract %q", filename)
	}
	return nil
}

// Inspect reads a function's I/O contract straight from its OCI manifest (ADR-0059): it fetches the
// manifest + the small contract blob ONLY — never the bundle layer, never executing code — and
// returns the raw contract JSON ({input?, output?, dialect}). It resolves by digest when one is
// supplied (tamper-evident: the inspected contract == the deployed one); a bare tag is resolved to
// its current manifest. fault.NotFound when the artifact carries no contract.
func Inspect(ctx context.Context, ref, digest string) (contract []byte, err error) {
	const op = "artifact.Inspect"
	target, reference, terr := resolveReadTarget(ctx, ref)
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
	blob, _, ierr := inspectFrom(ctx, target, fetchRef, digest)
	return blob, ierr
}

// InspectContract is Inspect that also returns the resolved manifest descriptor digest (ADR-0098): the
// single metadata fetch already yields it, so a caller (the workflow contract-check gate) can record the
// pinned ref@digest alongside the contract without a second round-trip. Same static-inspection invariant
// — the bundle bytes are never fetched.
func InspectContract(ctx context.Context, ref, digest string) (contract []byte, resolvedDigest string, err error) {
	const op = "artifact.InspectContract"
	target, reference, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return nil, "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	fetchRef := digest
	if fetchRef == "" {
		fetchRef = reference
	}
	if fetchRef == "" {
		return nil, "", fault.Invalidf(op, "inspect needs a digest or a tag (e.g. <ref>@<digest>)")
	}
	return inspectFrom(ctx, target, fetchRef, digest)
}

// InspectRuntime reads a function's runtime class straight from its OCI manifest annotation
// (dev.funcd.runtime.v1, ADR-0094): it fetches the manifest ONLY — never the bundle, never the
// contract, never executing code — so the workflow materializer can resolve a step image's runtime
// without pulling it. It resolves by digest when supplied (tamper-evident), else a bare tag.
// fault.NotFound when the artifact asserts no runtime.
func InspectRuntime(ctx context.Context, ref, digest string) (runtime string, err error) {
	const op = "artifact.InspectRuntime"
	target, reference, terr := resolveReadTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	fetchRef := digest
	if fetchRef == "" {
		fetchRef = reference
	}
	if fetchRef == "" {
		return "", fault.Invalidf(op, "inspect needs a digest or a tag (e.g. <ref>@<digest>)")
	}
	// An index is read through its first manifest: a funcd index shares one runtime (ADR-0145).
	_, manifest, ferr := fetchManifest(ctx, op, target, fetchRef, digest, "")
	if ferr != nil {
		return "", ferr
	}
	rt := manifest.Annotations[runtimeAnnotation]
	if rt == "" {
		return "", fault.NotFoundf(op, "artifact %s asserts no runtime", fetchRef)
	}
	return rt, nil
}

// inspectFrom is Inspect's core over an already-resolved target (the white-box seam the
// inspect-without-pull invariant test drives with a counting target). It fetches the manifest +
// contract blob ONLY; wantDigest (if non-empty) pins the manifest.
func inspectFrom(ctx context.Context, target oras.ReadOnlyTarget, fetchRef, wantDigest string) ([]byte, string, error) {
	const op = "artifact.Inspect"
	// An index is read through its first manifest (one contract across platforms, ADR-0145); resolved stays
	// the index digest, the pinned authority.
	resolved, manifest, ferr := fetchManifest(ctx, op, target, fetchRef, wantDigest, "")
	if ferr != nil {
		return nil, "", ferr
	}
	contractLayer, ok := layerByMediaType(manifest.Layers, contractMediaType)
	if !ok {
		return nil, "", fault.NotFoundf(op, "artifact %s carries no contract", fetchRef)
	}
	// Fetch ONLY the contract blob — the bundle layer is never fetched (the static-inspection invariant).
	blob, berr := content.FetchAll(ctx, target, contractLayer) // verifies the blob against its descriptor digest
	if berr != nil {
		return nil, "", fault.Wrapf(berr, fault.Internal, op, "fetch contract blob")
	}
	return blob, resolved, nil
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
	reg.Client = registryClient(nil)
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
// tag/fetch ops. oci-layout://<dir>[:<tag>] → a local OCI layout, created when missing; otherwise a registry.
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
		var store *oci.Store
		if serr := withLayout(dir, func(s *oci.Store) error { store = s; return nil }); serr != nil {
			return nil, "", fault.Wrapf(serr, fault.Internal, op, "open OCI layout %q", dir)
		}
		return &layoutTarget{ReadOnlyTarget: store, dir: dir}, tag, nil
	}
	repo, rerr := remote.NewRepository(ref)
	if rerr != nil {
		return nil, "", fault.Invalidf(op, "parse registry ref %q: %v", ref, rerr)
	}
	var cred auth.CredentialFunc
	if credStore, cerr := credentials.NewStoreFromDocker(credentials.StoreOptions{}); cerr == nil {
		cred = credentials.Credential(credStore)
	}
	repo.Client = registryClient(cred)
	return repo, repo.Reference.Reference, nil
}

// registryResponseTimeout bounds one wait for a registry's response headers. A registry that accepts a request and
// never answers then fails the call, after oras-go's retries, instead of holding the reconcile that made it, and the
// controller's only worker, forever (#697).
const registryResponseTimeout = 10 * time.Second

// registryClient is oras-go's retrying auth client over a transport of its own: its default client, also used when
// a repository's Client is nil, sends through http.DefaultTransport (#571).
func registryClient(cred auth.CredentialFunc) *auth.Client {
	tr := httpx.Transport()
	tr.ResponseHeaderTimeout = registryResponseTimeout
	return &auth.Client{
		Client:     &http.Client{Transport: retry.NewTransport(tr)},
		Cache:      auth.NewCache(),
		Credential: cred,
	}
}

// layoutTarget is a local OCI layout. oras-go's oci.Store reads index.json once, when it opens, and rewrites the whole
// file on every manifest push and tag, with locking that holds only inside one process. So reads use the store
// resolveTarget opened, and each write opens the store again under the layout lock: parallel pushes into one layout
// then add to the index.json the other wrote instead of overwriting it (issue #97).
type layoutTarget struct {
	oras.ReadOnlyTarget
	dir string
}

func (l *layoutTarget) Push(ctx context.Context, expected ocispec.Descriptor, r io.Reader) error {
	return withLayout(l.dir, func(s *oci.Store) error { return s.Push(ctx, expected, r) })
}

func (l *layoutTarget) Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error {
	return withLayout(l.dir, func(s *oci.Store) error { return s.Tag(ctx, desc, reference) })
}

// resolveReadTarget is resolveTarget for a read: it writes nothing, so a ref to a directory that holds no layout is
// fault.NotFound instead of an empty layout created there (issue #361). oci.New writes a missing oci-layout and
// index.json; the read-only store does not.
func resolveReadTarget(ctx context.Context, ref string) (oras.ReadOnlyTarget, string, error) {
	const op = "artifact.resolveReadTarget"
	dir, tag, ok := parseLocalRef(ref)
	if !ok || dir == "" {
		return resolveTarget(ctx, ref)
	}
	unlock, err := lockLayout(dir)
	if err == nil {
		defer unlock()
		var store *oci.ReadOnlyStore
		if store, err = oci.NewFromFS(ctx, os.DirFS(dir)); err == nil {
			return store, tag, nil
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", fault.NotFoundf(op, "no OCI layout at %q", dir)
	}
	return nil, "", fault.Wrapf(err, fault.Internal, op, "open OCI layout %q", dir)
}

// withLayout opens the OCI store at dir and runs fn while it holds the layout lock, so no other process rewrites
// index.json between the store reading it and fn writing it.
func withLayout(dir string, fn func(*oci.Store) error) error {
	const op = "artifact.withLayout"
	unlock, err := lockLayout(dir)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "lock OCI layout %q", dir)
	}
	defer unlock()
	store, err := oci.New(dir)
	if err != nil {
		return err
	}
	return fn(store)
}

// lockLayout takes an exclusive lock on the layout directory: oras-go rewrites index.json in place, so a reader must
// not open it mid-write either. O_RDONLY without O_CREATE locks the directory itself, so no lock file lands in the
// layout, and a missing directory fails with fs.ErrNotExist.
func lockLayout(dir string) (unlock func(), err error) {
	lock := flock.New(dir, flock.SetFlag(os.O_RDONLY))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = lock.Unlock() }, nil
}

// parseLocalRef splits oci-layout://<dir>[:<tag>][@<digest>] into its directory + optional reference. A
// tag is the suffix after the last ':' that contains no path separator; a digest wins over a tag, as in a
// registry ref.
func parseLocalRef(ref string) (dir, tag string, ok bool) {
	if !strings.HasPrefix(ref, ociLayoutScheme) {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, ociLayoutScheme)
	if i := strings.LastIndex(rest, "@"); i >= 0 && IsDigest(rest[i+1:]) {
		dir, _, _ = parseLocalRef(ociLayoutScheme + rest[:i])
		return dir, rest[i+1:], true
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		if cand := rest[i+1:]; cand != "" && !strings.ContainsAny(cand, `/\`) {
			return rest[:i], cand, true
		}
	}
	return rest, "", true
}

// IsDigest reports whether a ref's reference part is a digest rather than a tag.
func IsDigest(reference string) bool {
	return registry.Reference{Reference: reference}.ValidateReferenceAsDigest() == nil
}

// OrasMaterializer is a DRIVER of ADR-0030's internal/function.Materializer: it pulls a
// Function's artifact by digest into a per-digest cache dir (immutable) and returns the
// local path the shim reads. The local-file driver (ADR-0030) stays the no-dep test stand-in.
type OrasMaterializer struct {
	artifactDir string         // root for the per-digest artifact cache
	node        v1.OCIPlatform // the platform this node runs: selects an index's manifest (ADR-0145)

	mu        sync.Mutex
	platforms map[string][]v1.OCIPlatform // per digest; an artifact digest is immutable
}

// NewOrasMaterializer builds the OCI-backed Materializer caching under artifactDir, for a node running
// node ("" ⇒ the daemon's own platform).
func NewOrasMaterializer(artifactDir string, node v1.OCIPlatform) *OrasMaterializer {
	return &OrasMaterializer{artifactDir: artifactDir, node: nodeOrHost(node), platforms: map[string][]v1.OCIPlatform{}}
}

// Platforms implements function.PlatformResolver (ADR-0145): the platforms the artifact at digest provides,
// cached per digest in memory and in the artifact cache dir, so a restarted daemon starts a cached artifact without
// its source.
func (m *OrasMaterializer) Platforms(ctx context.Context, uri, digest string) ([]v1.OCIPlatform, error) {
	if digest == "" {
		return Platforms(ctx, uri, digest)
	}
	m.mu.Lock()
	cached, ok := m.platforms[digest]
	m.mu.Unlock()
	if ok {
		return cached, nil
	}
	file := filepath.Join(m.artifactDir, sanitizeDigest(digest)+".platforms.json")
	var ps []v1.OCIPlatform
	data, err := os.ReadFile(file) //nolint:gosec // file is in the daemon-owned artifact cache
	if err != nil || json.Unmarshal(data, &ps) != nil {
		if ps, err = Platforms(ctx, uri, digest); err != nil {
			return nil, err
		}
		if data, err = json.Marshal(ps); err == nil && os.MkdirAll(m.artifactDir, 0o755) == nil {
			_ = os.WriteFile(file, data, 0o600) // best-effort: a miss asks the source again
		}
	}
	m.mu.Lock()
	m.platforms[digest] = ps
	m.mu.Unlock()
	return ps, nil
}

// Materialize resolves fn.spec.image → target, pulls by fn.spec.imageDigest
// (the authority; empty → fault.Invalid), and returns the cached local path.
func (m *OrasMaterializer) Materialize(ctx context.Context, fn *v1.Function) (string, error) {
	const op = "artifact.OrasMaterializer.Materialize"
	ref := fn.Spec.Image
	digest := fn.Spec.ImageDigest
	if ref == "" {
		return "", fault.Invalidf(op, "function %s/%s has no spec.image", fn.Namespace, fn.Name)
	}
	if digest == "" {
		return "", fault.Invalidf(op, "function %s/%s has no spec.imageDigest (the digest is the authority)", fn.Namespace, fn.Name)
	}
	// keyed by the node platform too: an index materializes a different bundle per platform (ADR-0145)
	cacheDir := filepath.Join(m.artifactDir, sanitizeDigest(digest)+"-"+m.node.OS()+"-"+m.node.Arch())
	if entries, derr := os.ReadDir(cacheDir); derr == nil && len(entries) > 0 {
		// A multi-file bundle (ADR-0089) leaves an entry sidecar on the miss path; on a hit its
		// entry is authoritative (entries[0] is non-deterministic across a bundle's many files). A
		// single-file cache has no sidecar → fall back to the lone NON-dotfile regular file. Dotfiles
		// (.funcd-entry, .funcd-contract.json — ADR-0123) are metadata, never the handler, so the
		// resolver skips them; the delivered contract sidecar can never displace the handler. A
		// directory is never the handler either: Python writes __pycache__/ beside it on import.
		if entry := bundleEntryFromCache(cacheDir); entry != "" {
			root, aerr := filepath.Abs(cacheDir)
			if aerr != nil {
				return "", fault.Wrapf(aerr, fault.Internal, op, "resolve cache dir")
			}
			// The sidecar is cache content: a cache filled before titles were checked may hold one
			// that leaves the cache dir.
			return safeJoin(op, root, entry)
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") && e.Type().IsRegular() {
				return filepath.Join(cacheDir, e.Name()), nil // cached single-file (immutable per digest)
			}
		}
		// Only dotfiles cached (no handler) — fall through to Pull to re-materialize.
	}
	path, perr := Pull(ctx, ref, digest, cacheDir, m.node)
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
	target, ref, terr := resolveReadTarget(ctx, uri)
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

var _ function.PlatformResolver = (*OrasMaterializer)(nil) // implements the ADR-0145 platform seam
