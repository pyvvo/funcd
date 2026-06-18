# ADR-0043: Single-binary substrate selection — file by default, `--memory` for ephemeral (refines ADR-0028)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16** — judge: right decision, leak argument + metastore-honesty verified, advance as-is. **Implemented 2026-06-16** — review: pass, all 3 scenarios tested, leak-free, full suite green.)
- **Deciders**: green-0-rabbit
- **Tags**: daemon, single-binary, substrate, durability, cli
- **Realizes**: [FEAT-0000/F19](../feat/0000-feat-v1.md) (single-binary daemon) + [F21](../feat/0000-feat-v1.md) (substrate layers)
- **Relates to / refines**: [ADR-0028](0028-platform-control-plane-wiring.md) (`Production()` — its blob/bus join
  store/runtime as **deployment-injected**), [ADR-0042](0042-cobra-cli-framework.md) (the daemon's `--memory` flag),
  [ADR-0007](0007-blob-storage-layer-port.md)/[ADR-0008](0008-bus-messaging-port.md) (the file↔memory blob + bus drivers).

## Context & Need

The single `funcd` binary should choose its **durability posture**: persist to disk by default, or run **fully in
memory** for ephemeral/dev/CI/container runs. Today `Production()` hard-wires the **file** blob + **file** JetStream bus,
and the daemon has no switch. ADR-0028 already established the pattern: the *deployment* injects the variable drivers
(store, runtime) via `With…`; `Production()` only wires the fixed ones. The blob + bus are pure-Go in **both** file and
memory forms, so they belong in that same deployment-injected set — letting the daemon pick. The user's requirement:
**file-based is the default**; a `--memory` flag switches to memory-only.

**Honesty about scope:** "file" persists the **blob service data + JetStream event streams** (and artifacts, already a
dir). It does **not** yet persist the **metastore** (the `store` stays the memory driver in both modes — durable metastore
is the slatedb/cgo lane, ADR-0006/0026). So `funcd` (file) survives a restart for blob + events but not yet for resource
definitions; `funcd --memory` writes **nothing** to disk. The default is still file — the right production posture, and
the binary is already file-shaped for when the durable metastore lands.

## Scenarios

- **scenario: daemon-file-default** — *when* `funcd` runs with no flag, *then* it wires the **file** blob (`<dataDir>/blob`)
  + **file** JetStream bus (`<dataDir>/nats`) — durable across restarts (modulo the memory metastore).
- **scenario: daemon-memory-flag** — *when* `funcd --memory` runs, *then* it wires the **memory** blob (`mem://`) + memory
  JetStream — nothing is written under `<dataDir>` for the substrate; the daemon is fully ephemeral.
- **scenario: production-injects-substrate** — *when* `funcd.New(Production(), …)` is assembled **without** `WithBlob`/
  `WithBus`, *then* `New` fails (a missing-port error) — `Production()` no longer wires blob/bus; the deployment must, like
  store/runtime.

## Scope

**In:** move blob + bus out of `Production()` into deployment injection (`WithBlob`/`WithBus`); a `--memory` (bool, default
false) flag on the `funcd` cobra root; the daemon builds the file (default) or memory substrate from it; honest docs that
file persists blob+bus (not yet the metastore).

**Out:** persisting the **metastore** (slatedb/cgo store — the durable-control-plane follow-up); S3/remote blob; per-service
substrate choice (one substrate for the whole daemon); `funcd-bench`/tests (they wire `InMemory()`/explicit drivers, unchanged).

## Constraints & Decision drivers

- **File is the default** (the ask) — the production posture is durable; ephemeral is opt-in.
- **No leaked driver** — `--memory` must **not** construct a file NATS server (an embedded server with a goroutine + file
  handles) and then discard it. So the substrate is chosen *before* construction, not overridden after — i.e. `Production()`
  must stop hard-wiring it.
- **ADR-0028 pattern** — variable drivers are deployment-injected; blob/bus (now selectable) join store/runtime there.
- **No new dependency** — stdlib + the existing `internal/blob/gocloud` + `internal/bus/nats` drivers.

## Alternatives considered

- **Keep `Production()` wiring file blob/bus; `--memory` overrides via `WithBlob`/`WithBus`** — rejected: `Production()`
  would still **construct** the file NATS server, which the override then orphans (a leaked embedded server). Choosing the
  substrate before construction is the only leak-free option.
- **A whole `InMemory()` for `--memory`** — rejected: `InMemory()` also sets ephemeral `127.0.0.1:0` binds + a default dev
  token + a text logger; the daemon wants the production bind (`:8080`), env token, and JSON logger. Mixing means many overrides.
- **An env var (`FUNCD_SUBSTRATE=memory`) instead of a flag** — rejected: the user asked for a flag; a cobra `--memory`
  flag is discoverable (`--help`) and the cobra-native choice (ADR-0042). (An env fallback can be added later if wanted.)

## Decision

1. **`Production()` stops wiring blob + bus.** They join `store`/`runtime` as deployment-injected (ADR-0028). `Production()`
   keeps the fixed production drivers: embedded gateway, JSON logger, no-op telemetry, `0.0.0.0:8080` bind, RBAC, no default
   token. `funcd.New(Production(), …)` now requires `WithBlob` + `WithBus` (else a missing-port error — the existing validation).
2. **The `funcd` daemon picks the substrate** from a cobra `--memory` bool flag (default false → file):
   - **file (default):** `WithBlob(gocloud.Open(ctx, "file://"+dataDir+"/blob"))` + `WithBus(nats.Open(ctx, {FileStorage,
     StoreDir: dataDir+"/nats"}))`.
   - **`--memory`:** `WithBlob(gocloud.Open(ctx, "mem://"))` + `WithBus(nats.Open(ctx, {MemoryStorage}))`.
   The store stays the memory driver in both (slatedb/cgo is the durable-metastore lane); runtime/execution selection is
   unchanged (ADR-0036).
3. The platform owns the injected blob/bus lifecycle (closed on `Shutdown`), so there is no daemon-side leak.

## Temporary workarounds

- **The metastore is memory in both modes** until the slatedb store lands — so even file mode doesn't survive a restart for
  resource definitions. **Exit criterion:** the slatedb/cgo store lane (ADR-0006/0026) makes the metastore durable; the
  daemon then wires it under the file default (a `WithStore(slatedb…)` behind `-tags slatedb`).

## Contracts

```go
// pkg/funcd: Production() no longer sets c.blob / c.bus (deployment-injected, like c.store / c.runtime).
//   funcd.New(Production(), WithBlob(...), WithBus(...), WithStore(...), WithRuntime(...)) — the full daemon assembly.
// cmd/funcd: newRootCmd gains a persistent --memory bool; serve(ctx, memoryOnly) builds the substrate:
//   memoryOnly ? mem:// blob + NATS MemoryStorage : file://<dataDir>/blob + NATS FileStorage(<dataDir>/nats).
```

## Implementation plan

1. **`pkg/funcd/presets.go`** — remove the blob + bus construction from `Production()` (and its `gocloud`/`nats` imports
   if now unused there); update the doc comment to list blob/bus among the deployment-injected drivers.
2. **`cmd/funcd/main.go`** — add a `--memory` persistent flag to the cobra root; thread it into `serve(ctx, memoryOnly)`;
   build the file or memory `WithBlob`/`WithBus` (import `internal/blob/gocloud` + `internal/bus/nats`) from it; log which
   substrate is active.
3. **Tests** — `pkg/funcd`: `Production()` alone now fails `New` without blob/bus (`production-injects-substrate`);
   `cmd/funcd`: a unit asserting `serve`'s option set wires file vs memory blob/bus per `--memory` (or, lighter, that the
   flag is wired + the two substrate builders return the expected driver) — both `daemon-file-default` + `daemon-memory-flag`.
   Update the `tests/e2e/linux_integration_test.go` doc-comment that shows the `Production()` assembly.
