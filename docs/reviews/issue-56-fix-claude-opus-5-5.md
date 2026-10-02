## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #56 fix, model: claude-opus-5-5)

Change: branch `fix/i56`, commit `7460b83` — `fix(sensor): drop a deleted Sensor's subscriptions when it is
re-created under the same name`. Two files: `internal/sensor/sensor.go` (+5/-4), `internal/sensor/sensor_test.go` (+25).

The issue: the Sensor subscription registry (`subs map[sensorKey]*subEntry`) matched a Sensor by (namespace,
name) and generation only. The store stamps `Generation = 1` on every Create (`internal/store/store.go:328`),
and the controller queue coalesces a delete and a re-create into one Reconcile that sees only the new object,
so `cancelAll` never ran and the B1 idempotency check matched. The fix records the object's UID in `subEntry`
and requires `e.uid == se.UID && e.generation == se.Generation` for the no-op path; a UID change takes the
existing cancel-and-replace branch.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Mutant M2 survives: nothing pins that the UID is stored, so resync churn is undetected** · attribution: `model`
  Evidence: mutant `subEntry{uid: se.UID, generation: …}` → `subEntry{generation: …}` (UID never recorded,
  so every resync cancels and re-subscribes) — `go test ./internal/sensor/` → `ok`. `TestReconcileIsIdempotent`
  counts runs after one firing, which a cancel-then-resubscribe still satisfies. The behavior regression is a
  silent re-subscribe on every resync (a firing that lands between cancel and re-subscribe is dropped). The gap
  predates the fix (dropping the stored generation survives the same way), but the fix adds a new key line with
  no test pinning it. Fix (optional): count subscriptions through a counting `Subscriber` wrapper in the harness,
  or assert in `TestIssue56_…`/`TestReconcileIsIdempotent` that a second Reconcile of the re-created Sensor does
  not re-subscribe.

### ✅ Verified correct (keep it)
- **Regression test fails on the pre-fix code, for the issue's reason.** `git revert --no-commit 7460b83` also
  removes the test (`[no tests to run]`), so the pre-fix `internal/sensor/sensor.go` from `origin/main` was
  overlaid with `go test -overlay`: `--- FAIL: TestIssue56_RecreateSameNameReplacesSubscriptions` —
  `expected: "new-wf"`, `actual: "old-wf"`. The old source fires the old action; the new dependency never fires,
  exactly the issue's unit symptom.
- **It passes with the fix** under `-race`: `--- PASS: TestIssue56_RecreateSameNameReplacesSubscriptions`,
  `ok github.com/pyvvo/funcd/internal/sensor`. The whole package passes under `-race`.
- **Mutants**: M1 (drop `e.uid == se.UID`) → FAIL `TestIssue56_…`; M3 (`uid == || generation ==`) → FAIL
  `TestIssue56_…`; M2 survives (Minor above).
- **Cause, not symptom.** The test reproduces the coalesced case directly: delete + re-create, then one
  Reconcile that sees only the new object. The fix keys on object identity, which is what the issue's root
  cause names. There is no timeout, retry or swallowed error. `meta.UID` is fresh on Create
  (`internal/store/store.go:327`) and kept across Update (`store.go:404`), so a spec edit still goes through the
  generation path and a resync stays a no-op.
- **ADR conformance.** ADR-0109 Decision §2 B1 ("a spec change cancels-and-replaces"; "on delete, cancel all")
  holds: a re-created object now replaces its subscriptions. ADR-0015 (one reconciler per GVK) is untouched.
  No ADR file is in the diff.
- **Scope.** Every hunk serves the issue: the `subEntry` field and its doc comment, the guard, the
  constructor literal, and the regression test. No test was weakened or deleted.
- **Reuse.** The fix reuses the existing cancel-and-replace branch and the existing test harness (`harness`,
  `createSensor`, `reqOf`, `fire`, `runs`). It adds no helper. Comparing UIDs to tell a re-created object
  apart matches existing precedent (`internal/site/materialize.go:57`). No other reconciler has a
  generation-only registry (grep for `generation ==` in non-test code returns only this line).
- **Conventions.** It uses the typed `v1.UID` (ADR-0002 typed IDs). It adds no imports, no `any`, and no
  logging change. The doc comment explains why ("Generation restarts at 1"), and the test comment is two lines.
- **Checks (touched package).** `gofmt -l internal/sensor` is clean, `go build ./...` passes, and
  `go vet ./internal/sensor/` passes. `go test -race ./internal/sensor/` → `ok`, and
  `golangci-lint run ./internal/sensor/...` → `0 issues.` The e2e suite, Linux lint and the lanes are left
  to the group gate.
- **Shape.** The subject is `fix(sensor): …`, the body has `Fixes #56` and the attribution trailer, and the
  commit covers one issue.

### Recommendation
Pass. The fix is minimal and fixes the root cause. The M2 survivor is an optional test hardening and does
not block the merge.
