# ADR-0190 implementation review — slice 3 of 3 (pools, Decision 8) — claude-opus-5-5 — loop 1

- **Work**: the last commit on `feat/adr-0190-run-bound-to-its-revision`, `2ba44e0b` ("drain a pool rebuild and run a
  non-current pinned member solo"), on top of slices 1 and 2 (`83bfffbd`, `43b7e63e`, already reviewed).
- **Scope judged**: S3 only. Decision 8: a pinned non-current pooled member runs solo; the manifest rebuild starts a
  second pool worker named by the manifest signature; the rebuild is due when no listed worker holds the current
  signature; the resolver hands out the newest listening worker; the drain clock starts at listen and retires the old
  worker on `CallTracker.Idle(upstream, HandOutSettle)` or `DrainGrace`; the socket's member set is the union of
  both workers' members until the old one is retired; the stopped and liveness-silent restarts stay stop and create.
  The scenario is `pooled-edit-isolated`, plus the resolver-after-restart unit test.
- **Verdict**: **changes-requested**. 0 Blockers, 1 Major, 3 Minors. The drain mechanism is correct and well tested.
  The Major is a regression in the pinned resolver: during the rebuild window, a call pinned to a pooled member's
  *current* revision is handed the old pool worker, which runs that member's *previous* code.

## Verification run (all through `scripts/agent/d`, in the slice worktree)

| Check | Result |
|---|---|
| `go build ./...` (darwin) and `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` on `internal/function`, `internal/activator`, `pkg/funcd` (darwin, linux, and `-tags e2e`) | exit 0 on all three runs |
| `golangci-lint run` on the three touched packages (darwin; linux with `GOOS=linux` and a host-built binary; linux `--build-tags e2e` on `pkg/funcd`) | `0 issues.` on all three runs |
| `go test -race -count=1 ./internal/function/ ./internal/activator/` | `ok` / `ok`, exit 0 |
| `go test -race -count=1 -tags e2e -run 'TestScenarioPooledEditIsolated$' ./pkg/funcd/` | `--- PASS (1.02s)`, exit 0 |
| **Revert check, scenario**: the same e2e run with the four production files overlaid from `HEAD~1` | `--- FAIL (60.87s)`: r1's step `a` failed with `invoke default/flow-a: … EOF`. The restart killed the in-flight call, which is the exact defect the scenario targets |
| **Revert check, unit**: `TestPoolRebuild*` and `TestPoolResolverAfterRestart` with the production files from `HEAD~1` | all 4 FAIL (`should have 2 item(s), but has 1`, which is a restart in place). `TestPoolCrashedWorkerRestartsInPlace` passes on both sides, as a regression guard should |
| Mutant 1, `drainPool`: retire the old worker as soon as the new one listens (Idle and DrainGrace dropped) | killed: `TestPoolRebuildServesInFlightCall` and `TestPoolRebuildKeepsOldUntilNewListens` fail |
| Mutant 2, `newestPool`: oldest instead of newest by `CreatedAt` | killed: `TestPoolRebuildServesInFlightCall` and `TestPoolResolverAfterRestart` fail |
| Mutant 3, `createPool`: drop the union with the draining members (`keep`) | killed: `TestPoolRebuildSocketKeepsDepartingMember` fails |
| Probe (overlaid test, not part of the work): a call pinned to `b`'s current revision while the new pool worker is not listening yet | **FAILS on HEAD** (`up=<old worker>/function/b ready=true`) and **passes on `HEAD~1`**. See the Major finding |
| ADR substance unchanged by the slice; no docs touched | `git diff HEAD~1 HEAD -- docs/` is empty |

## 🟡 Major

**M1 — A call pinned to a pooled member's current revision is served by the old pool worker during a rebuild
(regression; attribution: model).**

- `endpoints.pinned` (`internal/function/function.go`, the `e.r.pooled(f) && ref.Revision == CurrentRevision`
  branch) resolves through `upstreamForFn`. This slice changed `upstreamForFn` to return `servingPool(insts)`, the
  newest *listening* worker of the key. While the new worker boots, that worker is the old one, which holds the
  member at its previous code. The readiness branch then sets `ready = up != ""`, because
  `ref.Revision != servingRevision(f)` (the edited member's `ServingRevision` stays at the old revision until the
  new worker serves it, as `TestPoolRebuildKeepsOldUntilNewListens` itself asserts).
- Evidence: the overlaid probe edits `b` with the new worker held, then calls
  `Upstream({Name: b, Revision: <b's CurrentRevision>, UID})`. The result is `up="http://127.0.0.1:<old port>/function/b"`
  and `ready=true`, so the assertion fails. On `HEAD~1` the same probe passes, because the rebuild stopped the old worker.
- Failure scenario: `b` (pooled) is edited from v1 to v2, and `flow` turns Ready, since slice 1's content gate waits
  only for the stamp, not for the serving switch. A run R2 starts and pins `flow-b-2`. If R2 reaches `b` before the
  new pool worker listens, it dispatches with pin `flow-b-2`, gets the old worker, runs **v1** and records v2's
  digest. This is the #776 defect class that ADR-0190 exists to close. Decision 4 says that nothing falls back to the
  serving revision, and the checklist says that no pinned dispatch falls back. The window is the new pool host's
  boot time (sub-second on the process runtime, seconds on containerd), and every pooled workflow step is pinned.
  The e2e scenario does not catch this, because it waits on `waitServing(flow-b, v2)` before it starts r2.
- Fix direction (for `adr-impl`): a pinned ref at a pooled member's current revision must resolve only to a worker
  that holds the *current* manifest signature. Hand it out (ready) only once that worker listens, or keep the
  unpinned-only `servingPool` choice. Add a unit test of the pinned-current call during the window.
- Attribution note: the sentence in Decision 8 that the resolver hands out the newest listening worker is written
  for unpinned traffic. Decision 4 and the checklist still bind pinned calls, so this finding counts against the
  model. The ADR wording could name the pinned case explicitly in a later ADR.

## Minor

- **m1 — The liveness-silent restart (`:350`) has no in-place assertion (model, test coverage).** Plan step 6 lists
  ":344/:350 still restart". `TestPoolCrashedWorkerRestartsInPlace` covers `:344` with an assertion on the same
  instance id. `:350` relies on the existing `TestIssue422_NeverReadyPoolWorkerIsReplaced`, which asserts only
  `creates+1`. The behavior holds in practice: the instance id derives from the signature, so a second worker of the
  same signature cannot exist. The plan item is still not pinned by a test.
- **m2 — The e2e proves "solo" only indirectly (model).** `TestScenarioPooledEditIsolated` asserts that
  `Revision(flow-b-1).status.phase` is non-empty, which shows a held-revision wake. It does not assert that a solo
  worker of `flow-b-1` ran `b`. The logged `v1` passes whether the solo worker or a not-yet-retired old pool worker
  answered.
- **m3 — Upgrade cost is not stated (adr).** A pool worker created before this change carries revision `""`. After an
  upgrade, every pool therefore takes one drained rebuild on its first pass: two workers per pool until each old one
  drains. This follows from Decision 8's naming and is safe, but the Consequences of ADR-0190 do not mention it.

Tracking note (not scored, workflow): `docs/adr/0190-*.md` still reads `Accepted (2026-10-05)` after the last slice.
The `adr-impl` exit duties (ADR `Accepted → Reviewing`, feat row → `reviewing`) are owed before the gate can stamp
`Implemented`.

## ✅ Verified correct — keep

- **The rebuild is driven by the runtime.** `splitPool(insts, sig)` against `manifestSignature` in the `Revision`
  slot of the worker spec replaces the in-memory `poolSigs`. `TestPoolResolverAfterRestart` proves that a fresh
  reconciler does no second rebuild and keeps the old worker while its call is in flight.
- **Drain semantics match Decision 8 exactly.** The clock starts at the first time the new worker is seen listening
  (`poolDrainSince`, keyed on the new worker's id, so a replacement restarts it). The old worker retires on `Idle`
  or `DrainGrace`. A non-running old worker retires at once. A new worker that never listens leaves the old one
  serving (`TestPoolRebuildKeepsOldUntilNewListens`, with off-by-one checks at `DrainGrace − 1 ms` and `DrainGrace`).
- **The socket and member union.** `drainingMembers` → `createPool(…, keep)`, and the narrowing at retirement through
  `PoolSocketFor` with the manifest's names. Neither the socket nor the manifest is removed. Mutant 3 shows the test
  is load-bearing.
- **The stopped and liveness-silent paths stay stop and create** on the current worker only (`restartPool(cur, …)`).
  `poolSilent(cur)` never judges a draining worker.
- **Member judgement:** a member whose serving revision is current is judged on the worker the resolver hands out,
  and an edited member on the current-manifest worker. This keeps siblings Ready through the rebuild and keeps the
  edited member from switching early.
- **`HeldRevision` for pools:** any non-current pin of a pooled Function is held and runs solo. `convergeHeld` now
  reuses `activator.HeldRevision` instead of duplicating the rule.
- **Liveness is keyed per worker** (`poolLiveness{worker, at}`), so a new worker never inherits the old worker's
  last answer.
- **No existing pooling test was weakened.** `TestPoolManifestIsScopedToItsNamespace` was adapted to the
  signature-named id and gained a `Len(ids, 1)` assertion. The fake-runtime additions (`poolAlias`, `idPort`,
  `holdNew`) are small and explicit.
- **Mutation testing:** 3 of 3 mutants on key lines were killed.

## Recommendation

Loop back to `adr-impl` for M1, with a unit test of a pinned-current call during the window that fails on this
commit. Optionally add the `:350` in-place assertion (m1) and a solo-worker assertion in the scenario (m2). The ADR
stays at `Accepted`/`Reviewing`, and no status is advanced.

```json
{
  "date": "2026-10-05",
  "adr": "0190",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 9,
  "dod_total": 10,
  "report": "docs/reviews/adr-0190-slice3-implementation-claude-opus-5-5.md",
  "notes": "slice 3/3 loop 1 (2ba44e0b, pools Decision 8): build/vet/lint clean also Linux; internal/function+activator -race ok; e2e PooledEditIsolated passes and fails with the slice reverted (in-flight EOF); 4 drain unit tests fail reverted; mutants 3/3 killed. M1 [model] a call pinned to a pooled member's current revision resolves to the old pool worker (old code, ready=true) while the new one boots, proven by an overlay probe that fails on HEAD and passes on HEAD~1; m1 [model] :350 silent restart lacks an in-place assertion; m2 [model] e2e proves solo only via Revision phase; m3 [adr] upgrade rebuilds every pool once (revision \"\")."
}
```
