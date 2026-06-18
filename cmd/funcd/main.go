// Command funcd is the thin shell over the pkg/funcd platform library: it selects
// drivers and runs the platform until a signal arrives (ADR-0014). It holds no
// business logic — all of that lives in pkg/funcd and the internal packages. It wires
// function execution (ADR-0036) so the standalone binary actually runs functions:
// process mode (default; the embedded Node shim) or containerd mode (FUNCD_RUNTIME).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/runtime/containerd"
	"github.com/green-0-rabbit/funcd/internal/runtime/ctrmanager"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/internal/version"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
	shimnode "github.com/green-0-rabbit/funcd/shim/nodejs"
	shimpython "github.com/green-0-rabbit/funcd/shim/python"
)

func main() {
	if err := newRootCmd(os.Stdout).Execute(); err != nil {
		slog.Error("funcd", "error", err)
		os.Exit(1)
	}
}

// newRootCmd builds the funcd daemon command tree (ADR-0042): the root runs the platform; the
// `version` subcommand prints the stamped build identity (ADR-0026) to out (the test seam).
func newRootCmd(out io.Writer) *cobra.Command {
	var memoryOnly bool
	root := &cobra.Command{
		Use:           "funcd",
		Short:         "funcd — the single-binary serverless platform daemon",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), memoryOnly) },
	}
	root.Flags().BoolVar(&memoryOnly, "memory", false,
		"run fully in memory (ephemeral — no disk); default is file-backed/durable (ADR-0043)")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build identity and exit",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(out, version.Get().String())
			return err
		},
	})
	root.AddCommand(newBenchCmd(out))
	root.AddCommand(newInstallCmd(out))
	root.AddCommand(newUninstallCmd(out))
	return root
}

// serve assembles the platform (drivers + execution wiring) and runs it until a signal arrives.
// memoryOnly selects the substrate: file-backed/durable by default, fully in-memory when set (ADR-0043).
func serve(parent context.Context, memoryOnly bool) error {
	// The control-plane credential: a developer token from FUNCD_TOKEN, or the
	// built-in dev token with a warning (Production() ships no default token — ADR-0028).
	token := os.Getenv("FUNCD_TOKEN")
	if token == "" {
		token = funcd.DevToken
		slog.Warn("funcd: FUNCD_TOKEN unset — using the built-in dev token (not for production)")
	}

	dataDir := envOr("FUNCD_DATA_DIR", "/var/lib/funcd")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}

	// Substrate: file-backed (durable) by default, in-memory (ephemeral) with --memory (ADR-0043).
	substrateOpts, substrate, err := substrateOptions(parent, memoryOnly, dataDir)
	if err != nil {
		return err
	}

	// Production() wires the fixed production drivers + the data-plane listener (ADR-0028/0033).
	// The store stays the memory driver (slatedb is a build-tag lane, ADR-0026); blob + bus are the
	// selected substrate; the runtime + execution wiring is selected by FUNCD_RUNTIME (ADR-0036).
	opts := []funcd.Option{funcd.Production()}
	opts = append(opts, substrateOpts...)
	opts = append(opts,
		funcd.WithStore(store.New(memory.New())),
		funcd.WithDevAuth(token, "default"),
		funcd.WithArtifactStore(filepath.Join(dataDir, "artifacts")),
	)
	execOpts, closeExec, err := executionOptions(parent, dataDir)
	if err != nil {
		return fmt.Errorf("wire execution: %w", err)
	}
	defer func() {
		if cerr := closeExec(); cerr != nil {
			slog.Warn("funcd: closing execution runtime", "error", cerr)
		}
	}()
	opts = append(opts, execOpts...)

	platform, err := funcd.New(opts...)
	if err != nil {
		return fmt.Errorf("assemble platform: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.InfoContext(ctx, "funcd starting",
		"version", version.Get().Version, "commit", version.Get().Commit, "substrate", substrate)

	if err := platform.Run(ctx); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// substrateOptions builds the blob + bus drivers for the daemon (ADR-0043): in-memory (ephemeral,
// no disk) when memoryOnly, else file-backed under dataDir (durable). It returns the options plus
// the active substrate label for the startup log. The platform owns + closes the drivers.
func substrateOptions(ctx context.Context, memoryOnly bool, dataDir string) ([]funcd.Option, string, error) {
	if memoryOnly {
		bucket, err := gocloud.Open(ctx, "mem://")
		if err != nil {
			return nil, "", fmt.Errorf("open in-memory blob: %w", err)
		}
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
		if err != nil {
			return nil, "", fmt.Errorf("open in-memory bus: %w", err)
		}
		return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "memory", nil
	}
	blobDir, natsDir := filepath.Join(dataDir, "blob"), filepath.Join(dataDir, "nats")
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create blob dir %s: %w", blobDir, err)
	}
	if err := os.MkdirAll(natsDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create nats dir %s: %w", natsDir, err)
	}
	bucket, err := gocloud.Open(ctx, "file://"+blobDir)
	if err != nil {
		return nil, "", fmt.Errorf("open file blob: %w", err)
	}
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.FileStorage, StoreDir: natsDir})
	if err != nil {
		return nil, "", fmt.Errorf("open file bus: %w", err)
	}
	return []funcd.Option{funcd.WithBlob(bucket), funcd.WithBus(messaging)}, "file", nil
}

// noopClose is the execution closer for the process lane (nothing to tear down).
func noopClose() error { return nil }

