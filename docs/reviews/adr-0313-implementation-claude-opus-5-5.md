# ADR-0313 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 4 minors (ADR-0313 implementation, model: claude-opus-5-5)

Reviewed `87ffbae4` on main `5b27e74a`: the check in `scripts/behavior`, the 14 chapters and the index, the named numbers, the skill, the
hook, CI's light path and the step each gate gains. The ADR was accepted in the same commit that closes the work, so it has no later
edit to compare. The four Minors need no rework before the merge; one small commit would close them.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

All four are placed here and attributed to `model`. I checked each by running it; none changes a value or a row.

1. **`Defaults()` took the doc comment of `defaults()`.** `go doc ./internal/platform/config Defaults` prints "defaults returns the
   Config with every built-in default set ... Defaults is every key's built-in default", and `defaults` has no comment
   (`internal/platform/config/config.go:478-484`). Smallest change: put `Defaults` and its one line above the older comment.
2. **The fallback beside a key is not compared in two shapes** (`scripts/behavior/main.go:323`, `:428`). Scenario `inline-number` says a
   number beside its key passes while it equals the default and fails when it differs.
   - `parse("invoke.activationTimeout", 1.5*time.Second)` passes with a default of 30s: `eval` gives no value for a fraction, so nothing
     is compared.
   - `parse("functions.defaultResources.memory", 512<<20)` is refused though it equals the default: `falls back to 512MiB ... whose
     default is 512Mi`.
   - No site in the tree has either shape. Smallest change: an `eval` that fails is refused as an inline number, and `differs` reads
     `512Mi` as `sameValue` does.
