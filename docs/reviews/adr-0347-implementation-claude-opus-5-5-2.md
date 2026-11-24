## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0347 implementation, round 2, model: claude-opus-5-5)

Round 2 reviews the branch `feat/jobs` at f7977e96, rebased onto origin/main (db7cb569) without afe2468a, which main
holds. `git range-diff` maps the 18 commits of round 1 ([report](adr-0347-implementation-claude-opus-5-5.md)) onto
the rebased ones, each with an equal patch. After them come the owner-kind bump d05267aa and round 2's seven commits:
the fixes, and the pin f7977e96 of funcd-typescript v0.34.0 and funcd-python v0.29.0, which carry
`examples/job-report`. The reviewer did not write the work, and every check below was run, not read.

Round 1's Major and its five Minors are fixed, and so are ADR-0351's three Minors (ADR-0351 passed in round 1). The one
new finding is a Minor in the lane's plumbing.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The plumbing for a lane with no `dir` serves no lane** · model · `scripts/lane.py:70-74`, the note at
  `scripts/lanes.yaml:13-14`, and `e2e/README.md:24`. Round 1 added the plumbing for the job lane, which then had no
  `dir`. Round 2 gives that lane `module` and `dir` back, so no lane in `scripts/lanes.yaml` lacks one, and the branch and
  its note are left over. The README's row for `job.venom.yml` does not name the new job-report case.

### Round 1's findings

| Finding | Round 2 | Evidence |
|---|---|---|
| P1 (Major): the first example had no handler and no contract | fixed: f1369484, f7977e96 | The page includes the job-report handler, contract and Job of each language repo (`docs/guide/develop/workloads/job/index.md:43-166`), with the commands that clone, push, apply, run, describe, read the logs and cancel. The Examples section links both folders (`:275-280`). The job lane stages both examples and runs them in its new case job-report-examples (`scripts/lanes.yaml`, `e2e/job.venom.yml`). |
| `jobNeedsContract`, a constant `true` (Minor) | fixed: 86fc9a22 | The case reads `fault.KindOf(err) == fault.NotFound && r.requiresContract()` (`internal/function/job.go:150`). The comment names the amendment. |
| P2: StartFailed's message (Minor) | fixed: 93fd51aa | The row lists each cause and names the binding only when the last readiness answer named one (`index.md:241`), as `jobexec.go`'s `ready` and `startFailed` do. |
| P3: "two of its own" (Minor) | fixed: 93fd51aa | "with a Function's reasons, such as `ContractMissing` … and `ContractInvalid`" (`index.md:251-253`). |
| P5: the order of a run's end (Minor) | fixed: 93fd51aa | funcd "stops and removes the worker at every end, and then writes the run's end" (`platform/workloads/job.md:13-15`), as `endWorker` returns before the pass writes the end. |
| P7: a run created before its Job (Minor) | fixed: 93fd51aa | Only a run that holds the Job's UID ends `JobDeleted`; one created before its Job, not started yet, waits with `JobNotFound` (`index.md:262-264`, `jobrun.go:157`). |

### ADR-0351's three Minors, checked

| Finding | Round 2 | Evidence |
|---|---|---|
| A run re-created under a reused name could lose its socket | fixed: c6ef67ed | `endWorker` stops the worker, then removes its socket, its egress index entry and its emptyDirs, and removes the worker last (`internal/function/jobexec.go:330-356`). The runtime port's `List` omits an instance only after `Remove` (`internal/runtime/runtime.go:160-162`), so `nameFree` lets a new run of the name bind only once the old socket is gone. `TestJobRunReusedNameMeetsNothingOfTheOldRun` holds the old run's index removal, as round 1's probe did. It checks no bind while the old worker exists, the new bind after it, and the old emptyDir released while its worker still exists. It passes with `-race`, and fails at its first check on round 1's `jobexec.go` (an overlay run). |
| A KV table's owner kind was not narrowed | fixed: 0cf6ae50 | `validateTables` takes only `Function` and `Job` (`api/types/v1alpha1/kvstore.go:132-134`). An overlay probe shows a KVStore, a Workflow's declared store and a Job's declared store each refusing `{kind: Lake}`, with the entry's path, and refusing a kind such as `App` and a name that is no label. `TestOwnersNameTheirKind` gains both table cases. The Workflow's own check of a Lake owner is gone, since the shared one covers it. |
| `JobSpec.FunctionSpec`'s doc comment was split | fixed: 9303b8b4 | The method's comment is whole above it (`api/types/v1alpha1/job.go:95-97`). |

