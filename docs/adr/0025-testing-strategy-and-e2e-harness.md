# ADR-0025: Testing strategy & e2e harness — the four-tier test taxonomy (`tests/e2e`, CI lanes)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-15** · **Accepted 2026-06-15** after judge pass — no Blockers. Folded the judge's two
  **Majors** by making the asserted claims *runnable*: M1 an `e2e-boundary` lint fixture
  (`e2e-boundary-rule-fires`) proving depguard rejects a stray `internal/` import (mirrors
  `tests/lint-fixtures/lintrules_test.go`); M2 a `contract-suite-coverage-guard` walking `internal/` so a
  deleted/emptied `<port>contract` fails CI (new-port coverage stays review-enforced — stated explicitly).
  Minors: a Scope note that this **supersedes** the blueprint's `tests/{contract,integration,e2e/…}` sketch
  (contract suites live at `internal/<port>contract` per ADR-0002; blueprint synced on accept), and a clarified
  L3b (re-proves boot/run-shutdown *from the public surface* + adds multi-instance). Decision unchanged: the
  four-tier taxonomy (unit / port-contract / control-plane+embed e2e / Linux-integration), the `InMemory()`
  embed harness, the Linux lane defined+gated, and the honest deferral of the full exit-criterion walk. No new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: testing, e2e, contract-suites, ci, integration-lane, inmemory, quality
- **Realizes**: [FEAT-0000/F20](../feat/0000-feat-v1.md) (testing strategy: contract suites per port, e2e harness on `funcd.InMemory()`, Linux integration lane)
- **Relates to**: [ADR-0014](0014-platform-facade-lifecycle-harness.md) (the `pkg/funcd` facade + the
  `InMemory()` preset the embed-e2e drives; its control-plane-wiring seam is the prerequisite for the full
  deploy→invoke walk), [ADR-0024](0024-funcdcli-and-sdk.md) (the SDK/CLI that drive the control-plane e2e
  path), [ADR-0023](0023-eventing-core.md) (timer-invoke, an exit-criterion clause), [ADR-0016](0016-activator-scale-to-zero.md)
  (scale-to-zero, an exit-criterion clause), [ADR-0011](0011-runtime-sandbox-port.md) (the containerd/crun
  Linux runtime the L4 lane exercises — its `//go:build linux && integration` tag is the precedent),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (no-mocks: in-memory drivers + contract suites)

## Context & Need

FEAT-0000/F20 asks for a *testing strategy*, not a single test: **contract suites per port**, an **e2e
harness on `funcd.InMemory()`**, and a **Linux integration lane**. Most of the lower tiers already exist —
every port ships an `internal/<port>/<port>contract` suite (8 today), and `pkg/funcd` has `InMemory()` — but
they were built ADR-by-ADR with no single document naming the taxonomy, the boundaries, or which CI lane
runs what. Without that, the strategy silently drifts (a new port skips its suite; an e2e reaches into
`internal/`; the Linux-only tests get expected to run in the pure-Go gate and "fail").

The platform's exit criterion is a full walk — *deploy a JS/Python artifact, invoke it over HTTP and by a
timer, read a secret, persist KV, scale to zero, wake* — **on a Linux box**. Two facts bound what V1's
**pure-Go** test gate can prove of that walk:
1. The real function-execution path (curated runtime image + the runtime shim serving the handler over HTTP,
   real sandbox + netns lateral-deny) needs **Linux + a container runtime** — it cannot run in `just ci` on
   a dev macOS, exactly as the containerd driver's tests are already `//go:build linux && integration`.
