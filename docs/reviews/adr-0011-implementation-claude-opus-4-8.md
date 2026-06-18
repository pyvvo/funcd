# ADR-0011 Implementation Review — Function runtime port (process + containerd/crun) (model: claude-opus-4-8)

**Verdict**: **pass** — DoD met, zero Blockers/Majors. The port, the fully-tested process driver, and
the cross-compiled Linux containerd/crun driver realize the Contracts; conservative-OCI + default-deny
posture preserved; containerd integration correctly deferred to the Linux lane. One non-blocking Minor.
**Reviewed against**: ADR-0011 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (§1/§2/§5/§6/§8) ·
ADR-0003 (typed names) · blueprint "Function runtime"/"Security & isolation" · FEAT-0000/F12.
**Date**: 2026-06-14

## Verification run (evidence)

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (linux) | `GOOS=linux go build ./...` | exit 0 — the containerd/crun driver compiles |
| Integration test compiles | `GOOS=linux go vet -tags integration ./internal/runtime/containerd/...` | exit 0 |
| Vet | `go vet ./internal/runtime/...` | exit 0 |
| Lint (full) | `go tool golangci-lint run ./...` | **0 issues** |
| Tests (no cache) | `go test -count=1 -v ./internal/runtime/...` | process: 6 contract subtests PASS; stub: PASS |
| Tree vs ADR | `git status --porcelain internal/runtime/` | port + process + contract + containerd(linux+stub+helpers) + integration test — matches plan |
| Stubs/skips/panic | `grep "not implemented\|t.Skip\|panic("` | none in CI path (only the build-tagged deferred integration skip) |
| `any` in sigs | `grep "\bany\b"` | none (a comment + the sanctioned `...any` mapErr variadic) |
| crun wired | `containerd_linux.go:128` | `WithRuntime("io.containerd.runc.v2", &runcoptions.Options{BinaryName: ociRuntimeBinary})`, `ociRuntimeBinary = "crun"` |
| Deps | `grep go.mod` | `containerd/v2 v2.3.1` + `go-cni v1.1.13` (Apache-2.0), Linux-tagged |
| Identity | identity grep (username / `/Users/` paths / email) | clean |

## 🔴 Blockers
None.

## 🟡 Major
None.

## Minor
- **The containerd driver is two Linux files (`containerd_linux.go` + `helpers_linux.go`), not the one
  the ADR plan listed.** Evidence: ADR plan step 3 named `containerd_linux.go`; the impl split the
  error/state/wait helpers (`mapErr`/`mapState`/`waitTask`/`waitProc`) into `helpers_linux.go`. →
  *Attribution: `model`.* Non-blocking and **defensible** — ADR-0002 §8 sanctions splitting "by
  responsibility when the file is actually large", and `containerd_linux.go` is ~290 lines; the helpers
  are a clean responsibility seam. If strict one-file-per-driver is preferred, fold them back. Recorded
  for transparency, not a rework trigger.

## Recorded — not model-attributed
- **crun ADR amendment (`adr`/decider-attributed).** ADR-0011's substance (Context/Decision/
  Alternatives/Scenarios) changed beyond the status bump — runc → **crun**. This was an explicit
  **decider-directed, pre-implementation amendment** (documented in the ADR header note), with the
  blueprint synced (4 refs) and a new Alternatives row added. The implementation correctly follows the
  amended ADR (`BinaryName: "crun"` via the `runc.v2` shim). This is a sanctioned decision change by the
  decider, **not** a builder overreach — recorded, excluded from the model score.
- **containerd integration scenarios deferred (sequencing-attributed).** `containerd-sandbox-lifecycle`
  + `sandbox-lateral-deny` need Linux+root+containerd+crun+CNI — un-runnable in the pure-Go CI lane.
  They are build-tagged (`//go:build linux && integration`) + `FUNCD_IT`-gated in
  `containerd_linux_test.go`, cross-compile-verified, and run on the Linux VM lane (P-S/F20). Per the
  ADR's Test sequencing this is sequencing, **not** a model gap.

## ✅ Verified correct — keep it
- **Process driver is a real, race-free child supervisor**: `sandbox-create-start-running` (PID +
  `Running`), `sandbox-logs-captured` (stdout→file→`Logs`), `sandbox-stop-idempotent` (SIGTERM→grace→
  SIGKILL, second `Stop` nil), `instance-not-found` (`fault.NotFound`), `sandbox-exec` — all pass. The
  single-`cmd.Wait`-owner design (wait goroutine owns `Wait`; `Stop` waits on `done`) avoids double-Wait,
  and `logFile.Close()` precedes the `Stopped` state set, so logs are flushed when the contract reads them.
- **Port stays container-lib-free** (`runtime.go` imports only `api/types` + stdlib) — ADR-0002 §2.
- **Conservative-OCI + default-deny preserved** in the Linux driver: `WithNoNewPrivileges`, no added
  caps, optional `WithMemoryLimit`/`WithCPUCFS`, `funcd/*` labels, `cio.LogFile`, go-cni netns; errors
  mapped to `api/fault` via `errdefs`. Security posture intact.
- **crun via the `runc.v2` shim `BinaryName`** is the minimal, reversible swap (config-level), exactly
  as the amendment specifies — and it keeps funcd cgo-free (crun is an invoked host binary, not linked).
- **Build-tag discipline**: `//go:build linux` driver + `!linux` stub (`New`→`fault.Unavailable`,
  tested) + `linux && integration` deferred tests; `GOOS=linux go build` compiles the real driver,
  `just ci` builds the stub. Matches the slatedb-cgo-lane precedent.

## Conventions spot-check
ports/drivers (flat subpackages) ✓ · `api/fault` ✓ · typed `SandboxSpec`/`Instance`/`InstanceID`/`State` ✓ ·
ctx-first on all 7 blocking methods ✓ · no `any` in signatures ✓ · no globals/`init` ✓ · slog-only (no
logger needed here) ✓ · no cgo ✓ · drivers return the port interface (ADR-0002 §1 sanctioned exception) ✓.

## DoD
ADR Review-checklist: **7/7** items hold. Scenarios: 6 CI-tested (process), 2 deferred to the Linux lane
(build-tagged, traceable), 1 stub-tested (containerd-requires-linux) — none silently dropped.

## Recommendation
**pass** → stamp ADR-0011 `Reviewing → Implemented`, feat F12 → `implemented`. The lone Minor (helpers
file split) is a sanctioned size-driven seam, not a rework trigger. The containerd driver's runtime
behavior is validated on the Linux lane (P-S), as the ADR sequences.
