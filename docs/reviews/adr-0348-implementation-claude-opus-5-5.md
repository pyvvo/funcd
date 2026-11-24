# ADR-0348 implementation review: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 2 majors (ADR-0348 implementation, model: claude-opus-5-5)

Reviewed:

- funcd `feat/jobs-approval` at `db2c9c1d`, the 7 commits on `feat/jobs` (`f7977e96`).
- The ADR-0348 parts of pyvvo/funcd-typescript `feat/job-approval-start` at `461f2f8` and of pyvvo/funcd-python
  `feat/job-approval-start` at `7af8ec1`. The `ctx.job` client in the same commits belongs to ADR-0349 and is not judged
  here.

funcd ran against the two shim branches through a `go.work`. During the review the builder's checkout held
uncommitted edits, and the builder then pushed `ac45c0a6` and `428de99a`. So every check and mutant ran on `db2c9c1d`
in a separate checkout, and those two commits belong to the next round.

The decision is built soundly. A run previews in a slot and waits holding none. It runs only on an approval that the
API server recorded for its current preview, within the expiry, which is checked whenever the run takes a slot. A
rejection wins over an approval. Both shims call the preview export and fill `context.approval` from `GET /job/run`.
The work has two misses, and each is a few lines:

- the egress lane's inline Job now waits for an approval;
- funcd's own type generator still types a Job's context as it was before this ADR.

Not judged yet: the guide page (plan step 5), and the shims' release and the go.mod bump. No status is stamped.

### 🔴 Blockers

None.

### 🟡 Major 1: the egress lane's Job now waits for an approval that never comes · attribution: model

The step `job-egress-named` (`e2e/egress.venom.yml:482-536`) is ADR-0351 Decision 6's case on the lane. It applies Job
`egress-probe` with no `spec.approval` (lines 489-497). It then starts a run and polls for 60 s for `Succeeded`,
`Failed` or `Cancelled`. It expects `Succeeded true` (line 536) and retries 10 times.

Since Decision 1, a Job without `approval.bypass` previews and then waits in `AwaitingApproval` with no deadline. The
ADR's Consequences say that a Job for unattended work sets `bypass: true`. On this branch,
`TestScenarioJobRunWaitsForApproval` proves that wait: the run stays AwaitingApproval and its handler never runs. So
every try of the step ends with "did not end", and the step fails.

The builder set bypass for the job lane's sleeper (`e2e/fixtures/job-sleep.yaml`, `f8b26032`) and in every Go test
rig, but not for this inline Job. No code sets bypass by default: `git grep` for `Bypass` finds only the field and the
reconciler's check. The lane was not run: Lima lanes are outside this review.

Fix (builder): add `approval: bypass: true` to the Job in that step, as `job-sleep.yaml` does.

### 🟡 Major 2: `funcdctl types` still types a Job's context as ADR-0347 did · attribution: model

By ADR-0347 Decision 9, `pkg/sdk/types_gen.go` writes a typed `JobContext`. ADR-0348 changes that context in two ways:

- Decision 6 makes `context.run.phase` `preview` in the preview call;
- the Contracts give `RunInfo.phase` `preview` or `run`, and add `JobContext.approval: Approval | None`.

The generator is unchanged. `pkg/sdk/types_gen.go:142-143` (Python) and `:232-233` (TypeScript) still write
`phase: "run"` and a `JobContext` that holds `run` alone. `pkg/sdk/contract_file_test.go:153,161` pins that output.
`funcdctl types -f report.contract.yaml`, run on a scratch copy of job-report's files, wrote:

```ts
export interface RunInfo {
  name: string;
  job: string;
  revision: string;
  phase: "run";
  startedBy: string;
}

export interface JobContext {
  run: RunInfo;
```

A handler typed with this output cannot read `ctx.approval`, and a `preview` cannot compare `ctx.run.phase` with
`"preview"`. The shims' own types are right. The plan does not name `pkg/sdk`, but by ADR-0347's decision the
generator mirrors the shims' `JobContext`.

Fix (builder):

- type the phase as `"run" | "preview"` in TypeScript and `Literal["run", "preview"]` in Python;
- add an `Approval` type of `approvedBy` (`approved_by` in Python), `digest` and `preview`;
- add `approval: Approval | null` (`Approval | None`) to `JobContext`;
- update the test.

### Minor

- **`job approve` treats /dev/null as a terminal** · model · `cmd/funcdctl/job.go:340-351`. `answerSource` asks for a
  confirmation whenever stdin's mode has `os.ModeCharDevice`, and /dev/null is a character device: a probe printed
  `stdin char device: true` for `< /dev/null`. So `funcdctl job approve <run> < /dev/null`, as under cron or
  systemd, prints the question, reads EOF, prints `not approved` and exits 0. Decision 7 asks only on a terminal, and
  in that case the command should fail with the message that names `--digest`. The command fails safe, since nothing
  is approved. Later commits change this code, and the next round judges them.
