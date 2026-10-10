# Fix review — issues #857 and #856 (`scripts/agent/d`), claude-opus-5-5

- **Branch**: `fix/856-857-devshell-wrapper`, two commits on `origin/main` (`ec274537`)
  - `6f077d3c` fix(agents): source the whole dev-shell environment when scripts/agent/d runs under bash 3.2 (Fixes #857)
  - `62acddd2` fix(agents): stop scripts/agent/d leaving a nix-shell directory in TMPDIR on every call (Fixes #856)
- **Producing model**: claude-opus-5-5
- **Date**: 2026-10-08
- **Verdict**: **changes-requested** for both issues. The code is correct and well tested. The one Major for each
  issue is that the same `scripts/agent/d` ships in funcd-typescript and funcd-python with both defects, and nothing
  reports it. Filing that report is enough to clear the Major. The diff itself needs no change for a pass.

| Issue | Blockers | Majors | Minors | Model-attributed | DoD |
|---|---|---|---|---|---|
| #857 | 0 | 1 | 1 | 2 | 11/12 |
| #856 | 0 | 1 | 1 | 2 | 11/12 |

## Verification run

Every command ran through `scripts/agent/d` from a plain macOS shell. In that shell `/usr/bin/env bash` is the
system bash 3.2, so each call also exercised the new re-exec path.

| Check | Result |
|---|---|
| `go test -race -count=1 ./scripts/` (darwin, branch) | ok (25.6 s) |
| `TestIssue857_…` with `scripts/agent/d` from `origin/main` (scratch worktree, `git show`, no stash) | FAIL: `expected "yes"`, `actual ""`, "d under /bin/bash must source the whole environment". This is the issue's reason. |
| `TestIssue856_…` with `scripts/agent/d` from `origin/main` | FAIL: two `nix-shell.*` directories left behind. This is the issue's reason. |
| `TestIssue856_…` with `scripts/agent/d` from `6f077d3c` (the #857 fix only) | FAIL, same leak; `TestIssue857_…` PASS. The commits split cleanly. |
| Both tests with the branch | PASS, not skipped, under `-race` |
| Linux: `go test -race -count=1 ./scripts/` in `golang:1.26.4`, `--user 1000:1000`, bash 5.2 | all four `d` tests PASS. `TestLaneRegistryListsItsVenomLanes` FAILs the same way on `origin/main`, because the image has no dev-shell python3 (env, not attributable). |
| `go vet ./scripts/` darwin + `GOOS=linux` | ok |
| golangci-lint `./scripts/` darwin + `GOOS=linux` (the binary from `go tool -n`, as `gate.sh` runs it) | 0 issues on both |
| `bash -n scripts/agent/d` under bash 3.2 and 5.3 | ok |
| Repo-wide gate | not run yet on this branch (`.cache/gate` is empty). It runs once per PR. |

### The user-visible behaviour, with the real cached `nix print-dev-env` script (2063 lines, `;&` on line 1909)

- **#856 leak**: the `origin/main` `d` under the dev shell's bash 5.3 left one `nix-shell.*` directory per call: +2
  under `$TMPDIR` and +1 under `/tmp` with `TMPDIR` unset. The fixed `d` ran 12 times: under bash 3.2 and 5.3, and
  with `TMPDIR` set and unset. It left **0** directories. It left 0 in a checkout whose `.git` is a directory too,
  where the shellHook runs `lefthook install`. macOS `TMPDIR` ends in `/`, and `mktemp -t` still returns a
  single-slash path that the `"${tmp_base%/}"/nix-shell.*` pattern matches. The `-x` trace shows
  `rmdir $TMPDIR/nix-shell.1gwpq8`.
- **#857 partial source**: `/bin/bash -x scripts/agent/d true` traces `exec <store>/bash-5.3p9/bin/bash scripts/agent/d true`.
  The second run sources the whole script, and its trace reaches the `case $NIX_BUILD_TOP` / `rmdir` lines. That
  code runs only after line 2057, so the second run sourced past the `;&` line. The run also works with a relative
  `$0` (`cd scripts && /bin/bash agent/d …`).

### No regression for any caller of `d`

The callers are `scripts/agent/gate.sh` (`"$d" …` steps and the `linux()` lint), `scripts/agent/lanes.sh`
(`scripts/agent/d just lima-example …` in a fresh worktree), `.claude/workflows/shim-fix.js` and `fix-batch.js`,
and the skills and CLAUDE.md docs. `justfile`, `lefthook.yml` and `.github/workflows` do not call `d`.

- **Exit codes and signals pass through**: `d` running a script that exits 7 returns 7, and a child that sends
  itself SIGTERM returns 143, under both bashes. After the re-exec and the final `exec`, the pid of `d` is the pid of
  the command (`ps` shows `sleep`), so a signal sent to `d` reaches the command.
- **No re-exec loop**: the re-exec runs only after a check that the found bash is version 4 or later. The new bash
  then skips the `-lt 4` branch. Mutant M3 drops that check and does loop forever (see Minor 2).
- **PATH holds the pinned toolchain**: `command -v go` resolves to the store `go-1.26.3` under both bashes.
- **The #853 nested-call guard holds**: in an outer and a nested `d` call, the hash of `TMPDIR|PATH` is identical,
  and `FUNCD_DEVSHELL_KEY` is set. `TestIssue853_NestedCallKeepsTheEnvironment` passes on darwin and Linux.
- **TMPDIR**: the caller's value comes back byte for byte, trailing `/` included. An unset `TMPDIR` stays unset.
  `TMP`/`TEMP`/`TEMPDIR` keep their caller state. `NIX_BUILD_TOP` is unset, and nothing in the repo reads it (grep).
  The only `TMPDIR` reader among the scripts is `wave-check.sh` (`${TMPDIR:-/tmp}`), and it works with either value.
  Children now get a shorter `TMPDIR` than before, which helps the #41 socket-length limit.
- **gate.sh / lanes.sh**: they call `d` the same way as before. `gate.sh`'s `linux()` pattern
  (`go tool -n golangci-lint`, then `GOOS=linux "$d" "$lint" run`) ran green above. `lanes.sh` starts `d` from a
  plain shell, so it now gets the whole environment instead of the bash 3.2 partial one, which is the issue's
  intent. `lanes_test.go` passes.
- **What gets removed**: only `rmdir`, so only an empty directory, and only when `$NIX_BUILD_TOP` lies directly under
  the caller's `TMPDIR`. The one gap is in Minor 1.

### Mutants (scratch worktree, `scripts/agent/d` mutated, the four `d` tests run)

| Mutant | Result |
|---|---|
| M1 the re-exec line replaced with `:` | killed (`TestIssue857_…`) |
| M2 `-lt 4` → `-lt 3` (no re-exec) | killed (`TestIssue857_…`) |
| M3 drop the "found bash is ≥ 4" check | killed only by `go test -timeout` (infinite exec loop). It left an orphaned looping process, which this review killed. |
| M4 drop the `rmdir` line | killed (`TestIssue856_…`) |
| M5 drop `eval "$saved_tmp"` | killed (`TestIssue856_…`, `TestIssue853_…`) |
| M6 keep `NIX_BUILD_TOP` | killed (`TestIssue856_…`) |

## Findings

### Blockers

None.

### Majors

**Major 1 — #857 and #856: the same defects in the sibling repos' `scripts/agent/d` are neither fixed nor reported.**
(model)

- **Evidence**: on GitHub, `pyvvo/funcd-typescript` and `pyvvo/funcd-python` both carry `scripts/agent/d` (the same
  blob in both). It does `source "$env_file" >/dev/null 2>&1` and then `exec "$@"`, with no bash version check and
  no `nix-shell.*` cleanup. That gives both repos the #857 silent partial source under macOS bash 3.2 and the #856
  leak on every call. Neither repo has an issue about it. #856, #857, the commit bodies and the branch do not
  mention the siblings either.
- **Rule**: CLAUDE.md says the agent wrapper is "the same script in every repo" and names funcd-typescript and
  funcd-python. fix-review step 13 makes a sibling with the same cause, left unfixed and unreported, a Major.
- **Fix**: report it. File one issue per sibling repo, or record it on #856/#857 and in the PR body, so that the
  shim-repo flow picks it up. Those repos also lack the #853 guard, so a port should take the whole current
  `d`. This PR's diff needs no change.

### Minors

**Minor 1 — #856: `rmdir` can remove a directory that the environment script did not create.** (model)

- **Problem**: the guard is "`$NIX_BUILD_TOP` matches `${TMPDIR%/}/nix-shell.*`". It does not check that the
  sourced script set the variable. If the cached script does not set `NIX_BUILD_TOP`, the caller's own value
  survives the source. The caller's empty directory is then removed and `NIX_BUILD_TOP` is unset for the child.
- **Reproduced**: a stand-in environment script `true`, a caller with `TMPDIR=<scratch>/tmp` and
  `NIX_BUILD_TOP=<scratch>/tmp/nix-shell.callers` (empty), then `d true`. The caller's directory is gone afterwards.
- **Impact**: low. A real `nix print-dev-env` always sets `NIX_BUILD_TOP`, `rmdir` removes only empty directories,
  and `nix develop` sets `TMPDIR` equal to `NIX_BUILD_TOP`, not to a directory above it.
- **Fix**: `unset NIX_BUILD_TOP` before the `source`. Any value after the source then comes from the script. The
  alternative is to compare the value before and after.

**Minor 2 — #857: the test catches a re-exec loop only by hanging.** (model)

- **Problem**: in `TestIssue857_OldBashSourcesTheWholeEnvironment`, the "no bash 4" case runs `d` with a plain
  `exec.Command`. A regression that drops the version check (mutant M3) makes the test hang until
  `go test -timeout`, 10 minutes by default. It also leaves an orphaned bash that execs itself forever.
- **Fix**: `exec.CommandContext` with a short deadline, or a `WaitDelay`, would fail fast and kill the child.

## ✅ Verified correct — keep

- **Cause, not symptom (#857)**: the fix does not stop hiding the error. It removes the cause by running the source
  under a bash that can parse `;&`, and it fails loudly with "needs bash 4 or later" when no such bash exists.
- **Cause, not symptom (#856)**: the fix removes the directory that would otherwise leak, right after the script
  creates it. It also restores the caller's `TMPDIR` family, so a child no longer sees a deeper `TMPDIR`.
- **Finding the bash**: the bash 4 comes from the part of the script that bash 3.2 does source, so no path is
  hard-coded.
- **Saving the variables**: `declare -p` and `eval` keep the exact quoting and set/unset state of each variable.
- **Scope**: two files and one issue per commit. Every hunk serves its issue. The #853 comment was trimmed only
  where the old TMPDIR rationale became stale, and no test was weakened.
- **Tests**: both reuse `devshellCheckout`, and the new helpers `bashMajor` and `devBash` have no existing
  equivalent. `TestIssue857_…` covers the error branch where the host bash is old (macOS), and
  `TestIssue856_…` checks that `TMPDIR` is restored and `TMP` and `NIX_BUILD_TOP` stay unset.
- **Shape**: `fix(agents):` subjects, `Fixes #857` / `Fixes #856`, the attribution trailer, project identity on
  both commits, no dev-machine reference in the diff.
- **ADRs**: no ADR governs `scripts/agent/d`, and no ADR or doc was edited.

## Checklist (Definition of Done), each issue

1 test reproduces ✅ · 2 fails pre-fix for the reason ✅ · 3 passes with `-race` ✅ · 4 mutants killed ✅ (M3 only
by timeout, see Minor 2) · 5 root cause ✅ · 6 scope ✅ · 7 ADRs and docs ✅ · 8 build/vet/lint/tests, host and Linux ✅ ·
9 conventions ✅ · 10 reuse ✅ · 11 shape ✅ · 12 siblings ❌ (Major 1). **11/12.**

## Recommendation

Back to `/fix`, for the report only. File the sibling issues, or record them on #856/#857 and in the PR body. Then
re-review. Minors 1 and 2 are cheap, optional hardening.

Side effect of this review: running the pre-fix `d` to reproduce #856 left three new empty `nix-shell.*`
directories on the host. Two are under `$TMPDIR` and one is under `/tmp`, the same kind as the issue's existing
ones. They are harmless, and this review did not remove them.
