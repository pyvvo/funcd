// Package provision is funcd's TEMPORARY container-runtime self-provisioning (ADR-0056):
// `funcd install` downloads the pinned, SHA-256-verified static binaries the managed-containerd
// path needs — crun (the OCI runtime the runc-v2 shim execs), containerd (+ its runc-v2 shim +
// ctr), and the CNI plugins — laying crun/containerd into funcd's bin dir and the CNI plugins
// into /opt/cni/bin. It is the interim stand-in for ADR-0054's bundled-release embed; its exit
// criterion is shipping those bytes funcd built.
//
// Security model (the load-bearing property): every asset is read FULLY into memory, its
// SHA-256 is compared to the pinned digest, and ONLY on a match is anything written/chmod'd to
// disk. A mismatch returns a fault.Invalid error having written nothing — no unverified bytes
// ever land on disk or run as root.
//
// Stdlib only (no new Go module): net/http + crypto/sha256 + archive/tar + compress/gzip.
package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/httpx"
)

// Pinned runtime versions (ADR-0056). These match the validated Lima/homebox set
// (scripts/lima/funcd.yaml): crun 1.28, the CNI plugins v1.9.1, and a current containerd 2.2.x.
const (
	crunVersion       = "1.28"
	containerdVersion = "2.2.4"
	cniVersion        = "v1.9.1"
)

// maxAssetBytes caps an in-memory asset read so a hostile/oversized response can't exhaust RAM
// before the digest check. The largest pinned asset (the containerd tarball) is well under this.
const maxAssetBytes = 256 << 20 // 256 MiB

// Pinned SHA-256 digests, arch → hex. Obtained from the upstream release checksum files (crun:
// the per-asset binaries; containerd: containerd-<ver>-linux-<arch>.tar.gz.sha256sum; cni:
// cni-plugins-linux-<arch>-<ver>.tgz.sha256). Verified BEFORE any chmod/exec.
const (
	crunSHA256Amd64 = "2aa6b7024a9c9f153895c0d11ae233d3758f54844011c3a039e3e89048d01d42"
	crunSHA256Arm64 = "cc1e8ec89aef1422e0741be196f9ed099e2e09d2f48f30f27cd44a22ef1f0342"

	containerdSHA256Amd64 = "62e77f6294e432dca5b56ad7e7d6085b5bbb526ebba0a51d832714f9b04cfdfd"
	containerdSHA256Arm64 = "d0897c8e27c96a7b7d4c73e9b278ea9f559dda619d497d88b323a528bd1412c3"

	cniSHA256Amd64 = "b98f74a0f8522f0a83867178729c1aa70f2158f90c45a2ca8fa791db1c76b303"
	cniSHA256Arm64 = "56171987d3947707c3563db2f4001bccaf50fd63468611b9f3cbecb1375ee7ec"
)

// Asset is a pinned, checksum-verified downloadable runtime binary/tarball. Install is a
// PER-ASSET closure encoding the asset's shape — a lone binary (chmod 0755 into binDir), or an
// untar selecting named members to their dests. It runs ONLY after the SHA-256 of data matched
// the pin.
type Asset struct {
	Name    string                   // "crun" | "containerd" | "cni-plugins"
	Version string                   // pinned, e.g. "1.28"
	URLFor  func(arch string) string // arch ∈ {"amd64","arm64"}
	SHA256  map[string]string        // arch → hex digest (verified before Install)
	Install func(data []byte, binDir, cniDir string) error

	// present reports whether this asset is already laid down at its pinned version (idempotency).
	present func(binDir, cniDir string) bool
}

// assets is the single pinned table both LayDown and Plan iterate, so `--print` can never drift
// from the real download set.
//
//nolint:gochecknoglobals // sanctioned static, read-only lookup table (the pinned asset set), same form as embedimg.tarFile — ADR-0002 §5.
var assets = []Asset{
	{
		Name:    "crun",
		Version: crunVersion,
		URLFor: func(arch string) string {
			return fmt.Sprintf("https://github.com/containers/crun/releases/download/%s/crun-%s-linux-%s",
				crunVersion, crunVersion, arch)
		},
		SHA256:  map[string]string{"amd64": crunSHA256Amd64, "arm64": crunSHA256Arm64},
		Install: installBinary("crun"),
		present: func(binDir, _ string) bool { return isFile(filepath.Join(binDir, "crun")) },
	},
	{
		Name:    "containerd",
		Version: containerdVersion,
		URLFor: func(arch string) string {
			return fmt.Sprintf("https://github.com/containerd/containerd/releases/download/v%s/containerd-%s-linux-%s.tar.gz",
				containerdVersion, containerdVersion, arch)
		},
		SHA256: map[string]string{"amd64": containerdSHA256Amd64, "arm64": containerdSHA256Arm64},
		Install: installTarGz(map[string]string{
			"bin/containerd":              "containerd",
			"bin/containerd-shim-runc-v2": "containerd-shim-runc-v2",
			"bin/ctr":                     "ctr",
		}, destBin),
		present: func(binDir, _ string) bool {
			return isFile(filepath.Join(binDir, "containerd")) &&
				isFile(filepath.Join(binDir, "containerd-shim-runc-v2")) &&
				isFile(filepath.Join(binDir, "ctr"))
		},
	},
	{
		Name:    "cni-plugins",
		Version: cniVersion,
		URLFor: func(arch string) string {
			return fmt.Sprintf("https://github.com/containernetworking/plugins/releases/download/%s/cni-plugins-linux-%s-%s.tgz",
				cniVersion, arch, cniVersion)
		},
		SHA256: map[string]string{"amd64": cniSHA256Amd64, "arm64": cniSHA256Arm64},
		Install: installTarGz(map[string]string{
			"./bridge":     "bridge",
			"./firewall":   "firewall",
			"./host-local": "host-local",
			"./loopback":   "loopback",
			"./portmap":    "portmap",
		}, destCNI),
		present: func(_, cniDir string) bool {
			for _, p := range []string{"bridge", "firewall", "host-local", "loopback", "portmap"} {
				if !isFile(filepath.Join(cniDir, p)) {
					return false
				}
			}
			return true
		},
	},
}