- **No test pins the Job kind of the 403 check** · model · `internal/controlplane/admission/jobrun.go:216`. Mutant
  m8b, which authorizes `update` on `JobRun` instead of `Job`, survives every unit and e2e test. The built-in roles
  grant both updates together, and the API server's own JobRun check refuses the viewer of
  `TestScenarioJobApproveRefusedToViewer` before admission runs. A stub authorizer in `TestJobRunApprovalAdmission`
  that records the request would pin Decision 3's `auth.VerbUpdate` on `Job`.
- **The cancel row misstates a preview** · model · `docs/behavior/jobs.md:53`. The row says of a Running or
  Previewing run that "the handler may have done part of its work". A Previewing run's handler never ran: the code's
  own message says "the preview's worker was stopped, and the handler never ran". The row should say this of a
  Running run only.

### Routed (not this work's)

- **The cause's name** · elsewhere: the decider (ADR-0348's text). The Contracts name `job.notAJobWorker`, and
  `internal/workernode/local/job.go:21` declares `job.not-a-job-worker`. The code follows the kebab form of every
  other local API cause under ADR-0330's `<package>.<name>` rule, such as `local.addon-no-binding`. The builder has
  nothing to change. The decider may confirm the name, and may say whether the prefix should be `local.`, as
  ADR-0330's `<package>` would have it.
- **An update that keeps an old approval** · elsewhere: the decider (the text of Decision 3). Decision 3 checks "an
  update that sets or changes `spec.approve`". But `spec.approve` is never cleared, so in `job-approve-outdated-digest`
  an approval of d1, once the preview is d2, leaves the field unchanged. `admission.JobRunApproves`
  (`internal/controlplane/admission/jobrun.go:152`) therefore also treats as an approval an update that keeps a
  `spec.approve` with no recorded approval of the current preview. The scenario gets its 409 naming d2, and a cancel
  or a rejection is still taken. Any other update of such a run, a change of its tags for example, is refused with 409
  until someone approves, rejects or cancels the run. The field's doc and a row of `jobs.md` state this. The reading
  is sound, and only the decider can widen the text.
- **What the digest can be checked against** · above: a question for the decider (`uncovered`). The digest is
  "`sha256:` of the answer's bytes", as Decision 2 says. The stored `document` is re-encoded, compacted and
  HTML-escaped. A probe ran `encoding/json` on the branch's `JobRunPreview` with a Python shim's answer,
  `{"month": "2026-10", "plan": "a -> b"}`. The stored document came back as
  `{"month":"2026-10","plan":"a -> b"}`, and its sha256 did not equal the digest. Approvals still work, because
  funcd compares digests that it computed itself. But a person who reads `status.preview.document` cannot recompute
  the digest from it. Whether they should be able to is not decided.
- **512 bytes or 512 characters** · elsewhere: the ADR's text. Decision 5 limits `spec.rejectReason` to 512 bytes,
  and `Validate` counts bytes. The Contract's tag `maxLength:"512"` makes the generated reference say "0 to 512
  characters". So a reason of 200 emoji is within the reference's limit and is still refused. The code follows both
  texts of the ADR.

Pending by the plan, and not findings:

- the guide page;
- the shims' release and the go.mod bump. Until the bump lands, the `job` lane (`scripts/lanes.yaml`) stages
  job-report's `report.yaml` from the pinned v0.34.0 and v0.29.0, which have no `approval.bypass`. So the bump must
  ship with this branch.

### ✅ Verified correct (keep it)

**The contracts match, field for field.**

- `JobApproval`: bypass, and an expiry of 1m to 168h, 24h unset, with named constants.
- The run's spec: `approve` (its pattern, and a check in `Validate`), `reject` and `rejectReason`.
- The run's status: `preview`, `approvedBy`, `approvedAt` and `rejectedBy`, and `JobRunPreview`.
- Phases and reasons: the three phases, `Ended()` with Rejected, and the reasons PreviewFailed, PreviewExpired and
  Rejected.
- The invoke chain: `invoke.JobPreview` comes after `JobRun`, and contract-output skips it
  (`TestAPreviewAnswerIsNotChecked`).
- The local API: `local.JobFacet(JobRunReader)` answers the Contract's JSON (`TestGetJobRunAnswersTheCallersOwnRun`),
  and the facet's cause has its matrix row.

**The run reconciler follows Decisions 2 to 5.**

