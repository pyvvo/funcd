# Fix review: issue #853 (claude-opus-5-5)

- **Issue**: #853. A nested `scripts/agent/d` call sources the dev-shell environment again, and each level lengthens `TMPDIR` by one `nix-shell.XXXXXX` directory.
- **Branch**: `fix/853-nested-d-tmpdir`, one commit `a9afef92` (`fix(agents): keep TMPDIR when scripts/agent/d runs inside another d call`).
- **Model**: claude-opus-5-5
- **Verdict**: **pass**. There are 0 Blockers, 0 Majors and 2 Minors, and both Minors are attributed to the model.
- **Checklist**: 11 of 12 items hold.

## The change

`scripts/agent/d` now sources the cached environment and exports `FUNCD_DEVSHELL_KEY=<flake key>` only when the
inherited `FUNCD_DEVSHELL_KEY` differs from this flake's key. A nested call in the same flake's environment therefore
runs the command in the environment it inherits. `GOLANGCI_LINT_CACHE` and `exec` stay outside the guard. The
`devshellCheckout` test helper now takes the content of the environment script, and
`TestIssue853_NestedCallKeepsTheEnvironment` checks three cases:

- A nested call keeps `TMPDIR` and keeps the pinned toolchain on `PATH`.
- A direct call provides the toolchain.
- A call that inherits a different key sources the environment again.

## Verification (run through `scripts/agent/d`)

1. **The test fails without the fix.** The test reads `scripts/agent/d` at runtime, so an overlay does not reach the
   script. I ran the test in a scratch worktree of `origin/main` with the branch's `devshell_test.go` copied in. It
   failed at `devshell_test.go:83` with the message "a nested call must keep TMPDIR": the nested `TMPDIR` was the
   outer `TMPDIR` plus a second `/nix-shell.XXXXXX` directory. This is the reason that the issue reports.
2. **The test passes with the fix.** `go test -race -count=1 -run 'TestIssue853|TestAgentShell' ./scripts/` passed on
   darwin. It also passed on Linux, in Docker (`golang:1.26.4`, `--user 1000:1000`, source copied in with
   `git archive`).
3. **The issue's own steps behave as expected.** On the branch, `TMPDIR` has the same length with one, two and three
   levels of `d` (49 characters plus a newline in each case). In the triple-nested call, `which go` resolves to the
   pinned Go in the Nix store. With the pre-fix script, two levels gave 65 characters, which is the growth that the
   issue reports.
4. **The fix removes the cause.** The fix stops the second sourcing of the environment script. That sourcing is the
   step that runs `export TMPDIR="$(mktemp -d -t nix-shell.XXXXXX)"` (the cached script sets
   `NIX_BUILD_TOP`, `TMP`, `TMPDIR`, `TEMP` and `TEMPDIR` to the new directory). The fix does not shorten paths or
   work around the socket limit.
5. **Every mutant was killed.** I ran each mutant in a scratch worktree of the branch:
   - M1 drops `export FUNCD_DEVSHELL_KEY=$key`. The test fails with "a nested call must keep TMPDIR".
   - M2 changes the guard to `[ -z "${FUNCD_DEVSHELL_KEY:-}" ]`, which ignores a key mismatch. The test fails with
     `pinned-tool: command not found`, from the case where the call inherits another flake's key.
   - M3 drops the append of the original `PATH`. The test fails with `exec: sh: not found`.
6. **The scope is limited to the issue.** The two files serve the issue. The helper change only adds a parameter, and
   the existing lint-cache test now passes `"true\n"`, which is the content it used before. No test was weakened.
7. **The change reuses what exists.** The only new name is the `FUNCD_DEVSHELL_KEY` environment variable, and nothing
   else in the repository uses that name. The existing variables `NIX_BUILD_TOP` and `IN_NIX_SHELL` do not identify the
   flake, so they cannot tell this environment from another flake's environment. The test reuses the existing
   `devshellCheckout` harness.
