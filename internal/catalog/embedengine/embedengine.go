//go:build dev

// Package embedengine carries the DuckDB + Quack catalog ENGINE — the standalone `duckdb` CLI
// plus the pinned `httpfs`/`ducklake`/`quack`/`sqlite` extensions — baked into the `-tags dev`
// funcdctl (ADR-0125 Decision 5, FEAT-0001/F90). `funcdctl dev` extracts it to a temp dir and
// runs `duckdb`+`quack` as a host SUBPROCESS serving the Quack endpoint: a process-mode catalog
// engine, no container and no cgo (a subprocess, not linked). It is the dev analogue of the prod
// `CatalogService`, which supervises the curated `funcd/runtime-duckdb` CONTAINER image (ADR-0086).
//
// Build-tagged `dev`: only the fat `-tags dev` funcdctl compiles this; the thin release client
// never embeds the engine.
//
// PLACEHOLDER NOTE (impl-time, honest): the committed engine.tar.gz is a tiny (<1 KiB) labeled
// STAND-IN, not a real engine — it exists only so the go:embed directive has a file at compile
// time. The REAL per-arch bundle (~130 MB: duckdb CLI + the 4 extensions, fetched from
// duckdb.org) is produced by `just build-catalog-engine <os> <arch>` and overwrites the
// placeholder (skip-worktree'd) before the dev binary ships — exactly the embedimg pattern
// (internal/runtime/embedimg). A `-tags dev` build on the placeholder COMPILES and Bundled()
// reports false; `funcdctl dev` then reports catalog-unavailable with a build-the-engine
// remediation instead of launching a non-existent binary.
package embedengine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

//go:embed engine.tar.gz
var engineArchive []byte

// placeholderMaxBytes bounds the committed placeholder. A real per-arch bundle is ~130 MB and the
// placeholder is well under 4 KiB, so this cleanly distinguishes "real engine embedded" from
// "placeholder still in place".
const placeholderMaxBytes = 4096

// Paths locates the extracted engine on disk.
type Paths struct {
	DuckDB       string // the executable duckdb CLI
	ExtensionDir string // directory holding the *.duckdb_extension files
}

// Bundled reports whether a REAL per-arch engine is embedded (vs the tiny placeholder). The dev
// catalog driver gates on it: false ⇒ report catalog-unavailable with a build-the-engine
// remediation rather than launching a non-existent binary.
func Bundled() bool { return len(engineArchive) > placeholderMaxBytes }

// Extract unpacks the embedded engine.tar.gz into dir and returns the duckdb binary + extension
// dir. Callers gate on Bundled() first; on the placeholder Extract still unpacks the marker, but
// the returned DuckDB path won't be a runnable binary.
func Extract(dir string) (Paths, error) {
	const op = "embedengine.Extract"
	gz, err := gzip.NewReader(bytes.NewReader(engineArchive))
	if err != nil {
		return Paths{}, fault.Wrapf(err, fault.Internal, op, "open engine gzip")
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return Paths{}, fault.Wrapf(terr, fault.Internal, op, "read engine tar")
		}
		// Reject path traversal even though we control the producer (defense-in-depth).
		clean := filepath.Clean("/" + hdr.Name)
		if strings.Contains(clean, "..") {
			return Paths{}, fault.Internalf(op, "engine tar entry escapes root: %q", hdr.Name)
		}
		target := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if merr := os.MkdirAll(target, 0o755); merr != nil {
				return Paths{}, fault.Wrapf(merr, fault.Internal, op, "mkdir %s", target)
			}
		case tar.TypeReg:
			if merr := os.MkdirAll(filepath.Dir(target), 0o755); merr != nil {
				return Paths{}, fault.Wrapf(merr, fault.Internal, op, "mkdir parent of %s", target)
			}
			if werr := writeFile(target, tr, os.FileMode(hdr.Mode&0o777)); werr != nil { //nolint:gosec // mode is masked to 0o777
				return Paths{}, fault.Wrapf(werr, fault.Internal, op, "write %s", target)
			}
		}
	}
	return Paths{
		DuckDB:       filepath.Join(dir, "duckdb"),
		ExtensionDir: filepath.Join(dir, "extensions"),
	}, nil
}

// writeFile streams r into path with mode, copying in bounded chunks (the archive is our own
// build product, so its total size is the ~130 MB engine, not attacker-controlled).
func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, cerr := io.Copy(f, r); cerr != nil { //nolint:gosec // size bounded by our own embedded archive
		_ = f.Close()
		return cerr
	}
	return f.Close()
}