- `take` stores a run Previewing, with its pin and its trace, before its executor starts (`storedOf`).
- A Previewing run holds a slot and an AwaitingApproval run holds none (`holdsSlot`, `waitsForSlot`, `MapRun`).
- The expiry is checked in `take`, so on the approval and when the slot frees.
- A preview made again with the approved digest goes Running in the same slot; another digest clears `approvedBy` and
  `approvedAt`.
- A rejection is checked before an approval, both in `pass` and in `previewing`.
- `Boot`, and a pass that finds no executor, put a Previewing run back to Pending and keep its approval.
- A waiting run whose Job or pin is gone ends `JobDeleted`.
- The revision sweep keeps the pins of the new phases, and a restore ends their runs `Restored`; both go through
  `Ended()`.

**Admission and the API server follow Decisions 3 and 5.**

- A create that carries an approval or a rejection is refused with 422, naming each field.
- An approval of another preview gets a 409 that names the current digest.
- A caller whom the RBAC authorizer does not let update the Job gets a 403.
- A rejection is one-way, and an approval is never cleared.
- The server stamps `approvedAt`, `approvedBy` and `rejectedBy` inside the conditional retry of `replaceObjWith`, and
  never takes them from the body: `TestJobRunApprovalThroughTheAPI` sends `mallory` in the body and reads back `dev`.

**funcdctl follows Decision 7.**

- `approve` prints the indented document and the digest, asks on a terminal, refuses a pipe with the message that
  names `--digest`, and approves `--digest` with no question.
- `reject` checks the 512-byte limit.
- `describe` shows the preview, `approvedBy` and `rejectedBy`.

**Both shims follow Decision 6.**

- They call `preview(context, event)` with the phase `preview` and a null approval.
- With no preview export they answer the input, and 204 when the run has none.
- They read `context.approval` from `GET /job/run` in the run's call only, and fail the call before the handler when
  they cannot read it.
- They refuse a preview export that is no function at the shape gate.
- Both job-report examples export a `preview` and set `approval.bypass`.
- The committed TS bundles equal a fresh esbuild build of `shim.mjs` and `pool.mjs`.

**The ruling on ADR-0351.** Ruling A is met: in `TestScenarioJobRunsHaveTheirOwnSockets`, each of two Running runs
reads its own run. Mutant m7, a reader that answers the Job's lowest-named Running run, fails that assertion in 3 of 3
runs.

**The design model.**

- Each of the five assertions has an oracle marker on a passing e2e test, as the ADR's table names them, and three of
  them also on a unit test.
- `check-specs` passes `approval.fizz` (168 states), and each of its six mutants fails the assertion it names.

**The e2e suite stays fast.** Every new scenario calls `t.Parallel()` and takes 1.6 to 3.4 s under `-race`, and no
timeout was raised.

**Rows and generated files.**

- The behavior rows cover each decision; `check-behavior` passes.
- `just generate` leaves the tree unchanged: the OpenAPI, the guide's spec pages, and the config and command specs
  are current.
- The ADR's only edit is its Status line, and F138 is `reviewing`.

### Mutants on the code

Each mutant was applied in the review checkout, run, and undone with `git checkout -- <file>`; the tree ended clean.

| Mutant | Edit | Unit | e2e |
|---|---|---|---|
| m1a, the spec's `apply-unmatched` | `previewing` runs a preview made again whose digest differs | killed (`TestJobRunStalePreviewIsPreviewedAgain/another_document`) | killed (`TestScenarioJobStalePreview/another_preview_at_the_approval`, `TestScenarioJobApproveOutdatedDigest`) |
| m1b, the spec's `apply-unmatched` | admission drops the digest comparison | killed (`TestJobRunApprovalAdmission`, `TestJobRunApprovalThroughTheAPI`, `TestCLIJobApproveAndReject`) | killed (`TestScenarioJobApproveOutdatedDigest`) |
| m2, the spec's `apply-stale` | `take` never previews again | killed | killed |
| m3, the spec's `no-slot-time-check` | `take` checks the expiry only for a run AwaitingApproval | killed (`TestJobRunExpiryIsCheckedWhenTheSlotFrees`) | killed (`stale_when_the_slot_frees`) |
| m4, the spec's `approve-after-reject` | `pass` rejects only a run without an approval | killed (`TestJobRunRejection`) | survived: the e2e test rejects a run without an approval |
| m5, the spec's `approve-at-create` | admission skips the create check | killed | killed (`TestScenarioJobApproveAtCreation`) |
| m5b | `Approved()` ignores `approvedAt` | killed (`TestJobRunApproved`, `TestJobRunApprovalNotRecordedStartsNothing`, `TestGetJobRunAnswersTheCallersOwnRun`) | survived: no e2e path can write an unrecorded approval |
| m6, the spec's `no-repreview` | `take` leaves a stale approved run Queued | killed | killed |
| m7 | the reader answers the Job's lowest-named Running run | survived: the unit test uses a fake reader | killed 3/3 (`TestScenarioJobRunsHaveTheirOwnSockets`) |
| m8 | admission's 403 removed | killed (`TestJobRunApprovalAdmission`) | survived: the API server refuses the viewer first |
| m8b | admission authorizes `update` on JobRun, not Job | survived | survived (Minor 2) |
| m9 | `Boot` ends a Previewing run Interrupted | killed (`TestJobRunBootPreviewsAgain`) | killed (`TestScenarioJobRestartPreviewsAgain`) |
| m10 | a waiting run skips the gone check | killed (`TestJobRunWaitingRunOfAGoneJob`) | killed (`TestScenarioJobRecreatedCancelsWaitingRun`) |

