//go:build linux

package ctrmanager

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	containerd "github.com/containerd/containerd/v2/client"

	"github.com/pyvvo/funcd/api/fault"
)

// startTimeout bounds how long Ensure waits for the private containerd socket to come up
// before declaring the daemon unhealthy.
const startTimeout = 30 * time.Second

// privateManager supervises a funcd-private containerd child on a private socket + data-root
// (the k3s model). It does NOT import the curated images itself: containerd images are
// per-namespace, and the Manager does not know the function namespaces — so the containerd
// driver owns image availability, importing the embedded tar into each function's own
// namespace on demand (no registry pull — ADR-0054). Linux+root only — exercised in the
// deferred integration lane (FUNCD_IT=1, root).
type privateManager struct {
	cfg     Config
	socket  string
	cmd     *exec.Cmd
	client  *containerd.Client
	started bool
}

// newPrivate builds the private-managed Manager (Linux build).
func newPrivate(cfg Config) (Manager, error) {
	if cfg.DataRoot == "" {
		cfg.DataRoot = "/var/lib/funcd/containerd"
	}
	return &privateManager{
		cfg:    cfg,
		socket: filepath.Join(cfg.DataRoot, "containerd.sock"),
	}, nil
}

// Ensure starts + supervises the private containerd, lays down crun, and returns the private
// socket. It does NOT import the curated images — that is per-namespace and owned by the
// containerd driver (ADR-0054, no registry pull). Requires root (cgroups/netns/mounts).
// scenario: runs-out-of-the-box / embedded-image-no-registry (integration lane).
func (m *privateManager) Ensure(ctx context.Context) (string, error) {
	const op = "ctrmanager.privateManager.Ensure"
	if os.Geteuid() != 0 {
		return "", fault.Forbiddenf(op,
			"the private managed containerd needs root (cgroups, netns, mounts); run as root or pass --containerd <socket>")
	}
	if err := os.MkdirAll(m.cfg.DataRoot, 0o700); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create data-root %q", m.cfg.DataRoot)
	}
	if err := m.layDownCrun(op); err != nil {
		return "", err
	}
	if err := m.startContainerd(ctx, op); err != nil {
		return "", err
	}
	client, err := containerd.New(m.socket)
	if err != nil {
		return "", fault.Wrapf(err, fault.Unavailable, op, "connect to private containerd at %q", m.socket)
	}
	m.client = client
	return m.socket, nil
}

// resolveBin resolves a runtime binary by name: funcd's laid-down BinDir first (where `funcd
// install`/provision.LayDown writes — ADR-0056), then the system PATH. A system install
// (--containerd <socket>) is unaffected; this only governs the private-managed path.
func resolveBin(name string) (string, error) {
	if p := filepath.Join(BinDir(), name); isExecutable(p) {
		return p, nil
	}
	return exec.LookPath(name)
}

// isExecutable reports whether path exists as a regular file (the laid-down binaries are 0755).
func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// startContainerd launches the containerd child on the private socket + data-root and waits
// for the socket to appear (a healthy daemon). containerd is resolved from funcd's BinDir first
// (ADR-0056), and the child runs with BinDir prepended to its PATH so containerd finds
// containerd-shim-runc-v2 + crun there.
func (m *privateManager) startContainerd(ctx context.Context, op string) error {
	bin, err := resolveBin("containerd")
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op,
			"bundled containerd not found in funcd's bin dir or on PATH; `funcd install` lays it down, or pass --containerd <socket>")
	}
	cmd := exec.CommandContext(ctx, bin,
		"--address", m.socket,
		"--root", filepath.Join(m.cfg.DataRoot, "root"),
		"--state", filepath.Join(m.cfg.DataRoot, "state"),
	)
	// Prepend BinDir to PATH so the runc-v2 shim + crun (laid down beside containerd) are found.
	cmd.Env = append(os.Environ(), "PATH="+BinDir()+":"+os.Getenv("PATH"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "start private containerd")
	}
	m.cmd, m.started = cmd, true

	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(m.socket); statErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fault.Wrapf(ctx.Err(), fault.Unavailable, op, "waiting for private containerd socket")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fault.Unavailablef(op, "private containerd did not create %q within %s", m.socket, startTimeout)
}

// layDownCrun ensures crun (the OCI runtime the runc-v2 shim execs) is present. crun is
// shipped as a separate binary funcd execs — never linked — so its GPL does not reach
// funcd's Go code (the ADR-0011 boundary). Here we only verify it is reachable; `funcd
// install` is what places the bundled binary on disk.
func (m *privateManager) layDownCrun(op string) error {
	if _, err := resolveBin("crun"); err != nil {
		return fault.Wrapf(err, fault.Unavailable, op,
			"crun not found in funcd's bin dir or on PATH; `funcd install` lays down crun (the runc-v2 shim execs it)")
	}
	return nil
}

// Close stops the supervised containerd child and closes the client (idempotent).
func (m *privateManager) Close() error {
	var firstErr error
	if m.client != nil {
		if err := m.client.Close(); err != nil {
			firstErr = err
		}
		m.client = nil
	}
	if m.started && m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = m.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = m.cmd.Process.Kill()
		}
		m.started = false
	}
	if firstErr != nil {
		return fmt.Errorf("ctrmanager close: %w", firstErr)
	}
	return nil
}