### Page critic (round 2)

The two pages were checked again against the code at f7977e96, `template.yaml` and `.claude/rules/docs-guide.md`. Every
finding of round 1 is fixed, and no new one is open. The facts were checked against the code:

- **The examples**: the regions that the page includes exist in the pinned modules: `src/main.ts#handler`,
  `report.contract.yaml#contract`, `report.yaml#job`, and their Python siblings. The TypeScript example commits its
  build `report.mjs`. The Python handler imports the standard library and `funcd_shim`, which the runtime provides,
  so one file pushed beside its contract runs.
- **The 422 that `{"day":1}` gets**: the input schema reports `/day` and `/month` (an overlay probe of
  `internal/contract`), which admission names `body.spec.input.day` and `body.spec.input.month`.
- **The settings and outputs**: `FUNCD_PYTHON` is the interpreter variable (`cmd/funcd/main.go:1730`). Each describe
  output matches `renderJobRunDescribe`, and the cancel message matches the run reconciler's.
- **The Examples links**: they point at main, at the folders that the job lane runs.
- **The tests**: `TestScenarioGuideIncludesResolve`, `TestScenarioGuideExamplesApply` and the guide-template tests
  pass. `TestScenarioGuideExamplesApply` no longer takes an included contract file for a manifest (the new
  `laneManifest`, with `TestContractIsNoGuideManifest`).

The site build and the fresh reader follow, outside this review.

### ✅ Verified correct (keep it)

- **The checks**, each through `scripts/agent/d`, with no go.work: `go build ./...` for darwin and linux; `go vet`,
  also for linux and with `-tags e2e`; and `go tool golangci-lint run`, 0 issues, also with `--build-tags dev` and for
  linux. These ran on `api/types/v1alpha1`, `internal/function`, `pkg/funcd`, `cmd/funcd`, `cmd/funcdctl`,
  `internal/testkit/langmod` and `./scripts/...`. `go test` on the same set exits 0, and so does `-tags dev` on
  `cmd/funcdctl`. That run covers the lane guards (`TestScenarioLaneStagesResolve`,
  `TestLaneDaemonUnitsStopLikeTheInstalledUnit`, `./scripts`' lane tests) and `TestExampleResourcesDecode`, which now
  passes on the pinned tags.
- **The scenarios**: `go test -race -tags e2e -count=1 -run 'TestScenarioJob' ./pkg/funcd/ -v` exits 0, with all 18
  of ADR-0347's and ADR-0351's. The longest is `TestScenarioJobTimeout` at 3.10 s, and the package takes 5.5 s.
  `go test -race` on the Job tests of `internal/function` exits 0 (31 tests).
- **The real platform**: the job lane passed on containerd on the lane commit before round 2. Round 2 keeps each
  worker's `Stop` before its `Remove`, and moves only funcd's own cleanups between them. The rerun on this tip, with
  the job-report cases, is in the coordinator's lane run, outside this review.
- **What round 1 verified** stands: the 18 commits are unchanged, and round 2 touches only the executor's cleanup, the
  contract case, the table owners, a doc comment, the pages, the lane and the pins.

### Definition of Done

22 of 22 items hold: round 1's 21, and the guide page of plan step 6, whose first example now runs on tested code in
both languages. `just ci-full` runs in the gate next.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0347 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 22/22. See
`docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0347 is stamped `Implemented`, and F137 turns `implemented`. The lane Minor can go with any later change, and it
blocks nothing. Feature issues #1015, #1018 and #1027 are the ones that the implementing PR closes. If the
coordinator's lane rerun fails on a case that round 2 changed, the fix goes in as a commit on this branch, and the
decision stays as it is.
