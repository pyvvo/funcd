// Command demo-server starts an embedded funcd wired for real function execution, on
// fixed local ports, so a CLI demo (docs/demo/journey.sh) can drive `funcdctl` against
// it and invoke functions over HTTP. It is demo tooling — NOT the production daemon
// (that is cmd/funcd). The production daemon's execution wiring is a separate decision;
// this launcher uses the ADR-0014 embed path with the ADR-0030 shim + ADR-0031 oras
// materializer so the journey actually runs the handler.
//
// Run from the repo root: `go run ./docs/demo/server` (override FUNCD_SHIM if needed).
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

	"github.com/green-0-rabbit/funcd/pkg/funcd"
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
	shim := os.Getenv("FUNCD_SHIM")
	if shim == "" {
		shim, _ = filepath.Abs(filepath.Join("shim", "nodejs", "shim.mjs"))
	}
	cache, err := os.MkdirTemp("", "funcd-demo-artifacts-*")
	if err != nil {
		slog.Error("demo-server: temp dir", "error", err)
		os.Exit(1)
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
