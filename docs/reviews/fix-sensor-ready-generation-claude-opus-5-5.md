## Verdict: pass — 0 blockers, 0 majors, 0 minors  (fix-sensor-ready-generation, model: claude-opus-5-5)

This report reviews branch `fix/status-observed-generation`: one commit, 2fa60c17 on e2000b88,
`fix(sensor): an edited Sensor's Ready condition carries its new generation`. It changes one line of
`internal/sensor/sensor.go` and adds one test to `internal/sensor/sensor_test.go`. The fix has no issue, at the decider's
request, so the commit body states the defect, and the `TestIssue<N>` name, the `Fixes #N` line and an issue's own steps
do not apply.

The defect: the Sensor reconciler rewrote its Ready condition only when the condition's status, reason or message, or the
phase, changed (`internal/sensor/sensor.go:228-229` on the base). A spec edit that kept the verdict, `Bound` or
`TargetServesHTTP`, left Ready stamped with the old generation. ADR-0109 Decision 2 sets Ready with its
`ObservedGeneration`, and ADR-0199 Decision 5 counts an App part Ready only at its own generation, so an App upgrade that
changed a Sensor part could not switch and failed at `app.upgradeTimeout` (ADR-0200 Decision 6).

### Blocker / Major / Minor
None.

### Routed (not this work's)
- **`status.observedGeneration`, the status field, not the condition** · home: above · destination: the decider. The
  fixer reports it empty on Route, Sensor, EventSource, Lake and Workflow, judged it uncovered and changed nothing. It is
  outside this review's brief and was not assessed here.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the defect's reason.** With main's `sensor.go` as a `go test -overlay`
  replacement (the file is the same on e2000b88 and on origin/main 578c86d8), both subtests fail at
  `require.Equal(t, se.Generation, c.ObservedGeneration, "Ready observes the edited spec")` with `expected: 2`,
  `actual: 1`. The assertions before it, generation 2 and the unchanged verdict, pass, so the test fails on the stamp
  alone.
- **The test passes with the fix**, not skipped, under `-race`: `--- PASS: TestSpecEditRestampsReadyCondition/bound` and
  `/target_serves_http`. `go test -race ./internal/sensor/` is ok, and 20 runs of the test under `-race` pass.
- **The user-visible behavior is fixed.** A probe ran the App reconciler and the real Sensor reconciler on one in-memory
  store with the manual clock: an App with one Sensor part, then an upgrade from 1.0.0 to 2.0.0 that changes the
  dependency's event. With main's `sensor.go`, the Sensor had generation 2 and Ready True `Bound` at observedGeneration 1.
  The App stayed `Deploying` with revision 1 current and the Sensor `Pending` `Progressing`. After `app.upgradeTimeout`,
  the App and revision 2 were `Failed`. With the fix, the stamp read 2 and the App was `Ready` on revision 2. The probe was
  a scratch file added through `-overlay` and is not part of the change.
- **The fix removes the cause, not the symptom.** The check now compares every field of the condition except
  `LastTransitionTime`, which `Conditions.Set` keeps when the status does not change
  (`api/types/v1alpha1/status.go:35`). It copies the EventSource check (`internal/eventing/eventing.go:178`), whose
  generation comparison came with #148's fix (9380edf6); `setWatching` (`:215`) makes the same comparison. Nothing is
  masked: there is no timeout, retry or skipped test. The gap predates ADR-0243: before 5d716f95, the check compared only
  the status and the phase.
- **It cannot loop.** A status-only Update does not bump the generation, so the next pass finds the stamp equal and writes
  nothing. A resync probe after the restamp left the Sensor's resourceVersion unchanged.
- **Mutants.** M1 (`!=` to `==` on the new clause) fails both subtests. M3 (restamp only for a True verdict) fails
  `target serves http`, so both subtests carry weight. M2 compares with `status.observedGeneration` instead, which a
  Sensor never sets, so the check always fires. It passes the package, but it is equivalent: the store discards a
  byte-identical Update (`internal/store/store.go:491-503`, ADR-0047), and the resync probe under M2 also leaves the
  resourceVersion unchanged. Its only cost is one more no-op store call per pass.
