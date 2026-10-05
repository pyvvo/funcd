# ADR-0151 implementation review — claude-opus-5-5 (loop 2)

- **ADR**: [ADR-0151](../adr/0151-external-invoke-deadline.md) — external invoke deadline (FEAT-0006/F107)
- **Work**: funcd `feat/adr-0151-invoke-deadline` (1 commit `3c5e547c`, 28 files, +1110/−30) and pyvvo/funcd-typescript
  `feat/adr-0151-invoke-deadline` (1 commit `bdbcb12`, 3 files, +66/−6, unchanged since loop 1)
- **Model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 4 Minor (0 model, 2 adr, 2 env)

Loop 1 asked for two model fixes: the real-shim e2e legs of four scenarios, and the `require` calls inside goroutines.
Both are fixed. Production code is byte-identical to loop 1: only four test files changed (`git diff f1c58b19 3c5e547c`).
Everything that remains open is blocked by the environment (the funcd-typescript release and pin) or by an ADR text
issue. None of it is model work. The pass carries one condition: the integration step must pin the new
funcd-typescript tag, and `just ci-full` must then run the four new e2e legs green. This review was told not to run
e2e, so it did not see them pass.

## Loop-1 findings — status

| Loop-1 finding | Attribution | Status |
|---|---|---|
| Major: the `pkg/funcd` e2e column (real shims) is missing | model | **Resolved.** `pkg/funcd/invoke_deadline_e2e_test.go` (`//go:build e2e`) adds `TestScenarioHungHandlerCutAtDefault`, `TestScenarioPooledNodeFollowsLimit`, `TestScenarioLinkKeepsOwnLimit` and `TestScenarioStepKeepsOwnLimit`. Each one matches its cell of the ADR test plan (see below). |
| Minor 1: `require` inside goroutines in `TestScenarioSpecTimeoutRaisesLimit` | model | **Resolved.** `internal/dataplane/deadline_test.go:113-130`: the goroutines now only record their results, and the assertions run after `wg.Wait()`. Mutant MB shows that the moved assertions still fail the test. |
| Minor 2: the Contract misquotes the `ErrorHandler`'s other-error line | adr | Open. The ADR is Accepted, so its text cannot change. The code keeps main's `fault.Unavailablef(op, "upstream call failed")` (`internal/activator/activator.go:446`), which is what the Contract intends. |
| Minor 3: the `safeBeforeFuncd` caveat | adr | Open, re-verified (details below). |
| Minor 4: funcd-typescript tag not pinned | env | Open. `go.mod:40` still pins `v0.4.4`. |
| Minor 5: tracking not advanced in the branch | env | Open. The ADR is still `Accepted` and the F107 row is still `accepted`. This review was told not to stamp, so the workflow applies the status moves. |

### The new e2e legs against the ADR test plan

| Scenario | ADR `pkg/funcd` e2e cell | Test (`pkg/funcd/invoke_deadline_e2e_test.go`) |
|---|---|---|
| hung-handler-cut-at-default | never-settling `nodeFn`, `WithDefaultInvokeTimeout(2s)` | `:46`. Solo `nodeFn(neverSettles)` at D = 2 s; expects 504 `urn:funcd:problem:deadline-exceeded`, received at or after 2 s and before 3 s. |
| pooled-node-follows-limit | `nodeFn(...).pooled(...)`: 32 s at 40 s ⇒ 200; never settling at 2 s ⇒ 504 | `:57`. Two Functions in pool `w151p`, run concurrently. The 32 s call at `spec.timeout` 40 s expects 200 `{"done":true}`. The never-settling call at 2 s expects 504 before limit + 1 s, so it lands before the pool's 503. |
| link-keeps-own-limit | pooled callee, link 40 s, 32 s handler ⇒ output | `:74`. A solo caller calls a pooled callee through a link with a 40 s timeout and expects 200 with the callee's output. |
| step-keeps-own-limit | step 5 s at D = 1 s; `shared`-pool step 40 s, 32 s ⇒ `Succeeded` | `:90`. Shared pool, D = 1 s. The `brief` step (5 s timeout) answers after 2 s, and the `long` step (40 s) answers after 32 s. The run must reach `Succeeded`. |

