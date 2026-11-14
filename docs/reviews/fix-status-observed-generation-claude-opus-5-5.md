## Verdict: pass — 0 blockers, 0 majors, 0 minors  (fix-status-observed-generation, model: claude-opus-5-5)

This report reviews branch `fix/status-observed-generation-kinds`: one commit, e525d76d on a10c6541,
`fix(status): Route, EventSource, Sensor, Workflow and Lake fill status.observedGeneration`. It changes five reconcilers
and their tests. The fix has no issue, at the decider's request, so the commit body states the defect, and the
`TestIssue<N>` name, the `Fixes #N` line and an issue's own steps do not apply.

This review is not independent. The decider's standing rule is to run no review agents, so the model that wrote the fix
ran the fix-review steps on its own change. Every claim below comes from a command that ran on the branch.

The defect: the Route, EventSource, Sensor, Workflow and Lake reconcilers stamped their `Ready` condition with the
generation but never set `status.observedGeneration`, which stayed empty after an apply and after each edit. The field's
description is "The generation of the spec that funcd last acted on" (`api/types/v1alpha1/status.go:65`).

### Blocker / Major / Minor
None.

### Routed (not this work's)
None.

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the defect's reason.** With main's version of each of the five
  changed files as a `go test -overlay` replacement, every subtest fails at its `status.observedGeneration` assertion
  with `expected: 1`, `actual: 0`, and the condition assertions before it pass: `TestStatusObservesGeneration` in
  `internal/route` (`ready`, `backend missing`), `internal/eventing` (`timer`, `blob watching`, `blob bucket missing`)
  and `internal/workflow` (`type checked`, `kv store not owned`), `TestSpecEditRestampsReadyCondition` in
  `internal/sensor` (`bound`, `target serves http`, `invalid input`) and `TestReadyObservesGeneration` in
  `internal/services/lake` (`engine ready`, `engine not ready`, `bucket missing`).
- **The tests pass with the fix**, not skipped, under `-race`, and in 20 repeated runs each.
- **The user-visible behavior is fixed.** `funcdctl dev`, built with `-tags dev`, ran a folder with two Routes (one
  Ready, one `BackendNotFound`), a timer EventSource, a Sensor, a Workflow with a builtin step and a Lake (held
  `LakeEngineNotBundled`, as this build embeds no engine), beside the Bucket, Secret and Function they need. A binary
  built with main's five files left `status.observedGeneration` absent on all six, at generation 1 and after an edit
  of each at generation 2, while each `Ready` carried the generation. The branch's binary showed the field at 1, then
  at 2, on all six. The probe folder lived in a scratch directory and is not part of the change.
- **The fix removes the cause.** Each status write that stamps `Ready` now sets the field to the same generation, on
  success and on failure, as `internal/services/sql/reconcile.go:166` does. The skip checks compare the field too:
  `applyStatus` in `internal/route/reconcile.go`, the Sensor's write check, the EventSource's timer branch and
  `setWatching`, and the Workflow's `notReady`. A pass on an object that an earlier funcd stamped without the field
  therefore writes once, and the next pass writes nothing: each test clears the field, reconciles, sees it filled,
  reconciles again and finds the resourceVersion unchanged. The Lake and the Workflow's contract gate write on every
  pass, and the store discards a byte-identical write (ADR-0047), which the Lake test now checks as well. Nothing is
  masked: there is no timeout, retry or skipped test.
- **Mutants.** Sixteen overlay mutants, each reverting one stamp or one comparison, fail a test: the Route's stamp and
  comparison (R1, R2), the Sensor's static-defect stamp, write stamp and comparison (S1 to S3), the EventSource's timer
  stamp and comparison, `setWatching` stamp and comparison and `setNotReady` stamp (E1 to E5), the Lake's Ready,
  not-Ready and `setHeld` stamps (L1 to L3), and the Workflow's gate stamp, `notReady` stamp and comparison (W1 to W3).
  Each comparison mutant fails at the step after the field is cleared, which only the comparison can pass.
- **Scope.** Every hunk serves the defect. No condition, reason or phase changed. The Sensor test gained the
  `invalid input` row, the only path to the static-defect write. The Lake test gained the no-op check. No assertion was
  removed or weakened: the Sensor and Lake tests keep every earlier assertion.
