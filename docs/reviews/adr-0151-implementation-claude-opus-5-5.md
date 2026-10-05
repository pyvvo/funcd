# ADR-0151 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0151](../adr/0151-external-invoke-deadline.md) — external invoke deadline (FEAT-0006/F107)
- **Work**: funcd `feat/adr-0151-invoke-deadline` (1 commit, 26 files, +1003/−27) and pyvvo/funcd-typescript
  `feat/adr-0151-invoke-deadline` (1 commit, 3 files, +66/−6)
- **Model**: claude-opus-5-5
- **Verdict**: **changes-requested** — 0 Blocker, 1 Major (model), 5 Minor (1 model, 2 adr, 2 env)

The production code is correct and matches every Contract. Both repos are green, and all six mutants were killed.
The gap is in the tests: the ADR's `pkg/funcd` e2e column, the one that runs real shims, was not written. This leaves
the cross-repo behavior (funcd's header followed by the real pool, and the 504 arriving before the pool's 503) without
an end-to-end test.

## Verification run (evidence)

Both sides were verified together. A local `go.work` over `.` and the funcd-typescript worktree was created in the funcd
worktree for the run and then removed. The tree is clean afterwards.

| Check | Command | Result |
|---|---|---|
| Build (host) | `scripts/agent/d go build ./...` | exit 0 |
| Vet (host) | `go vet ./api/... ./cmd/funcd/ ./internal/{activator,controlplane,dataplane,function,platform/config}/ ./pkg/funcd/` | exit 0 |
| Build + vet (Linux) | `GOOS=linux go build ./...`, `GOOS=linux go vet` on activator, dataplane, pkg/funcd, cmd/funcd | exit 0 |
| Race tests | `go test -race -count=1` on all 10 touched packages (api/fault, api/openapi, api/types/v1alpha1, cmd/funcd, internal/activator, internal/controlplane, internal/dataplane, internal/function, internal/platform/config, pkg/funcd) | all `ok`, exit 0 |
| Lint (host) | `go tool golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| Lint (Linux) | `GOOS=linux <golangci-lint from go tool -n> run` on the touched packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | no output |
| Modules | `GOWORK=off go mod verify` | `all modules verified` |
| OpenAPI regenerated | `TestSpecGeneratedFromGo` (in the race run) | pass; the diff adds only `FunctionSpec.timeout` (int64, 0..3600000000000) |
| funcd-typescript | `scripts/agent/d just ci` (install, biome lint, typecheck, test, build, go-check, clean-tree check) | exit 0; 89/89 tests; `shim/pool.mjs` current (no stale build output) |
| Not run (out of this review's scope) | `just ci-full` (e2e), Lima lanes | — |

### Mutants (6 of 6 killed)

| # | Side | Mutation | Killed by |
|---|---|---|---|
| M1 | funcd | `serveFunction` sets the deadline on internal (link) requests too | `TestScenarioLinkKeepsOwnLimit` |
| M2 | funcd | `forward`'s `ErrorHandler` drops the `DeadlineExceeded` branch (→ 503) | `TestScenarioHungHandlerCutAtDefault`, `TestForwardErrorHandlerDeadlineFaultVersusPlainDeadline` |
| M3 | funcd | `ServeHTTP` wakes under `r.Context()` instead of the deadline context | `TestScenarioColdWakeCutAtLimit` |
| M4 | funcd | `DeadlineTransport` no longer deletes a caller's `X-Funcd-Timeout-Ms` | `TestDeadlineTransportHeader` |
| T1 | ts | `callTimeoutMs` returns the header value without the 1 s margin | both `scenario pooled-node-follows-limit` tests |
| T2 | ts | `PooledHandler.invoke` arms its timer with the fixed 30 s, not `timeoutMs` | `scenario pooled-node-follows-limit: a never-settling handler answers 503 after header + 1 s` |

The funcd mutants were applied with `go test -overlay`. The TS mutants were applied in throwaway clones. The work was
never edited.

## 🟡 Major

1. **The test plan's `pkg/funcd` e2e column (real shims) is missing** — attribution **model**.
   ADR `docs/adr/0151-external-invoke-deadline.md:220-231` assigns an e2e leg to four scenarios:
   - `hung-handler-cut-at-default`: a never-settling `nodeFn` with `WithDefaultInvokeTimeout(2s)`.
   - `link-keeps-own-limit`: a pooled callee, a 40 s link and a 32 s handler ⇒ output.
   - `step-keeps-own-limit`: a 5 s step at D = 1 s, and a `shared`-pool step of 40 s with a 32 s handler ⇒
     `Succeeded`.
   - `pooled-node-follows-limit`: `nodeFn(...).pooled(...)`; 32 s at 40 s ⇒ 200, and never settling at 2 s ⇒ 504.

   None of these tests exists. In `pkg/funcd` the diff adds only `invoke_deadline_internal_test.go`.
   `TestScenarioStepKeepsOwnLimit` (`pkg/funcd/invoke_deadline_internal_test.go:73`) checks `workerClient`'s header
   against an `httptest` server. It runs no workflow engine, sets no D and uses no pool. `pooled-node-follows-limit`
   has no funcd test at all; the commit message defers it to funcd-typescript's `callTimeoutMs` unit tests, which
   cover only the pool half.

   The unit halves are each proven: the header reaches the worker (dataplane tests), and the pool follows the header
   (TS tests). Decision 7's end-to-end claims are not proven: the 32 s answer is now served where v0.4.4 answered 503
   at 30 s, and funcd's 504 beats the pool's 503. To fix: write the four e2e legs under the existing `TestScenario<Name>`
   names, or as subtests of them. Run them locally over a `go.work`. Land them with the funcd-typescript pin bump
   (Minor 4), because they cannot pass against the pinned v0.4.4.

## Minor

1. **`require` inside goroutines** — attribution **model**. `TestScenarioSpecTimeoutRaisesLimit`
   (`internal/dataplane/deadline_test.go:113`) calls `require.Equal` and `requireDeadline504` (which wraps `require`)
   inside two `go func()` bodies. On failure this calls `t.FailNow` off the test goroutine, which the Go `testing`
   documentation forbids. To fix: collect the results and assert them after `wg.Wait()`, or use `assert`.
2. **The Contract misquotes the existing `ErrorHandler` line** — attribution **adr**.
   `docs/adr/0151-external-invoke-deadline.md:171-172` says that other errors "stay
   `fault.Wrapf(perr, fault.Unavailable, op, "upstream call failed")`". On main, the line is
   `fault.Unavailablef(op, "upstream call failed")` (`internal/activator/activator.go`, `forward`). The implementation
   keeps main's line unchanged (`internal/activator/activator.go:441-447`), which is what "keeps … and stays" intends.
   No code change is needed.
3. **The `safeBeforeFuncd` claim for funcd-typescript holds, with one caveat** — attribution **adr**. Without the
   header, the pool keeps exactly the old 30 s, and today's funcd sends no header, so releasing the shim first is
   functionally safe. However, funcd before this change forwards a caller-supplied `X-Funcd-Timeout-Ms` to the worker
   verbatim: main's `forward` and `serveFunction` filter no request headers; only `DeadlineTransport`, added here,
   deletes it.

   The ADR lets another funcd PR pin a tag that holds this change before ADR-0151's funcd half lands
   (`:207`, `:216-217`, the release-order note). If that happens, an external caller of a pooled Node function can set
   the pool's timer to any value from 1 ms to about 24.8 days. With a never-settling handler, each pending entry and
   its timer then live for the caller's chosen time instead of 30 s. To avoid this, pin a tag holding this change only
   in this ADR's funcd PR, or after it.
4. **funcd-typescript tag not pinned** — attribution **env**. `go.mod`/`go.sum` are unchanged, so the checklist
   item "new funcd-typescript tag pinned" is open. It cannot be met until the funcd-typescript PR merges and is
   released. The funcd PR must wait for that release (`go get github.com/pyvvo/funcd-typescript@<tag>`).
5. **Tracking not advanced in the branch** — attribution **env**. The ADR is still `Accepted` and the F107 row is
   still `accepted`; the adr-impl exit bump (`Accepted → Reviewing`, row → `reviewing`) is not in the branch. This
   review was told not to edit docs, so the campaign workflow presumably applies the status stamps itself.

## ✅ Verified correct (what's strong — keep it)

- **Contracts match exactly.** The following were checked against the ADR:
  - `fault.DeadlineExceeded` / `DeadlineExceededf`, and the `kindProblem` row (urn, `Gateway Timeout`, 504).
  - `v1.MaxInvokeTimeout` / `DefaultInvokeTimeout`, and the `FunctionSpec.Timeout` tags, which are pinned to the
    constant by `TestFunctionSpecTimeoutTagIsMaxInvokeTimeout`.
  - `ResponseDeadline`, `WithResponseDeadline`, `DeadlineOp`, `TimeoutHeader` and `DeadlineTransport`.
  - The `dataplane.Handler` signature, `WithDefaultInvokeTimeout` (0 ⇒ 60 s; < 0 or > 1 h ⇒ `Invalid` from `New`),
    and `workerClient`.
  - The `Invoke.DefaultTimeout` key with `FUNCD_INVOKE_DEFAULT_TIMEOUT`, and `parseDuration("invoke.defaultTimeout", …, 0, true)`.
  - On the TS side, `callTimeoutMs` and `PooledHandler.invoke(event, timeoutMs, …)`.
- **Checklist invariants hold**, verified by grep:
  - Only `internal/activator/deadline.go:42` raises the Kind.
  - Only `internal/dataplane/dataplane.go:201`, under `!internal`, sets a `ResponseDeadline`.
  - `ServeHTTP` passes the original `r` to `forward`, so the proxied request carries no context deadline.
  - The OpenAPI diff is the `timeout` property alone.
- **`DeadlineTransport` is careful**:
  - It clones the request and never mutates the caller's request, which the test checks.
  - Its mutex-guarded `returned`/`expired` handoff closes a late response and frees the `CallTracker` count.
  - It never cancels a response that was returned in time.
  - It returns the inner response unwrapped, so the 101 tunnel works (`TestDeadlineTransportTunnelsUpgrade`).
  - The header is rounded up, with a minimum of 1.
- **Wake handling follows ADR-0016.** `context.WithDeadlineCause`, combined with the `r.Context().Err() == nil` check,
  turns only a deadline end into 504. The shared activation continues: the second call is served warm, and no second
  scale-up happens (`TestScenarioColdWakeCutAtLimit`). An earlier `ActivationTimeout` stays 503
  (`TestServeHTTPActivationTimeoutBeforeDeadlineIs503`).
- **The deadline is per call.** It is counted from `received` in `Server.ServeHTTP` and read from the Function loaded
  for that call, so an edit applies to the next call. Static and Upstream Routes are untouched
  (`TestUpstreamRouteNotBoundByInvokeDeadline`).
- **Scenario tests in `internal/dataplane` are real.** They use a real Handler and activator with `httptest` workers,
  assert the exact problem type, title and detail, and check the timing bounds in both directions (not before the
  limit, and within 1 s after it).
- **funcd-typescript is tight.** `callTimeoutMs` rejects every malformed form (`''`, `' 500'`, `'1.5'`, `'-5'`, over
  the cap, `> 2^53`). `shim/pool.mjs` is rebuilt and committed, and `just ci`'s clean-tree check confirms it is current.
- **Commit markers are present**: `feat(edge)!:`, `BREAKING CHANGE:` and `Fixes #187`.

## Recommendation

Send the work back to `adr-impl` for the Major and Minor 1. Write the four real-shim e2e legs and run them over a
local `go.work`. Land them in the funcd PR together with the funcd-typescript pin bump after the TS release.
Production code needs no change. Release order: merge and release funcd-typescript first, then the funcd PR with the
pin, and do not let another PR pin that tag earlier (Minor 3). The ADR stays at its current status, and nothing is
advanced.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0151",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 5,
  "model_attributed": 2,
  "dod_passed": 10,
  "dod_total": 14,
  "report": "docs/reviews/adr-0151-implementation-claude-opus-5-5.md",
  "notes": "contracts match, all checklist invariants hold, race tests/vet/lint (host+Linux)/mod verify green, funcd-typescript just ci green, 6/6 mutants killed; pkg/funcd real-shim e2e legs of 4 scenarios missing, pooled-node-follows-limit untested in funcd (model, Major); require in goroutines in TestScenarioSpecTimeoutRaisesLimit (model); Contract misquotes ErrorHandler line, pre-0151 funcd forwards a caller X-Funcd-Timeout-Ms if another PR pins the shim first (adr); tag not pinned yet, status stamps deferred (env); ci-full/Lima not run (scope)"
}
```
