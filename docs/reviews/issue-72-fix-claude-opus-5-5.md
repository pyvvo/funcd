## Verdict: pass — 0 blockers, 0 majors  (issue #72 fix, model: claude-opus-5-5)

Fix under review: commit 2500327, `fix(function): keep healthy pool members Ready while a sibling's thread respawns`,
on the pooling group branch (head 2500327). Touched files: `internal/function/function.go`, `internal/function/pool.go`,
`internal/function/pool_test.go` and `internal/function/supervision_internal_test.go`.

The issue: `convergePooled` judged every pooled member by the pool host's `GET /health/readiness`. `pool.mjs` returns
503 on that endpoint while any one handler's worker thread respawns after a post-boot fault (ADR-0044 Decision 4), and
the host keeps serving the other handlers. When a sibling faulted, a pass of a healthy member in that window set the
member to `Degraded` (`Restarting`) and dropped its route.

The fix: `readyReplicas` and `probeReady` take the health path as an argument. The pooled path probes
`/health/liveness`, and the solo and switch paths keep `/health/readiness`. Both pool hosts open their listener only
after every member's handler has loaded: `pool.mjs` awaits `pool.ready` before `serve`, and the Python `pool.py` calls
`await_ready()` on each handler before it binds. A boot-time shape error still exits the host with code 3. A live pool
therefore means that every member booted.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The route assertion in the regression test is vacuous** · attribution: model. `require.NotEmpty(t, h.routes(t),
  "good keeps its route")` in `internal/function/pool_test.go` also holds on the pre-fix code, because `crasher` is
  still Ready and keeps its own route. In an overlay of the pre-fix `function.go`, `pool.go` and
  `supervision_internal_test.go`, with the phase and upstream assertions removed, the test passes and logs
  `good phase=Degraded routes=1`. The phase and `Upstream` assertions catch the bug, so the test still guards the fix.
  Fix: assert that a route for `good` exists, not only that some route exists.
- **Two comments still say that readiness is always `/health/readiness`** · attribution: model.
  `internal/function/function.go:73` (the `Materializer` field: "gates readiness on the shim's /health/readiness") and
  `internal/function/function.go:1307` (`Upstream`: "set only after /health/readiness passes") are now wrong for
  pooled members, which are gated on `/health/liveness`. Fix: say "the health probe" or name both paths.
- **A crash-looping member now always shows as Ready** · attribution: adr (recorded, not scored). Pool liveness is
  200 while the host is up, so a member whose own thread keeps faulting after boot shows as `Ready`, and its own calls
  return 503 from the pool. Before the fix it showed as Degraded in about 30% of passes, which happened only because
  of the defect. Neither pool host exposes per-member health (the ADR-0044 contract defines only pool-level
  `/health/liveness` and `/health/readiness`). The commit message says this. A per-member signal would change the
  funcd-to-shim contract, so it needs an ADR and a language-repo release. It is not part of this fix.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** `git revert --no-commit 2500327` applied cleanly. The
  test file `pool_test.go` was then restored to HEAD so that the test still exists. The
  `supervision_internal_test.go` call sites go back with the old signature. `go test -race -run TestIssue72_
  ./internal/function/` fails: `expected: "Ready", actual: "Degraded" — a sibling's thread fault is not good's`. This
  is the issue's symptom.
- **The test passes with the fix.** After `git reset --hard 2500327`, it runs un-skipped and passes under `-race`
  (`-count=3`).
- **The behavior users see is fixed with the real pool host.** A scratch e2e probe (not committed) was overlaid into
  `pkg/funcd`. It used the real `pool.mjs` (funcd-typescript v0.4.0), the process driver and the full platform. A
  member `late` throws 300 ms after every boot, and its sibling `good` shares the worker id. Over 30 rounds, the probe
  re-applied `good`, waited 100 ms, and then read its phase and called it.
  - With the fix, `good` was not Ready in 0/30 rounds, and 0/30 calls failed.
  - Pre-fix (overlay of `function.go` and `pool.go`), `good` was not Ready in 11/30 rounds. This matches the
    issue's 8–9/30.
- **The cause is fixed, not masked.** The pass no longer applies the pool-wide readiness verdict to one member, which
  is the cause the issue names (`pool.go:131`). The fix adds no timeout, retry or swallowed error. Liveness is the
  correct pool-level signal: an endpoint answers only after every handler has loaded, and a boot failure exits the host
  with code 3, which `readyReplicas` still reports as `shapeFailed`. Both behaviors were confirmed in the pinned
  `pool.mjs` and `pool.py`.
- **The Python pool host is unaffected.** `pool.py` answers 200 on both endpoints once it serves. The two paths behave
  the same for Python members.
- **Mutants are killed.**
  - The pooled path probing `readinessPath` again (the revert) fails the test.
  - The solo and switch paths probing `livenessPath` fail 12 existing tests (for example
    `TestScenarioShimNotReadyRequeues`, `TestScenarioRedeploySwitchesToNewRevision`, `TestGateFailureStopsTheBootingRevision`).
  - `probeReady` ignoring its `path` and always probing liveness fails the same 12 tests.

  The change cannot leak into the solo path without a test failing.
- **The scope is correct.** Every hunk serves the issue:
  - the path parameter and its two constants;
  - the pooled call site;
  - the updated call sites in `supervision_internal_test.go`;
  - the `serveCalls` fake, which now answers `/health/liveness` because pooled tests now probe that path.

  No test was weakened or deleted.
- **The change reuses existing code.** No constant for either health path existed in `internal/` or `pkg/`. The
  provider's `ReadinessProbe` is a separate engine-probe type. The fix extends the existing
  `readyReplicas`/`probeReady` code and adds no second probe helper.
- **The conventions hold.** The new parameter is a plain path string, and the change adds no `any`, no new error type
  and no inline import. The new comment in `convergePooled` explains why and cites ADR-0044 Decision 4. The
  ADR-0046 Decision 5 citation repeats the one already in the function's doc comment ("the member is ready iff the
  pool worker serves").
- **No ADR is contradicted or edited.** The ADR-0044 contract defines liveness as "200 while the host is up" and
  readiness as "200 once every worker loaded". The fix relies on both definitions as written. No file under
  `docs/adr/` changed.
- **The checks are green** (touched packages):
  - `gofmt -l` is clean.
  - `go build ./...` passes on the host and with `GOOS=linux`.
  - `go vet` passes on the host and on Linux.
  - `golangci-lint` reports 0 issues on the host and on Linux.
  - `go test -race -count=1 ./internal/function/...` passes.
  - `go test -tags e2e -count=1 ./pkg/funcd/...` passes (110 s).
  - `just check-hygiene` is clean.

  The Lima lanes were not run, because the fix touches no containerd, network or lane path.
- **The commit shape is correct.** The commit has a `fix(function):` subject, `Fixes #72` and the attribution trailer,
  and it holds one issue.

### Definition of Done
11 / 11 items hold. The two model Minors concern test precision and comment accuracy. Neither makes an item fail.

### Model scorecard
To record: claude-opus-5-5 on issue #72 (fix) → pass. 0 blockers, 0 majors, 3 minors; 2 of them are model-attributed.
DoD 11/11.

### Recommendation
Ship. Optional polish for the builder: make the route assertion name `good`, and update the two comments at
`internal/function/function.go:73` and `:1307`. A per-member health signal for pooled members is a separate funcd ADR
and a language-repo change.
