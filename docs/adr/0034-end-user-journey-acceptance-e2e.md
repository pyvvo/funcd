# ADR-0034: End-user journey acceptance e2e — drive the real CLI + HTTP

- **Status**: Implemented
- **Date**: 2026-06-15 (**Accepted + Implemented 2026-06-15** — `tests/e2e/journey_test.go` realizes all four
  scenarios; `TestE2EUserJourney` passes node-gated (drives the real `funcdcli` push/apply/get + data-plane
  HTTP), `e2e-boundary` lint clean, no new dependency. The daemon-execution gap is recorded as the open
  question for a follow-up ADR.)
- **Deciders**: green-0-rabbit
- **Tags**: testing, e2e, acceptance, user-journey, funcdcli, F20
- **Realizes**: [FEAT-0000/F20](../feat/0000-feat-v1.md) (the acceptance lane proving the V1 exit criterion
  through the public CLI/API, co-realized with ADR-0025)
- **Relates to**: [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (extends its taxonomy with one lane) ·
  [ADR-0024](0024-funcdcli-and-sdk.md) · [ADR-0030](0030-function-execution-runtime-shim-node.md) ·
  [ADR-0031](0031-oci-artifact-distribution-oras.md) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) ·
  [ADR-0014](0014-platform-facade-lifecycle-harness.md)

## Context & Need

The V1 exit criterion is phrased entirely from the user's side: *"a user can `funcdcli apply` … invoked over
HTTP … scales to zero and wakes — all through the public API/CLI."* ADR-0025's tiers prove the components and
the control plane, but **nothing walks the user's actual path**: `funcdcli push → apply → get → HTTP invoke`.
This ADR adds that lane — a test that drives the **real `funcdcli` binary** + **data-plane HTTP**, importing
only `pkg/**` + `api/**`. It composes already-Implemented behavior (ADR-0030/0031/0033); it adds no mechanism.

## Scenarios

- **scenario: user-pushes-and-deploys** — *when* the user `funcdcli push`es a bundle then `apply`s a manifest
  referencing it by digest, *then* `funcdcli get -o json` shows `Ready`.
- **scenario: user-invokes-over-http** — *when* the user POSTs `<dataPlaneAddr>/function/<name>`, *then* the
  handler runs in the shim and its body is returned.
- **scenario: user-wakes-scaled-to-zero** — *given* a `minReplicas:0` function settled `Idle`, *when* a cold
  POST arrives, *then* it wakes and returns the handler's body (not 503).
- **scenario: public-surface-only** — the test imports only `pkg/**` + `api/**` and drives the `funcdcli`
  binary + HTTP (enforced by the `e2e-boundary` depguard) — exactly the surface a user has.

## Scope

- **In**: one node-gated `tests/e2e` test that boots an embedded execution-wired platform, **builds + drives
  the real `funcdcli`** (`push`/`apply`/`get`), and invokes over HTTP — covering deploy-from-artifact, HTTP
  invocation, and scale-to-zero wake.
- **Out**: wiring `cmd/funcd` for execution (the daemon gap — Open questions); the secret-read / KV-persist /
  timer exit-criterion clauses (added as they land); Python (P-V-3); the Linux/crun path (ADR-0032).

## Constraints & Decision drivers

- **Public surface only** — `pkg/**` + `api/**` + the real CLI binary (`e2e-boundary`, ADR-0027).
- **Runnable without Linux** — node-gated on the process shim; skips cleanly without `node`.
- **No new dependency** — `go build` the in-repo CLI + stdlib `net/http`/`os/exec` + existing testify.
- **Embedded server is faithful** — ADR-0014 makes "embed funcd" first-class; the surfaces under test (CLI +
  HTTP) are the user's regardless.

## Decision

Add `tests/e2e/journey_test.go` — `TestE2EUserJourney`, a fifth **acceptance** lane on ADR-0025's taxonomy
(same package, same `e2e-boundary`):

1. **Server** = embedded `funcd.New(InMemory(), WithRuntimeShim(node, shim), WithArtifactStore(dir))`, `Run` in
   the background; exposes `Addr()` + `DataPlaneAddr()`.
2. **Client** = the **real `funcdcli` binary** (`go build ./cmd/funcdcli`), driven with `--server`/`--token`
   for `push` / `apply` / `get`; invocation is `http.Post` to the data plane.