The helper changes in `pkg/funcd/shim_regression_e2e_test.go` are minimal: `shimFn.withTimeout` sets `spec.timeout`,
and `postWithin` is `post` with a configurable client timeout. `post` keeps its 30 s behaviour by delegating.
`postWithin` returns transport errors as data, so the call inside the goroutine at `:65` never fails the test off its
goroutine. The goroutine's result channel is buffered, so the goroutine cannot block after a failure.

The loop-1 internal `TestScenarioStepKeepsOwnLimit`, an `httptest` check of the header, was removed. This frees the
scenario name for the e2e leg, which is what the ADR asks for: that row of the test plan has no `internal/dataplane`
cell. The header contract it tested is still covered by `TestWorkerClientSendsDeadline`
(`pkg/funcd/invoke_deadline_internal_test.go:52`), which mutant MA fails.

The implementer's revert run is a local scratch log, not tracked. In that run, the three pooled legs were run against
the pinned v0.4.4 shim, and all three failed for the expected reason:
- The pooled-node and link legs got the pool's `503 function … timed out` after 30.3 s and 30.004 s.
- The `long` step failed with `returned 503` after 30.0 s, while the `brief` step still succeeded, which shows D does
  not apply to steps.

So the legs depend on the shim change, as intended. This review did not see them pass against the new shim; see the
condition above.

## Verification run (evidence)

A local `go.work` over `.` and `../wt-0151-ts` was created in the funcd worktree. With it, `go list -m` resolves
`github.com/pyvvo/funcd-typescript` to the TS worktree. The `go.work` was removed afterwards, and both trees are clean.

| Check | Command | Result |
|---|---|---|
| Build (host, Linux) | `scripts/agent/d go build ./...`; `GOOS=linux … go build ./...` | exit 0 / exit 0 |
| Vet (host) | `go vet` on the 11 touched packages¹ | exit 0 |
| Vet, e2e tag (host, Linux) | `go vet -tags e2e ./pkg/funcd/`; `GOOS=linux go vet -tags e2e` on pkg/funcd, activator, dataplane, cmd/funcd | exit 0 / exit 0 |
| Race tests | `go test -race -count=1` on the 11 touched packages¹ | all `ok`, exit 0 |
| e2e legs present | `go test -tags e2e -list 'TestScenario(HungHandler\|PooledNode\|LinkKeeps\|StepKeeps)' ./pkg/funcd/` | all 4 listed (compiled) |
| Lint (host, Linux) | `golangci-lint run` on the 10 touched package trees; `GOOS=linux` with the same binary | `0 issues.` / `0 issues.` |
| Lint, e2e tag (host, Linux) | `golangci-lint run --build-tags e2e ./pkg/funcd/...` | `0 issues.` / `0 issues.` |
| gofmt | `gofmt -l` on the changed `.go` files | no output |
| Modules | `GOWORK=off go mod verify` | `all modules verified` |
| OpenAPI | `git diff origin/main...HEAD -- api/openapi/funcd.v1alpha1.yaml` | adds only `FunctionSpec.timeout` (int64, 0..3600000000000) |
| funcd-typescript | `scripts/agent/d just ci` (install, lint, typecheck, test, build, go-check, clean-tree) | `EXIT 0`; shim 89/89, both `scenario pooled-node-follows-limit` tests pass (the never-settling one in 1571 ms); no stale build output |
| Not run (instructed) | e2e (`just ci-full`, the four legs above), Lima lanes | — |

¹ api/fault, api/openapi, api/types/v1alpha1, cmd/funcd, internal/activator, internal/controlplane, internal/dataplane,
internal/function, internal/platform/config, pkg/funcd, internal/workernode/local (the link path).

### Mutants (5 of 5 killed)

