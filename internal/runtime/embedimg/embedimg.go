// Package embedimg carries funcd's curated runtime images baked into the binary
// (ADR-0054). At release/integration time each per-arch curated image is exported to
// an OCI tar and embedded here via go:embed; at startup the ctrmanager imports the
// matching tar into the privately-managed containerd (client.Import) so the platform
// runs functions with NO registry pull (the k3s/dockerd self-contained model).
//
// PLACEHOLDER NOTE (impl-time, honest): the committed nodejs22.tar / python314.tar are
// tiny (<1 KB) clearly-labeled STAND-INS, not real OCI images — they exist only so the
// embed directive has a file at compile time and Tar(...) returns a non-empty reader (the
// ADR-0054 non-gated unit test). The REAL multi-MB per-arch distroless tars are produced
// by `just build-runtime-images` and overwrite the placeholders before the binary ships
// (see README.md). `go build` / `just ci` are green on the placeholder; building and
// importing the real images is the Linux+root release/integration concern.
package embedimg

import (
	"bytes"
	"compress/gzip"
	_ "embed" // the images below
	"io"
	"strings"

	"github.com/distribution/reference"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// curatedRepoPrefix is the normalized repository prefix of every embedded image (`just
// build-runtime-images` tags funcd/runtime-<rt>:latest), the name client.Import stores it under.
const curatedRepoPrefix = "docker.io/" + config.DefaultImagePrefix

// The embedded per-arch OCI image tars, each in a go:embed of its own, so that a binary embeds only the images it
// reads: the build wires the arch-matching tars in (a per-arch release build embeds its own matching-arch image —
// ADR-0054). Each worker runtime's tar has a reference file beside it, the dev reference that `just
// build-worker-images` gives its image (ADR-0359 Decision 4), empty in the committed placeholder.
var (
	//go:embed nodejs22.tar
	nodejs22Tar []byte
	//go:embed nodejs22.ref
	nodejs22Ref string
	//go:embed python314.tar
	python314Tar []byte
	//go:embed python314.ref
	python314Ref string
	//go:embed duckdb.tar
	duckdbTar []byte
	//go:embed rqlite.tar
	rqliteTar []byte
)

// Runtimes are the names of funcd's own runtimes, those whose images it embeds, sorted: no Addon may ship a runtime of
// one of these names (ADR-0344 Decision 4). It reads no image, so a binary that calls only it embeds none.
func Runtimes() []string { return []string{"duckdb", "nodejs22", "python314", "rqlite"} }

// Tar returns the embedded OCI image tar for a runtime ("nodejs22"/"python314") as a fresh,
// UNCOMPRESSED reader ready for containerd's client.Import (which reads a raw tar stream, not a
// gzip one). The curated export may be gzip-compressed (e.g. `docker save`-style output): if the
// embedded bytes start with the gzip magic, Tar transparently wraps them in a gzip.Reader so the
// caller always sees a plain tar. ok==false means "no embedded tar for this runtime" — the driver
// then pulls the ref only when the operator chose its registry (an imageOverride entry or a custom
// imagePrefix); any other ref, and a pull that finds no image, is the driver's NotFound wrapping
// runtime.ErrImageUnavailable (ADR-0149), never a silent miss. The returned reader is independent per call.
func Tar(runtime string) (r io.Reader, ok bool) {
	var data []byte
	switch runtime {
	case "nodejs22":
		data = nodejs22Tar
	case "python314":
		data = python314Tar
	case "duckdb":
		data = duckdbTar
	case "rqlite":
		data = rqliteTar
	default:
		return nil, false
	}
	return tarReader(data), true
}

// WorkerTar returns the embedded tar of a worker runtime's image, "nodejs22" or "python314", as Tar does, and the dev
// reference the image carries (ADR-0359 Decisions 3 and 4); ok is false for another runtime, or for the committed
// placeholder, whose reference is empty. It reads no other image, so funcdctl, which calls it and not Tar, embeds
// these two alone.
func WorkerTar(runtime string) (r io.Reader, ref string, ok bool) {
	var data []byte
	switch runtime {
	case "nodejs22":
		data, ref = nodejs22Tar, nodejs22Ref
	case "python314":
		data, ref = python314Tar, python314Ref
	default:
		return nil, "", false
	}
	if ref = strings.TrimSpace(ref); ref == "" {
		return nil, "", false
	}
	return tarReader(data), ref, true
}

// tarReader returns a plain-tar reader over data, transparently gunzipping it when it carries the
// gzip magic (a `docker save`-style export). containerd's client.Import reads a RAW tar stream and
// rejects a gzip frame ("archive/tar: invalid tar header"), so the caller must always see a plain
// tar regardless of how the curated image was exported.
func tarReader(data []byte) io.Reader {
	br := bytes.NewReader(data)
	if isGzip(data) {
		// gzip.NewReader only fails on a bad header; the magic check above guards that, but be
		// defensive — on an unexpected error fall back to the raw bytes rather than dropping the embed.
		if zr, zerr := gzip.NewReader(br); zerr == nil {
			return zr
		}
		_, _ = br.Seek(0, io.SeekStart)
	}
	return br
}

// isGzip reports whether b begins with the gzip magic (RFC 1952: 0x1f 0x8b).
func isGzip(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

// TarForImageRef maps a curated image reference to its embedded OCI tar, so the containerd
// driver can import the embed into the function's own containerd namespace on demand (no
// registry pull — ADR-0054). Only the image an embedded tar carries matches: the ref must
// normalize to "docker.io/funcd/runtime-<rt>:latest" (so "funcd/runtime-nodejs22" and
// "funcd/runtime-nodejs22:latest" do). Any other ref — an ImageOverride such as
// "ghcr.io/pyvvo/runtime-nodejs22:v2", another tag, registry or digest — reports ok==false and the
// driver pulls it: importing the tar would run the curated image in its place.
func TarForImageRef(ref string) (r io.Reader, ok bool) {
	named, err := reference.ParseDockerRef(ref)
	if err != nil {
		return nil, false
	}
	name, curated := strings.CutPrefix(named.String(), curatedRepoPrefix)
	if !curated {
		return nil, false
	}
	rt, latest := strings.CutSuffix(name, ":latest")
	if !latest {
		return nil, false
	}
	return Tar(rt)
}
