# ADR-0285: AI gateways in funcdctl dev, against the real engine

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged once)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, dev
- **Realizes**: [FEAT-0015/F198](../feat/0015-feat-ai-gateway.md) (AI gateways in funcdctl dev)
- **Relates to**: [ADR-0281](0281-ai-gateways.md) (`hostengine`) · [ADR-0280](0280-the-model-engine-on-bifrosts-go-library.md)
  (the engine) · [ADR-0125](0125-funcdctl-dev-local-run.md) (funcdctl dev) · [ADR-0237](0237-sql-in-funcdctl-dev.md)
  (the pattern: a dev engine embedded and run as a host process) · [ADR-0254](0254-funcdctl-dev-config-file.md)
  (declared Secrets' values) · [ADR-0260](0260-a-loopback-listener-owns-its-port.md) (dev engines prove their port) ·
  source: [design report](../reports/ai-gateway-design.md) · issue #1272

## Context & Need

The dev loop must match production, and the decider chose that no gateway is mocked (FEAT-0015 F198). `funcdctl dev`
already runs the real engines of SQL and the lake as host processes. This ADR runs the real AI engine the same way,
with the keys from the developer's environment, so a function that binds a model runs in dev as it does on funcd.

## Scenarios

- `scenario: dev-runs-gateway-engine` — Given an App whose gateway uses declared key `OPENROUTER_API_KEY`, set in the
  shell, When `funcdctl dev` starts, Then the engine runs as a host process on loopback, the App's function chats
  through it, and the call's usage row lands in the dev SQL database.
- `scenario: dev-key-from-env-file` — Given the key is only in `envFile`, Then dev uses it; with it only in `env`,
  dev uses that.
- `scenario: dev-missing-key-stops` — Given the key is set nowhere, Then dev stops before the platform starts, naming
  the key and the three places it reads.
- `scenario: dev-folder-gateway` — Given a folder with an `AIGateway` document that declares Secret `vendor-keys`, a
  function that binds the gateway, and no document that defines the Secret, Then dev reads its keys from the
  environment and runs it.
- `scenario: dev-key-change-reloads` — When the key changes in `envFile`, Then dev reloads and a new engine runs with
  the new key.
- `scenario: dev-placeholder-build-stops` — Given a build without the embedded engine, When a document declares a
  gateway, Then dev stops at start naming `just build-ai-engine`.
- `scenario: dev-engine-reaped` — Given dev crashed with an engine running, When dev starts again, Then the leftover
  engine process is stopped.

## Scope

**In**: the dev driver's use, the embedded binary and its build, the placeholder check.

**Out**: the gateway, its reconciler and `hostengine` itself ([ADR-0281](0281-ai-gateways.md)); egress, which dev does
not run; the usage database, which runs on dev's SQL engine ([ADR-0237](0237-sql-in-funcdctl-dev.md)).

## Constraints & Decision drivers

- The real engine, never a mock; keys from the shell, `envFile` or `env` (decided, ADR-0254).
- A dev engine proves its port before it counts as started (ADR-0260).
- `just ci` stays green with a placeholder in place of the embedded binary (ADR-0237).

## Alternatives considered

1. **A mock gateway in dev**: no vendor needed, but the dev loop would diverge from production. Rejected by the decider.
2. **The engine image under a local container runtime**: the same bytes as production, but dev runs no containers and
   needs none for SQL or the lake. Rejected for a host process, as ADR-0237 runs rqlited.

## Decision

1. **The driver.** `funcdctl dev` injects ADR-0281's `internal/ai/hostengine` with `funcd.WithAIProviderRuntime`
   whenever a document declares an AIGateway, as `devSQLOptions` injects the SQL driver (`cmd/funcdctl/dev.go`). Its
   processes register with `procreg` in the state directory for ADR-0167's reap, as the SQL and lake drivers do. Each
   engine binds `127.0.0.1:0` and is counted as started once its first output line names its address and `/readyz`
   answers 200 (ADR-0280 Decision 8), so it holds no free port and needs no held listener (ADR-0260).
2. **The binary.** `internal/ai/embedengine` (`-tags dev`) embeds a gzipped `funcd-ai` for the build's OS and
   architecture. `scripts/build-ai-engine.sh <os> <arch>` (`just build-ai-engine`) builds it with `CGO_ENABLED=0`,
   `GOTOOLCHAIN=auto`, `GOOS` and `GOARCH`; the engine is pure Go, so one host builds every target and the release
   workflow needs no macOS job for it. The committed file is a placeholder. `embedengine.Extract` writes the binary to
   the state directory once per start.
3. **A placeholder build stops at start.** Before the platform starts, dev refuses when a document declares an
   AIGateway and the embedded `funcd-ai`, or the embedded `rqlited` its usage database needs (ADR-0237 Decision 3), is
   a placeholder, naming the recipe that builds it.
4. **Keys.** A gateway's keys are declared Secrets: its own in folder mode (ADR-0281 Decision 2), the App's in App
   mode (ADR-0284). ADR-0254 Decision 5 reads a declared Secret that no document defines from the shell, `envFile` or
   `env`. A key changed in `envFile` or `env` reloads (ADR-0254 Decision 6), and the new value replaces the engine
   (ADR-0281 Decision 5).

## Design model

None. Dev reuses the add-on runtime's `Converge` through `hostengine`, `procreg`'s reap and ADR-0254's reload, and
collects a gateway's declared Secrets as it does a Function's; the engine's own address line removes the port race
ADR-0260 guards against.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: AIGateway
metadata:
  name: assistant
spec:
  secrets:
    - name: vendor-keys
      keys:
        - OPENROUTER_API_KEY
  bindings:
    secret:
      - vendor-keys
  sql:
    - name: ai-usage
  models:
    - name: fast
      type: chat
      vendor: openrouter
      id: example/chat-model
      secret:
        name: vendor-keys
        key: OPENROUTER_API_KEY
```

With this document in a dev folder and no `vendor-keys` document, dev reads `OPENROUTER_API_KEY` from the shell, else
`envFile`, else `env` in `funcdctl.config.yaml` (ADR-0254 Decision 5).

```go
// internal/ai/embedengine (build tag dev)
func Bundled() bool                              // false for the committed placeholder
func Extract(dir string) (path string, err error) // writes the funcd-ai binary under dir, executable
```

| Consumes | Exposes |
|---|---|
| ADR-0281's `hostengine`, ADR-0237's SQL driver, ADR-0254's declared-Secret values | `just build-ai-engine <os> <arch>` |
| `engines/ai` (ADR-0280) | the dev engine |

## Implementation plan

1. `internal/ai/embedengine` (placeholder and `-tags dev` loader); `scripts/build-ai-engine.sh`; `justfile`:
   `build-ai-engine`; the release workflow's build of each target.
2. `cmd/funcdctl/dev.go`: the driver option, the placeholder check beside the SQL one, and the AIGateway kind in
   dev's declared-Secret collection, which reads only Functions' declarations today.
3. Tests: the keys in folder and App mode (shell, `envFile`, `env`, none); the placeholder check; one
   `TestScenario<Name>` per scenario with `-tags dev`, on the real engine and `funcd-ai-vendortest` as the vendor;
   vet, test and lint with `-tags dev`.

**Definition of done**: the tests pass with and without `-tags dev`; `just build-ai-engine` builds every target;
`just ci` passes; no username or absolute path in a changed file.

## Review checklist

- [ ] Dev runs the embedded `funcd-ai` as a host process, registered with `procreg`; no mock exists.
- [ ] An engine counts as started only after its address line and a 200 from `/readyz`.
- [ ] A placeholder build with a declared gateway stops dev before the platform starts.
- [ ] Key values never reach a log line or dev's printed environment.
- [ ] One test per scenario, named for it.

## Consequences

- A developer needs a vendor key, or a self-hosted server such as Ollama, to run a function that binds a model.
- `funcdctl` grows by the engine's compressed size per platform.

## Open questions

None.

## References

- [ADR-0237](0237-sql-in-funcdctl-dev.md), [ADR-0254](0254-funcdctl-dev-config-file.md),
  [ADR-0260](0260-a-loopback-listener-owns-its-port.md); `cmd/funcdctl/dev.go` (`devSQLOptions`); issue #1272.
