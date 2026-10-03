## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #27 fix, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r27-py`, commit 2aa263a
`fix(shim): stop the pool parallelism test failing on a loaded host`, against issue #27
("test_pool_parallel compares wall-clock times and fails on a loaded host"). The change touches only
`shim/tests/test_pool.py` (+35/−25).

### Minor

- **The commit body says `Refs #27`, not `Fixes #27`** · attribution: model · `git show -s 2aa263a`
  ends with `Refs #27`. The `/fix` commit template and this repo's earlier fix commits on `main` (for
  example the ones that close #17 and #18) use `Fixes #<N>`. The repo squash-merges, so the effect is
  harmless as long as the PR description carries `Fixes #27`. Fix: use `Fixes #27` in the commit or
  the PR description.
- **When the pool serializes handlers, the test fails slowly and with an unclear message** ·
  attribution: model · Mutant M2 (a global lock around `_Pooled.invoke`) leaves the first handler
  blocked on the FIFO. Both `_post` calls then time out after 10 s inside the threads, and the test
  reports `assert [] == [200, 200]` with two `TimeoutError` thread warnings. It does not say that the
  handlers ran one after the other. The test still fails, which is correct, and the pool host still
  exits on `terminate` (no leftover process). Fix (optional): collect the thread exceptions, or
  assert with a message that names serialization.

### ✅ Verified correct (keep it)

- **The revert check reproduces the issue.** `git revert --no-commit 2aa263a` restores the old
  `test_pool_parallel`. At a load average of about 12 on a 14-core host, it failed 1 of 25 runs with
  `two concurrent CPU handlers serialized: …`, which is the issue's wall-clock failure. After
  `git reset --hard 2aa263a`, the new test passed 25 of 25 runs at the same load. The worktree was
  left clean at 2aa263a.
- **The cause is fixed, not masked.** The issue names a single-sample baseline and a fixed 1.7x
  wall-clock bound as the cause. The new test removes both: it compares no times. Instead, it counts
  the moments at which the two handlers run at once. The handlers meet on a FIFO, write to their own
  byte of a shared `mmap`, and count the changes that they see in the sibling's byte. The change adds
  no timeout, retry or skip.
- **Mutants: both were killed.**
  - M1 replaces `InterpreterPoolExecutor` with `ThreadPoolExecutor`, so the handlers share one GIL.
    The test fails with `saw each other run 10 times: a shared GIL` in 0.4 s.
  - M2 adds a global lock around `invoke`, so the handlers run one after the other. The test fails
    with `assert [] == [200, 200]` after a 10 s timeout.
- **The margin is wide.** A threshold mutant on the fixed code measured 1,272,075 to 1,910,697 seen
  changes under load. A shared GIL gives about 10. The `> 10_000` bound sits about two orders of
  magnitude above the shared-GIL case and about two below the parallel case.
- **Scope.** Every hunk serves the issue. The test was replaced, not weakened: it still covers ADR-0050
  scenario `py-pool-parallel` (per-interpreter GILs beat the shared-GIL threaded equivalent), and it
  now separates the two cases far more sharply. `time` and `threading` are still used by the module.
- **ADR conformance.** The change touches no shim source and nothing in the funcd ↔ shim contract
  (`FUNCD_*` env vars, health endpoints, invoke socket, log capture, trace spans). It conforms to
  ADR-0050's `py-pool-parallel` Then-clause, which asks for a measurable gain over a shared GIL.
- **Reuse.** The test reuses the module's `_start`, `_manifest` and `_post` harness. The FIFO and
  `mmap` come from the standard library. The change adds no new helper or dependency.
- **Conventions.** The name `test_issue_r27_…` follows this repo's `r<N>` prefix for its own issues
  (`test_issue_r17_…`, `test_issue_r18_…`), which keeps them apart from funcd issue numbers. Imports
  stay at the top level. The two comments explain why the approach works, not what each line does.
  ruff format and ruff check are clean.
- **Checks.** `TMPDIR=/tmp d-py just ci` exited 0 in 44 s: ruff passed for every project, mypy was
  clean, the shim ran 121 tests and bundle ran 12 (all passed), the examples passed, Go vet, build and
  test were `ok`, and the porcelain gate found no changed files.

### Definition of Done

10 of 11 items hold. Python has no `-race`, so item 3 was checked as "passes un-skipped, repeatedly,
under load". The one miss is item 11 (commit shape: `Refs` instead of `Fixes`), attributed to the model.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #27 (fix) → pass, 0/0/2, 2 model-attributed,
DoD 10/11.

### Recommendation

Ship it. Put `Fixes #27` in the PR description. The clearer failure message for the serialized case
is optional polish.
