## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #436 fix, model: claude-opus-5-5)

Change: branch `fix/i436`, commit 82a9dae `fix(funcd): reject a negative or malformed funclog.segmentMaxAge`
(`cmd/funcd/main.go` +3/-6, `cmd/funcd/main_test.go` +33). Governing ADR: ADR-0081 (function-log capture); precedent
fix #333 (the shared `parseDuration` helper for duration config keys).

### 🟡 Minor 1 — the config fixture is copied from TestIssue333  ·  attribution: model

`TestIssue436_NegativeFunclogSegmentMaxAgeRejected` re-writes the same six-line fixture (the `shortDataDir`, the inline
`funcdconfig.yaml` with loopback addresses and memory storage, `config.Load`) that `TestIssue333_NegativeWorkflowEventingDurationRejected`
already holds in its `load` closure in the same file. Lifting that closure to a file-level helper shared by both tests
would remove the copy. Trivial and test-only, so Minor. The subtests' assertions (`fault.Invalid`, key named in the
message, `0s` accepted) match the #333 pattern, which is the right shape.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With the fix commit reverted (`git revert --no-commit 82a9dae`,
  the new test file kept), `TestIssue436/-3h` fails with "An error is expected but got nil" (the negative value is
  accepted, the issue's symptom) and `TestIssue436/bogus` fails with expected `invalid`, actual `internal` (the plain
  `fmt.Errorf`, the issue's second symptom). The worktree was then reset to 82a9dae and is clean.
- **Passes with the fix under `-race`**, un-skipped, with the whole `cmd/funcd` package (`go test -race ./cmd/funcd/` ok).
- **User-visible behavior.** `buildOptions` is the daemon's startup path from config to options, so the test exercises
  the issue's steps (a loaded config with `funclog.segmentMaxAge: -3h`) directly; startup now fails with
  `fault.Invalid` naming the key.
- **Root cause fixed.** The issue names a bare `time.ParseDuration` with no sign check and a plain error. The fix
  replaces it with the shared `parseDuration(key, s, 0, true)`, which rejects negative and malformed values with
  `fault.Invalidf` naming the key, and keeps empty and `0` as "use the sink default" — exactly the expected behavior.
  The sink's `SegmentMaxAge <= 0` fallback is left alone, which is correct: it now only sees 0 for "unset".
- **Mutants (2, both killed; the file was restored after each):**
  1. `zeroOK` `true` → `false` → `TestIssue436/ok=0s` fails (0 must keep the sink default);
  2. the returned error replaced with `nil` → `TestIssue436/-3h` and `/bogus` fail.
  The helper's own sign logic is covered by `TestIssue333_…`, so a third mutant there would duplicate that coverage.
- **Scope.** One hunk in `main.go` and one new test; nothing unrelated.
- **Reuse / conventions.** The fix is reuse itself: it removes the hand-rolled parse in favour of the existing
  `parseDuration`, the same call the four workflow/eventing keys make since #333. `fault` errors, no new helper, type or
  dependency; imports unchanged; the test uses `shortDataDir`, not `t.TempDir()`. The comment above the call
  ("a zero segment size/age keeps the sink default") still holds.
- **ADRs.** It contradicts no Accepted or Implemented ADR and edits no ADR file; ADR-0081's default-on-zero semantics
  are preserved.
- **Checks (touched package only).** `go vet ./cmd/funcd/` clean; `golangci-lint run ./cmd/funcd/...` 0 issues;
  `go test -race` ok. Linux lint, e2e and lanes are left to the group gate.
- **Commit shape.** `fix(funcd):` subject, a Cause/Fix/Test body, `Fixes #436`, the attribution trailer; one issue.

### Definition of Done

10 / 11 hold: item 10 (reuse) does not fully hold because of the copied test fixture in Minor 1. For item 8, the
host-side checks above are green; Linux lint and e2e are deferred to the group gate.

### Model scorecard

claude-opus-5-5 · phase fix · issue #436 · pass · B0 / M0 / m1 · model-attributed 1 · DoD 10/11.

### Recommendation

Pass. Optionally share the #333 `load` closure with the #436 test when the group PR is assembled. This does not block
the merge.
