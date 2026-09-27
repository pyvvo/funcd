// Command demo-server starts an embedded funcd wired for real function execution, on
// fixed local ports, so a CLI demo (docs/demo/journey.sh) can drive `funcdctl` against
// it and invoke functions over HTTP. It is demo tooling — NOT the production daemon
// (that is cmd/funcd). The production daemon's execution wiring is a separate decision;
// this launcher uses the ADR-0014 embed path with the ADR-0030 shim + ADR-0031 oras
// materializer so the journey actually runs the handler.
//
// Run from the repo root: `go run ./docs/demo/server`. It runs the embedded Node shim (ADR-0141);
// FUNCD_SHIM overrides it with a path.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/pyvvo/funcd/pkg/funcd"
)

const (
	controlAddr = "127.0.0.1:8080"
	dataAddr    = "127.0.0.1:8081"
)

func main() {
	node, err := exec.LookPath("node")
	if err != nil {
		slog.Error("demo-server: node is required on PATH (the runtime shim runs JS)")
		os.Exit(1)
	}
	cache, err := os.MkdirTemp("", "funcd-demo-artifacts-*")
	if err != nil {
		slog.Error("demo-server: temp dir", "error", err)
		os.Exit(1)
	}
	shim := os.Getenv("FUNCD_SHIM")
	if shim == "" {
		dir, derr := os.MkdirTemp("", "funcd-demo-shim-*")
		if derr != nil {
			slog.Error("demo-server: temp dir", "error", derr)
			os.Exit(1)
		}
		shim = filepath.Join(dir, "shim.mjs")
		if werr := os.WriteFile(shim, shimnode.Shim, 0o600); werr != nil {
			slog.Error("demo-server: extract the embedded shim", "error", werr)
			os.Exit(1)
		}
	}

	p, err := funcd.New(
		funcd.InMemory(),                  // mem store/blob/bus + process runtime + dev auth
		funcd.WithListenAddr(controlAddr), // fixed control-plane port for `funcdctl --server`
		funcd.WithDataPlaneAddr(dataAddr), // fixed data-plane port for `curl`
		funcd.WithRuntimeShim(node, shim), // execute functions on the Node shim (ADR-0030)
		funcd.WithArtifactStore(cache),    // pull artifacts by digest via oras (ADR-0031)
	)
	if err != nil {
		slog.Error("demo-server: assemble platform", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	_, _ = fmt.Fprintf(os.Stdout, "funcd-demo ready — control http://%s · data http://%s · token %s\n",
		controlAddr, dataAddr, funcd.DevToken)
	if err := p.Run(ctx); err != nil {
		slog.Error("demo-server: run", "error", err)
		os.Exit(1)
	}
}