4. **Verify**: `go build ./...`, `go vet`, `golangci-lint`, `go test ./pkg/funcd/ ./cmd/funcd/ -count=1` green; `funcd --help`
   shows `--memory`; `funcd --memory version` + a boot smoke; no identity/path leak.
5. **Definition of done**: file is the default substrate; `--memory` is fully ephemeral (no disk writes for blob/bus); no
   leaked NATS server; `Production()` requires injected blob/bus; honest docs on the memory-metastore caveat.

## Review checklist

- [ ] `Production()` no longer wires blob/bus; `funcd.New(Production())` without `WithBlob`/`WithBus` fails (`production-injects-substrate`).
- [ ] `funcd` defaults to **file** blob + bus (`daemon-file-default`); `funcd --memory` wires **memory** blob + bus, no disk (`daemon-memory-flag`).
- [ ] No file NATS server is constructed in `--memory` mode (chosen before construction, not overridden) — no leak.
- [ ] `--memory` appears in `funcd --help`; the store stays memory in both (caveat documented); no new dependency; no identity/path leak.

## Consequences

- (+) The single binary picks its durability posture: durable by default, fully ephemeral with `--memory` (clean for
  CI/containers/dev — zero disk).
- (+) Blob/bus join the ADR-0028 deployment-injected set — consistent, and leak-free substrate selection.
- (−) `Production()` is now even more a *base* (needs blob/bus + store + runtime injected) — but it's daemon-only and the
  daemon always injects them.
- (−) "File" persists blob + event streams + artifacts, **not** the metastore (memory store) — an honest, documented gap
  closed by the slatedb lane; until then a restart still loses resource definitions in both modes.

## Open questions

- **Durable metastore** (slatedb/cgo behind the file default) — the follow-up that makes file mode survive a restart for resources.
- **`FUNCD_SUBSTRATE` env** mirroring `--memory` (for systemd `Environment=`) — add if operators want env-only config.

## References

- [ADR-0028](0028-platform-control-plane-wiring.md) (Production preset) · [ADR-0042](0042-cobra-cli-framework.md) (cobra) · [ADR-0007](0007-blob-storage-layer-port.md)/[ADR-0008](0008-bus-messaging-port.md) (blob/bus drivers).
