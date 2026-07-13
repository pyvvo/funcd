package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbedIncludesEveryRuntimeModule guards against a new funcd_shim module (e.g. blob.py for context.blob)
// being added to src/funcd_shim/ but forgotten in embed.go's explicit go:embed list — which would ship a shim
// that ModuleNotFoundErrors the moment a handler touches the missing accessor (the exact ADR-0127 blob.py miss).
func TestEmbedIncludesEveryRuntimeModule(t *testing.T) {
	// notShipped: source modules intentionally NOT embedded (build-time only, never imported at runtime).
	notShipped := map[string]bool{
		"build.py": true, // the push-time AST baker (pydantic/typia)
	}
	srcDir := filepath.Join("src", "funcd_shim")
	ents, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read %s: %v", srcDir, err)
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".py") || notShipped[name] {
			continue
		}
		if _, oerr := shimFS.Open("src/funcd_shim/" + name); oerr != nil {
			t.Errorf("source module %q is not in embed.go's go:embed list — the extracted shim would be missing it "+
				"(add it, or add it to notShipped if build-only): %v", name, oerr)
		}
	}
}