3. **Journey** = push a bundle to a local OCI layout → apply a Function by digest → poll `get` to `Ready` →
   POST and assert the handler's body → repeat for a `minReplicas:0` function (Idle → cold POST wakes it). The
   layout is shared CLI-push ↔ platform-pull (zero-infra, ADR-0031).

## Contracts

A test, not a Go API — the harness shape + assertions:

```go
// tests/e2e/journey_test.go (package e2e_test; imports pkg/** + api/** + stdlib + testify)
func TestE2EUserJourney(t *testing.T)   // node-gated
func buildFuncdcli(t) string            // go build -o <tmp> ./cmd/funcdcli
// runCLI(args...) → combined output;  applyFunction(...) writes+applies a manifest;  cliPhase(name) → Status.Phase
```

Assertions: `push` prints `<ref>@sha256:<digest>`; `apply` prints `applied`; `get -o json` reaches `Ready`;
the HTTP POST returns `200` with the handler body; the `minReplicas:0` function settles `Idle` then a cold POST
returns `200`.

### Dependencies & I/O
| | Item |
|---|---|
| Adds (lib) | none (`go build` the CLI + stdlib + testify) |
| Consumes | `pkg/funcd` (embedded server), the `funcdcli` binary, data-plane HTTP, the `api` manifest shape |
| External | `node` on PATH (gate) + the `go` toolchain (build the CLI) |

## Implementation plan

1. `tests/e2e/journey_test.go` — `TestE2EUserJourney` + helpers (already written; this ADR formalizes it).
   Shim at `../../shim/nodejs/shim.mjs`; `go build ./cmd/funcdcli` with `cmd.Dir = ../..`.
2. Scenario → test: each scenario maps to the matching step; `public-surface-only` is enforced by the
   `e2e-boundary` depguard.
3. **Definition of done**: passes node-gated (skips without `node`); imports only `pkg/**` + `api/**`; lint
   clean (e2e-boundary holds); no new dependency; `just ci` green; no identity/path leak.

## Review checklist

- [ ] Drives the **real `funcdcli` binary** for `push`/`apply`/`get` (not the SDK in-process, not `internal/`).
- [ ] Invokes over **HTTP** and asserts the handler body (`user-invokes-over-http`).
- [ ] **Scale-to-zero** proven (`user-wakes-scaled-to-zero`): Idle → cold POST wakes + serves.
- [ ] **`e2e-boundary` clean** (`public-surface-only`); node-gated; no new dependency; no leak.

## Consequences

- (+) The exit criterion is now **executable** — one test fails if the CLI, artifact distribution, execution,
  or data plane regresses; it also catches binary-only bugs (flags, manifest decode, push output) the other
  tiers can't see.
- (−) The server is the **embedded** platform, not the `funcd` daemon (which lacks execution wiring), so it
  doesn't yet prove the *standalone binary* runs functions.
- (−) Covers deploy + HTTP + scale-to-zero, not yet secret/KV/timer — the lane is built to grow.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| server is the **embedded** platform, not the daemon | `cmd/funcd` doesn't wire execution yet | wire the daemon (follow-up), then target the binary |

## Alternatives considered

- **Run the `funcd` daemon as the server** — rejected for now: it doesn't wire execution, so functions don't
  run; faithful only once the daemon is wired (follow-up).
- **Drive `pkg/sdk` in-process** — rejected: the SDK isn't the *user's* surface; the binary also covers its own
  flag/manifest/push-output logic.
- **Shell out to `curl`** — rejected: stdlib `net/http` is the same surface, hermetic, no extra dependency.

## Open questions

| Question | Answered in |
|---|---|
| Should `cmd/funcd` wire execution (`WithRuntimeShim`/`WithContainerExecution`/`WithArtifactStore`) so the standalone binary runs functions — and where does the shim/image live for a shipped binary? | a follow-up ADR (daemon execution wiring) |
| When do secret-read + KV-persist (and the timer clause) join the journey? | when **P-W** lands + a KV step is added |

## References

- [ADR-0025](0025-testing-strategy-and-e2e-harness.md) — the taxonomy this extends.
- [FEAT-0000](../feat/0000-feat-v1.md) — the exit criterion this proves through the public CLI/API.
