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
	"embed"
	"io"
	"strings"
)

// curated holds the embedded per-arch OCI image tars. The build wires the arch-matching
// tars in (a per-arch release build embeds its own matching-arch image — ADR-0054).
//
//go:embed nodejs22.tar
//go:embed python314.tar
//go:embed duckdb.tar
var curated embed.FS

// tarFile maps a runtime name to its embedded OCI tar filename.
//
//nolint:gochecknoglobals // sanctioned static lookup table (read-only), same form as api/fault/problem.go — ADR-0002 §5.
var tarFile = map[string]string{
	"nodejs22":  "nodejs22.tar",
	"python314": "python314.tar",
	"duckdb":    "duckdb.tar",
}

// Tar returns the embedded OCI image tar for a runtime ("nodejs22"/"python314") as a fresh,
// UNCOMPRESSED reader ready for containerd's client.Import (which reads a raw tar stream, not a
// gzip one). The curated export may be gzip-compressed (e.g. `docker save`-style output): if the
// embedded bytes start with the gzip magic, Tar transparently wraps them in a gzip.Reader so the
// caller always sees a plain tar. ok==false means "no embedded tar for this runtime" — the driver
// then falls through to ImageOverride/Pull; a runtime in neither embed nor override is the
// caller's NotFound (never a silent miss). The returned reader is independent per call.
func Tar(runtime string) (r io.Reader, ok bool) {
	name, known := tarFile[runtime]
	if !known {
		return nil, false
	}
	data, err := curated.ReadFile(name)
	if err != nil {
		// The file is embedded at compile time, so a read error here is impossible in a
		// built binary; treat a (theoretical) miss as "no embedded tar" rather than panic.
		return nil, false
	}
	return tarReader(data), true
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
// registry pull — ADR-0054). It parses the runtime out of the ref: the path component after
// the last "/", with a leading "runtime-" and a trailing ":<tag>" stripped — so
// "funcd/runtime-nodejs22:latest" → "nodejs22" → Tar("nodejs22"). ok==false means the ref does
// not match the curated pattern (an ImageOverride / arbitrary registry ref) — the driver then
// falls back to a registry Pull.
func TarForImageRef(ref string) (r io.Reader, ok bool) {
	name := ref
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:] // drop the registry/repo prefix, keep the last path component
	}
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[:i] // strip the :<tag>
	}
	name = strings.TrimPrefix(name, "runtime-")
	return Tar(name)
}
