## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0223 implementation, re-review round 2, model: claude-opus-5-5)

Reviewed commit `26cdd40b` (`feat: implement ADR-0223 — funcdctl backup plan (FEAT-0009/F112)`) on
`feat/adr-0223-funcdctl-backup-plan`, rebased onto `origin/main` `3eccee2f` (ADR-0222). Round 1 reviewed `42c571d6`
on `9fd461d9`. `9fd461d9` and `3eccee2f` have the same tree (`2e417947`), so the rebase changed no base file. The
round-2 delta (`git diff 42c571d6 26cdd40b`) is two files: `cmd/funcdctl/backup_plan_test.go` (+24) and
`internal/backup/plan/edit.go` (the `Apply` doc comment rewrapped, no code change). The ADR diff is still the
`Accepted → Reviewing` status line alone. Mutants and probes ran through `go test -overlay` with copies outside the
worktree; nothing in the worktree was edited, and `git status` is clean. The repo-wide gate runs separately.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **TestFlagRules' doc comment now heads the new test** · attribution: `model` ·
  `cmd/funcdctl/backup_plan_test.go:310-312`. The new `TestPlanReadsTheFileAndPreflights` was inserted between
  `TestFlagRules`' doc comment and its `func`. The new test's comment therefore starts with
  "TestFlagRules (ADR-0223 Decisions 1, 4): each refusal names its flag, exits 1 and prints nothing.", which does not
  describe it, and `TestFlagRules` (`:331`) has no doc comment and no ADR reference. vet and golangci-lint do not
  flag it.
  Fix: move line 310 down to sit directly above `func TestFlagRules`, and give the new test its own first line
  (`// TestPlanReadsTheFileAndPreflights (ADR-0223 Decision 3, Review checklist 3): …`).

### Round-1 findings — resolved

1. **Command-level wirings untested** (round-1 Minor 1): resolved. `TestPlanReadsTheFileAndPreflights` kills both
   mutants:
   - `curZero`: `plan.Derive(c, o, config.Config{})` at `cmd/funcdctl/backup.go:216`. The test fails
     (`--- FAIL: TestPlanReadsTheFileAndPreflights`, exit 1) on the assertion that `blob.backup.interval: 2h` in the
     file gives `retention: 722h`.
   - `bothPreflight`: both `preflight(docs)` calls (`:256`, `:279`) replaced by `error(nil)`. The test fails on the
     two-identical-recipients manifest. The shipped code refuses that manifest through
     `api/types/v1alpha1/backup.go:219` (`spec.encryption.recipients[%d] repeats %q`). The test asserts exit 1, the
     `manifest is invalid (BackupSchedule "orders-nightly")` message, an empty stdout and the file unchanged under
     `--write`.
2. **Insert before a trailing document marker untested** (round-1 Minor 2): resolved. The two new
   `TestApplyKeepsBytes` rows kill these mutants:
   - `docEndLen`: `documentEnd` returns `len(src)` (`internal/backup/plan/edit.go:289`). The `...` row fails.
   - `stepKeyBranch`: `stepUnder` returns `defaultStep` in its `keyIndent >= 0` branch (`:215`). The 4-space row fails.
   - `stepRootBranch`: `stepUnder` returns `defaultStep` before its root scan (`:221`). This one fails too.
3. **Round-1 unscored note**: the `Apply` doc comment (`edit.go:19-24`) now wraps within the file's width.

### Unscored notes

- **One round-1 classification is corrected.** Round 1 counted "either single preflight" as an equivalent survivor.
  Both single-call mutants still pass every test (`firstPreflight`, `secondPreflight`: exit 0). The second is
  equivalent: `Derive`'s schedule and ttl always pass `Validate`. The first is not equivalent. A probe used a
  manifest whose only fault is `schedule: nope`:
  - The shipped code exits 1 with `funcdctl apply: manifest is invalid (BackupSchedule "orders-nightly"):
    BackupSchedule.Validate: spec.schedule "nope" is not a valid schedule: …`.
  - Without the pre-edit call, plan replaces the schedule and prints a valid manifest (err nil).

  This is not a finding. Decision 3 and Review checklist item 3 ask only that the schedule pass through `preflight`,
  and the post-edit call does that. Checking the input as well is the implementer's choice, and it fails closed.
  A test for that choice would be a manifest with an invalid `spec.schedule` or `spec.ttl`.
- **Still open from round 1, cosmetic**: `lastLine`'s comment skip (`edit.go:277`) still survives a mutant
  (`lastLineComment`, exit 0). With the mutant, an indented comment after a mapping's last child goes above the
  inserted key instead of below it. The YAML parses the same either way. `planSchedule`'s `n != 1` count repeats
  `Apply`'s own refusal, but its message, which names `-f`, is clearer. CRLF files get LF-terminated inserted lines.

### ✅ Verified correct (keep it)

- **Tests**: `scripts/agent/d go test -race -count=1` on `internal/platform/config`, `internal/platform/units`,
  `internal/backup/plan`, `internal/provider` and `cmd/funcdctl`: all 5 `ok`, exit 0.
- **Scenarios**: all 7 `TestScenario*` of ADR-0223 pass under `-race -v`, and so do `TestPlanReadsTheFileAndPreflights`,
  `TestFlagRules` and `TestApplyKeepsBytes`. No scenario assertion was weakened: the only test-file change is the 24
  added lines.
- **Build, vet, lint, format**:
  - `go build ./...`: exit 0 on darwin and on `GOOS=linux`.
  - `go vet` over the 5 packages: exit 0 on darwin and on `GOOS=linux`.
  - golangci-lint over the 5 packages (host-built binary from `go tool -n golangci-lint`): `0 issues.`, exit 0 on
    darwin and on `GOOS=linux`.
  - `gofmt -l` on the changed Go files prints nothing. `go mod tidy -diff` exits 0, and `go.mod`/`go.sum` are
    unchanged against `3eccee2f`.
- **Mutants run this round: 8, killed 5**:
  - Killed: `curZero`, `bothPreflight`, `docEndLen`, `stepKeyBranch`, `stepRootBranch`.
  - Survived: `secondPreflight` (equivalent), `firstPreflight` (an implementer's choice the ADR does not require; see
    the notes), `lastLineComment` (cosmetic).
- **Round 1's verified items still hold.** The only code change since round 1 is a comment, and the rebase base has
  the same tree. Round 1 verified the Contracts, the Decisions, the Apply edge probes, the `parseMemoryBytes` move,
  the tree against the Repository surface, and the ADR edit (the status line alone).

### Definition of Done

16 / 16 hold (ADR Review checklist 5, ADR Definition of done 1, generic implement-gate DoD 10). `just ci` itself runs
in the separate gate; every check it runs held on the touched packages. The FEAT-0009 F112 row is left to the
disaster-recovery session, as in round 1.

### Model scorecard

To record: claude-opus-5-5 on ADR-0223 (implementation, re-review round 2) → pass, 0/0/1, 1 model-attributed,
DoD 16/16.

### Recommendation

Sign off: ADR-0223 can be stamped `Implemented`. The one Minor is a two-line comment move in the test file. It does
not block, and it can go into the same commit before the PR if the builder touches the branch again.

## Follow-up

The round-2 minor was fixed in the same commit before the PR: `TestFlagRules`' doc comment is back above `func TestFlagRules`, and `TestPlanReadsTheFileAndPreflights` keeps its own first line. No code changed.
