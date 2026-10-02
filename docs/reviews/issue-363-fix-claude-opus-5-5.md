# Fix review — issue #363 (claude-opus-5-5)

**Issue**: A bundle push drops symlinked files without an error or a warning.
**Change**: `fix/i363`, commit `0869e9f fix(artifact): refuse a symlink in a bundle instead of dropping it`.
**Verdict**: **pass** — 0 Blocker · 0 Major · 1 Minor. Checklist 10/10 of the items that apply here (the group gate covers Linux lint, repo-wide tests and e2e).

## Blocker

None.

## Major

None.

## Minor

1. **The "symlinked dir" subtest asserts a loose substring** (`internal/artifact/bundle_test.go`,
   `ErrorContains(err, "vendor")`). `goodBundle` already has a `vendored/` directory, so the substring
   would also match an error that named `vendored/...` instead of the symlinked `vendor` path. Asserting
   the quoted path (`"\"vendor\""`) would pin it. The kind assertion still kills the revert and both
   mutants below, so this is a precision gap, not a coverage hole. Attribution: `model`.

## Verified correct

- **Fails without the fix, for the issue's reason.** With the `origin/main` `internal/artifact/bundle.go`
  overlaid, all three subtests (`symlinked_file`, `symlinked_dir`, `site`) fail on
  `expected: "invalid" / actual: ""`: the old packer returned no error and left the link out.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/artifact/` → `ok`.
- **Root cause, not symptom.** The skip branch in `packDir` (the cause the issue names) now returns
  `fault.Invalid` naming the slash-relative path. One code path covers `PackBundle`, `PushBundle`
  (both `funcdctl push` call sites) and `PushSite`, which the test exercises end to end through a push to
  an OCI layout.
- **Walk error keeps its kind.** `fault.Wrapf(walkErr, fault.KindOf(walkErr), …)` keeps the new
  `Invalid`; `fault.KindOf` returns `Internal` for a plain OS error, so I/O failures are still `Internal`.
- **Mutants (overlay, `-run 'TestIssue363|TestIssue155'`)**:
  - revert the skip branch to `return nil` → `TestIssue363` fails;
  - wrap the walk error as `fault.Internal` again → `TestIssue363` and `TestIssue155` fail;
  - drop the path from the message → `TestIssue363` fails (`ErrorContains`).
  None survives.
- **Scope.** Two files: the packer (one branch, the wrap kind, two doc comments, and the entry message,
  which no longer claims the symlink rule now that the walk enforces it) and the new test.
  `TestIssue155_SymlinkedEntryRefused` is unchanged and still passes.
- **ADRs.** ADR-0089 does not decide symlink handling (the issue says so). Refusing a symlink matches the
  existing untar side (`untarBundle` refuses symlink/hardlink/device entries, per ADR-0139's description
  of it), so pack and unpack now agree. No ADR file is edited.
- **No regressions on known bundles.** The pinned funcd-typescript and funcd-python modules (the examples)
  contain no symlinks. The `node_modules` symlink in `cmd/funcdctl/dev.go` is placed in a temp dir that
  dev mode runs in place and never packs.
- **Reuse.** No new helper; it uses `fault.Invalidf`, `fault.KindOf` and the existing test helpers
  (`goodBundle`, `siteDir`, `layoutRef`).
- **Conventions.** `api/fault` errors, no comment bloat, no imports added, idiomatic with the surrounding code.
- **Checks (touched package)**: tests with `-race` pass, `go vet` is clean, golangci-lint reports `0 issues`.
- **Shape.** The subject is `fix(artifact): …`, the body has Cause/Fix/Test, `Fixes #363` and the attribution trailer, and there is one issue per commit.
- **Worktree** left clean at `0869e9f`.

## Recommendation

Pass. Optionally tighten the `symlinked dir` assertion to the quoted path while the group PR is assembled.
