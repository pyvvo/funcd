//go:build dev

package main

import (
	"path/filepath"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// manifestFileName is the colocated authoring manifest funcdctl dev still reads (ADR-0122); push and types read the
// contract file instead (ADR-0247), and dev follows in the same change.
const manifestFileName = "funcdctl.yaml"

// manifestEntry resolves a manifest's bundle root + entry file. With the wrangler-style `main` set (a
// handler file relative to the manifest dir), the handler's OWN dir is the bundle root — its siblings
// (SQL, vendored deps under a bundle/) resolve and FUNCD_BUNDLE_DIR points there — so it runs IN PLACE.
// Without `main`, it's the flat stem/generic convention: defaultEntry co-located with the manifest,
// isolated per defaultIsolate.
func manifestEntry(m *sdk.Manifest, dir, defaultEntry string, defaultIsolate bool) (srcDir, entry string, isolate bool) {
	if strings.TrimSpace(m.Main) != "" {
		full := filepath.Join(dir, m.Main)
		return filepath.Dir(full), filepath.Base(full), false
	}
	return dir, defaultEntry, defaultIsolate
}

// defaultEntry is the conventional handler entry file for a runtime family: handler.py for the python
// family, handler.mjs otherwise. Overridable with --entry.
func defaultEntry(rt v1.RuntimeName) string {
	if strings.HasPrefix(string(rt), "python") {
		return "handler.py"
	}
	return "handler.mjs"
}