- **Scope.** Both files serve the defect. No test was changed or removed.
- **Design check: `bug`.** ADR-0109 was read whole. Decision 2 ends "Set `Ready` (with `ObservedGeneration`)", and its
  Scope, Alternatives, Temporary workarounds, Consequences and Open questions accept no stale stamp. ADR-0156, its partial
  supersession, changes only the delivery steps of Decision 3. ADR-0243 Decision 5 adds the `TargetServesHTTP` verdict
  and says nothing about the stamp. ADR-0199 Decision 5 counts every part other than a Function or a Bucket Ready only
  when its Ready condition is True at its generation, and `judge` implements that (`internal/app/status.go:93`). The
  ADRs that supersede parts of it (ADR-0212, 0213, 0215, 0219) do not touch the Sensor rule. ADR-0200 Decision 6 switches
  only when no part is Pending, and fails the latest revision at `app.upgradeTimeout`. An issue search, open and closed,
  for "Sensor observedGeneration", "Sensor Ready generation", "observedGeneration stale", "Ready condition generation
  edit", "TargetServesHTTP", "App upgrade Sensor Pending" and "observedGeneration" finds no issue that accepts it. #148,
  closed as `kind/bug`, treated the same stale stamp on an EventSource as a defect. No board card covers it: the Sensor
  cards are GenerateName, durable delivery and the DLQ park rate. `docs/behavior/` has no row for it. The apps row "A part
  Pending at the deadline" is decided for a part that is not ready, not for a Ready part stamped with an old generation.
  No test pinned the old behavior, and the App tests' `markReady` stamps a part at its generation. No design spec under
  `docs/design/` models it. No ADR file was edited.
- **Siblings.** No other Ready writer repeats the check. Route (`applyStatus`, `cur != prev`) and the Function's
  IngressReady (`prev != cur`) compare the whole condition. SQLDatabase, KVStore and Bucket compare the whole status with
  `reflect.DeepEqual`. Site, Lake and Workflow write their status with the current generation on every pass. The Sensor's
  own static-defect path writes with the generation on every pass. Every case the commit names, Bound and
  TargetServesHTTP, has a subtest.
- **Reuse.** The test uses the package's `harness`, `createSensor`, `dep` and `reqOf`, and builds the HTTP Function inline,
  as `TestScenarioSensorNamingAnHTTPFunctionIsNotReady` does. No helper, type or dependency was added.
- **Conventions.** The test has one why-comment that cites ADR-0109 and ADR-0199, and it follows the file's idiom.
- **Checks.** On the branch: gofmt lists no file; `go build ./...` exits 0 on the host and with `GOOS=linux`;
  `go vet ./internal/sensor/` exits 0 on both; golangci-lint on `./internal/sensor/` reports `0 issues` on both, the Linux
  run with the host binary as `scripts/agent/gate.sh` runs it. The repo-wide gate has not run on this branch, and this
  review did not run it: the brief and CLAUDE.md keep the e2e suite and `go test ./...` out of a review. No e2e test or
  lane runs an App upgrade that edits a Sensor part.
- **Commit shape.** One commit with a `fix(sensor):` subject. The body states the cause, the consequence, the fix, the
  evidence and the regression test, cites "issue 148" with no closing keyword, and ends with the attribution trailer.

### Against the current main
origin/main has moved to 578c86d8 (#1460), which changes only `cmd/funcdctl` and a review document.
`git merge-tree --write-tree origin/main HEAD` merges cleanly, and neither `internal/sensor` nor `internal/app` depends on
`cmd/funcdctl`, so the results above hold on the merged tree. The branch needs no rebase before its PR.

### Definition of Done
12 / 12 items hold. This is the fix checklist with items 1 and 11 adapted to a fix that has no issue; item 13 does not
apply, because no design spec models Sensor or App part readiness. Misses: none.

### Model scorecard
No ledger row is recorded: the ledger is keyed on an issue, and this fix has none. The row for this review:
claude-opus-5-5 on fix-sensor-ready-generation (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for its PR once `scripts/agent/gate.sh` has run once on the branch. The PR body needs no closing keyword, since
there is no issue.
