package artifact

// Multi-arch function artifacts (ADR-0145): a per-platform push records the platform on its manifest, an OCI image
// index lists one such manifest per platform, and a node pulls the manifest for its own platform.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// PlatformAnnotation records, on a function manifest, the platform its bundle was built for (ADR-0145).
const PlatformAnnotation = "dev.funcd.platform"

// IsArtifactPlatform reports whether `funcdctl push --platform` accepts p.
func IsArtifactPlatform(p v1.OCIPlatform) bool {
	return p == v1.PlatformLinuxAMD64 || p == v1.PlatformLinuxARM64
}

// nodeOrHost is the node platform a pull selects by: node, or the daemon's own when empty.
func nodeOrHost(node v1.OCIPlatform) v1.OCIPlatform {
	if node == "" {
		return v1.HostPlatform()
	}
	return node
}

// manifestKind is the shape of a fetched manifest-type blob: an image index or a manifest.
type manifestKind struct {
	MediaType string            `json:"mediaType"`
	Manifests []json.RawMessage `json:"manifests"`
}

func isIndex(desc ocispec.Descriptor, data []byte) bool {
	if desc.MediaType == ocispec.MediaTypeImageIndex {
		return true
	}
	var k manifestKind
	if json.Unmarshal(data, &k) != nil {
		return false
	}
	return k.MediaType == ocispec.MediaTypeImageIndex || (k.MediaType == "" && len(k.Manifests) > 0)
}

