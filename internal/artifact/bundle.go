// bundle.go — the multi-file function bundle (ADR-0089): a deployment-package artifact.
//
// A bundle is a directory (handler + vendored native deps + the ADR-0090 I/O contract) pushed
// as ONE deterministic tar+gzip OCI layer alongside the ADR-0031 single-blob layer (additive —
// a lone .py/.mjs push is unchanged). The transport machinery here is language-agnostic; only
// the PYTHONPATH worker env (internal/function) is python-family-gated. The tar is deterministic
// (entries sorted; mtime/uid/gid/uname/gname zeroed) so identical trees yield an identical
// digest (ADR-0035 pinning holds), and untar is traversal-safe (a "../"/absolute/symlink-escape
// path is refused — a security gate).
package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/contract"
)

const (
	// BundleTarMediaType is the multi-file function bundle layer (ADR-0089): a deterministic
	// tar+gzip deployment package (handler + vendored deps). It sits ALONGSIDE the single-file
	// bundleMediaType layer (ADR-0031) — additive, never replacing it.
	BundleTarMediaType = "application/vnd.funcd.bundle.tar+gzip"
	// BundleEntryAnnotation records the handler entry file, relative to the bundle root, on the
	// bundle layer + manifest so Pull/Materialize resolve FUNCD_ARTIFACT to <root>/<entry>.
	BundleEntryAnnotation = "dev.funcd.bundle.entry"
	// bundleContractFile is the ADR-0090 I/O contract the bundle embeds ({input, output}); push
	// gates it (VerifyBundleContract) and promotes it to the ADR-0059 contract layer. On
	// materialization (ADR-0123) it is (re)written in the bundle dir with the exact digest-pinned
	// ADR-0059 blob so the shim compiles the same bytes `funcdctl inspect` advertises.
	bundleContractFile = "__funcd_contract.json"
	// singleFileContractSidecar is the ADR-0123 dotfile sidecar carrying the delivered contract blob
	// next to a single-file handler in the materializer cache. It is a DOTFILE (mirroring
	// entrySidecar) so the single-file FUNCD_ARTIFACT resolver — which selects the lone non-dotfile
	// entry — never mistakes it for the handler.
	singleFileContractSidecar = ".funcd-contract.json"
	// entrySidecar records the bundle entry in the materializer's per-digest cache dir so the
	// cache-hit fast-path returns <cacheDir>/<entry> (not a non-deterministic entries[0]).
	entrySidecar = ".funcd-entry"
)

// zeroTime is the canonical mtime/atime/ctime baked into every tar header so the packed bytes are
// reproducible (a real mtime would make identical trees produce different digests).
//
//nolint:gochecknoglobals // an immutable determinism constant (time.Time can't be a const)
var zeroTime = time.Unix(0, 0).UTC()

// PackBundle writes dir as a DETERMINISTIC tar+gzip stream: entries are sorted by path and
// mtime/uid/gid/uname/gname are zeroed, so two identical trees produce byte-identical output
// (and thus an identical digest — ADR-0035). Only regular files and directories are packed;
// entry must name an existing regular file relative to dir (the handler) — else fault.Invalid.
func PackBundle(dir, entry string) (data []byte, err error) {
	const op = "artifact.PackBundle"
	info, serr := os.Stat(dir)
	if serr != nil || !info.IsDir() {
		return nil, fault.Invalidf(op, "bundle path %q is not a directory", dir)
	}
	entryInfo, eerr := os.Stat(filepath.Join(dir, entry))
	if eerr != nil || !entryInfo.Mode().IsRegular() {
		return nil, fault.Invalidf(op, "entry %q is not a regular file in bundle %q", entry, dir)
	}
	return packDir(op, dir)
}

