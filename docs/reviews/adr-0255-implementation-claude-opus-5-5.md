## Verdict: pass — 0 blockers, 0 majors, 0 minors open  (ADR-0255 implementation, model: claude-opus-5-5)

This report reviews PR #1124, branch `feat/adr-0255-dev-loop`, in two rounds:

- **Round 1** reviewed 2e7d6dcd and 13c1c9d4 on bed8b89b and requested changes: 1 Major and 4 Minors, all model.
- **Round 2** reviewed the fix a87a2048. Rebased on main e34dadad, the head is 62bf5603. `git diff a87a2048 62bf5603 --
  cmd/ internal/ pkg/` is empty, and 62bf5603 only moves ADR-0255 to Reviewing, F166 to reviewing and the
  PROJECT-SUMMARY row. Every round-1 finding is closed.

The change is `cmd/funcdctl` (`devwatch.go` and `devlivereload.go`, both new; `devconfig.go`, `dev_app.go`, `dev.go`,
`dev_folder.go`, `devwatch_test.go`), plus `internal/runtime/process/process.go`, which exports `DefaultStopGrace`.

### Round 1 findings (all closed in a87a2048)
- 🟡 **Major: live reload refused a Site build folder that is a symlink** · attribution: model · evidence: a throwaway
  overlay probe with `web -> dist`.
  - `siteCopy` failed with `copy <dir>/web: . is not a regular file or directory (symlinks are not packed)`, while
    `artifact.PushSite` on the same root succeeded.
  - Decision 5 says the copy keeps and refuses what `packDir` does, and `packDir` resolves its root with
    `filepath.EvalSymlinks` (`internal/artifact/bundle.go:92`).
  - Closed: `copySite` resolves the root first. `TestDevSiteCopy` covers a symlinked root, a dot-dir, the root index
    tag only, and a symlink inside the build (still refused). The mutant without `EvalSymlinks` fails it.
- **Minor: the early checks had no test** · attribution: model · evidence: mutant M7, which turns `s.earlyChecks` into
  a no-op, left every test passing. Closed by `TestDevWatchEarlyChecks`: with `TODO_API_TOKEN` unset, there is no
  `watch web:` line and no `watch.pid`. M7 now fails it.
- **Minor: the backoff's reset after a 30 s run had no test** · attribution: model · evidence: mutant M1 survived.
  Closed by `restartDelay(last, ran)` and `TestDevRestartDelay` (doubling, cap, reset). M1 now fails it.
- **Minor: the Site index miss had no test** · attribution: model · evidence: mutant M2 survived, because the scenario
  script copies in one `cp -R`. Closed by `TestDevSiteHoldsIndex` (empty, then `app.js` only, then the index). M2 now
  fails it.
- **Minor: `devWatchGrace` restated the process driver's grace, and the `dir` refusal test asserted only a prefix** ·
  attribution: model.
  - Closed: `devWatchGrace = process.DefaultStopGrace`. The constant is exported, and its uses and its test follow.
  - The `dir` case now asserts `/nowhere is not a directory`.

### ✅ Verified correct (keep it)
- **Opt-in.** Without `watch` and `liveReload`, `earlyChecks`, `startDevCommands` and `startLiveReload` never run.
  The `buildMiss` wrappers keep each refusal's message and kind (`Unwrap`). `TestScenarioDevLoopAbsent` proves that
  a missing build stops dev and that the Site's bytes are the build's.
- **The supervisor.**
  - Each command runs as `sh -c` in its own process group (`Setpgid`), with standard input from `/dev/null` and dev's
    environment.
  - Each is recorded in `procreg.Open(<process dir>, "commands")` under the registry lock, and a start once stop began
    is refused.
  - A command's exit kills its group (`pid > 1`).
  - `stop` and the crash reap go through `Reap`'s `PGID > 1 && Owned` guard.
- **Stop paths.** `stopsignal.Signals()` through `NotifyContext`, then `inst.stop`, then the cleanups. A refusal after
  the commands started runs the deferred `cmds.stop` (`TestScenarioDevWatchStop`, including "a failure after the
  start"), and `TestScenarioDevWatchCrashReap` passes.
- **The wait.** `waitForBuilds` judges a refusal, and a miss once every command exited 0, only on a settled poll
  (#1118). A miss is printed once, with its own text naming the image (13c1c9d4). `TestDevWaitForBuilds` covers each
  case.
- **Live reload.**
  - `withTag` uses the `x/net/html` tokenizer, as ADR-0239's copy does; the comment, script, upper-case and no-head
    cases pass.
  - The port is kept in `livereload.port`.
  - The event rule compares with the last event's Site images, so a follow that a newer revision cancelled loses no
    event, and a function-only change sends none. It has a unit test and the scenario.
  - A `--persist` restart with the same builds stamps no revision.
- **Config.** The refusals and their texts match the Contracts. Both keys are App-only through `appOnlyKeysSet`, and
  `changedDevKeys` lists them, so a change prints the restart line. `--print-env` starts no command.
- **Tests.** There is one test per scenario, named after it, and they run serially since they fork. Unit tests cover
  the tag, the event rule, the wait, the restart delay, the index miss and the Site copy.
- **Mutants.** The fixer killed three: no group kill after an exit, an unsettled refusal, no reap at start. The review
  added and killed four: M1, M2, M7, and the copy without `EvalSymlinks`.
- **Checks on a87a2048.**
  - gofmt is clean.
  - `go build ./...` and vet pass, with and without `-tags dev`.
  - `golangci-lint` reports 0 issues on `cmd/funcdctl` (with and without `-tags dev`) and on
    `internal/runtime/process`.
  - Tests:
    - `go test -race ./internal/runtime/process/` passes (6.5 s);
    - `go test -tags dev -race ./cmd/funcdctl/` passes (149 s);
    - `go test ./cmd/funcdctl/` passes (4.6 s).
  - The repo-wide gate runs on 62bf5603 before the merge.
- **The live check** is recorded on #1124: experiment 0001 at 13c1c9d4, 53 of 53 checks, with `vite build --watch`
  and `liveReload`. The code since then is the copy's root resolution and tests.
- **Tracking.** ADR-0255 is Reviewing, its substance unchanged (62bf5603 rewraps only the Status line), and F166 is
  reviewing.

### Definition of Done
- **Round 1:** 5 / 7, counting the 6 checklist items plus Done-when. Item 5, the Site copy, was the Major. The live
  check, in item 6 and Done-when, was still pending (env).
- **Round 2:** 7 / 7.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0255 (implementation):

- round 1 → changes-requested, 0/1/4, 5 model-attributed, DoD 5/7;
- round 2 → pass, 0/0/0, DoD 7/7.

### Recommendation
Pass. The ADR can be stamped `Reviewing → Implemented` and F166 moved to `implemented` once #1124's gate and CI are
green.