// executionOptions selects the runtime driver + function-execution wiring from
// FUNCD_RUNTIME (ADR-0036): "containerd" → the containerd/crun worker running curated
// images; anything else (default) → the process driver running the embedded Node shim. It
// returns a closer the caller must defer — for containerd mode it stops the ctrmanager-
// supervised private containerd (ADR-0054); for process mode it is a no-op.
func executionOptions(ctx context.Context, dataDir string) ([]funcd.Option, func() error, error) {
	if os.Getenv("FUNCD_RUNTIME") == "containerd" {
		// ADR-0054: bring the container runtime up through the Manager. By default it starts +
		// supervises a PRIVATE containerd and imports the embedded curated images; with
		// --containerd/FUNCD_CONTAINERD_SOCKET set it returns that external socket and starts no
		// child. The driver then dials whatever socket Ensure yields.
		mgr, err := ctrmanager.New(ctrmanager.Config{
			ExternalSocket: os.Getenv("FUNCD_CONTAINERD_SOCKET"),
			DataRoot:       envOr("FUNCD_CONTAINERD_ROOT", filepath.Join(dataDir, "containerd")),
			ImageOverride:  imageOverrides(),
		})
		if err != nil {
			return nil, noopClose, fmt.Errorf("build container manager: %w", err)
		}
		socket, err := mgr.Ensure(ctx)
		if err != nil {
			_ = mgr.Close()
			return nil, noopClose, fmt.Errorf("ensure container runtime (private containerd is Linux+root; pass --containerd <socket> otherwise): %w", err)
		}
		cd, err := containerd.New(containerd.Config{
			Socket:      socket,
			Snapshotter: envOr("FUNCD_SNAPSHOTTER", "overlayfs"),
			CNIBinDir:   envOr("FUNCD_CNI_BIN_DIR", "/opt/cni/bin"),
			CNIConfDir:  envOr("FUNCD_CNI_CONF_DIR", filepath.Join(dataDir, "cni")),
			SubnetCIDR:  envOr("FUNCD_SUBNET_CIDR", "10.63.0.0/16"),
		})
		if err != nil {
			_ = mgr.Close()
			return nil, noopClose, fmt.Errorf("containerd runtime (FUNCD_RUNTIME=containerd is Linux-only): %w", err)
		}
		prefix := envOr("FUNCD_IMAGE_PREFIX", "funcd/runtime-")
		imageFor := func(rt string) string { return prefix + rt + ":latest" }
		return []funcd.Option{funcd.WithRuntime(cd), funcd.WithContainerExecution(imageFor)}, mgr.Close, nil
	}

	// process mode (default, cross-platform): run the embedded Node shim on the process driver.
	opts := []funcd.Option{funcd.WithRuntime(process.New())}
	node := envOr("FUNCD_NODE", "")
	if node == "" {
		if p, lerr := exec.LookPath("node"); lerr == nil {
			node = p
		}
	}
	if node == "" {
		slog.Warn("funcd: node not found — functions will NOT execute (control plane only); set FUNCD_NODE or FUNCD_RUNTIME=containerd")
		return opts, noopClose, nil
	}
	shimPath := filepath.Join(dataDir, "shim.mjs")
	if werr := os.WriteFile(shimPath, shimnode.Shim, 0o600); werr != nil {
		return nil, noopClose, fmt.Errorf("extract runtime shim to %s: %w", shimPath, werr)
	}
	opts = append(opts, funcd.WithRuntimeShim(node, shimPath))

	// Optional second curated language — the Python shim (ADR-0049). If a python3 is present,
	// extract the embedded (stdlib-only, no pip) shim package and register it for the `python*`
	// runtime family; node functions are unaffected when it is absent.
	python := envOr("FUNCD_PYTHON", "")
	if python == "" {
		if p, lerr := exec.LookPath("python3"); lerr == nil {
			python = p
		}
	}
	if python == "" {
		slog.Info("funcd: python3 not found — python functions will not execute in process mode (set FUNCD_PYTHON); node functions unaffected")
		return opts, noopClose, nil
	}
	shimEntry, poolEntry, perr := shimpython.Extract(filepath.Join(dataDir, "shim-python"))
	if perr != nil {
		return nil, noopClose, fmt.Errorf("extract python runtime shim: %w", perr)
	}
	opts = append(opts, funcd.WithRuntimeShimFor("python", python, shimEntry))

	// Python worker pooling (ADR-0050) needs `concurrent.interpreters` (Python ≥3.14). Register the
	// subinterpreter pool host for the python* family only when the interpreter supports it; below
	// 3.14, python functions still run solo (the runtime shim above), they just don't co-pool.
	if pythonAtLeast314(python) {
		opts = append(opts, funcd.WithPoolShimFor("python", python, poolEntry))
	} else {
		slog.Info("funcd: python < 3.14 — python worker pooling disabled (needs concurrent.interpreters); python functions run solo")
	}
	return opts, noopClose, nil
}

// imageOverrides parses FUNCD_IMAGE_OVERRIDE ("runtime=ref,runtime=ref") into the Manager's
// --image override map (ADR-0054): a listed runtime is pulled from its registry ref instead
// of imported from the embedded curated tar. Empty/malformed entries are skipped.
func imageOverrides() map[string]string {
	raw := os.Getenv("FUNCD_IMAGE_OVERRIDE")
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		if rt, ref, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && rt != "" && ref != "" {
			out[rt] = ref
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pythonAtLeast314 reports whether the interpreter at path is Python ≥3.14 (the floor for the
// subinterpreter pool host, ADR-0050). Best-effort: a non-zero exit / missing interpreter ⇒ false.
func pythonAtLeast314(python string) bool {
	cmd := exec.Command(python, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 14) else 1)")
	return cmd.Run() == nil
}

// envOr returns the env var value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
