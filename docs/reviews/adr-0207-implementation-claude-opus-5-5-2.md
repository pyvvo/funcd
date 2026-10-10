# ADR-0207 implementation review, round 2 (claude-opus-5-5)

- Branch: `feat/adr-0207-pre-upgrade-snapshot-and-safe-mode`, five commits on `origin/main` (`bb936da2`): the four
  ADR-0207 commits from round 1 plus `e2b509fc fix(funcd): keep the --print hint on install and uninstall`.
- Previous report: `review-0207-loop1.md` (pass, one model-attributed Minor, one adr-attributed Minor).
- Verdict: **pass**.

## Round-1 model finding: resolved

The finding was that `requireLinuxRoot`, now shared with `funcd upgrade --unit`, dropped the `--print` hint from the
`funcd install` and `funcd uninstall` errors.

- `requireLinuxRoot(op string, printHint bool)` (`cmd/funcd/install.go`) appends "; use --print to preview the unit"
  off Linux and " (or use --print)" without root when `printHint` is true. These are the hint texts the function had
  on `origin/main`. `runInstall` and `runUninstall` pass `true`, and `runUpgrade` passes `false`
  (`cmd/funcd/upgrade.go`). There is still one helper and no duplicated gate (`grep requireLinuxRoot`: one definition
  and three call sites).
- The upgrade message is unchanged apart from the parameter: "funcd upgrade --unit manages a systemd unit — Linux
  only (got …)" or "… needs root — re-run with sudo", with no `--print`, which the upgrade command does not have.
- `TestUnitGateHint` (`cmd/funcd/install_test.go`) runs `install`, `uninstall` and `upgrade` through `newRootCmd`. It
  asserts that each command reaches the gate and that `--print` appears exactly for install and uninstall. It skips
  only as root on Linux, where the gate passes. It passes on this host.
- Mutants applied with `go test -overlay` (the work was not edited) were both killed:
  1. install and uninstall pass `false`: the test fails with "[install]: names --print = false, want true".
  2. upgrade passes `true`: the test fails with "[upgrade …]: names --print = true, want false".
- The rest of the gate wording ("<op> manages a systemd unit — Linux only (got …)") comes from the round-1 commits and
  was accepted there. Nothing outside the test matches on the old wording.

## Regression checks (run in the worktree through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go test -race -count=1` on cmd/funcd, internal/upgrade, internal/safemode, internal/platform/{version,config,hold}, internal/backup/..., internal/restore | all `ok` (12 packages) |
| `go build ./...` darwin and `GOOS=linux` | ok |
| `go vet` on the touched packages (incl. pkg/funcd, internal/platform/...) darwin and `GOOS=linux` | ok |
| golangci-lint on the touched packages, darwin and `GOOS=linux` | 0 issues on both |
| `gofmt -l cmd internal pkg` | empty |
| `just check-hygiene` | clean |
| `git diff origin/main...HEAD -- docs/feat docs/reviews` | empty |
| Leak grep of the branch diff (absolute home-directory prefixes, the local username, a personal email) | no hit |

The e2e suite, a repo-wide `go test` and the Lima lanes were not run, as instructed. The PR gate runs them.

## Findings

- Blocker: none.
- Major: none.
- Minor (adr-attributed, carried from round 1): Q13. `funcd upgrade` refuses a platform under a hold. The DR session
  made this decision and it belongs in DR's addendum ADR. It is not a defect of the model's work.

The new commit introduced no model-attributed finding.

## Definition of Done

9 of 9 still hold, as in round 1. The new commit touches only `cmd/funcd` (install.go, upgrade.go, install_test.go),
and its tests, vet, lint and fmt are green.

## Recommendation

Pass. Stamp ADR-0207 `Implemented` through the orchestrator's ledger and stamp step. Q13 goes to DR's addendum ADR.

## Ledger row

```json
{"date": "2026-10-11", "adr": "0207", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 1, "model_attributed": 0, "dod_passed": 9, "dod_total": 9, "report": "docs/reviews/adr-0207-implementation-claude-opus-5-5-2.md", "notes": "round 2: e2b509fc restores the --print hint for install/uninstall via requireLinuxRoot(op, printHint), upgrade --unit unchanged, one helper; TestUnitGateHint pins it, both overlay mutants killed; adr: Q13 upgrade refuses a held platform (DR decision); race ok on 12 packages, build/vet/lint 0 darwin+linux, gofmt+hygiene clean, docs/feat+docs/reviews untouched"}
```
