# ADR-0030 Implementation Review — Function execution / runtime shim (Node)

**Verdict**: **pass** — real function execution conforms to the shim HTTP contract; all five
Scenarios have un-skipped passing tests (the node-gated lane ran: `node` was present); the
four sub-checks are green; no new dependency; no identity leak.
**Reviewed against**: ADR-0030 Contracts / Scenarios / Review checklist / Definition of done ·
blueprint (runtime shim, materialization gate) · ADR-0002 conventions · ADR-0011 (Instance
refinement) · ADR-0020 (readiness refinement) · ADR-0023 (timer invoke path).
**Producing model**: claude-opus-4-8
**ADR status at review**: Reviewing

## Verification run (evidence)

- `go build ./...` → exit 0.
- `go test ./...` → all packages `ok`; node-gated lane executed (node on PATH):
  - `TestScenarioShimEndToEndNode` PASS — real Node shim boots from a `file://` artifact,
    becomes Ready, serves the handler over HTTP at the gateway-resolved upstream (200).
  - `TestScenarioTimerInvokesRealHandler` PASS — a timer EventSource fired through the
    eventing invoker (ADR-0023) POSTs the CloudEvent to the Ready shim; the real handler runs
    and a `Ready` Invocation is recorded.
  - `TestScenarioShimLaunchesWithArtifactAndHandler`, `…ShimReadinessGatesReadyAndRoute`,
    `…ShimNotReadyRequeues`, `…ShimShapeFailureBlocksReady` PASS — pure-Go fake-shim coverage
    of materialize + sandboxSpec + readiness-gate + requeue + shape-failure.
  - The eight ADR-0020 scenarios still PASS (legacy placeholder mode, unchanged).
- `go tool golangci-lint run ./...` → `0 issues`.
- `go mod verify` → `all modules verified`; `git diff go.mod go.sum` empty (no new dep —
  the shim is `node:`-builtins only; FileMaterializer is stdlib).
- Identity grep over changed files → no local username / path / email.

## Scenario → test map (all named, un-skipped, passing)

| Scenario | Test | Lane |
|---|---|---|
| shim-executes-handler | `TestScenarioShimEndToEndNode` | node-gated |
| shim-readiness-gate | `…ShimEndToEndNode` (200) + `…ShimShapeFailureBlocksReady` (shape error) | node + pure-Go |
| reconciler-materializes-and-runs | `…ShimEndToEndNode` (happy) + `…ShimShapeFailureBlocksReady` (Failed/ShapeValid:False) | node + pure-Go |
| timer-invokes-real-handler | `TestScenarioTimerInvokesRealHandler` | node-gated |
| platform-side-without-node | `…ShimLaunches…` / `…ReadinessGates…` / `…NotReadyRequeues` / `…ShapeFailure…` | pure-Go |

## ✅ Verified correct — keep it

- **Dual-mode reconciler is the right seam.** Shim mode keys on a configured `Materializer`;
  with none, the legacy ADR-0020 placeholder path is byte-for-byte unchanged — so the eight
  prior scenarios stay green and execution is purely additive. No status walked backward.
- **Readiness refines ADR-0020 honestly.** `running` is necessary-not-sufficient; Ready now
  requires a 200 from `GET /health/readiness`; a not-yet-serving shim → `Deploying` +
  `RequeueAfter` (re-poll), a crashed shim → `Failed` + `ShapeValid:False`. Legacy mode keeps
  `ready == running`, so the refinement is invisible where no shim runs.
- **Per-replica loopback + `FUNCD_PORTFILE` handshake** (the judge's B1/B2) is implemented end
  to end: the process driver clears+reads the port file, surfaces `Instance.Port`, and
  `upstreamFor` resolves `IP:Port` — proven by the real-node e2e (a real OS-assigned port).
- **Dependency-free shim.** `shim/nodejs/shim.mjs` uses only `node:` builtins, exits 3 on a
  shape failure, and serves the exact contract (`POST /`, `/health/readiness`,
  `/health/liveness`). No npm, no Go dep.
- **Production wiring is opt-in and minimal** — `WithRuntimeShim` / `WithMaterializer`,
  defaulting to the local-file driver, leaving the OCI driver to P-V-A.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- The real-shim **missing-`handle`** path (shim.mjs `process.exit(3)`) is asserted at the
  reconciler level via a fake `Failed` instance, not by a node-gated test that actually runs
  the shim on a bad artifact. The mechanism is covered (pure-Go) but not end-to-end on node.
  Low risk; a one-test follow-up could close it. *(model)*

## Definition of done

| DoD item | Status |
|---|---|
| `just ci` green (pure-Go always; node-gated when node present) | ✅ (4 sub-checks green; node lane ran) |
| A real JS handler executes + is invocable on the process driver | ✅ (`…EndToEndNode`, `…TimerInvokesRealHandler`) |
| Bad artifact → `Failed` / `ShapeValid:False` | ✅ (`…ShimShapeFailureBlocksReady`) |
| No new Go/npm dependency | ✅ (`go.mod`/`go.sum` unchanged) |
| No identity/path leak | ✅ (grep clean) |

## Recommendation

Stamp **ADR-0030 Reviewing → Implemented**. The one Minor (node-level bad-artifact test) is a
cheap optional follow-up, not a blocker. F12/F13 already read `implemented` (co-realized by
ADR-0011/0020) and link ADR-0030 — restored to exact agreement by this stamp.