8. **The conventions hold.** The imports are at the top level. The new comments explain why (the `TMPDIR` mechanism
   and #853) and do not narrate the code. The style matches the surrounding script.
9. **No ADR is affected.** No ADR file changed, and no Accepted ADR governs `scripts/agent/d`. The description of
   `d` in `CLAUDE.md` is still true.
10. **The checks are green.** The following checks passed on darwin:
    - `go test -race -count=1 ./scripts/` (`ok`, 25.4s)
    - `go vet ./scripts/`
    - `go tool golangci-lint run ./scripts/` (0 issues)
    - `GOOS=linux go vet ./scripts/`

    On Linux in Docker, the two devshell tests and `go vet ./scripts/` passed. The full `./scripts/` run on Linux
    failed only `TestLaneRegistryListsItsVenomLanes`, because the bare `golang` image has no Python with PyYAML
    ("the dev shell's python3 must run the lane tools"). That failure comes from the environment and is unrelated to
    the diff. `shellcheck` is not available in the dev shell, so I did not run it.
11. **The commit has the expected shape.** The subject is `fix(agents): …`, the body contains `Fixes #853`, the
    commit carries the attribution trailer, and the commit covers one issue.
12. **Every caller still works.** I searched for callers in `justfile`, `scripts/`, `.github/` and `.claude/`:
    - `scripts/agent/gate.sh` calls `"$d" …` for the audit, `just ci-full` and the Linux build, vet and lint. A
      direct run sources the environment once. A run under `d timeout` now keeps the outer environment, which is the
      purpose of the fix.
    - `scripts/agent/lanes.sh` calls `scripts/agent/d just lima-example …` in a fresh checkout. A run outside `d`
      behaves as before. A run inside `d` reuses an identical environment, because the key is the hash of
      `flake.nix` and `flake.lock`. One small difference remains: the flake's `shellHook` (the `lefthook install`)
      does not run again, and it is a no-op in a worktree anyway.
    - `.claude/workflows/shim-fix.js` and `fix-batch.js` only tell agents to use `d`. The `justfile` and
      `.github/` workflows do not call `d`.
    - `GOLANGCI_LINT_CACHE` is inherited across nested calls, as it was before the fix.

## Findings

### Minor

1. **The guard trusts an inherited key without checking that the environment is still active** (model). The key
   records that this flake's environment was sourced at some point. It does not record that this environment is
   still the active one. A process between two `d` calls can change `PATH` and keep the variable. Examples are a
   different flake's environment (an unpatched sibling `d`, or `nix develop` of another flake) and a step that resets
   `PATH`. In that case the inner `d` runs without this flake's toolchain first on `PATH`.
   - Evidence: `scripts/agent/d env PATH=/usr/bin:/bin scripts/agent/d which go` exits 1 on the branch. With the
     pre-fix script, the same command prints the pinned Go.
   - The test's "another flake's environment" case passes `FUNCD_DEVSHELL_KEY=0123456789abcdef`. That case assumes
     the other environment sets this variable, which no other repository does today.
   - Impact: no caller in the repository creates such a chain. The trade-off is reasonable, but the test comment
     overstates the guarantee.
   - Recommendation: no code change is needed. Narrow the test comment, or document the assumption in the comment of
     `d`.
2. **The sibling copies of `d` keep the same cause, and the fix does not report them** (model).
   `pyvvo/funcd-typescript` and `pyvvo/funcd-python` ship the same `scripts/agent/d`, as `CLAUDE.md` says. Their
   `origin/main` copies still source the environment on every call. Neither the commit nor the issue mentions them.
   - Impact: neither sibling repository has a nested caller today, so the defect is latent there.
   - Further effect: until the siblings set the same variable, a sibling `d` inside a funcd `d` call leaves the funcd
     key in place, which leads to Minor 1.
   - Recommendation: file a follow-up in each language repository, or comment it on the PR (the shim-fix flow), so
     that the copies stay identical.

## Verified correct

- The regression test reproduces the issue's exact mechanism, `mktemp -d` under the inherited `TMPDIR`. It runs
  without Nix, so it runs on CI on both operating systems.
- The test checks the toolchain on `PATH` as well as `TMPDIR`. A fix that skipped sourcing entirely would therefore
  fail the test.
- The flake hash already computed for the cache file name is reused as the key. A change to the flake therefore makes
  nested calls source the new environment.
- The fix is three lines of logic, and the rest of the diff is the re-indented block.

## Recommendation

Pass. The fix is ready for the group PR. Address the two Minors as a comment tweak and a sibling follow-up. They do not
block the PR.