// destKind selects which destination directory an untar member lands in.
type destKind int

const (
	destBin destKind = iota // binDir
	destCNI                 // cniDir
)

// LayDown downloads each pinned asset for the host arch, verifies its SHA-256 BEFORE any
// chmod/exec, then installs it. Idempotent: an asset already present at its pinned version is
// skipped. A checksum mismatch returns fault.Invalid and installs NOTHING (no partial state, no
// chmod, no exec). binDir/cniDir are the destinations.
func LayDown(ctx context.Context, binDir, cniDir string) error {
	const op = "provision.LayDown"
	arch := hostArch()
	if arch == "" {
		return fault.Invalidf(op, "unsupported architecture %q (want amd64 or arm64)", runtime.GOARCH)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil { //nolint:gosec // bin dir holds world-execed runtime binaries
		return fault.Wrapf(err, fault.Internal, op, "create bin dir %q", binDir)
	}
	if err := os.MkdirAll(cniDir, 0o755); err != nil { //nolint:gosec // CNI bin dir is the OS-standard /opt/cni/bin
		return fault.Wrapf(err, fault.Internal, op, "create cni dir %q", cniDir)
	}

	// One client per LayDown, not per asset: the downloads share the release hosts' keep-alive connections.
	client := httpx.Client(0)
	for _, a := range assets {
		if a.present != nil && a.present(binDir, cniDir) {
			continue // idempotent: already laid down at the pinned version
		}
		want, ok := a.SHA256[arch]
		if !ok {
			return fault.Invalidf(op, "%s: no pinned digest for arch %q", a.Name, arch)
		}
		data, err := download(ctx, client, a.URLFor(arch))
		if err != nil {
			return fault.Wrapf(err, fault.Unavailable, op, "download %s %s", a.Name, a.Version)
		}
		// VERIFY BEFORE WRITE — nothing has touched disk yet.
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			return fault.Invalidf(op,
				"%s %s checksum mismatch: got %s, want %s — refusing to install unverified bytes",
				a.Name, a.Version, got, want)
		}
		if err := a.Install(data, binDir, cniDir); err != nil {
			return fault.Wrapf(err, fault.Internal, op, "install %s %s", a.Name, a.Version)
		}
	}
	return nil
}

// Plan returns the human-readable lay-down plan (asset → version, source URL, dest) for `funcd
// install --print`, for the HOST arch, by iterating the same assets table. Pure — no network, no
// disk.
func Plan(binDir, cniDir string) []string {
	arch := hostArch()
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		dest := binDir
		if a.Name == "cni-plugins" {
			dest = cniDir
		}
		if arch == "" {
			out = append(out, fmt.Sprintf("%s %s  (no asset for arch %q)", a.Name, a.Version, runtime.GOARCH))
			continue
		}
		out = append(out, fmt.Sprintf("%s %s  %s  -> %s", a.Name, a.Version, a.URLFor(arch), dest))
	}
	return out
}

// download fetches url fully into memory (bounded by maxAssetBytes). The bytes are NOT written to
// disk — the caller verifies the SHA-256 first.
func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("GET %s: asset exceeds %d bytes", url, maxAssetBytes)
	}
	return data, nil
}

// installBinary writes a lone static binary (the verified data) to binDir/name with mode 0755.
func installBinary(name string) func(data []byte, binDir, cniDir string) error {
	return func(data []byte, binDir, _ string) error {
		return os.WriteFile(filepath.Join(binDir, name), data, 0o755) //nolint:gosec // a runtime binary must be world-executable
	}
}

// installTarGz gunzips+untars the verified data and extracts the named members (tar path →
// dest filename) to either binDir or cniDir (per dest), each with mode 0755.
func installTarGz(members map[string]string, dest destKind) func(data []byte, binDir, cniDir string) error {
	return func(data []byte, binDir, cniDir string) error {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("gunzip: %w", err)
		}
		defer func() { _ = gz.Close() }()
		tr := tar.NewReader(gz)
		dir := binDir
		if dest == destCNI {
			dir = cniDir
		}
		found := map[string]bool{}
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("untar: %w", err)
			}
			out, ok := members[hdr.Name]
			if !ok {
				continue
			}
			buf, err := io.ReadAll(io.LimitReader(tr, maxAssetBytes+1)) //nolint:gosec // bounded read of an already-checksum-verified archive
			if err != nil {
				return fmt.Errorf("read %s: %w", hdr.Name, err)
			}
			if err := os.WriteFile(filepath.Join(dir, out), buf, 0o755); err != nil { //nolint:gosec // runtime binaries must be world-executable
				return fmt.Errorf("write %s: %w", out, err)
			}
			found[hdr.Name] = true
		}
		for member := range members {
			if !found[member] {
				return fmt.Errorf("archive missing expected member %q", member)
			}
		}
		return nil
	}
}

// hostArch maps runtime.GOARCH to the release asset arch token ("amd64"|"arm64"), or "" if
// unsupported.
func hostArch() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH
	default:
		return ""
	}
}

// isFile reports whether path exists as a regular (non-dir) file.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
