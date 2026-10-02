## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #96 fix, model: claude-opus-5-5)

Change: branch `fix/i96`, one commit `51c37ee fix(artifact): fail an image index that names no platform with
NoMatchingPlatform` (4 files: `internal/artifact/platform.go`, `internal/function/function.go`,
`internal/function/materializer.go`, `internal/function/platform_test.go`).

Root cause (issue): `artifact.Platforms` returned `nil, nil` for an index whose descriptors all lack a usable
platform; the reconciler's step-2b gate reads `nil` as "runs anywhere", so it let the Function through, and
`selectManifest` in `Pull` then refused the same index on every reconcile ("it provides []"), retried forever.

The fix makes `Platforms` return `fault.Invalid` wrapping `scheduler.ErrNoMatchingPlatform` when an index yields no
platform, so the existing gate (`function.go` step 2b) and the pool filter (`pool.go`) take their existing
NoMatchingPlatform branches. `placementMessage` now walks to the innermost `fault.Error` message so the status names
the problem rather than the `function.artifactPlatforms` wrapper.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Sentinel doc not updated** · attribution: `model` · `internal/scheduler/scheduler.go:27-28` still says
  `ErrNoMatchingPlatform` "is wrapped (fault.Invalid) by Schedule when no worker node's platform is in
  Request.Platforms"; it is now also wrapped by `artifact.Platforms` for an index that names no platform. The fix
  updated the `PlatformResolver` and `Platforms` doc comments but not the sentinel's. Fix: one more clause on the
  sentinel comment.
- **Status message shape for this case is outside ADR-0145's stated format** · attribution: `adr` · ADR-0145's
  Contracts give the gate-failure message as `artifact provides [<list>]; node <name> runs <platform>`; the new case
  reports `artifact <digest> provides no platform: no manifest in its index names one` (no node named). The ADR did
  not anticipate this index shape and the issue asks only for "a reason that names the problem", which this message
  does; the reason (`NoMatchingPlatform`) and the phase (`Failed`) match the ADR. Not scored; a future ADR touching
  placement messages may want to fold this case into the documented format.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the three non-test files reverted to the pre-fix
  versions and the test kept, `TestIssue96_IndexWithoutPlatformsIsNoMatchingPlatform` failed in both subtests
  (`no platform`, `unknown/unknown`) with the issue's exact error: `function.converge: materialize artifact: …
  artifact.Pull: artifact sha256:… has no bundle for darwin/arm64; it provides []`. Worktree reset to `51c37ee`,
  clean.
- **Passes with the fix**, un-skipped, under `-race`: both subtests PASS.
- **Mutants (2, both killed):**
  - M1 — `Platforms` returns a plain `fault.Invalidf` without wrapping `scheduler.ErrNoMatchingPlatform`: the gate
    treats it as a retryable resolver error; `TestIssue96_…` FAILS.
  - M2 — `placementMessage` stops at the outermost `fault.Error` (the pre-fix behavior): status message becomes the
    `function.artifactPlatforms` wrapper text; `TestIssue96_…` FAILS. The existing scheduler-path scenario
    (`TestScenarioFunctionNoMatchingPlatform`) still passes with the loop, so the message change is backward
    compatible for the scheduler refusal (singlenode wraps a plain sentinel, so the walk stops at its `Msg`).
- **Cause, not symptom.** The resolver no longer reports "any platform" for an index that names none — the two
  readers of the same index now agree. No retry, timeout or swallowed error was added; the transient-outage path
  (`fault.Unavailable` from the resolver → retried, no status) is untouched.
- **Scope.** Every hunk serves the issue: the resolver check, the message walk the new error chain needs, and two
  doc comments. No test weakened or deleted; existing ADR-0145 tests in `internal/function` and `internal/artifact`
  pass unchanged. The pooled path (`pool.go:109`) benefits through the same sentinel with no code change.
- **Reuse.** Reuses the existing `scheduler.ErrNoMatchingPlatform` sentinel, `fault.Wrapf`, the gate's existing
  `gateFailed` branch, the `newShimHarness` test harness and the real `artifact.OrasMaterializer`. The hand-written
  index helper `indexOverUnannotated` is necessary — `funcdctl index` / `artifact` refuse unannotated sources, and no
  test helper in `internal/testkit` or the artifact tests builds such an index (searched `MediaTypeImageIndex`).
- **Import graph.** `internal/artifact` → `internal/scheduler` is a new edge; `internal/scheduler` depends only on
  `api/fault` and `api/types/v1alpha1`, so no cycle and no depguard rule breached (lint clean).
- **Conventions.** `api/fault` errors with an op, ctx-first, no `any` in signatures, top-level imports, concise
  comments, surrounding naming kept.
- **ADRs.** No ADR file edited. ADR-0145 Decision 4 (`nil` = unannotated manifest runs anywhere) is preserved —
  only the index branch changed — and the change realizes the ADR's purpose (fail before any worker starts with
  `NoMatchingPlatform`).
- **Checks (touched packages):** `gofmt -l` clean; `go build ./...` OK; `go test -race -count=1` ok for
  `internal/artifact/...`, `internal/function/...`, `internal/scheduler/...`, `cmd/funcdctl` (a `Platforms` caller);
  `go vet` OK; `golangci-lint run ./internal/artifact/... ./internal/function/...` 0 issues. e2e, Linux lint and
  lanes are left to the group gate.
- **Shape.** `fix(artifact):` subject, `Fixes #96`, attribution trailer, one issue in the commit.

### Recommendation
Pass. Optionally add the one-clause sentinel doc update before the PR; it does not block.
