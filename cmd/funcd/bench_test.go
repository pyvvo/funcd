package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/pyvvo/funcd/api/fault"
)

// scenario: bench-uses-embedded-shims (ADR-0141) — with no --shim/--pool-shim, funcd bench runs the
// embedded shims from a temp dir that cleanup removes; an explicit path overrides, and a missing
// explicit pool shim skips the pooling comparison.
func TestScenarioBenchUsesEmbeddedShims(t *testing.T) {
	shim, pool, cleanup, err := resolveShimPaths(benchConfig{})
	require.NoError(t, err)
	for path, want := range map[string][]byte{shim: shimnode.Shim, pool: shimnode.Pool} {
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		require.Equal(t, want, got, "%s is the embedded shim", filepath.Base(path))
	}
	cleanup()
	_, err = os.Stat(shim)
	require.ErrorIs(t, err, fs.ErrNotExist, "cleanup removes the extracted shims")

	explicit := filepath.Join(t.TempDir(), "shim.mjs")
	require.NoError(t, os.WriteFile(explicit, shimnode.Shim, 0o600))
	shim, pool, cleanup, err = resolveShimPaths(benchConfig{shim: explicit, poolShim: filepath.Join(t.TempDir(), "absent.mjs")})
	require.NoError(t, err)
	defer cleanup()
	require.Equal(t, explicit, shim, "an explicit --shim wins")
	require.Empty(t, pool, "a missing explicit --pool-shim skips the comparison")
}

// scenario: bench-is-one-flag-driven-verb — `funcd bench --help` is a SINGLE verb whose mode is
// chosen by mutually-exclusive flags (--containerd, --doctor), not a process/containerd/doctor
// subcommand tree.
func TestBenchIsOneFlagDrivenVerb(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--help"})
	root.SetOut(out)
	root.SetErr(out)
	if err := root.Execute(); err != nil {
		t.Fatalf("bench --help: %v", err)
	}
	got := out.String()
	for _, flag := range []string{"--containerd", "--doctor"} {
		if !strings.Contains(got, flag) {
			t.Errorf("bench --help missing the %s mode flag:\n%s", flag, got)
		}
	}
	// It is one verb, not a subcommand tree: --help must not advertise nested
	// process/containerd/doctor commands under an "Available Commands:" section.
	if strings.Contains(got, "Available Commands:") {
		t.Errorf("bench should be a single verb, not a subcommand tree:\n%s", got)
	}
}

// scenario: mode-flags-mutually-exclusive — `funcd bench --containerd --doctor` (two modes at
// once) fails fast with a clean api/fault usage error.
func TestBenchModeFlagsMutuallyExclusive(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--containerd", "--doctor"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("want an error for --containerd --doctor, got nil")
	}
	if fault.KindOf(err) != fault.Invalid {
		t.Fatalf("want fault.Invalid, got %v (kind %s)", err, fault.KindOf(err))
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion: %v", err)
	}
}

// scenario: containerd-lane-reuses-embedded-runtime (clean-skip half) — `funcd bench --containerd`
// on a host without the production path (this darwin/CI box: the ADR-0054 private containerd is
// Linux+root only) brings up the runtime Manager, finds it unavailable, and SKIPS cleanly: exit 0,
// output reports the lane was skipped. The real measurement is the deferred Linux+root integration
// lane. (Default --containerd-socket "" ⇒ the privately-managed containerd path.)
func TestBenchContainerdSkipsWhenManagerUnavailable(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--containerd"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bench --containerd should exit 0 (clean skip) on a box without the runtime, got: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "skipped") {
		t.Errorf("bench --containerd should report the lane was skipped on this host:\n%s", got)
	}
}

// scenario: doctor-checks-not-installs — `funcd bench --doctor` reports whether each lane can run
// (and warns on a live daemon) but never installs anything; it exits 0 even when components are
// missing. The containerd socket is pointed at a nonexistent path for determinism.
func TestBenchDoctorReportsAndSkips(t *testing.T) {
	out := &bytes.Buffer{}
	root := newRootCmd(out)
	root.SetArgs([]string{"bench", "--doctor", "--containerd-socket", "/nonexistent/funcd-bench-test.sock"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bench --doctor should exit 0, got: %v", err)
	}
	got := out.String()
	if got == "" {
		t.Fatalf("doctor produced no output")
	}
	for _, lane := range []string{"in-process lane", "python pool", "containerd lane"} {
		if !strings.Contains(got, lane) {
			t.Errorf("doctor output missing the %q line:\n%s", lane, got)
		}
	}
	// On a box without containerd (any non-Linux CI, or Linux missing the socket), the lane is
	// reported "not available" with a non-empty reason — never a hard failure.
	if !strings.Contains(got, "not available") {
		t.Errorf("doctor should report a missing containerd lane as 'not available':\n%s", got)
	}
}
