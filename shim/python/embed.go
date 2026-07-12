// Package python embeds the funcd Python runtime shim (ADR-0049) into the binary, so the single
// self-contained `funcd` daemon ships it with no sidecar (ADR-0036, mirroring shim/nodejs). The
// daemon extracts the package tree to its data dir on boot and points WithRuntimeShimFor("python", …)
// at the extracted entry script. The shim calls the artifact's precompiled fastjsonschema validator
// (ADR-0058 — pure-Python, subinterpreter-safe); pydantic + fastjsonschema run only at build time.
package python

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Explicit file list (not a directory glob): includes the underscore-prefixed __init__.py /
// __main__.py that an `all:` glob would need but that would also drag in __pycache__/*.pyc. Listing
// the sources by name embeds exactly the shim, nothing machine-generated. Every module the extracted
// shim imports at runtime is listed — solo (shim.py) and pool (pool.py/_poolworker.py) both import
// funclog + tracespan (+ tracespan→invcontext) at load, and contract.py (ADR-0123) is compiled at
// worker init; kv.py backs context.kv. build.py is the push-time AST baker (build-only) — not shipped.
//
//go:embed src/funcd_shim/__init__.py src/funcd_shim/__main__.py src/funcd_shim/shim.py src/funcd_shim/invoke.py src/funcd_shim/pool.py src/funcd_shim/_poolworker.py src/funcd_shim/runtime.py src/funcd_shim/types.py src/funcd_shim/contract.py src/funcd_shim/funclog.py src/funcd_shim/tracespan.py src/funcd_shim/invcontext.py src/funcd_shim/kv.py src/funcd_shim/py.typed
var shimFS embed.FS

// The entry scripts launch the package: Python prepends a script's own directory to sys.path, so
// placing them beside the extracted `funcd_shim/` package makes `from funcd_shim...` resolve with
// no PYTHONPATH. Each runs a main() that reads its config (FUNCD_ARTIFACT / FUNCD_POOL_MANIFEST /
// FUNCD_PORT…) from the env.
const (
	shimEntryScript = "from funcd_shim.shim import main\nraise SystemExit(main())\n"
	poolEntryScript = "from funcd_shim.pool import main\nraise SystemExit(main())\n" // ADR-0050, needs Python ≥3.14
)

// Extract writes the embedded Python shim package under dir and returns the entry script paths to
// launch as `python3 <entry>`: shimEntry is the solo shim (ADR-0049), poolEntry the subinterpreter
// pool host (ADR-0050, requires Python ≥3.14). Idempotent: overwrites whatever is there.
func Extract(dir string) (shimEntry, poolEntry string, err error) {
	walkErr := fs.WalkDir(shimFS, "src/funcd_shim", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, rerr := filepath.Rel("src", path) // funcd_shim/...
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, readErr := shimFS.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(dst, data, 0o600)
	})
	if walkErr != nil {
		return "", "", walkErr
	}
	shimEntry = filepath.Join(dir, "funcd_shim_entry.py")
	if werr := os.WriteFile(shimEntry, []byte(shimEntryScript), 0o600); werr != nil {
		return "", "", werr
	}
	poolEntry = filepath.Join(dir, "funcd_pool_entry.py")
	if werr := os.WriteFile(poolEntry, []byte(poolEntryScript), 0o600); werr != nil {
		return "", "", werr
	}
	return shimEntry, poolEntry, nil
}
