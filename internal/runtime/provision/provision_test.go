package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// tarGz packs name→bytes members into a gzip'd tar (matching the upstream archive shape).
func tarGz(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withAssets swaps the package assets table for a test set + restores it.
func withAssets(t *testing.T, replacement []Asset) {
	t.Helper()
	orig := assets
	assets = replacement
	t.Cleanup(func() { assets = orig })
}

func dirEmpty(t *testing.T, dir string) bool {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	return len(es) == 0
}

// scenario: install-lays-down-runtime — matching SHA-256 installs each asset shape (a lone
// binary into binDir, an untar selecting members into binDir/cniDir) to the temp dirs.
func TestLayDownInstallsVerifiedAssets(t *testing.T) {
	arch := hostArch()
	if arch == "" {
		t.Skipf("unsupported test arch %q", runtime.GOARCH)
	}
	crunBytes := []byte("fake-crun-static-binary")
	ctrTar := tarGz(t, map[string][]byte{
		"bin/containerd":              []byte("containerd-bin"),
		"bin/containerd-shim-runc-v2": []byte("shim-bin"),
		"bin/ctr":                     []byte("ctr-bin"),
		"bin/README":                  []byte("ignored"),
	})
	cniTar := tarGz(t, map[string][]byte{
		"./bridge":     []byte("bridge-bin"),
		"./firewall":   []byte("firewall-bin"),
		"./host-local": []byte("host-local-bin"),
		"./loopback":   []byte("loopback-bin"),
		"./portmap":    []byte("portmap-bin"),
		"./vlan":       []byte("ignored"),
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/crun", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(crunBytes) })
	mux.HandleFunc("/containerd.tgz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(ctrTar) })
	mux.HandleFunc("/cni.tgz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(cniTar) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	withAssets(t, []Asset{
		{
			Name: "crun", Version: "test",
			URLFor:  func(string) string { return srv.URL + "/crun" },
			SHA256:  map[string]string{arch: sum(crunBytes)},
			Install: installBinary("crun"),
		},
		{
			Name: "containerd", Version: "test",
			URLFor: func(string) string { return srv.URL + "/containerd.tgz" },
			SHA256: map[string]string{arch: sum(ctrTar)},
			Install: installTarGz(map[string]string{
				"bin/containerd":              "containerd",
				"bin/containerd-shim-runc-v2": "containerd-shim-runc-v2",
				"bin/ctr":                     "ctr",
			}, destBin),
		},
		{
			Name: "cni-plugins", Version: "test",
			URLFor: func(string) string { return srv.URL + "/cni.tgz" },
			SHA256: map[string]string{arch: sum(cniTar)},
			Install: installTarGz(map[string]string{
				"./bridge": "bridge", "./firewall": "firewall", "./host-local": "host-local",
				"./loopback": "loopback", "./portmap": "portmap",
			}, destCNI),
		},
	})

	binDir, cniDir := filepath.Join(t.TempDir(), "bin"), filepath.Join(t.TempDir(), "cni")
	if err := LayDown(context.Background(), binDir, cniDir); err != nil {
		t.Fatalf("LayDown: %v", err)
	}

	for _, name := range []string{"crun", "containerd", "containerd-shim-runc-v2", "ctr"} {
		if !isFile(filepath.Join(binDir, name)) {
			t.Errorf("expected %q in binDir", name)
		}
	}
	for _, name := range []string{"bridge", "firewall", "host-local", "loopback", "portmap"} {
		if !isFile(filepath.Join(cniDir, name)) {
			t.Errorf("expected %q in cniDir", name)
		}
	}
	// The ignored archive members must NOT be extracted.
	if isFile(filepath.Join(binDir, "README")) || isFile(filepath.Join(cniDir, "vlan")) {
		t.Error("unselected archive member was extracted")
	}
	// Mode is 0755 (verified-then-executable).
	fi, _ := os.Stat(filepath.Join(binDir, "crun"))
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("crun mode = %v, want 0755", fi.Mode().Perm())
	}
}

// scenario: checksum-mismatch-aborts — a mismatching digest returns fault.Invalid and leaves the
// dirs EMPTY (no chmod, no exec, no partial state).
func TestLayDownChecksumMismatchAbortsWritingNothing(t *testing.T) {
	arch := hostArch()
	if arch == "" {
		t.Skipf("unsupported test arch %q", runtime.GOARCH)
	}
	served := []byte("the-real-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(served)
	}))
	t.Cleanup(srv.Close)

	withAssets(t, []Asset{{
		Name: "crun", Version: "test",
		URLFor:  func(string) string { return srv.URL },
		SHA256:  map[string]string{arch: sum([]byte("DIFFERENT-pinned-bytes"))},
		Install: installBinary("crun"),
	}})

	binDir, cniDir := filepath.Join(t.TempDir(), "bin"), filepath.Join(t.TempDir(), "cni")
	err := LayDown(context.Background(), binDir, cniDir)
	if err == nil {
		t.Fatal("expected a checksum-mismatch error, got nil")
	}
	if k := fault.KindOf(err); k != fault.Invalid {
		t.Errorf("kind = %v, want %v", k, fault.Invalid)
	}
	if !dirEmpty(t, binDir) || !dirEmpty(t, cniDir) {
		t.Error("dirs must be empty after a checksum mismatch — no unverified bytes written")
	}
}

