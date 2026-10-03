## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-typescript issue #30 fix, model: claude-opus-5-5)

This reviews a pyvvo/funcd-typescript change: branch `fix/r30-ts`, commit `ec67caa` ("fix(shim): wait for the
FUNCD_LOG_SOCK records in the console test, not a fixed 150 ms"), against `origin/main`. The change touches one
file, `shim/test/funclog.test.ts` (+55/-20). It is test-only: no shim source, built output or contract changed.

Issue #30: the console-no-double-capture test over `FUNCD_LOG_SOCK` slept a fixed 150 ms for the asynchronous
`net.connect` sink (`shim/src/funclog.ts`) to deliver two records. On a loaded host the data arrived after the
sleep and the record count was below two.

### 🟡 Major
None.

### Minor
- **The "wait for n lines" condition is not exercised by a split delivery** · attribution: model ·
  Mutant M2 (`length > n` changed to `length >= n` in `readLines`, which stops after the first line) survives:
  both tests pass, 7/7. Both records are written before the connection opens, so they arrive in one chunk, and
  a loop that stops after one chunk passes. An off-by-one in the line count would therefore go unnoticed and
  could bring back the flake on a host that splits the delivery. Fix: one assertion where the second record is
  written only after the first has been read (or written after a delay), so that the two lines arrive in
  separate chunks.
- **The commit says `Refs #30`, not `Fixes #30`** · attribution: model · `git log -1` body ends with
  `Refs #30`. The `/fix` shape (and `/fix-batch` Step 1) requires `Fixes #N` in the commit. Because PRs here are
  squash-merged with the PR body, this only matters if the PR body does not carry `Fixes #30`. Fix: use
  `Fixes #30` in the commit or the PR body.

### ✅ Verified correct (keep it)
- **The cause is removed, not masked.** The fixed `setTimeout(resolve, 150)` is gone. `readLines` waits for
  the server's next `connection` event and then reads `data` until the n-th newline. The 10 s
  `AbortSignal.timeout` is only an upper bound that fails the test. No retry and no longer sleep was added.
- **The regression test reproduces the issue.** `issue r30: both FUNCD_LOG_SOCK records are read when they
  arrive after 300 ms` uses `logServer(..., 300)`, a reader that `pauseOnConnect`s and resumes after 300 ms,
  like a reader on a loaded host. It passes with the fix (304 ms, `# pass 7 # fail 0` for the file).
- **Revert check.** `git revert --no-commit ec67caa` removes the regression test along with the fix, because
  the fix is test-only. On the revert, the file passes 6/6 under the old 150 ms wait (on an idle host). The fix
  and the test share the helper, so the pre-fix behavior was checked with mutant M1 (below). Afterwards,
  `git reset --hard ec67caa` restored the tree: clean, HEAD `ec67caa`.
- **Mutants.**
  - M1 (pre-fix semantics: `readLines` collects data for a fixed 150 ms and returns): the regression test
    fails with `actual: []`, so no records arrived inside the fixed wait. This is the issue's failure. It also
    shows that the 300 ms delayed reader really holds the data back.
  - M3 (remove the `break`): both FUNCD_LOG_SOCK tests fail with `AbortError: The operation was aborted`, so
    the timeout fails the test instead of hanging.
  - M2 survives (see Minor).
  - Each mutant was restored with `git checkout`.
- **The no-double-capture assertions are unchanged.** `originalCalls === 0`, exactly two records, and the
  body/sev/attrs/`funcd.source`/inv/trace fields are still asserted. No assertion was weakened or deleted.
- **Ordering is sound.** `readLines` is called after synchronous code with no `await` between the shim's
  `connect()` and the `once(server, 'connection')`. The UDS accept comes on a later event-loop turn, so the
  event is not missed (the helper's comment states this).
- **Scope and reuse.** The one change is the test harness. `logServer` replaces the inline server/teardown
  and is shared by both socket tests. Teardown moved to `t.after`, which also covers a failing assertion. The
  existing `tempDir` helper is reused. `once`/`on` from `node:events` and `AbortSignal.timeout` are standard
  library; nothing is hand-rolled.
- **Conventions.** The imports are at the top of the module. The helper comments explain why (close waits for
  live connections; the accept comes on a later turn), not what. Biome passes. There are no built files to
  regenerate (`just build` left the tree clean).
- **ADR conformance.** funcd ADR-0081 (scenario console-no-double-capture, UDS transport) is still covered with
  the same assertions. The `FUNCD_LOG_SOCK` env var, the NDJSON wire format and the shim's sink are untouched,
  so there is no funcd ↔ shim contract change and no ADR is needed.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, clean-tree gate) exited with code 0;
  all suites passed (78/78 shim tests, 0 failures).

### Definition of Done
9 / 11 items hold (the `/fix-review` fix checklist, adapted to TypeScript: `node --test` instead of `-race`,
and the regression test named `issue r30: …`, following this repo's `issue rNN` pattern).
Misses: item 4 (mutant M2 survives · model) and item 11 (`Refs #30` instead of `Fixes #30` · model).

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-typescript #30 (fix) → pass, 0/0/2, 2 model-attributed, DoD 9/11.
(Not recorded in funcd's ledger: this reviews a language-repo change, and funcd is read-only for this gate.)

### Recommendation
Pass. Before the PR, optionally add a split-delivery assertion so that the line-count condition is tested, and
make sure the commit or the PR body says `Fixes #30`.
