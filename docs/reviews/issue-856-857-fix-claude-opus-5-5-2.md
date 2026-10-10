# Fix review, round 2: issues #857 and #856 (`scripts/agent/d`), claude-opus-5-5

- **Branch**: `fix/856-857-devshell-wrapper`, three commits on `origin/main` (`ec274537`)
  - `6f077d3c` fix(agents): source the whole dev-shell environment when scripts/agent/d runs under bash 3.2 (Fixes #857)
  - `62acddd2` fix(agents): stop scripts/agent/d leaving a nix-shell directory in TMPDIR on every call (Fixes #856)
  - `c107ff17` fix(agents): address review of #856 and #857 (new in this round)
- **Producing model**: claude-opus-5-5
- **Date**: 2026-10-08
- **Previous round**: changes-requested (one Major per issue: the sibling repos' `d` was not reported; two Minors).
- **Verdict**: **pass** for both issues. All three findings from round 1 are resolved. Two cosmetic Minors remain,
  and the integrator can handle both when it opens the PR.

| Issue | Blockers | Majors | Minors | Model-attributed | DoD |
|---|---|---|---|---|---|
| #857 | 0 | 0 | 2 | 2 | 12/12 |
| #856 | 0 | 0 | 2 | 2 | 12/12 |

## Round-1 findings: status

| Round-1 finding | Status | Evidence |
|---|---|---|
| Major 1: the sibling repos' `d` was neither fixed nor reported | **resolved (reported)** | The `c107ff17` body says that funcd-typescript and funcd-python ship the same `d` with both defects and without the #853 guard, and that they need a port of the current `d` through the shim-repo flow. No issue exists in either sibling repo, and #856/#857 have no comment (checked with `gh`). The report lives only in the commit body (see Minor 2). |
| Minor 1: `rmdir` could remove a directory that the env script did not create | **fixed** | `d` now saves `NIX_BUILD_TOP` with the `TMPDIR` family and unsets it before the `source`, so the `case`/`rmdir` sees only a value that the script set. The new subtest in `TestIssue856_…` FAILs on the round-1 head (`62acddd2`): `unable to find file …/nix-shell.callers`. |
| Minor 2: the #857 test caught a re-exec loop only by hanging | **fixed** | `exec.CommandContext` with a 1-minute deadline, `WaitDelay`, and `require.NoError(ctx.Err())`. Mutant M3 now fails in 60.1 s with "d under /bin/bash must not re-run itself forever", and no looping bash is left behind (`pgrep` is empty). |

## Verification run

Every command ran through `scripts/agent/d` from a plain macOS shell. The pre-fix and mutant runs used a scratch
detached worktree of the branch, with `scripts/agent/d` replaced by a `git show` copy. No stash was used.

| Check | Result |
|---|---|
| `go test -race -count=1 ./scripts/` (darwin, branch) | ok (26.0 s) |
| `d` from `origin/main` | `TestIssue857_…` FAIL (`expected "yes"`, `actual ""`), `TestIssue856_…` FAIL (three `nix-shell.*` directories left). Both fail for the issue's reason. |
| `d` from `6f077d3c` (#857 only) | `TestIssue857_…` PASS, `TestIssue856_…` FAIL (the same leak). The commits still split cleanly. |
| `d` from `62acddd2` (round-1 head) | `TestIssue856_…` FAILs on the new caller-directory subtest. The new test catches the round-1 gap. |
| Linux: `golang:1.26.4`, `--user 1000:1000`, bash 5.2, the branch fed in with `git archive` | the four `d` tests PASS under `-race`. `TestLaneRegistryListsItsVenomLanes` FAILs because the image has no dev-shell python3. It fails the same way on `origin/main` (round 1), so this is env and not attributed. |
| `go vet ./scripts/`, darwin and `GOOS=linux` | ok |
| golangci-lint `./scripts/` (the binary from `go tool -n`, as `gate.sh` runs it), darwin and `GOOS=linux` | 0 issues on both |
| Repo-wide gate | not run on this branch yet (`.cache/gate` is empty). It runs once per PR. |

### Mutants (new lines in this round, plus the loop check)

| Mutant | Result |
|---|---|
| M3: drop the "found bash is ≥ 4" check (infinite re-exec) | killed in 60.1 s (`TestIssue857_…`, ctx deadline). No orphan. |
| M7: drop `unset NIX_BUILD_TOP` | killed (`TestIssue856_…`: the caller's directory is removed) |
| M8: leave `NIX_BUILD_TOP` out of the `declare -p` save | killed (`TestIssue856_…`: `expected …/nix-shell.callers`, `actual ""`) |

Mutants M1, M2, M4, M5 and M6 from round 1 target lines that this round did not change. The round-1 kills still
apply.

### The user-visible behaviour, with the real cached `nix print-dev-env` script (`NIX_BUILD_TOP` mktemp on line 2057)

- **#856 leak**: the branch's `d` ran 12 times, under `/bin/bash` 3.2 and the dev shell's bash 5.3, with `TMPDIR` set
  to the host value, to a scratch directory, and unset. It left **0** new `nix-shell.*` directories in all three
  places. The bash 3.2 lookup subshell stops before line 2057, so it creates nothing either.
- **Caller's `NIX_BUILD_TOP`**: with `NIX_BUILD_TOP` set to an empty `nix-shell.callers` under the caller's
  `TMPDIR`, both bashes hand the child the exact caller values of `NIX_BUILD_TOP` and `TMPDIR`, and the directory
  survives. An unset `NIX_BUILD_TOP` stays unset.
- **#857**: under `/bin/bash` 3.2 the child gets the whole environment (round 1 traced the re-exec; it still passes
  `TestIssue857_…`).

### No regression for any caller of `d`

The callers are `scripts/agent/gate.sh` (`d="$root/scripts/agent/d"`, its steps, and the `linux()` lint),
`scripts/agent/lanes.sh` (`scripts/agent/d "${recipe[@]}"` in a fresh worktree), `.claude/workflows/shim-fix.js`
and `fix-batch.js`, `scripts/lanes_test.go`, and the skills, `CLAUDE.md` and `.github/copilot-instructions.md`
docs. `justfile`, `lefthook.yml` and `.github/workflows` do not call `d`. This round changed only the save/unset of
`NIX_BUILD_TOP`, so the round-1 caller analysis still holds. The checks below were rerun on the new head.

- **Exit codes and signals**: `sh -c 'exit 7'` returns 7. A child that sends itself SIGTERM returns 143. After the
  re-exec and the final `exec`, the pid of `d` runs `sleep`, so a SIGTERM sent to `d` reaches the command (143).
- **No re-exec loop**: the version check guards the re-exec, and M3 shows that the test now fails fast without it.
- **PATH**: `command -v go` resolves to the store `go-1.26.3`.
- **#853 nested-call guard**: in an outer and a nested `d` call, the hash of `TMPDIR` and `PATH` is identical, and
  `FUNCD_DEVSHELL_KEY` is set. `TestIssue853_…` passes on darwin and Linux.
- **What gets removed**: only `rmdir`, so only an empty directory. With this round's fix, `rmdir` runs only on a
  `NIX_BUILD_TOP` that the sourced script set (it is unset just before), and only when that value is directly under
  the caller's `TMPDIR`. The caller's own directory is now proven to survive (test and real-script probe).

## Findings

### Blockers

None.

### Majors

None.

### Minors

**Minor 1: #857 and #856: the rework commit mixes both issues and carries no `Fixes` line.** (model)

- **Evidence**: `c107ff17` changes `d` for #856 (the `NIX_BUILD_TOP` save/unset) and `devshell_test.go` for both
  issues (the #857 deadline and the #856 subtest). It has no `Fixes #N`.
- **Rule**: `/fix` Step 6 asks for one commit per issue.
- **Impact**: cosmetic. Both issues ship in one PR, and `6f077d3c`/`62acddd2` carry the `Fixes` lines. But an
  integrator that cherry-picks per issue cannot take one issue without the other's rework.
- **Fix (optional)**: fold the hunks into their issue's commit (`fixup!` and an autosquash rebase), or leave them as
  they are and let the PR keep all three commits.

**Minor 2: #857 and #856: the sibling-repo report lives only in a commit body.** (model)

- **Evidence**: round 1 asked for the report in sibling issues, or on #856/#857 and in the PR body. The branch has
  it in the `c107ff17` body only. No issue or comment exists yet, and no PR exists yet.
- **Impact**: low. The report is now on record, which clears round 1's Major. A squash merge whose message comes
  from the PR body would drop it, and nothing yet routes it to the shim-repo flow.
- **Fix**: when the PR is opened, carry the sibling note into its body, or comment it on #856/#857.

## ✅ Verified correct: keep

- **Minimal rework**: the round-1 Minor 1 fix is two lines in `d` (one name added to the `declare -p` save and one
  `unset`). It reuses the existing save/restore instead of adding a before/after comparison.
- **Fail-fast test**: the deadline uses the standard `CommandContext` and `WaitDelay`. Because the re-exec keeps the
  pid, killing that one process ends a loop.
- **Caller-directory subtest**: it reuses `devshellCheckout` with a no-op environment script, which models the
  exact round-1 gap. It checks both that the directory survives and that the value comes back.
- **Everything verified in round 1**: cause, not symptom, for both issues, the bash lookup without a hard-coded path,
  `declare -p` and `eval` restoring exact set/unset state, and the scope of two files. Nothing regressed.
- **Shape**: `fix(agents):` subjects, the attribution trailer, project identity on all three commits, and no
  dev-machine reference in the diff or the messages.
- **ADRs**: no ADR governs `scripts/agent/d`, and no ADR or doc was edited.

## Checklist (Definition of Done), each issue

1 test reproduces ✅ · 2 fails pre-fix for the reason ✅ · 3 passes with `-race` ✅ · 4 mutants killed ✅ (M3 now
fails fast) · 5 root cause ✅ · 6 scope ✅ · 7 ADRs and docs ✅ · 8 build/vet/lint/tests, host and Linux ✅ ·
9 conventions ✅ · 10 reuse ✅ · 11 shape ✅ (Minor 1 is cosmetic) · 12 siblings reported ✅ (Minor 2). **12/12.**

## Recommendation

Pass. Hand back to `/fix` Step 8. When the PR is opened, put the sibling-repo note (Minor 2) in its body, and
optionally fold the rework into the per-issue commits (Minor 1). The repo-wide gate has not run on this branch and
runs once per PR.