// packDir is the entry-less deterministic packer behind PackBundle: the same sorted, zeroed
// tar+gzip stream, with no handler-entry gate — a site bundle (ADR-0139) has no entry file at push
// time (its index is a reconcile-time spec.index). dir must already be a directory.
func packDir(op, dir string) (data []byte, err error) {
	// Collect every regular file / directory, keyed by its slash-separated relative path, so the
	// walk order (OS-dependent) never leaks into the bytes — we sort the paths ourselves.
	type node struct {
		rel  string
		info fs.FileInfo
	}
	var nodes []node
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil // the bundle root itself is implicit
		}
		fi, ferr := d.Info()
		if ferr != nil {
			return ferr
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return nil // skip symlinks/devices/etc. — a bundle is plain code + data
		}
		nodes = append(nodes, node{rel: filepath.ToSlash(rel), info: fi})
		return nil
	})
	if walkErr != nil {
		return nil, fault.Wrapf(walkErr, fault.Internal, op, "walk bundle %q", dir)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].rel < nodes[j].rel })

	var buf bytes.Buffer
	gz, gerr := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if gerr != nil {
		return nil, fault.Internalf(op, "gzip writer: %v", gerr)
	}
	tw := tar.NewWriter(gz)
	for _, n := range nodes {
		hdr := &tar.Header{
			Name:    n.rel,
			Mode:    0o644,
			ModTime: zeroTime, AccessTime: zeroTime, ChangeTime: zeroTime, // zeroed for determinism
			// Uid/Gid/Uname/Gname left zero/empty (the header zero value) — no host identity leaks.
		}
		if n.info.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Name += "/"
			hdr.Mode = 0o755
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = n.info.Size()
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			return nil, fault.Wrapf(werr, fault.Internal, op, "write tar header %q", n.rel)
		}
		if hdr.Typeflag == tar.TypeReg {
			body, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(n.rel))) //nolint:gosec // path is under the user-supplied bundle dir
			if rerr != nil {
				return nil, fault.Wrapf(rerr, fault.Internal, op, "read %q", n.rel)
			}
			if _, werr := tw.Write(body); werr != nil {
				return nil, fault.Wrapf(werr, fault.Internal, op, "write %q", n.rel)
			}
		}
	}
	if cerr := tw.Close(); cerr != nil {
		return nil, fault.Wrapf(cerr, fault.Internal, op, "close tar")
	}
	if cerr := gz.Close(); cerr != nil {
		return nil, fault.Wrapf(cerr, fault.Internal, op, "close gzip")
	}
	return buf.Bytes(), nil
}

// VerifyBundleContract enforces the push-time bundle-contract gate (ADR-0089 §3 / ADR-0090 /
// ADR-0123): dir/__funcd_contract.json MUST exist and be a valid {input, output} document with
// BOTH keys present (a void side is {"type":"null"}, never absent) AND each side must lie within
// the funcd type profile (contract.Check — the same gate the single-file --schema path applies).
// It no longer requires a baked validator symbol in the entry file: the artifact is schema-only
// and the runtime shim compiles the validator from the delivered schema at worker warm-up
// (ADR-0123, supersedes ADR-0060's build-time bake). The `entry` argument is retained for the
// signature but is no longer read. It returns the {dialect, input, output} blob to promote through
// the EXISTING ContractBlob path. fault.Invalid on any gap — an out-of-profile schema that slipped
// through here would otherwise only fail at worker compile-time (cold-start fail-closed).
func VerifyBundleContract(dir, _ string) (contractBlob []byte, err error) {
	const op = "artifact.VerifyBundleContract"
	raw, rerr := os.ReadFile(filepath.Join(dir, bundleContractFile)) //nolint:gosec // path is under the user-supplied bundle dir
	if rerr != nil {
		return nil, fault.Invalidf(op, "bundle is missing %s (every function must declare an I/O contract, ADR-0090)", bundleContractFile)
	}
	var doc struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return nil, fault.Invalidf(op, "%s is not a valid {input, output} document: %v", bundleContractFile, jerr)
	}
	if len(doc.Input) == 0 {
		return nil, fault.Invalidf(op, "%s is missing the \"input\" key (a void side is %s)", bundleContractFile, VoidSchema)
	}
	if len(doc.Output) == 0 {
		return nil, fault.Invalidf(op, "%s is missing the \"output\" key (a void side is %s)", bundleContractFile, VoidSchema)
	}
	// Gate each side against the funcd profile (ADR-0058/0123). Mirrors gateSchema's single-file
	// path so a bundle can never ship an out-of-profile schema the worker then fails to compile.
	if cerr := contract.Check(doc.Input); cerr != nil {
		return nil, fault.Wrapf(cerr, fault.Invalid, op, "%s input side is outside the funcd profile", bundleContractFile)
	}
	if cerr := contract.Check(doc.Output); cerr != nil {
		return nil, fault.Wrapf(cerr, fault.Invalid, op, "%s output side is outside the funcd profile", bundleContractFile)
	}
	// Assemble the mandatory {dialect, input, output} blob via the EXISTING single-file path so a
	// bundle reaches the same contract layer as a single-file function — only the source differs.
	return ContractBlob(doc.Input, doc.Output)
}