2. `pkg/funcd.Platform` today wires the five **ports** + lifecycle but **not** the controller / control-plane
   HTTP server — `Run` is a documented "later seam" (ADR-0014). So the *embed* surface can't yet drive a
   reconcile-to-Ready→invoke loop; the *control-plane* surface (SDK/CLI → `internal/controlplane`) can, and
   does (ADR-0024's tests).

So this ADR's job is to **name the four-tier taxonomy**, **deliver the tiers that run in pure-Go CI now**
(the `InMemory()` embed-e2e + the CI lane wiring), and **honestly defer** the full exit-criterion walk to the
Linux integration lane (defined here, executed on a Linux runner) + the facade-wiring follow-up. It decides
test *architecture* (tiers, locations, build tags, CI lanes) — one topic at one altitude.

## Scope

- **In**: the four-tier test taxonomy (unit / port-contract / in-memory e2e / Linux integration); the
  `tests/e2e` embed-harness on `funcd.InMemory()` (public-surface-only, depguard-bounded); the CI lane
  definitions (`just ci` pure-Go gate + a `linux && integration` lane recipe); the L4 lane skeleton
  documenting the exit-criterion walk.
- **Out**: changing any production code or port; *wiring* the control-plane/controller into `pkg/funcd`
  (that is ADR-0014's seam — a named prerequisite, not decided here); the curated runtime images + shim
  (the runtime lane, F12/F13 follow-ups); a coverage *threshold* number (the lanes emit coverage; gating on a
  percentage is a follow-up); fuzz/property/load testing (V2).
- **Supersedes** the blueprint's sketched `tests/{contract,integration,e2e/{harness,fixtures,scenarios}}`
  sub-layout: contract suites already live at `internal/<port>/<port>contract` (accepted **ADR-0002**, so the
  blueprint sketch is the stale layer), and the Linux lane co-locates in `tests/e2e` behind a build tag. The
  blueprint is synced to this on acceptance (a one-line layout note).

## Constraints & Decision drivers

- **No mocks** (ADR-0002): every driver is tested by its port's contract suite + in-memory drivers — the
  taxonomy formalizes this, it does not invent a new mechanism.
- **Pure-Go `just ci` must stay green on macOS**: anything needing Linux + a container runtime is build-tagged
  out of the default gate (the containerd precedent), never silently failing.
- **e2e import discipline** (depguard `e2e-boundary`): `tests/e2e/**` may import only `pkg/**` + `api/**` —
  the embed-e2e proves the *public* surface is sufficient to run the platform with zero internal reach-in.
- **Honest tiering over a fake full-walk**: V1 proves what is wired (port composition + lifecycle via embed;
  deploy/get/delete via the SDK/CLI control-plane tests) and defers the rest with a named exit — never a
  green test that pretends the un-wired invoke path works.

## Scenarios

- **scenario: e2e-embed-inmemory-boots** — *Given* only the public `pkg/funcd` surface, *when* a caller does
  `funcd.New(funcd.InMemory())`, *then* it returns a non-nil platform and no error; and `funcd.New()` with no
  preset returns a `fault.Invalid` naming the first missing port (the embed contract, from outside `internal/`).
- **scenario: e2e-embed-run-shutdown-crash-only** — *Given* a booted in-memory platform, *when* `Run` is
  started and its context cancelled, *then* `Run` returns nil (graceful) and a second `Shutdown` is idempotent
  (no panic, no double-close).
- **scenario: e2e-embed-multiple-platforms** — *Given* the no-globals rule, *when* two independent
  `funcd.InMemory()` platforms are booted and shut down, *then* both succeed independently (the "embed N
  platforms in one process" property).
- **scenario: e2e-boundary-rule-fires** — *Given* a `lintfixture`-tagged package under `tests/e2e/**` that
  imports an `internal/` package, *when* golangci-lint runs the real config over it, *then* the depguard
  `e2e-boundary` rule **fires** — proving the public-surface claim is mechanically enforced, not just asserted
  (mirrors `tests/lint-fixtures/lintrules_test.go`).
- **scenario: contract-suite-coverage-guard** — *Given* the L2 standard, *when* a guard walks `internal/` on
  disk, *then* every `<port>contract` package it finds has a real `contract.go` and the known port set is all
  present — a drift guard against a deleted/emptied suite (new-port coverage stays review-enforced).
- **scenario: linux-integration-deploy-invoke** *(deferred — L4)* — *Given* a Linux runner with a container
  runtime, *when* a JS/Python artifact is applied and invoked over HTTP and by a timer, *then* it runs, reads
  a secret, persists KV, and scales to zero. **Deferred** to the `//go:build linux && integration` lane (it
  needs the curated runtime shim + the facade control-plane wiring); shipped as a documented, skipped skeleton,
  not a pure-Go-CI test.

## Decision

A **four-tier test taxonomy**, each tier with a fixed purpose, location, and CI lane:

| Tier | What it proves | Location | Lane |
|---|---|---|---|
| **L1 unit** | one package's logic, table-driven | `*_test.go` beside the code | `just ci` (pure-Go) |
| **L2 port contract** | every driver honors its port — real + in-memory, **no mocks** | `internal/<port>/<port>contract` + each driver's `_test.go` | `just ci` |
| **L3a control-plane e2e** | deploy/get/list/delete **through the public SDK/CLI** against the real `internal/controlplane` (authn+RBAC) on `httptest` | `pkg/sdk`, `cmd/funcdcli` tests (ADR-0024) | `just ci` |
| **L3b embed e2e** | `funcd.New(funcd.InMemory())` boots every port + crash-only lifecycle, **public surface only** | `tests/e2e` (depguard: `pkg/**`+`api/**`) | `just ci` |
| **L4 Linux integration** | the full exit-criterion walk: artifact deploy → invoke (HTTP+timer) → secret → KV → scale-to-zero, real sandbox + netns lateral-deny | `//go:build linux && integration` | `just test-integration` (Linux runner) |

1. **L2 is the standard, formalized + drift-guarded**: every port package **must** ship a `<port>contract`
   suite exercised by **≥2 drivers** (a real/embedded one + the in-memory one). 8 suites exist today; this ADR
   names the rule, it doesn't re-build them — but it **adds a runnable drift guard** (`contract-suite-coverage-guard`)
   that walks `internal/` and asserts every discovered `<port>contract` package has a real `contract.go` + the
   known port set is present, so a deleted/emptied suite **fails CI**. (Whether a *new* package is a "port"
   needs an interface heuristic the filesystem can't see, so new-port coverage stays **review-enforced** — the
   guard catches regression, the review catches omission.)
2. **L3b is the new harness**: `tests/e2e/e2e_test.go` (package `e2e_test`, black-box) embeds the platform
   through `pkg/funcd` **only** — the depguard `e2e-boundary` rule proves the public surface needs no
   `internal/` reach-in (the "platform as a library, zero infra" promise). It **re-proves** boot + crash-only
   Run/Shutdown *from outside `internal/`* (the boundary is the new fact — the internal `funcd_test.go` can't
   prove it because it imports `internal/`) and **adds** missing-dep + multi-instance. That public-surface
   claim is itself made runnable by an `e2e-boundary` **lint fixture** (`e2e-boundary-rule-fires`): a
   `lintfixture`-tagged package under `tests/e2e/**` that imports `internal/`, asserted to trip depguard —
   mirroring `tests/lint-fixtures/lintrules_test.go` (so the headline claim is enforced, not asserted).
3. **L3a is named, not re-built**: the SDK/CLI tests (ADR-0024) already drive deploy/get/delete through the
   public client against the real control plane — that **is** the exit criterion's "all through the public
   API/CLI", at the control-plane tier. The taxonomy records it as L3a (it lives in the package tests because
   it legitimately mounts `internal/controlplane`; `tests/e2e` cannot).
4. **L4 is defined now, executed later**: a `//go:build linux && integration` skeleton in `tests/e2e`
   documents the full walk and is wired to `just test-integration` (`go test -tags integration` on Linux),
   matching the containerd driver's existing tag. It is a **documented, skipped** deferral — its prerequisites
   are the **curated runtime shim** (F12/F13 lane) and the **facade→control-plane wiring** (ADR-0014 seam).
5. **CI lanes**: `just ci` stays the pure-Go gate (L1+L2+L3a+L3b, green on macOS); `just test-integration`
   adds the Linux L4 lane. Both emit coverage; a coverage *threshold* is a follow-up (out of scope).

### The missing decision this surfaces (per CLAUDE.md)

The full embed-path exit-criterion e2e (deploy→reconcile→Ready→invoke, all via `pkg/funcd`) is **blocked on a
decision this ADR does not make**: wiring the controller + control-plane server into `pkg/funcd.Run` (ADR-0014's
"later seam"). Recorded here as the next decision to take (a superseding/own ADR for the facade), with the L3b
harness already shaped to consume it.

## Contracts

### `tests/e2e` (`tests/e2e/e2e_test.go`, package `e2e_test`)
```go
// Imports: ONLY github.com/green-0-rabbit/funcd/pkg/funcd + api/** + stdlib/testify
// (enforced by depguard e2e-boundary). No internal/ imports.
func TestE2EEmbedInMemoryBoots(t *testing.T)         // scenario: e2e-embed-inmemory-boots
func TestE2EEmbedRunShutdownCrashOnly(t *testing.T)  // scenario: e2e-embed-run-shutdown-crash-only
func TestE2EEmbedMultiplePlatforms(t *testing.T)     // scenario: e2e-embed-multiple-platforms
```

### L2 coverage guard (`tests/e2e/contractcoverage_test.go`, package `e2e_test`)
```go
// Walks ../../internal on disk (stdlib os/filepath only — no internal import); asserts
// every *contract package has a contract.go + the known port set is present.
func TestContractSuiteCoverage(t *testing.T)         // scenario: contract-suite-coverage-guard
```

### `e2e-boundary` lint fixture (`tests/e2e/boundary-fixture/bad.go`, `//go:build lintfixture`)
```go
// A stray internal/ import under tests/e2e/** — depguard e2e-boundary must reject it.
import "github.com/green-0-rabbit/funcd/internal/store"
```
asserted by a new case in `tests/lint-fixtures/lintrules_test.go`:
```go
func TestScenario_E2EBoundaryBlocksInternalImport(t *testing.T) // scenario: e2e-boundary-rule-fires
```

### `tests/e2e` Linux lane (`tests/e2e/linux_integration_test.go`, `//go:build linux && integration`)
```go
func TestLinuxIntegrationDeployInvoke(t *testing.T)  // scenario: linux-integration-deploy-invoke (deferred: t.Skip)
```

### `justfile`
```
test-integration:   # the Linux L4 lane — go test -tags integration ./... (Linux runner only)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `pkg/funcd` (`New`/`InMemory`/`Run`/`Shutdown`), `api/fault`, stdlib `testing`/`context`, `testify` | `tests/e2e` imports **no** `internal/` (depguard) |
| Adds (lib) | none | reuses the existing test stack |
| Exposes | the `tests/e2e` package + the `just test-integration` lane + the documented taxonomy | L4 skeleton is build-tagged + skipped |

## Implementation plan

1. **`tests/e2e/e2e_test.go`** (package `e2e_test`) — the three embed scenarios via `pkg/funcd` only:
   boot+missing-dep, run/shutdown crash-only (re-proving the internal `funcd_test.go` behavior *from the
   public surface* — the boundary is the new fact), and the genuinely-new multi-instance. Imports only
   `pkg/**`+`api/**`.
2. **`tests/e2e/contractcoverage_test.go`** (package `e2e_test`) — the L2 drift guard: walk `../../internal`
   (stdlib `os`/`filepath` only, no internal import), assert each `*contract` dir has a `contract.go` + the
   known port set is present.
3. **`tests/e2e/boundary-fixture/bad.go`** (`//go:build lintfixture`) + a `TestScenario_E2EBoundaryBlocksInternalImport`
   case in **`tests/lint-fixtures/lintrules_test.go`** — the runnable proof the `e2e-boundary` rule fires on a
   stray `internal/` import (mirrors the existing any-leak / mock-framework fixtures).
4. **`tests/e2e/linux_integration_test.go`** — `//go:build linux && integration`; `TestLinuxIntegrationDeployInvoke`
   that `t.Skip`s with a precise reason (needs the curated runtime shim + the facade control-plane wiring),
   documenting the walk's steps as comments so the lane is real the moment its prerequisites land.
5. **`justfile`** — add a `test-integration` recipe (`go test -tags integration ./...`), documented as the
   Linux lane; leave `just ci` unchanged (it already runs the pure-Go tiers via `go test ./...`).
6. **Test plan**: L3b scenarios + the coverage guard + the boundary-rule fixture are the named tests above
   (run by `just ci`; the boundary fixture is `lintfixture`-tagged so `just ci` skips it, and the lintrules
   case runs it explicitly under `-short`-skippable shell-out). L4 is the deferred skip. Verify `just ci`'s
   four sub-checks stay green and that `tests/e2e` compiles under the `e2e-boundary` depguard rule.
5. **Definition of done**: `just ci` green (four sub-checks); `tests/e2e` runs the three embed scenarios and
   imports no `internal/`; the L4 lane recipe exists and the tagged skeleton is excluded from `just ci`;
   the deferral + the facade-wiring follow-up are recorded; no new dependency; no identity/path leak.

## Review checklist

- [ ] **Taxonomy is real, not just prose**: L3b `tests/e2e` exists and runs the three embed scenarios
      (`e2e-embed-inmemory-boots`, `…-run-shutdown-crash-only`, `…-multiple-platforms`) under `just ci`.
- [ ] **Public-surface discipline, enforced**: `tests/e2e` imports only `pkg/**`+`api/**`; and the
      `e2e-boundary-rule-fires` fixture **proves** depguard rejects a stray `internal/` import (the claim is
      mechanically checked, not asserted) — mirroring `tests/lint-fixtures/lintrules_test.go`.
- [ ] **L2 drift guard runs**: `contract-suite-coverage-guard` walks `internal/` and fails on a deleted/emptied
      `<port>contract` suite; new-port coverage is explicitly review-enforced (stated, not silently omitted).
- [ ] **L4 lane defined + gated**: `just test-integration` runs `-tags integration`; the
      `//go:build linux && integration` skeleton is **excluded** from `just ci` and `t.Skip`s with its
      prerequisite reason (matches the containerd tag precedent) — the deferral is honest, not a fake pass.
- [ ] **L2/L3a recorded, not regressed**: the doc names the 8 port-contract suites as the L2 standard (≥2
      drivers each) and the SDK/CLI tests as L3a — no existing suite weakened or deleted.
- [ ] `just ci` four sub-checks green; **no new dependency**; the facade→control-plane wiring follow-up
      recorded as the prerequisite for the full embed-walk; no identity/path leak; every (non-deferred)
      Scenario a named passing test.

## Consequences

- (+) **The testing strategy is now one named, enforced taxonomy** — four tiers, fixed locations, build tags,
  and CI lanes — so a new port/feature knows exactly which test it owes and which lane runs it. The
  critical-path item `P-Q → P-S` advances.
- (+) **The "platform as a library" promise is proven** by `tests/e2e` running the whole platform through
  `pkg/funcd` with **zero `internal/` imports** (depguard-enforced) — embed, boot, crash-only, multi-instance.
- (+) **The Linux lane is defined and gated** the same way as the containerd driver — pure-Go `just ci` stays
  green on macOS, and the full walk is one `just test-integration` away on a Linux runner once its
  prerequisites land.
- (−) **The full exit-criterion walk is not yet a running test** — it is honestly split across L3a (control-plane
  via SDK/CLI, running) and L4 (real-runtime invoke, deferred), and blocked on the facade→control-plane wiring.
  This ADR names that blocker rather than faking a green end-to-end.
- (note) **Roadmap build edges**: P-S's real edges are `ADR-0014` (the `InMemory()` harness it drives),
  `ADR-0024` (the SDK/CLI L3a path), and `ADR-0011` (the Linux runtime the L4 lane exercises). `P-Q`/`ADR-0016`
  are exit-criterion *clauses* the L4 walk covers, not build edges of the harness. Step-6 records this.

## Temporary workarounds

The **L4 lane skeleton** (`t.Skip` with a prerequisite reason) is the one stopgap — and it carries an explicit
exit: it becomes a live test when (a) the curated runtime shim and (b) the facade→control-plane wiring land. It
is build-tagged out of `just ci`, so it never masquerades as a passing exit-criterion proof.

## Alternatives considered

- **One big "full e2e" test now, faking the un-wired invoke path** — rejected: it would either reach into
  `internal/` (breaking the e2e-boundary that proves the public surface) or stub the runtime/control-plane
  (a mock, banned by ADR-0002), producing a green test that lies about the exit criterion. Honest tiering +
  a named blocker beats a fake pass.
- **Run the Linux integration tests in `just ci`** — rejected: `just ci` runs on dev macOS and has no
  container runtime; the containerd driver already tags its tests `linux && integration` for this exact
  reason. A separate lane keeps the default gate green and fast.
- **Put the L3b embed-e2e in `pkg/funcd`'s own `_test.go`** — rejected: `pkg/funcd`'s internal tests can
  import `internal/`, so they can't prove the *public* surface is self-sufficient. A separate `tests/e2e`
  under the `e2e-boundary` depguard rule is what makes "embed with zero internal reach-in" a *checked* fact.
- **A coverage-percentage gate** — deferred: the lanes emit coverage, but gating on a number now (before the
  full walk runs) would either be gameable or block on the deferred tiers. A threshold is a clean follow-up.

## Open questions

| Question | Where it gets answered |
|---|---|
| Wiring the controller + control-plane server into `pkg/funcd.Run` (unblocks the full embed-walk e2e) | a facade follow-up ADR (ADR-0014's seam) — recorded here as the next decision |
| The curated runtime images + shim that make L4's invoke real | the runtime lane (F12/F13) |
| A coverage threshold + where it gates | a follow-up once the full walk runs |
| Fuzz / property / load testing | V2 |

## References

- [blueprint.md](../../blueprint.md) — "tests/e2e — the same platform, zero infrastructure"
  (`funcd.New(funcd.InMemory())`); the ports-and-drivers "≥2 drivers, no mocks" rule.
- [ADR-0014](0014-platform-facade-lifecycle-harness.md) — the `pkg/funcd` facade + `InMemory()` the embed-e2e
  drives; its control-plane-wiring seam is the named prerequisite for the full walk.
- [ADR-0024](0024-funcdcli-and-sdk.md) — the SDK/CLI L3a control-plane e2e path.
- [ADR-0011](0011-runtime-sandbox-port.md) — the containerd/crun Linux runtime + the
  `//go:build linux && integration` tag the L4 lane reuses.
