//go:build dev

// Package embedengine carries rqlited, the SQL engine `funcdctl dev` runs as a host subprocess (ADR-0237 Decision 3),
// built with cgo from the rqlite source that images/runtime/rqlite/rqlite.pin pins. Only the `-tags dev` funcdctl
// compiles it. The committed rqlited.gz is a placeholder under 4 KiB, so `go build` and `just ci` stay green and
// Bundled reports false; `just build-sql-engine <os> <arch>` overwrites it with the real engine.
package embedengine

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"os"
	"path/filepath"

	"github.com/pyvvo/funcd/api/fault"
)

//go:embed rqlited.gz
var engine []byte

// placeholderMaxBytes tells the placeholder from a real engine, which is about 12 MB compressed.
const placeholderMaxBytes = 4096

// Bundled reports whether a real engine is embedded rather than the placeholder.
func Bundled() bool { return len(engine) > placeholderMaxBytes }

// Extract writes the engine to dir/rqlited, executable, and returns its path.
func Extract(dir string) (string, error) {
	const op = "embedengine.Extract"
	gz, err := gzip.NewReader(bytes.NewReader(engine))
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "open the engine archive")
	}
	defer func() { _ = gz.Close() }()
	path := filepath.Join(dir, "rqlited")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) //nolint:gosec // the engine must be executable
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create %s", path)
	}
	if _, err := io.Copy(f, gz); err != nil { //nolint:gosec // our own embedded build product
		_ = f.Close()
		return "", fault.Wrapf(err, fault.Internal, op, "write %s", path)
	}
	if err := f.Close(); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "write %s", path)
	}
	return path, nil
}