3. **A CamelCase word marked new is still refused** (`scripts/behavior/main.go:1005`). The Contracts' *A cited word* excepts what is
   "marked new". A row citing `ReviewOnlyWord` with that word on the note's `New words:` line fails with `no status reason, value or name
   in the code called ReviewOnlyWord`; a one-word name and a machine's state pass. Smallest change: add `&& !s.marked[word]`.
4. **Two generated values are sentence fragments**, because the example's comment wraps or refers back: `secrets.encryptionKeyFile` reads
   `"" ⇒ Secret values` (the next line says "are NOT encrypted") and `lake.scratchSize` reads `the larger of 1GiB and that`. The check
   does as the Contracts say; the words come from `examples/funcdconfig.yaml:209` and `:245`. Smallest change: put each default on its
   key's line in words that stand alone, then `just behavior-values`.

### Routed (not this work's)

- **"A docs-only PR with a broken row fails CI's light path, run once."** Home: the integration step. It needs a PR, and nothing is pushed
  yet, so no CI run exists. What I could run: in a fresh worktree, `just check-hygiene check-specs check-behavior` passes, and exits 1 at
  `check-behavior` with a broken row. The filter gives such a PR `nondocs` false and `design` true, the `ci` job's light step then runs
  that recipe, and the Go cache step no longer waits on `nondocs`. One throwaway docs-only PR closes this.
- **A number in a package-level composite or call.** Home: the ADR's Contracts, so the decider. `var c = http.Client{Timeout: 30 *
  time.Second}` is neither *a value* (not plain) nor *an inline number* (not in a function body), so the check passes and no block shows
  it; `Consequences` does not list this among the numbers that stay unseen. A scan finds no such site today: the nine package-level
  literals it finds are conversions, which the blocks show. Nothing stays here.
- **The 20 `uncovered` and 11 `differs` rows.** Home: the decider and the bug rule, as the plan's step 4 says. The four I sampled read as
  their cited clauses say (`funcdctl-dev.md:51`, `eventing.md:60`, `apps.md:40`, `platform-lifecycle.md:37`).

### ✅ Verified correct (keep it)

- **Commands, each exit 0.** `scripts/agent/d go run ./scripts/behavior -check` (0.42 s), `go test -count=1 ./scripts/behavior`,
  `go tool golangci-lint run ./scripts/behavior/...` (0 issues), `GOOS=linux go build` of the three Linux-only packages with named
  numbers, `go test -count=1` of `internal/platform/config`, `internal/platform/units`, `internal/contract`, `internal/backup/plan` and
  `internal/funclog`, and `go test -count=1 -tags dev -run 'TestDevBurst|TestDevRestartDelay' ./cmd/funcdctl`. By this gate's rule I ran
  no e2e suite and no `go test ./...`: `scripts/agent/gate.sh` and CI run them on the PR's tip.
- **Linux gives the same result.** The check built for linux/arm64 and run in the `funcd-linux-tests` VM through `scripts/linux-exec.sh`
  exits 0, so its blocks equal those written on macOS byte for byte.
- **No value changed.** Commit `72c05e69` touches 42 files and no test. A script substituted each new constant into its site and compared
  100 sites with the removed literal. I read the rest by hand, along with the whole Go diff: the `int64` format's two bounds and
  `LifecycleRule`'s `day`. The six renames of `2ad0510d` keep their values (`maxDevWatchLastLines` 20, `budgetMultipartFactor` 3,
  `exportConcurrency` 1, `cdcSeqBatch` 100, `maxRestoringWrites` 3, `maxEngineStarts` 3), the one test edit is the rename, and no old name
  is left in code. Only frozen ADRs and old reports still name them.
- **Each Scenario has its test, and each fails with its check removed.** Eight `TestScenario*` tests. Through `go test -overlay`, 25
  mutants of `main.go`, for the seven Scenarios the check answers and for `TestChapterShape`, `TestValues` and `TestSkippedFiles`: all 25
  fail, none survives. The eighth reads the real chapters: removing the row from `functions.md` in a scratch tree fails
  `TestScenarioSurpriseLookedUp`.
- **The hook refuses.** In a scratch worktree, with bare `git commit`:
  - `30*time.Second` and `1<<20` in a function: refused, naming the file, line and function, in 3.7 s the first time, and again with the
    flake's lefthook 2.1.5;
  - the numbers named but the block not regenerated: refused with `now:` rows and the skill's name;
  - after `just behavior-values`: accepted;
  - a docs-only commit with a broken row: refused with `docs/behavior/health.md:27: ADR-9999 does not exist`;
  - a README-only commit: the check does not run.
- **The Scenarios on the real tree.**
  - A changed default of `invoke.activationTimeout` prints the block's `now:` and `was:`, the fallback that now differs and each row that
    cites 30 s; regenerating changes only `calls.md`'s block.
  - A moved line and a new function pass. A new package fails the check and the regeneration.
  - `20*time.Second` beside `sql.callTimeout` is refused.
  - Two real branches that each add a value to one block conflict inside it at merge; `go run ./scripts/behavior` clears it and the check
    passes.
  - `grep "did not become ready"`, the index's own example, hits three rows that name `invoke.activationTimeout`, each with its status and
    source, and a fourth that its 1m0s tells apart.
- **Rows.** 319 rows, whose statuses add up as the ADR says: 198 `decided`, 71 `accepted`, 19 `deferred`, 20 `uncovered`, 11 `differs`; 538
  generated rows.
  - 40 rows sampled with a fixed seed, each cited clause read: 0 wrong, 35 hold, and 5 are imprecise in the ADR's sense, a citation that
    does not pin every fact (`funcdctl-dev.md:54`, `admission.md:53`, `functions.md:56`, `platform-lifecycle.md:52`, `services.md:50`).
  - 18 of the 40 traced into the code as well, among them the single controller worker, `TimeoutStopSec=45` with the 37 s sum, the 400 on a
    slow body, `kvstore.engine`'s default and the pool's 64 MiB heap in the pinned shim.
  - All 421 quoted strings of the *You see* cells searched in the code: 365 match, and the other 56 are format strings, concatenations,
    fields, condition shorthand, or a library's or the kernel's words.
- **The Contracts.** The value shapes, the name test, the inline shapes and the output format match the code, apart from Minors 2 and 3.
  The check parses every file whatever its tags, `defaults()` holds no host-derived value, and the blocks hold no line numbers and no totals.
- **The machine's home.** `blueprint.md` and `docs/PROJECT-SUMMARY.md` link to the chapter and hold no copy. Against the removed copy, each
  added edge has a row (`functions.md:45` to `:47`) and so does the dropped `Terminating` (`:73`, `:74`). `Failed --> Ready` is folded into
  `Failed --> Awake`, which the note says.
- **The process.** The seven skills carry Decision 10's step, the adr skill's template and partial-supersession list carry the section, and
  the bug rule starts at `docs/behavior/` in CLAUDE.md, `fix`, `fix-review`, `fix-batch.js` and `shim-fix.js`. `.claude/CLAUDE.md` and
  `.github/copilot-instructions.md` are byte-identical. The back-links in ADR-0000 and ADR-0272 are there. The bug rule's table, strict rule
  5, the conventions and *Before you finish* follow.
- **Tree and tracking.** Every changed file is in the plan or follows from it, with no new dependency. F227 and the ADR stood at `reviewing`. A process row has
  no feature issue, so the PR closes none; process rows such as F185 and F190 are not in `v1-plan.json`, so the roadmap owes nothing.

### Definition of Done

14 of 15 hold: six of the seven lines of the ADR's Review checklist (the seventh is the silent check the skill keeps out of a report) and
nine applicable generic items (no port has drivers here, so the contract suite item does not apply). The full suite here is the touched
packages; the gate runs on the PR's tip. The miss is the second half of the fourth line, routed above: CI's light path needs a PR to run once.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0313 (implementation): pass, 0 blockers, 0 majors, 4 minors, 4 model-attributed, DoD 14/15. See
docs/reviews/model-scorecard.md.

### Recommendation

Merge after `scripts/agent/gate.sh` and CI pass on the PR's tip. One small commit closes the four Minors, and one throwaway docs-only PR with
a broken row closes the routed line. The ADR and F227 are stamped `Implemented`.
