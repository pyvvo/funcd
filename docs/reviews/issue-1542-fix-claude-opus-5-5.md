## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1542 fix, model: claude-opus-5-5)

Branch `fix/1542-kill-case-waits-for-a-quiet-snapshot`, commit `5a6d0404`, `test(e2e): the rauthy-restore kill case waits
for a quiet rauthy snapshot`, on `origin/main` `3ed1dceb`. It changes `e2e/addon-rauthy-restore.venom.yml` only (+14). A
new first step of the kill case waits, for at most 60 tries one second apart, until rauthy's snapshot folder holds a
file whose name does not end in `~` and whose time is at least 3 whole seconds old. Its assertion fails when no such
file appears.

This review ran in a worktree detached at `5a6d0404`. `origin/main` had moved to `2b5008d4` (#1532, #1539, #1540,
#1541); none of these commits touches the suite, its fixtures or the backup of an add-on object. So the lane runs used
two probe refs on `2b5008d4`, built through a temporary index. Both probes add the same two steps to the kill case:

- a first step that lists rauthy's snapshot folder and then empties it, which is the state the issue's diagnostic found;
- a read-only step after funcd's restart that prints the status of the export Backup `login-killed-before`.

The two probe suites differ only by the fix's step.

### ✅ Verified correct (keep it)

- **The lane case fails without the fix, for the issue's reason.** Probe A (main and the emptying, no fix):
  `scripts/agent/lanes.sh exp/review-1542-base-empty:addon-rauthy-restore` gave `FAIL (395s)`.
  - The kill step printed `KILLED while ` and failed its `KILLED while Loading` assertion.
  - The export Backup ended `Failed` with reason `NoSnapshot` and the issue's message (`its engine has written no
    snapshot under /app/data/state_machine/backups yet: … holds no file unchanged for 2s`).
  - The Restore `login-killed` ended `Failed` with `ExportFailed`, 4 ms after its creation.
  - The later steps failed as the issue lists: no `Interrupted`, no hold, and `hold release` refused
    (`namespace "default" is not held`).

  Before the emptying, the folder held a snapshot about 91 s old and one 1.2 s old. So this run would have found a
  quiet file without the probe: the emptying made it fail, not the timing.
- **It passes with the fix.** Probe B (main, the fix and the same emptying):
  `scripts/agent/lanes.sh exp/review-1542-fix-empty:addon-rauthy-restore` gave `PASS (525s)`; that time includes the
  wait for probe A's lane lock.
  - The fix's step waited about 15 s: hiqlite wrote its next snapshot 11 s after the emptying, and the step printed
    `QUIET=backup_node_1_1795449315.sqlite` once that file was 3 whole seconds old.
  - The export Backup `login-killed-before` then ended `Succeeded`, and the kill step printed `KILLED while Loading`.
  - Every later step passed: the Restore ended `Failed` with `Interrupted`, the copy and `restore.env` were gone with
    the engine Ready, `hold status` listed `restore: login-killed`, and the release succeeded.
- **Cause, not symptom.** The suite applied the kill case's Restore at once and assumed that a quiet snapshot existed.
  The Restore's export takes the newest regular file of `snapshot.path` whose name does not end in `~` and that has not
  changed for 2 s, and it fails `NoSnapshot` when there is none (`internal/provider/files.go:40-73`,
  `internal/backup/workload/addons.go:19,75-79`). The new step waits for that same condition, on the folder itself. No
  timeout was raised, no retry was added to the kill step, and no assertion was loosened. funcd never clears
  `snapshot.path`: its `Clear` calls take `restore.path` only (`internal/backup/workload/addons.go:153`,
  `internal/services/addon/restore.go:102`). So the missing file is the engine's doing: hiqlite copies the file it
  restores into `state_machine/backups` (ADR-0333 Decision 1 notes this copy) and then renames it into its database,
  as the issue says. In both probes, the snapshot that Backup `login-b1` took was gone from the folder after the
  previous case.
- **The wait matches the rule it waits for.** `ls -tr | grep -v "~$" | head -1` takes the oldest file whose name does
  not end in `~`, so the step passes as soon as any such file is old enough, and `Newest` then finds one. The
  whole-second test (`date +%s` minus `stat -c %Y`, at least 3) holds only at a real age above 2 s. The provider's 2 s
  is therefore met at the check already, whatever the next step's latency: in probe A, the Restore was created 57 ms
  after the probe step printed its time. Sixty tries cover four of the lane's 15 s snapshot ticks.
- **Decided behavior, not a product defect.** The issue's two reasons are rows marked `decided`, and both cited clauses
  read as the rows say:
  - "A Backup copies an add-on object" (`docs/behavior/disaster-recovery.md:44`): ADR-0333 Decision 2, where no quiet
    file fails the Backup `NoSnapshot` (scenario `addon-backup-no-snapshot`);
  - "An in-place Restore's export" (`docs/behavior/disaster-recovery.md:50`): ADR-0222 Decision 7, where a failed
    export is `ExportFailed`, with no write.

  The fix changes the suite, not that behavior, and it edits no ADR.
- **Scope.** One hunk in one suite. Every other step and assertion of the case is unchanged, so the case still proves
  `Loading`, `Interrupted`, the kept hold and the engine's start without `restore.env`.
- **Siblings.** This suite is the only one that backs up an add-on object (`scope.addons` appears only in
  `e2e/fixtures/addon-rauthy-restore-backup.yaml`), and the kill case is its only step that runs a Backup right after a
  Restore. The rewind case's export comes after Backup `login-b1`, which waits for a snapshot of its own. The Go
  scenario tests (`pkg/funcd/addon_backup_internal_test.go`) run one Restore per rig, on a fake engine.
- **Reuse and conventions.** The step follows the suite's own snapshot wait (`e2e/addon-rauthy-restore.venom.yml:69-82`:
  `ls`, the `~` filter, `stat -c %Y`, a loop in the VM). It uses the suite's `vmsh` and `store` variables, and its
  `info` line has the suite's form. The YAML is block style, and the two-line comment says why, citing ADR-0333
  Decision 2.
- **Checks.** The lane guards, `scripts/agent/d go test -count=1 -run 'Lane' ./cmd/funcd/ ./internal/testkit/langmod/
  ./scripts/`, pass (3 packages ok, 15 tests). `TestVenomAssertionsAreStrings` passes, `just check-hygiene` is clean,
  and `git diff --check` is clean. No Go file changed, so build, vet and lint are not affected. The repo-wide gate runs
  once on the PR.
- **Shape.** The subject `test(e2e):` matches the repo's earlier fixes of flaky tests that changed tests only (#1532
  for #1531, #1540 for #1535). The commit carries `Fixes #1542` and the attribution trailer, in one commit.

### Definition of Done
12 / 12 items hold. Item 1: the regression test is the lane case itself, run with the snapshot folder emptied before
it, as the commit message says; no Go test reaches the suite's order of steps against a real hiqlite. Item 4: the revert
is probe A. Item 13 does not apply: the fix changes no modeled behavior, and ADR-0333's design model
(`docs/design/addon/restore-start.fizz`) leaves hiqlite's restore to this lane case.

### Model scorecard
Recorded: claude-opus-5-5 on #1542 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
`docs/reviews/model-scorecard.md`.

### Recommendation
Ready for the gate and the PR. `origin/main` has moved to `2b5008d4`, and its ledger PR (#1541) added rows, so this
review's ledger row goes in as main's rows plus this one, then a render.
