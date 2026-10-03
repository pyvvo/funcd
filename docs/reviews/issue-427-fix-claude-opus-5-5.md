# Fix review — issue #427 (funcdctl dev hot-reload Conflict logged as "hot-reload failed")

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #427 fix, model: claude-opus-5-5)

Change: branch `fix/i427`, commit e666aa6 `fix(funcdctl): retry a hot-reload apply that loses a Conflict
instead of logging a failure` — `cmd/funcdctl/dev.go` (3 lines), `cmd/funcdctl/dev_phase2_test.go` (+41).

The issue names the cause precisely: `reloadChanged` called `c.Apply` directly for the synthesized resources
and for the edited Function, while `applyDesired` (the Conflict re-apply added for #398) was used only by the
boot apply. The fix routes both reload applies through `applyDesired`; nothing else in the logic changes.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit e666aa6` with the
  new test file kept, then `go test -tags dev -run TestIssue427 ./cmd/funcdctl/` →
  `--- FAIL: TestIssue427_DevHotReloadRetriesConflict` with "Received unexpected error … a Conflict on a
  hot-reload apply is re-applied in place, not reported". The worktree was reset to e666aa6, clean.
- **Passes with the fix under `-race`**: `go test -tags dev -race -run 'TestIssue427|TestIssue398|Reload'`
  → PASS for TestIssue427, TestIssue398, TestIssue135 and TestIssue320 (the existing hot-reload tests).
  Whole touched package: `go test -tags dev -race -count=1 ./cmd/funcdctl/` → ok.
- **Mutants (both killed):**
  - M1 — the resource apply back to `c.Apply` → FAIL: `apply ConfigMap "app-config": … resourceVersion mismatch`.
  - M2 — the Function apply back to `c.Apply` → FAIL: `apply Function "001": … resourceVersion mismatch`.
  The test binds a ConfigMap so both apply sites are exercised, and asserts every path was PUT exactly twice
  (one Conflict, one re-apply), so it also pins the in-place retry count rather than just "no error".
- **Cause, not symptom.** The reload no longer surfaces a Conflict that a re-apply resolves; the bounded
  `devApplyAttempts` (5) loop is the same one the boot apply uses, the object is the whole desired state so
  re-applying is idempotent, and a Conflict that outlasts the attempts still clears `h.seen` and is reported
  (the existing Conflict branches stay reachable). No error is swallowed, no timeout lengthened.
- **Reuse.** The fix reuses `applyDesired` instead of adding a second retry loop. The test uses a small
  `httptest` fake control plane rather than the existing `conflictOnFirstPut` transport; that harness wraps a
  real platform via `http.DefaultClient` and targets one Function name, while this test calls `reloadChanged`
  directly against a stub that must conflict on the resources too — a different seam, not a duplicate.
  `fault.WriteProblem` / `fault.Conflictf` are reused for the Problem response.
- **Scope.** Two call-site swaps plus the doc-comment sentence that described the old "retried on the next
  poll" behavior; no unrelated hunk, no test weakened or deleted.
- **Conventions.** `api/fault` errors, ctx-first, top-level imports (`sync` added at the top), no YAML in flow
  style (the manifest literal is block style), comments limited to the why. The test lives beside the #398 test
  in the `dev`-tagged file, matching the package's layout.
- **ADRs.** Consistent with ADR-0018 (optimistic update; a re-apply of the full desired state on Conflict) and
  ADR-0125 (hot-reload re-applies on change). No ADR file touched.
- **Checks (touched package).** `go vet -tags dev ./cmd/funcdctl/` clean; `golangci-lint run` with and
  without `--build-tags dev` on `./cmd/funcdctl/...` → 0 issues. Linux lint, e2e and lanes are left to the
  group gate per this run's scope.
- **Shape.** `fix(funcdctl):` subject, `Fixes #427`, attribution trailer, one issue in one commit.

Not rerun: the issue's live `funcdctl dev` race (it needs a controller status write landing between the API's
read and update — not cheaply reproducible); the unit test injects exactly that Conflict at both apply sites.

### Definition of Done

11 / 11 apply and hold (item 8 on the host for the touched package; the repo-wide, Linux-lint and e2e legs
run at the group gate).

### Model scorecard

claude-opus-5-5 · issue #427 · fix · pass · 0 blockers · 0 majors · 0 minors · 0 model-attributed.

### Recommendation

Pass — hand back to `/fix` for the PR.
