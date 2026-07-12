//go:build dev

package embedengine

import (
	"os"
	"path/filepath"
	"testing"
)

// scenario: dev-catalog-query (unit half) — the `-tags dev` funcdctl embeds a catalog engine
// archive and can extract it to disk; the placeholder committed in git reports Bundled()==false so
// the dev driver knows to report catalog-unavailable rather than launch a non-existent binary. The
// real per-arch bundle (Bundled()==true, a runnable duckdb + extensions) is produced by
// `just build-catalog-engine` — the live Quack query is the deferred lane (ADR-0125 M2).
func TestEngineArchiveEmbeddedAndExtractable(t *testing.T) {
	if len(engineArchive) == 0 {
		t.Fatal("engine.tar.gz is not embedded (empty archive)")
	}
	dir := t.TempDir()
	paths, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, serr := os.Stat(paths.DuckDB); serr != nil {
		t.Fatalf("extracted duckdb binary missing at %s: %v", paths.DuckDB, serr)
	}
	if fi, serr := os.Stat(paths.ExtensionDir); serr != nil || !fi.IsDir() {
		t.Fatalf("extracted extension dir missing at %s: %v", paths.ExtensionDir, serr)
	}
}

// The committed engine.tar.gz is the placeholder, so Bundled() must be false — the dev catalog
// driver relies on this to distinguish "real engine built in" from "placeholder still in place".
func TestPlaceholderReportsNotBundled(t *testing.T) {
	if Bundled() {
		t.Fatalf("Bundled()==true on the committed placeholder (%d bytes); expected false — a real "+
			"engine must only be present after `just build-catalog-engine`", len(engineArchive))
	}
}

// Extract preserves the executable bit on the duckdb binary (the dev driver execs it directly).
func TestExtractPreservesExecutableBit(t *testing.T) {
	dir := t.TempDir()
	paths, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	fi, err := os.Stat(paths.DuckDB)
	if err != nil {
		t.Fatalf("stat duckdb: %v", err)
	}
	if fi.Mode()&0o100 == 0 {
		t.Errorf("duckdb at %s is not user-executable (mode %v)", filepath.Base(paths.DuckDB), fi.Mode())
	}
}