// scenario: idempotent-skip — an asset already present at its pinned version is skipped (no
// download attempt; a server that would fail proves it was not hit).
func TestLayDownSkipsPresentAsset(t *testing.T) {
	arch := hostArch()
	if arch == "" {
		t.Skipf("unsupported test arch %q", runtime.GOARCH)
	}
	binDir, cniDir := filepath.Join(t.TempDir(), "bin"), filepath.Join(t.TempDir(), "cni")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "crun"), []byte("already-here"), 0o755); err != nil {
		t.Fatal(err)
	}

	withAssets(t, []Asset{{
		Name: "crun", Version: "test",
		URLFor:  func(string) string { return "http://127.0.0.1:0/should-not-be-hit" },
		SHA256:  map[string]string{arch: "deadbeef"},
		Install: installBinary("crun"),
		present: func(b, _ string) bool { return isFile(filepath.Join(b, "crun")) },
	}})

	if err := LayDown(context.Background(), binDir, cniDir); err != nil {
		t.Fatalf("LayDown should skip a present asset, got: %v", err)
	}
}

// scenario: url-construction — each pinned asset builds the correct per-arch upstream URL.
func TestURLConstructionPerArch(t *testing.T) {
	byName := map[string]Asset{}
	for _, a := range assets {
		byName[a.Name] = a
	}
	cases := []struct {
		name, arch, want string
	}{
		{"crun", "amd64", "https://github.com/containers/crun/releases/download/1.28/crun-1.28-linux-amd64"},
		{"crun", "arm64", "https://github.com/containers/crun/releases/download/1.28/crun-1.28-linux-arm64"},
		{"containerd", "amd64", "https://github.com/containerd/containerd/releases/download/v2.2.4/containerd-2.2.4-linux-amd64.tar.gz"},
		{"containerd", "arm64", "https://github.com/containerd/containerd/releases/download/v2.2.4/containerd-2.2.4-linux-arm64.tar.gz"},
		{"cni-plugins", "amd64", "https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-amd64-v1.9.1.tgz"},
		{"cni-plugins", "arm64", "https://github.com/containernetworking/plugins/releases/download/v1.9.1/cni-plugins-linux-arm64-v1.9.1.tgz"},
	}
	for _, c := range cases {
		a, ok := byName[c.name]
		if !ok {
			t.Fatalf("asset %q not in table", c.name)
		}
		if got := a.URLFor(c.arch); got != c.want {
			t.Errorf("%s/%s URL = %q, want %q", c.name, c.arch, got, c.want)
		}
		if _, ok := a.SHA256[c.arch]; !ok {
			t.Errorf("%s has no pinned digest for %q", c.name, c.arch)
		}
	}
}

// scenario: install-print-no-touch (Plan facet) — Plan lists every asset for the host arch and
// touches nothing on disk.
func TestPlanListsEveryAssetAndTouchesNothing(t *testing.T) {
	binDir, cniDir := filepath.Join(t.TempDir(), "bin"), filepath.Join(t.TempDir(), "cni")
	lines := Plan(binDir, cniDir)
	if len(lines) != len(assets) {
		t.Fatalf("Plan returned %d lines, want %d (one per asset)", len(lines), len(assets))
	}
	joined := strings.Join(lines, "\n")
	for _, name := range []string{"crun", "containerd", "cni-plugins"} {
		if !strings.Contains(joined, name) {
			t.Errorf("plan missing asset %q:\n%s", name, joined)
		}
	}
	if hostArch() != "" {
		if !strings.Contains(joined, binDir) || !strings.Contains(joined, cniDir) {
			t.Errorf("plan must name both dest dirs:\n%s", joined)
		}
	}
	// Pure: no dir was created.
	if _, err := os.Stat(binDir); !os.IsNotExist(err) {
		t.Error("Plan created binDir — it must touch nothing")
	}
	if _, err := os.Stat(cniDir); !os.IsNotExist(err) {
		t.Error("Plan created cniDir — it must touch nothing")
	}
}
