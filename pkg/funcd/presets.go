package funcd

import (
	"context"
	"os"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/observability"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
)

// InMemory returns an Option that wires the real in-memory drivers for every port
// — memory store, gocloud mem bucket, embedded memory-storage NATS, process
// runtime, embedded reverse-proxy gateway, a stdout text logger, and the no-op
// telemetry pipeline. No root, no containerd, no network: the e2e-harness preset
// every later feature embeds. Drivers that need a context at construction use
// context.Background() (the facade's New is ctx-free, ADR-0002/ADR-0014); they are
// released by Shutdown.
func InMemory() Option {
	return func(c *config) error {
		const op = "funcd.InMemory"
		ctx := context.Background()

		bucket, err := gocloud.Open(ctx, "mem://")
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "open in-memory blob bucket")
		}
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "open in-memory bus")
		}
		logger, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, os.Stdout)
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build logger")
		}
		telemetry, err := observability.NewTelemetry(ctx, observability.TelemetryConfig{})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build telemetry")
		}

		c.store = store.New(memory.New())
		c.blob = bucket
		c.bus = messaging
		c.runtime = process.New()
		c.gateway = embedded.New()
		c.logger = logger.Root()
		c.telemetry = telemetry

		// Control plane (ADR-0028): an ephemeral local listener + a default dev
		// credential so an embedded platform is drivable via the SDK with no config.
		c.listenAddr = "127.0.0.1:0"
		c.dataPlaneAddr = "127.0.0.1:0" // ephemeral data plane (ADR-0033)
		c.authorizer = rbac.New()
		c.localNode = "local"
		return WithDevAuth(DevToken, "default")(c)
	}
}

// Production returns an Option that wires the pure-Go production drivers that do NOT vary by
// deployment: the embedded reverse-proxy gateway, a JSON logger, and the no-op telemetry
// pipeline (an OTLP endpoint is supplied via WithTelemetry when configured), plus the public
// control-plane bind, RBAC, and deliberately NO default credential. The **store** (slatedb,
// cgo/`-tags slatedb`), **runtime** (containerd, Linux), and **blob + bus** (file vs memory —
// the daemon's `--memory` substrate choice, ADR-0043) are deployment-injected via WithStore /
// WithRuntime / WithBlob / WithBus — so Production() is composed as
// funcd.New(funcd.Production(), WithBlob(...), WithBus(...), WithStore(...), WithRuntime(...)).
func Production() Option {
	return func(c *config) error {
		const op = "funcd.Production"
		ctx := context.Background()

		logger, err := observability.NewLogger(observability.Config{Format: observability.FormatJSON}, os.Stdout)
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build logger")
		}
		telemetry, err := observability.NewTelemetry(ctx, observability.TelemetryConfig{})
		if err != nil {
			return fault.Wrapf(err, fault.Internal, op, "build telemetry")
		}

		c.gateway = embedded.New()
		c.logger = logger.Root()
		c.telemetry = telemetry

		// Control plane (ADR-0028): a public bind + RBAC, but deliberately NO default
		// credential — the operator supplies one via WithDevAuth (no default prod token).
		c.listenAddr = "0.0.0.0:8080"
		c.authorizer = rbac.New()
		c.localNode = "local"
		// c.store, c.runtime, c.blob, c.bus are deployment-injected (build-gated or
		// substrate-selectable) via WithStore / WithRuntime / WithBlob / WithBus (ADR-0028/0043).
		return nil
	}
}