### What ran, on `db2c9c1d`

**funcd**

- `go build ./...`: exit 0.
- `go vet` on the 9 touched packages: exit 0. `go vet -tags e2e ./pkg/funcd/`: exit 0.
- `golangci-lint run` on the touched packages: exit 0, 0 issues. With `--build-tags dev ./cmd/funcdctl`: exit 0.
- `golangci-lint run --build-tags e2e ./pkg/funcd/...`: exit 1. Both hits (`gochecknoglobals`) are in files that this
  branch does not touch, `secret_policy_e2e_internal_test.go:80` and `secret_write_e2e_internal_test.go:34`, and CI
  does not lint with the e2e tag.
- `go test -race -count=1` on `api/types/v1alpha1`, `cmd/funcdctl`, `internal/contractcheck`,
  `internal/controlplane`, `internal/controlplane/admission`, `internal/function`, `internal/invoke` and
  `internal/workernode/local`: exit 0.
- `go test -race -count=1 ./pkg/funcd/`: exit 0 (187 s).
- `go test -tags dev -race -run 'Job|CommandSpec' ./cmd/funcdctl/`: exit 0.
- `go test -tags e2e -race -count=1 -v` on ADR-0348's 11 scenarios and `TestScenarioJobRunsHaveTheirOwnSockets`: exit
  0, from 1.57 s (`ApproveAtCreation`) to 3.43 s (`RestartPreviewsAgain`).
- `just check-behavior`: exit 0. `just check-specs`: exit 0, 27 specs and none failing. `just check-hygiene`: exit 0.
- `just generate`: exit 0, with the tree unchanged.

**pyvvo/funcd-python**

- `just check` (ruff, mypy, pytest): exit 0; the shim's pytest passed 525 tests, and every example passed.
- `just go-check`: exit 0. The tree stayed clean.

**pyvvo/funcd-typescript**

- `just lint`, `just typecheck`, `just test` (240 tests passed) and `just go-check`: each exit 0.
- An esbuild build of `shim.mjs` and `pool.mjs` into a scratch folder was identical to the committed files. The tree
  stayed clean.

**Not run**: `just ci-full`, the whole e2e suite, `scripts/agent/gate.sh`, the Lima lanes (egress, job) and
`just test-linux`.

### Definition of Done

14 of the 19 items hold:

- **The ADR's Review checklist**: 6 of 6 hold.
- **The ADR's plan and its "Done when"**: 2 of 5 hold, `check-specs` and the behavior rows. Three are open, and none
  of them counts against the model: `just ci-full` is the parent's gate, and the shims' release and bump and the
  guide page come later in the plan.
- **The generic DoD**: 6 of 8 hold. The misses are the Contracts (Major 2) and the complete tree (Major 1), both
  model.

### Model scorecard

Not recorded: the batch's ledger rows go in one commit. The arguments:

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0348 --phase implementation --model claude-opus-5-5 \
  --verdict changes-requested \
  --blockers 0 --majors 2 --minors 3 \
  --model-attributed 5 \
  --dod-passed 14 --dod-total 19 \
  --report docs/reviews/adr-0348-implementation-claude-opus-5-5.md \
  --notes "M1 egress lane's Job egress-probe lacks approval.bypass, so job-egress-named waits AwaitingApproval (model); M2 funcdctl types keeps RunInfo.phase run-only and no JobContext.approval (model); m1 approve treats /dev/null as a terminal (model); m2 the 403's Job kind is unpinned, mutant m8b survives (model); m3 jobs.md cancel row says a preview's handler may have run (model); routed: cause name vs ADR-0330, keeping an unrecorded spec.approve is an approval, digest vs re-encoded document (uncovered), rejectReason bytes vs maxLength; pending: guide page, shim release and bump"
```

### Recommendation

The builder fixes Majors 1 and 2 and the three Minors. Then send the branch back with the guide page, and with the
shims' release and the go.mod bump once they land. The `Implemented` stamp waits for the whole plan. The four routed
items need the decider's word, but none of them holds up this work.