- **Design check: `bug`.** ADR-0003 created the field without a rule on who fills it. ADR-0199 Decision 5 judges an
  App part by its `Ready` condition at its generation, and `judge` (`internal/app/status.go`) still reads only the
  condition. ADR-0139 gives a Site its success-only rule, and `internal/site` is unchanged; the Site and the Function's
  ingress read their Route's condition, not the field. No ADR on the five kinds' path decides the field: ADR-0108,
  0110, 0119, 0094, 0098, 0086 and 0087 do not name it, and ADR-0109 Decision 2 and ADR-0189 Decision 2 speak of the
  `Ready` condition's `ObservedGeneration`, which this fix keeps. The decided row of `docs/behavior/controller.md` ("A change
  reaches the queue") shows the field behind `metadata.generation` only while a change waits. An issue search, open
  and closed, for the field with each kind finds #148, #102 and #301, which concern the conditions; none accepts an
  empty field. No board card covers it. No test pinned the empty field: the touched packages' tests pass with the fix.
  No ADR file was edited.
- **Siblings.** Every other reconciler that stamps `Ready` with a generation already fills the field: SQLDatabase, the
  KVStore and Bucket health prober (`internal/health/prober.go`), Backup, the App at its switch, and the Function by
  its own rule. The other conditions stamped with a generation (`SeenListSaved`, `CatalogSaved`, `TargetSynced`) are
  not `Ready`, so the rule does not reach them.
- **Reuse.** The tests use each package's harness: `setup`, `seedFunction` and `seedRoute` for the Route; `newStore`,
  `createTimerSource`, `createBlobSource`, `createBucket`, `newBlobSource` and `getSource` for the EventSource;
  `storedWorkflow`, `apiKVStore`, `storeKV`, `reconcileByName`, `editWF` and `fakeContracts` for the Workflow. The
  Sensor and Lake tables already walked these paths, so they were extended rather than copied. The EventSource's new
  table covers its three Ready writers in one place; its `timer` row walks the same steps as
  `TestTimerSourceReadyObservesGeneration`, which checks only the condition, and that overlap was kept rather than
  spreading the field's assertions over three older tests. The code adds no helper, type or dependency; the paired
  assignment of phase and generation follows `internal/app/status.go`.
- **Conventions.** Each test has one doc comment that cites the field's description, and one why-comment on the step
  that clears the field. The Workflow test keeps its file's `t.Fatalf` style, the others `require`.
- **Checks.** On the branch: the pinned gofmt lists no file; `go build ./...` exits 0 on the host and with
  `GOOS=linux`; `go vet` exits 0 on the five packages on both; golangci-lint reports `0 issues` on both, the Linux run
  with the host binary as `scripts/agent/gate.sh` runs it; `go test -race` passes in `internal/route`, `internal/sensor`,
  `internal/eventing`, `internal/services/lake` and `internal/workflow` with their subpackages; `just check-hygiene` and
  `just check-behavior` pass. This review did not run the repo-wide gate, which runs once on the final branch.
- **Docs.** `docs/guide` and `docs/behavior` make no claim that the fix makes false. The networking guide says that a
  Route's `Ready` shows the new generation once funcd has processed a change, which still holds. The spec tables
  describe the field as the generation that funcd last acted on, which now holds for these kinds.
- **Commit shape.** One commit with a `fix(status):` subject. The body states the cause, the fix and the regression
  tests, has no closing keyword, and ends with the attribution trailer.

### Against the current main
origin/main is still a10c6541, the branch's base, so the results above hold on it.

### Definition of Done
12 / 12 items hold. This is the fix checklist with items 1 and 11 adapted to a fix that has no issue; item 13 does not
apply, because no design spec under `docs/design/` models these status writes. Misses: none.

### Model scorecard
claude-opus-5-5 on fix-status-observed-generation (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12, recorded in
`docs/reviews/model-ledger.json` as a self-review.

### Recommendation
Ready for its PR once `scripts/agent/gate.sh` has run once on the branch. The PR body needs no closing keyword, since
there is no issue.
