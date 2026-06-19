package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/internal/version"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
	shimnode "github.com/green-0-rabbit/funcd/shim/nodejs"
)

// scenario: production-injects-substrate (ADR-0043) — Production() no longer wires blob/bus, so
// New requires them injected (else a missing-port error), like store/runtime.
func TestProductionRequiresSubstrate(t *testing.T) {
	t.Parallel()
	_, err := funcd.New(
		funcd.Production(),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(process.New()),
		funcd.WithDevAuth("t", "default"),
	)
	require.Error(t, err, "Production() requires WithBlob + WithBus injected (ADR-0043)")
}

// scenario: daemon-file-default + daemon-memory-flag (ADR-0043) — the daemon's substrate is
// file-backed (durable) by default and in-memory (no disk) with --memory.
func TestDaemonSubstrate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		memoryOnly bool
		label      string
	}{
		{"file-default", false, "file"},
		{"memory-flag", true, "memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts, label, err := substrateOptions(context.Background(), tc.memoryOnly, dir)
			require.NoError(t, err)
			require.Equal(t, tc.label, label)

			// Assemble + shut down so the (file) NATS server is closed, not leaked.
			all := append([]funcd.Option{
				funcd.Production(),
				funcd.WithStore(store.New(memory.New())),
				funcd.WithRuntime(process.New()),
				funcd.WithDevAuth("t", "default"),
			}, opts...)
			p, err := funcd.New(all...)
			require.NoError(t, err)
			require.NoError(t, p.Shutdown(context.Background()))

			entries, _ := os.ReadDir(dir)
			if tc.memoryOnly {
				require.Empty(t, entries, "--memory writes nothing to disk")
			} else {
				require.NotEmpty(t, entries, "file substrate writes blob + nats under the data dir")
			}
		})
	}
}

// scenario: daemon-version-and-serve (ADR-0042) — `funcd version` prints the stamped build
// identity via the cobra root (the root's RunE serves; the version subcommand prints to out).
func TestDaemonVersion(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	root := newRootCmd(&out)
	root.SetArgs([]string{"version"})
	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), version.Get().Version)
}

// scenario: shim-is-self-contained — the Node shim is embedded in the binary (ADR-0036),
// so it ships with no sidecar file.
func TestShimEmbedded(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, shimnode.Shim, "the runtime shim is embedded")
	require.Contains(t, string(shimnode.Shim), "FUNCD_PORT", "it is the ADR-0030/0032 shim contract")
}

// scenario: process-mode extracts the shim — executionOptions (default mode) extracts the
// embedded shim to the data dir and wires the process driver + shim.
func TestExecutionOptionsProcessExtractsShim(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", node)
	dir := t.TempDir()

	opts, closeExec, err := executionOptions(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	require.NotEmpty(t, opts, "process mode wires the runtime + shim")

	got, err := os.ReadFile(filepath.Join(dir, "shim.mjs"))
	require.NoError(t, err)
	require.Equal(t, shimnode.Shim, got, "the embedded shim was extracted to the data dir")
}

// scenario: node-absent-degrades — with no node the daemon wires no shim (control plane
// only), returning a runtime-only option set without error (no crash).
func TestExecutionOptionsNodeAbsentDegrades(t *testing.T) {
	t.Setenv("FUNCD_RUNTIME", "")
	t.Setenv("FUNCD_NODE", "") // no explicit node
	t.Setenv("PATH", "")       // and none on PATH

	opts, closeExec, err := executionOptions(context.Background(), t.TempDir())
	require.NoError(t, err, "missing node degrades, never errors")
	t.Cleanup(func() { _ = closeExec() })
	require.Len(t, opts, 1, "only the runtime driver is wired (no shim)")
}

// scenario: container-mode-selected — FUNCD_RUNTIME=containerd routes through the
// ctrmanager (ADR-0054): with no --containerd it takes the private-managed path, which off
// Linux reports "Linux-only" and on Linux-non-root reports "needs root" → executionOptions
// fails fast either way (no silent no-op).
func TestExecutionOptionsContainerdMode(t *testing.T) {
	t.Setenv("FUNCD_RUNTIME", "containerd")
	t.Setenv("FUNCD_CONTAINERD_SOCKET", "") // force the private-managed path
	_, closeExec, err := executionOptions(context.Background(), t.TempDir())
	if closeExec != nil {
		t.Cleanup(func() { _ = closeExec() })
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		require.Error(t, err, "private containerd fails fast off Linux / without root")
		return
	}
	_ = err // Linux+root: depends on a bundled containerd being present; not asserted in unit
}

// scenario: daemon-executes-process (node-gated) — a platform assembled the way the daemon
// assembles it (the EMBEDDED shim via executionOptions + the artifact store) deploys an OCI
// function with no digest and serves it over HTTP.
func TestDaemonExecutesFunction(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	// Build the platform like cmd/funcd does, but InMemory for ephemeral ports.
	execOpts, closeExec, err := executionOptions(context.Background(), t.TempDir()) // extracts the embedded shim + WithRuntimeShim
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	opts := append([]funcd.Option{funcd.InMemory(), funcd.WithArtifactStore(t.TempDir())}, execOpts...)
	p, err := funcd.New(opts...)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})

	// push an OCI artifact + apply a Function with NO digest (the platform pins it, ADR-0035).
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle(_, e) { return { echoed: e }; }\n"), 0o600))
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	_, err = artifact.Push(ctx, ref, bundle, nil)
	require.NoError(t, err)

	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "echo", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Artifact = v1.ArtifactRef{URI: ref} // no digest
	fn.Spec.Replicas, fn.Spec.Scaling = 1, v1.Scaling{MinReplicas: 1}
	_, err = c.Apply(ctx, fn)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		got, gerr := c.Get(ctx, v1.KindFunction, "default", "echo")
		return gerr == nil && got.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 50*time.Millisecond, "the daemon-wired platform runs the embedded shim to Ready")

	resp, err := http.Post("http://"+p.DataPlaneAddr()+"/function/echo", "application/json", strings.NewReader(`{"hi":1}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "invoked over HTTP: %s", body)
	require.Contains(t, string(body), "echoed")
}