These are new mutants. Loop 1 killed M1–M4 and T1–T2, and the production code has not changed since then.

| # | Side | Mutation | Killed by |
|---|---|---|---|
| MA | funcd | `DeadlineTransport` ignores the context deadline (`at, hasAt := time.Time{}, false`), so links, steps and Sensors send no header | `TestDeadlineTransportHeader`, `TestScenarioLinkKeepsOwnLimit` (dataplane), `TestWorkerClientSendsDeadline` |
| MB | funcd | `responseDeadline` ignores `spec.timeout` | `TestScenarioSpecTimeoutRaisesLimit` (its assertions now run after `wg.Wait()`), `TestScenarioColdWakeCounts`, `TestScenarioColdWakeCutAtLimit` |
| MC | funcd | `DeadlineTransport` takes the later of the context deadline and the `ResponseDeadline` (`at.Before(d.At)`) | `TestDeadlineTransportHeader` |
| T3 | ts | `callTimeoutMs` accepts `'0'` (`ms < 0`) | `scenario pooled-node-follows-limit: the call timeout follows x-funcd-timeout-ms` |
| T4 | ts | `POST /function/:name` passes `callTimeoutMs(undefined)`, dropping the header | `scenario pooled-node-follows-limit: a never-settling handler answers 503 after header + 1 s` (times out at 15 s) |

The funcd mutants were applied with `go test -overlay`. The TS mutants were applied in throwaway copies of `shim/`.
The work was never edited.

## Minor

1. **The Contract misquotes the existing `ErrorHandler` line** (adr; carried over from loop 1).
   `docs/adr/0151-external-invoke-deadline.md:171-172` says other errors stay `fault.Wrapf(perr, fault.Unavailable,
   op, …)`. Main and the implementation both use `fault.Unavailablef(op, "upstream call failed")`
   (`internal/activator/activator.go:446`). The code is right, and the ADR is frozen.
2. **The `safeBeforeFuncd` claim for funcd-typescript is true, with the loop-1 caveat** (adr). Releasing the shim
   before funcd is safe:
   - Without the header, the pool keeps exactly 30 s (`callTimeoutMs(undefined)` returns 30 000, which the TS test
     checks).
   - No Go file on `origin/main` mentions `X-Funcd-Timeout-Ms` (`git grep -i` returns 0 hits), so today's funcd never
     sends the header.

   The same check shows the caveat: main also never deletes the header, and its proxy forwards request headers
   verbatim. If a funcd PR other than this ADR's pins the new tag first, an external caller could set the pool timer
   of a pooled Node call anywhere from 1 ms to about 24.8 days. To avoid this, pin the tag in this ADR's funcd PR or
   after it.
3. **funcd-typescript tag not pinned; the e2e legs depend on it** (env). `go.mod:40` is still `v0.4.4`, so the
   checklist item "new funcd-typescript tag pinned" is open. It cannot be met until the TS PR merges and is released.
   The work now makes this a hard ordering constraint: per the implementer's revert run, the three pooled e2e legs
   fail against v0.4.4. Until `go get github.com/pyvvo/funcd-typescript@<tag>` lands in this PR, the PR's
   `just ci-full` gate stays red. The commit message says so.
4. **Tracking not advanced in the branch** (env; carried over). The ADR is still `Accepted` and the F107 row is still
   `accepted` (`docs/feat/0006-feat-ingress-hardening.md:241`). The `Accepted → Reviewing → Implemented` moves and the
   board card belong to the workflow's stamping step.

## Review checklist and Definition of done (10 / 14)