// fetchManifest fetches fetchRef and returns the resolved digest (the index's, when it names one) and the function
// manifest. An index is followed to the descriptor matching node, or to its first descriptor when node is empty
// (inspection: every manifest of a funcd index shares its contract and runtime, ADR-0145 Decision 2).
func fetchManifest(ctx context.Context, op string, target oras.ReadOnlyTarget, fetchRef, wantDigest string, node v1.OCIPlatform) (string, ocispec.Manifest, error) {
	var manifest ocispec.Manifest
	desc, data, ferr := oras.FetchBytes(ctx, target, fetchRef, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return "", manifest, fault.NotFoundf(op, "fetch artifact %s: %v", fetchRef, ferr)
	}
	resolved := desc.Digest.String()
	if wantDigest != "" && resolved != wantDigest {
		return "", manifest, fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", resolved, wantDigest)
	}
	if isIndex(desc, data) {
		var index ocispec.Index
		if jerr := json.Unmarshal(data, &index); jerr != nil {
			return "", manifest, fault.Invalidf(op, "decode index: %v", jerr)
		}
		child, serr := selectManifest(op, resolved, index, node)
		if serr != nil {
			return "", manifest, serr
		}
		cdesc, cdata, cerr := oras.FetchBytes(ctx, target, child.Digest.String(), oras.DefaultFetchBytesOptions)
		if cerr != nil {
			return "", manifest, fault.NotFoundf(op, "fetch %s's manifest %s: %v", resolved, child.Digest, cerr)
		}
		if cdesc.Digest != child.Digest {
			return "", manifest, fault.Invalidf(op, "index %s: manifest %s resolved to %s", resolved, child.Digest, cdesc.Digest)
		}
		data = cdata
	}
	if jerr := json.Unmarshal(data, &manifest); jerr != nil {
		return "", manifest, fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	return resolved, manifest, nil
}

// selectManifest picks the index descriptor whose os and architecture equal node's (a variant is ignored;
// unknown/unknown entries never match); with an empty node, the first descriptor.
func selectManifest(op, indexDigest string, index ocispec.Index, node v1.OCIPlatform) (ocispec.Descriptor, error) {
	if len(index.Manifests) == 0 {
		return ocispec.Descriptor{}, fault.Invalidf(op, "index %s lists no manifest", indexDigest)
	}
	if node == "" {
		return index.Manifests[0], nil
	}
	for _, d := range index.Manifests {
		if d.Platform != nil && d.Platform.OS == node.OS() && d.Platform.Architecture == node.Arch() {
			return d, nil
		}
	}
	return ocispec.Descriptor{}, fault.NotFoundf(op, "artifact %s has no bundle for %s; it provides %s",
		indexDigest, node, platformList(indexPlatforms(index)))
}

func indexPlatforms(index ocispec.Index) []v1.OCIPlatform {
	var out []v1.OCIPlatform
	for _, d := range index.Manifests {
		if d.Platform == nil || d.Platform.OS == "unknown" || d.Platform.OS == "" || d.Platform.Architecture == "" {
			continue
		}
		out = append(out, v1.OCIPlatform(d.Platform.OS+"/"+d.Platform.Architecture))
	}
	return out
}

func platformList(ps []v1.OCIPlatform) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = string(p)
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// Platforms lists the platforms the artifact at digest (or at ref's tag when digest is empty) provides: an
// index's, an annotated manifest's one, or nil for an unannotated manifest, which runs anywhere.
func Platforms(ctx context.Context, ref, digest string) ([]v1.OCIPlatform, error) {
	const op = "artifact.Platforms"
	target, reference, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return nil, fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	fetchRef := digest
	if fetchRef == "" {
		fetchRef = reference
	}
	if fetchRef == "" {
		return nil, fault.Invalidf(op, "platforms need a digest or a tag (e.g. <ref>@<digest>)")
	}
	desc, data, ferr := oras.FetchBytes(ctx, target, fetchRef, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return nil, fault.NotFoundf(op, "fetch artifact %s: %v", fetchRef, ferr)
	}
	if digest != "" && desc.Digest.String() != digest {
		return nil, fault.Invalidf(op, "digest mismatch: ref resolved to %s, wanted %s", desc.Digest, digest)
	}
	if isIndex(desc, data) {
		var index ocispec.Index
		if jerr := json.Unmarshal(data, &index); jerr != nil {
			return nil, fault.Invalidf(op, "decode index: %v", jerr)
		}
		return indexPlatforms(index), nil
	}
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(data, &manifest); jerr != nil {
		return nil, fault.Invalidf(op, "decode manifest: %v", jerr)
	}
	if p := manifest.Annotations[PlatformAnnotation]; p != "" {
		return []v1.OCIPlatform{v1.OCIPlatform(p)}, nil
	}
	return nil, nil
}

// indexSource is one validated `funcdctl index` source: its manifest descriptor and the properties every source
// must share.
type indexSource struct {
	ref      string
	desc     ocispec.Descriptor
	platform v1.OCIPlatform
	contract string
	runtime  string
	bundle   bool
}

// PushIndex writes an OCI image index over sources — per-platform function manifests in the same repository or
// layout as ref — tags it from ref and returns its digest (ADR-0145 Decision 2). Every rule is checked before
// anything is written; a violation is fault.Invalid naming the source.
func PushIndex(ctx context.Context, ref string, sources []string) (digest string, err error) {
	const op = "artifact.PushIndex"
	if len(sources) == 0 {
		return "", fault.Invalidf(op, "an index needs at least one source")
	}
	target, tag, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}
	if tag == "" || strings.HasPrefix(tag, "sha256:") {
		return "", fault.Invalidf(op, "index ref %q needs a tag", ref)
	}
	home, herr := repositoryOf(ref)
	if herr != nil {
		return "", fault.Wrapf(herr, fault.KindOf(herr), op, "index ref")
	}
	var srcs []indexSource
	seen := map[v1.OCIPlatform]string{}
	for _, s := range sources {
		src, serr := readIndexSource(ctx, op, target, home, s)
		if serr != nil {
			return "", serr
		}
		if prev, dup := seen[src.platform]; dup {
			return "", fault.Invalidf(op, "source %s repeats platform %s (already from %s)", s, src.platform, prev)
		}
		seen[src.platform] = s
		if len(srcs) > 0 {
			first := srcs[0]
			switch {
			case src.contract != first.contract:
				return "", fault.Invalidf(op, "source %s has a different contract than %s", s, first.ref)
			case src.runtime != first.runtime:
				return "", fault.Invalidf(op, "source %s has runtime %q, %s has %q", s, src.runtime, first.ref, first.runtime)
			case src.bundle != first.bundle:
				return "", fault.Invalidf(op, "source %s and %s differ in kind (a bundle and a single file)", s, first.ref)
			}
		}
		srcs = append(srcs, src)
	}
	sort.SliceStable(srcs, func(i, j int) bool { return srcs[i].platform < srcs[j].platform })
	index := ocispec.Index{
		MediaType:    ocispec.MediaTypeImageIndex,
		ArtifactType: artifactType,
	}
	index.SchemaVersion = 2
	for _, src := range srcs {
		index.Manifests = append(index.Manifests, ocispec.Descriptor{
			MediaType:    src.desc.MediaType,
			ArtifactType: artifactType,
			Digest:       src.desc.Digest,
			Size:         src.desc.Size,
			Platform:     &ocispec.Platform{OS: src.platform.OS(), Architecture: src.platform.Arch()},
		})
	}
	data, merr := json.Marshal(index)
	if merr != nil {
		return "", fault.Wrapf(merr, fault.Internal, op, "encode index")
	}
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, data)
	if perr := target.Push(ctx, desc, bytes.NewReader(data)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
		return "", fault.Wrapf(perr, fault.Internal, op, "push index")
	}
	if gerr := target.Tag(ctx, desc, tag); gerr != nil {
		return "", fault.Wrapf(gerr, fault.Internal, op, "tag index")
	}
	return desc.Digest.String(), nil
}

