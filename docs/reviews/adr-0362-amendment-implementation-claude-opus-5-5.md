## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0362 amendment of 2026-11-27 (#1610), implementation, model: claude-opus-5-5)

The work is commit `d25c4488` on `origin/main` (`fb594292`), branch `feat/app-deploy-wait-lagging`. It is reviewed
against the amended Decisions 2, 6 and 7, the Scenarios `app-view-lagging` and `app-deploy-wait-lagging`, the
Contracts and the Review checklist of ADR-0362, with ADR-0357 Decision 1 and ADR-0002 binding. The decider's choice
on #1610 is option B: a Lagging part counted apart, each part that goes on named at the exit with its `funcdctl get`,
and an opt-in `--wait-lagging`.

### Verification run

| Check | Command | Result |
|---|---|---|
| tests | `go test ./cmd/funcdctl -race -count=1` | `ok github.com/pyvvo/funcd/cmd/funcdctl 12.442s`, exit 0 |
| vet | `go vet ./cmd/funcdctl/` | exit 0 |
| lint | `go tool golangci-lint run ./cmd/funcdctl/...` | `0 issues.`, exit 0 |
| behavior rows | `just check-behavior` | exit 0 |
| command reference | `TestScenarioCommandSpecGenerated` (in the package run above) | passes: `docs/guide/develop/tools/funcdctl/spec.md` matches the generated page |

The e2e suite, `go test ./...` and `scripts/agent/gate.sh` were not run here, as instructed: the gate runs once,
elsewhere.

### Minor

- **The `app-view-lagging` test drives the view, not the command** · attribution: `model` ·
  `cmd/funcdctl/appview_test.go:320`. The Scenario reads "*when* `funcdctl app deploy` exits there", and
  `TestScenarioAppViewLagging` calls `appView.deploy`, `show` and `done` directly, with two Lagging parts where the
  Scenario names one (`· 1 lagging`). The command's own exit is covered in a pipe by `TestAppDeployExitsWhileAPartLags`
  (`cmd/funcdctl/app_template_test.go:725`), and a live `1 lagging` through the command by
  `TestScenarioAppDeployWaitLagging` (`cmd/funcdctl/app_template_test.go:691`), so every clause holds somewhere; only the
  live exit without `--wait-lagging`, through `app deploy`, has no command-level test. Optional: a live
  `startLiveCLI` case that exits at the switch and asserts the `goes on` lines.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Decision 2, the progress line** (`cmd/funcdctl/appview.go:264`): Ready and NotStarted count as ready, Lagging is
  counted apart with an amber `▰` (`st.warn`) and `· <n> lagging` only while a part lags; Pruning is still left out.
  The existing `TestAppViewLaggingAndPastTheDeadline` was updated to `2/7 ready · 1 lagging`, which is the amended
  rule, not a weakened assertion: it still checks the Lagging row, its reason and its running time.
- **Decision 5 unchanged**: `settled` still counts Lagging as settled for the times and the fold, as the checklist's
  "counts as settled for the exit" and ADR-0357 Decision 1 require; the default deploy still exits at the switch.
- **Decision 2's last draw and Decision 6's lines** (`cmd/funcdctl/appview.go:228`, `:561`): `goesOn` lists only Lagging
  children; the kind follows the ADR, an add-on's at `v1.AddonPlural`, one of funcd's in lower case, and both forms
  resolve in `sdk.KindFromToken` (`pkg/sdk/kinds.go:104`), so the printed `funcdctl get` works. Both strings pass through
  `termSafe`. The `goes on` lines are drawn only by `done` (`v.final`), never by a poll, and the test asserts that a
  poll's draw names none. In a pipe, `show` records the App so `done` prints one plain line per part, with no escape.
- **Decision 7, `--wait-lagging`** (`cmd/funcdctl/app.go:528`, `:854`, `:877`): it waits past the switch while a child
  is Lagging or Pending, exits 0 when none is, and exits 0 when a later revision replaces its own (`case switched`);
  deletion and a spec change before the stamp keep their ADR-0217 exits. It is refused with `--no-wait` and
  `--dry-run` through cobra's mutually exclusive groups, tested for both. `funcdctl dev` passes `false`
  (`cmd/funcdctl/dev_app.go`), so its follow is unchanged.
- **Contracts**: `waitDeployEvery(ctx, c, applied, n0, ready, poll, v, lagging bool)` matches the amended signature;
  `done() error` is unchanged in shape.
- **Scenarios**: `TestScenarioAppViewLagging` and `TestScenarioAppDeployWaitLagging` exist, are named after their
  Scenarios, call `t.Parallel()`, and pass; the second walks Lagging at the switch, then a Pending reader, then
  Ready, and asserts no `goes on` line at the exit. The two branches of the wait are pinned: without the Pending
  check the second stage of `TestScenarioAppDeployWaitLagging` returns early and fails, and without `case switched`
  `TestAppDeployWaitLaggingEndsWhenReplaced` never returns.
- **ADR-0357 Decision 1**: the server's view of a Lagging part is untouched; the wait is a CLI opt-in, as the ADR's
  rejected alternative G records.
- **Docs and rows**: two rows in `docs/behavior/funcdctl.md` cite the amended Decisions and the tests; the App guide,
  the command's long help and the generated command reference describe the flag; ADR-0217's `Superseded in part by`
  line names the opt-in wait. The feat row F279 stays `implemented`, as an amendment in place makes no status move.
- **ADR-0002 conventions**: no `any` in the new signatures, no `panic`, no new dependency, errors through `api/fault`,
  ctx-first.

### Definition of Done

16 / 16 items hold (the ADR's 7 Review-checklist items, as amended, and 9 applicable generic items; the per-driver
contract-suite item does not apply). The Minor above does not fail "one test per Scenario, named after it".

### Model scorecard

Not recorded here (the caller records it): claude-opus-5-5 on ADR-0362 (implementation, amendment) → pass, 0/0/1,
1 model-attributed, DoD 16/16.

### Recommendation

Ship it once the gate passes. The Minor is optional and needs no new review round.