// PushBundle packs dir (PackBundle) as a BundleTarMediaType layer with the entry annotation,
// adds the VerifyBundleContract result as the ADR-0059 contract layer, and pushes it — mirroring
// Push's oras flow. Returns the manifest descriptor digest.
func PushBundle(ctx context.Context, ref, dir, entry, runtime string, platform v1.OCIPlatform) (digest string, err error) {
	const op = "artifact.PushBundle"
	contract, verr := VerifyBundleContract(dir, entry)
	if verr != nil {
		return "", fault.Wrapf(verr, fault.KindOf(verr), op, "verify bundle contract")
	}
	data, perr := PackBundle(dir, entry)
	if perr != nil {
		return "", fault.Wrapf(perr, fault.KindOf(perr), op, "pack bundle")
	}
	target, reference, terr := resolveTarget(ctx, ref)
	if terr != nil {
		return "", fault.Wrapf(terr, fault.KindOf(terr), op, "resolve target")
	}

	layer := content.NewDescriptorFromBytes(BundleTarMediaType, data)
	layer.Annotations = map[string]string{BundleEntryAnnotation: entry}
	if perr := target.Push(ctx, layer, bytes.NewReader(data)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
		return "", fault.Wrapf(perr, fault.Internal, op, "push bundle layer")
	}
	opts := oras.PackManifestOptions{
		Layers:              []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{BundleEntryAnnotation: entry},
	}
	if len(contract) > 0 {
		contractLayer := content.NewDescriptorFromBytes(contractMediaType, contract)
		if perr := target.Push(ctx, contractLayer, bytes.NewReader(contract)); perr != nil && !errors.Is(perr, errdef.ErrAlreadyExists) {
			return "", fault.Wrapf(perr, fault.Internal, op, "push contract blob")
		}
		opts.Layers = append(opts.Layers, contractLayer)
		opts.ManifestAnnotations[contractAnnotation] = contractLayer.Digest.String()
	}
	if runtime != "" {
		opts.ManifestAnnotations[runtimeAnnotation] = runtime
	}
	if platform != "" {
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

// pullBundle materializes a BundleTarMediaType layer into dir: it fetches (digest-verifies) the
// layer bytes, untars them traversal-safely, records the entry in a sidecar (so the materializer
// cache-HIT path can resolve <cacheDir>/<entry> without re-reading the manifest), and returns
// dir/<entry>. The entry comes from the layer's BundleEntryAnnotation (default handler.py).
func pullBundle(ctx context.Context, op string, target oras.ReadOnlyTarget, layer ocispec.Descriptor, dir string) (string, error) {
	entry := layer.Annotations[BundleEntryAnnotation]
	if entry == "" {
		entry = "handler.py"
	}
	// Guard the entry itself against traversal before we ever join it — a malicious annotation is
	// as dangerous as a malicious tar path.
	root, aerr := filepath.Abs(dir)
	if aerr != nil {
		return "", fault.Wrapf(aerr, fault.Internal, op, "resolve bundle root")
	}
	entryPath, jerr := safeJoin(op, root, entry)
	if jerr != nil {
		return "", fault.Wrapf(jerr, fault.KindOf(jerr), op, "bundle entry annotation")
	}
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		return "", fault.Wrapf(merr, fault.Internal, op, "create bundle dir")
	}
	// STREAM the layer (a bundle is large — vendored native deps run to ~100MB): content.FetchAll
	// buffers the whole blob in memory and caps at oras's 32 MiB maxDescriptorSize (that cap is for
	// small manifest/config blobs, not layers). target.Fetch + a VerifyReader streams it through
	// gzip→tar and still digest-verifies the bytes against the descriptor.
	rc, ferr := target.Fetch(ctx, layer)
	if ferr != nil {
		return "", fault.Wrapf(ferr, fault.Internal, op, "fetch bundle layer")
	}
	defer func() { _ = rc.Close() }()
	vr := content.NewVerifyReader(rc, layer)
	if uerr := untarBundle(op, vr, dir); uerr != nil {
		return "", uerr
	}
	// Drain any bytes gzip/tar didn't consume so the whole blob is read, then verify the digest.
	if _, derr := io.Copy(io.Discard, vr); derr != nil {
		return "", fault.Wrapf(derr, fault.Internal, op, "drain bundle layer")
	}
	if verr := vr.Verify(); verr != nil {
		return "", fault.Wrapf(verr, fault.Internal, op, "bundle layer digest mismatch")
	}
	if werr := os.WriteFile(filepath.Join(dir, entrySidecar), []byte(entry), 0o644); werr != nil { //nolint:gosec // non-secret cache metadata
		return "", fault.Wrapf(werr, fault.Internal, op, "write entry sidecar")
	}
	return entryPath, nil
}

// bundleEntryFromCache reads the entry sidecar pullBundle wrote into a per-digest cache dir,
// returning "" if the dir holds no bundle (a single-file cache has no sidecar).
func bundleEntryFromCache(cacheDir string) string {
	data, err := os.ReadFile(filepath.Join(cacheDir, entrySidecar)) //nolint:gosec // cacheDir is the daemon-owned artifact cache
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// untarBundle extracts a gzipped tar (a BundleTarMediaType layer's bytes) into dir, refusing any
// entry whose path would escape dir (absolute, "..", or a symlink) — the traversal-safety gate.
// Only regular files and directories are written; nothing else can appear in a bundle we packed.
func untarBundle(op string, r io.Reader, dir string) error {
	gz, gerr := gzip.NewReader(r)
	if gerr != nil {
		return fault.Invalidf(op, "bundle layer is not valid gzip: %v", gerr)
	}
	tr := tar.NewReader(gz)
	root, aerr := filepath.Abs(dir)
	if aerr != nil {
		return fault.Wrapf(aerr, fault.Internal, op, "resolve bundle root")
	}
	for {
		hdr, herr := tr.Next()
		if errors.Is(herr, io.EOF) {
			break
		}
		if herr != nil {
			return fault.Invalidf(op, "read bundle tar: %v", herr)
		}
		dest, serr := safeJoin(op, root, hdr.Name)
		if serr != nil {
			return serr
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if merr := os.MkdirAll(dest, 0o755); merr != nil {
				return fault.Wrapf(merr, fault.Internal, op, "create dir %q", hdr.Name)
			}
		case tar.TypeReg:
			if merr := os.MkdirAll(filepath.Dir(dest), 0o755); merr != nil {
				return fault.Wrapf(merr, fault.Internal, op, "create parent of %q", hdr.Name)
			}
			f, ferr := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // non-secret RO code; the container user must read it
			if ferr != nil {
				return fault.Wrapf(ferr, fault.Internal, op, "create %q", hdr.Name)
			}
			if _, cerr := io.Copy(f, tr); cerr != nil { //nolint:gosec // bundle is digest-verified before untar
				_ = f.Close()
				return fault.Wrapf(cerr, fault.Internal, op, "write %q", hdr.Name)
			}
			if cerr := f.Close(); cerr != nil {
				return fault.Wrapf(cerr, fault.Internal, op, "close %q", hdr.Name)
			}
		default:
			// Symlinks/hardlinks/devices are refused outright — a bundle is plain code + data, and
			// a symlink is the classic traversal-escape vector.
			return fault.Invalidf(op, "bundle entry %q has an unsupported type (only files and dirs allowed)", hdr.Name)
		}
	}
	return nil
}

// safeJoin joins a tar entry name onto root, REFUSING (never silently clamping) any path that is
// absolute or contains a ".." component — the path-traversal guard. Rejecting outright is stricter
// than sanitizing: a bundle we packed is always clean relative paths, so any escape attempt is a
// tampered layer and must fail closed, not be quietly rewritten.
func safeJoin(op, root, name string) (string, error) {
	slash := filepath.ToSlash(name)
	if slash == "" || slash == "/" {
		return "", fault.Invalidf(op, "bundle entry has an empty path")
	}
	if path.IsAbs(slash) {
		return "", fault.Invalidf(op, "bundle entry %q is an absolute path (path traversal refused)", name)
	}
	if slices.Contains(strings.Split(slash, "/"), "..") {
		return "", fault.Invalidf(op, "bundle entry %q contains a %q segment (path traversal refused)", name, "..")
	}
	dest := filepath.Join(root, filepath.FromSlash(path.Clean(slash)))
	// Defense in depth: after the join the destination must still be inside root.
	if dest != root && !strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return "", fault.Invalidf(op, "bundle entry %q escapes the bundle root (path traversal refused)", name)
	}
	return dest, nil
}