// readIndexSource resolves one source tag in the index's repository and checks it is a platform-annotated funcd
// function manifest.
func readIndexSource(ctx context.Context, op string, target oras.ReadOnlyTarget, home, source string) (indexSource, error) {
	src := indexSource{ref: source}
	repo, rerr := repositoryOf(source)
	if rerr != nil {
		return src, fault.Wrapf(rerr, fault.KindOf(rerr), op, "source %s", source)
	}
	if repo != home {
		return src, fault.Invalidf(op, "source %s is not in the index's repository %s", source, home)
	}
	_, tag, _ := resolveTargetRef(source)
	if tag == "" {
		return src, fault.Invalidf(op, "source %s needs a tag", source)
	}
	desc, data, ferr := oras.FetchBytes(ctx, target, tag, oras.DefaultFetchBytesOptions)
	if ferr != nil {
		return src, fault.Invalidf(op, "source %s: %v", source, ferr)
	}
	if isIndex(desc, data) {
		return src, fault.Invalidf(op, "source %s is an index, not a function manifest", source)
	}
	var manifest ocispec.Manifest
	if jerr := json.Unmarshal(data, &manifest); jerr != nil {
		return src, fault.Invalidf(op, "source %s: decode manifest: %v", source, jerr)
	}
	if manifest.ArtifactType != artifactType {
		return src, fault.Invalidf(op, "source %s is not a funcd function artifact (%q)", source, manifest.ArtifactType)
	}
	src.platform = v1.OCIPlatform(manifest.Annotations[PlatformAnnotation])
	if !IsArtifactPlatform(src.platform) {
		return src, fault.Invalidf(op, "source %s carries no %s annotation (push it with --platform)", source, PlatformAnnotation)
	}
	src.desc = ocispec.Descriptor{MediaType: desc.MediaType, Digest: desc.Digest, Size: desc.Size}
	if src.desc.MediaType == "" {
		src.desc.MediaType = ocispec.MediaTypeImageManifest
	}
	src.contract = manifest.Annotations[contractAnnotation]
	src.runtime = manifest.Annotations[runtimeAnnotation]
	_, src.bundle = layerByMediaType(manifest.Layers, BundleTarMediaType)
	return src, nil
}

// repositoryOf names the repository a ref lives in: the layout directory, or registry/repository.
func repositoryOf(ref string) (string, error) {
	if dir, _, ok := parseLocalRef(ref); ok {
		return "oci-layout://" + dir, nil
	}
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return "", fault.Invalidf("artifact.repositoryOf", "parse registry ref %q: %v", ref, err)
	}
	return repo.Reference.Registry + "/" + repo.Reference.Repository, nil
}

// resolveTargetRef is the tag or digest part of a ref, without opening a target.
func resolveTargetRef(ref string) (repo, tag string, err error) {
	if dir, t, ok := parseLocalRef(ref); ok {
		return dir, t, nil
	}
	r, rerr := remote.NewRepository(ref)
	if rerr != nil {
		return "", "", rerr
	}
	return r.Reference.Registry + "/" + r.Reference.Repository, r.Reference.Reference, nil
}