| Item | Status |
|---|---|
| Contracts match; schema tag = `v1.MaxInvokeTimeout`, no other OpenAPI change | ✓ (`api/types/v1alpha1/function.go:57`, OpenAPI diff above) |
| Only the activator raises the Kind | ✓ (`internal/activator/deadline.go:42` is the only non-test `DeadlineExceededf`) |
| Only non-internal `serveFunction` sets a `ResponseDeadline` | ✓ (`internal/dataplane/dataplane.go:201`, under `!internal`) |
| No context deadline on the proxied request | ✓ (`DeadlineTransport` uses `WithCancel` and a timer, not a context deadline; MC/M3 checks) |
| Every scenario has a same-name test | ✓ All 9 scenarios: 6 in `internal/dataplane`, `SpecTimeoutBounds` in `internal/controlplane`, and 4 e2e legs in `pkg/funcd`. Two names have both a unit leg and an e2e leg, as the ADR's two test-plan columns require. |
| PR has `Fixes #187`, `!`, `BREAKING CHANGE:` | ✓ (commit message) |
| New funcd-typescript tag pinned | ✗ env (Minor 3) |
| Build | ✓ |
| Lint | ✓ (host + Linux, with and without the e2e tag) |
| Test | ✓ (11 packages, `-race`) |
| `go mod verify` | ✓ |
| `just ci-full` green | ✗ not run (instructed); red until the pin |
| Lima lanes green | ✗ not run (instructed) |
| Every scenario is a named **passing** test in its repo | ✗ pending: the unit legs and both TS tests pass; this review did not run the four e2e legs |

All four open items are discharged at the integration gate after the pin. None of them needs model work.

## ✅ Verified correct (what's strong — keep it)

- **The e2e legs follow the test plan cell by cell.** The step leg sets D = 1 s, which proves that D does not reach
  the dispatcher. The pooled leg runs the 32 s call and the never-settling call concurrently in one pool, so it checks
  both directions of Decision 7 in one rig. The upper bound in `requireDeadline504` (`< limit + 1 s`) states the
  "504 arrives before the pool's 503" ordering directly.
- **The rework is narrow.** Production code is byte-identical to loop 1, and the e2e helpers grew by one field and
  one function. The removed `httptest` step test is still covered by `TestWorkerClientSendsDeadline`, which MA fails.
- **Everything verified in loop 1 still holds and was re-checked:**
  - The Contracts.
  - The Kind and `kindProblem` row.
  - The constants and tags.
  - `DeadlineTransport`'s earlier-of logic, clone, late-response close and unwrapped 101.
  - Wake under `context.WithDeadlineCause`.
  - The per-call deadline counted from `received`.
  - `parseDuration("invoke.defaultTimeout", …, 0, true)` in `cmd/funcd/main.go:339`.
  - `New` wrapping the transport (`internal/activator/activator.go:163`).
  - The `ErrorHandler`'s 504 branch (`:441`).
- **funcd-typescript is tight and green.** `callTimeoutMs` rejects every malformed header. The route test sends the
  header through the real Hono app, which kills T4 (the route dropping the header). `shim/pool.mjs` is up to date,
  as the clean-tree check in `just ci` shows.

## Recommendation

**Pass.** There is no model work left.

Integration order:
1. Merge and release funcd-typescript.
2. In this funcd PR (and in no earlier one, per Minor 2), run `go get github.com/pyvvo/funcd-typescript@<tag>`.
3. Run the PR gate. Its `just ci-full` must show the four `pkg/funcd` e2e legs green. If any leg fails there, the
   review reopens.

The workflow then applies the status moves (Minor 4). They were not stamped here.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0151",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 4,
  "model_attributed": 0,
  "dod_passed": 10,
  "dod_total": 14,
  "report": "docs/reviews/adr-0151-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: both loop-1 model findings resolved (4 real-shim e2e legs added under the ADR names and matching the test plan; require-in-goroutine fixed), production code unchanged; build/vet/race tests (11 pkgs)/lint host+Linux incl. e2e tag/mod verify green over a local go.work, funcd-typescript just ci green, 5/5 new mutants killed; open: Contract misquotes ErrorHandler line, safeBeforeFuncd true but pin only in this PR (adr); tag not pinned so pooled e2e legs + ci-full red until the release, status stamps deferred (env); e2e/Lima not run (instructed)"
}
```
