# Fix review — issue #490 (model: claude-opus-5-5)

Issue: removing one kv table or blob prefix binding makes `funcdctl dev` hot-reload retry forever.
Change: branch `fix/i490`, commit a33b8e7 `fix(funcdctl): let dev drop a kv table or blob prefix a Function bound`
(`cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_test.go`).

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #490 fix, model: claude-opus-5-5)

### Minor 1 — other admission Conflicts on the first resource pass still re-run every poll  ·  attribution: model

The issue's expected behavior also asks that "a Conflict that is not a resourceVersion race is reported once".
The fix removes the cause of this issue's Conflict: the first pass never drops a bound table or prefix, and the
final pass (`lastRes`) reports a failure once without clearing `h.seen`. But the `firstRes` loop in
`reloadChanged` still treats every Conflict as a status race (`h.seen = ""`), so a different admission Conflict
there (none is known today) would loop the same way. This is not needed for #490's reproduction; it is a
follow-up hardening, not a defect of this fix.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit a33b8e7` with the new test file kept,
  `go test -tags dev -run TestIssue490_`: `reload` fails after 20 s while the log repeats
  `admission.kvstore-deletion-protection: table "cold" of KVStore "cache" is still bound by function ...` on every
  poll; `persist-restart` fails at boot with the same Conflict. Both are the issue's two scenarios.
- **Passes with the fix under `-race`**: both subtests PASS (1.1 s), not skipped (node was on PATH).
- **Mutants (each killed):**
  - M1 `keepDropped` always returns nil → both subtests FAIL.
  - M2 the `lastRes` apply loop in `reloadChanged` skipped → `reload` FAILs (the table is never dropped).
  - M3 `lastRes` dropped from the `bootDev` apply groups → `persist-restart` FAILs.
- **Cause, not symptom.** The apply order is fixed: a KVStore or Bucket that drops a table or prefix is applied
  first with the live entries kept, then the Functions, then as desired. No retry, timeout or swallowed error was
  added; admission's deletion protection (ADR-0073) is unchanged and still enforced.
- **Final state matches the old intent.** The last apply uses the desired object, so live entries nobody binds
  are removed as before; the last pass runs only when every Function apply succeeded (`len(errs) == loadErrs`),
  so a failed Function apply cannot trigger the protected drop.
- **Scope.** Two files: the staging helper, its two call sites, the updated doc comments, and the regression
  test. No test was weakened or deleted.
- **Reuse.** `keepDropped` uses `slices.ContainsFunc`/`slices.Clone`; the comparable set-difference in
  `internal/controlplane/admission/kvstore.go` and `bucket.go` is unexported and works on another shape, so
  there is nothing to call from `cmd/funcdctl`. Applies go through the existing `applyDesired`; errors through
  `api/fault` (`fault.Wrapf`, `fault.KindOf`), matching the surrounding code.
- **Conventions.** ctx-first, no `any` in signatures, top-level imports, comments explain the why (the ADR-0073 /
  ADR-0121 ordering constraint). The test's embedded YAML is block style; the persist test uses
  `os.MkdirTemp("", "funcd")` per the socket-path rule.
- **ADRs.** Conforms to ADR-0125 (watch and re-apply), ADR-0073 (deletion protection), ADR-0121 (existence gate).
  No ADR file was touched.
- **Checks (touched package).** `go test -race ./cmd/funcdctl/` ok; `go test -tags dev -race ./cmd/funcdctl/` ok
  (29.8 s); `go vet` with and without `-tags dev` clean; `golangci-lint run` with and without `--build-tags dev`:
  0 issues. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape.** `fix(funcdctl):` subject, `Fixes #490`, attribution trailer, one issue in one commit.

### Definition of Done

11 of 11 items hold (item 8 for the host checks run here; Linux lint and e2e go to the group gate).

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 1 minor (model-attributed).

### Recommendation

Pass. Hand back to `/fix` Step 8. Optionally file the Minor as a follow-up: distinguish a resourceVersion race
from an admission Conflict in the first resource pass of `reloadChanged`.
