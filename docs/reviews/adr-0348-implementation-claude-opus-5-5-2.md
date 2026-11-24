# ADR-0348 implementation review, round 2: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 2 minors (ADR-0348 implementation, round 2, model: claude-opus-5-5)

This round reviews the three commits that follow round 1's `db2c9c1d`, up to `1c944088`:

- `ac45c0a6`: the guide page "Approve a run" and its tested manifest `api/types/v1alpha1/examples/job/approval.yaml`.
  The commit also adds the Job page's lines and the preview in `job.payloadLimit`'s doc. It changes how funcdctl
  detects a terminal: the go-isatty module decides, and a new test covers /dev/null.
- `428de99a`: shorter approval rows in `docs/behavior/jobs.md`.
- `1c944088`: the fixes of round 1.

Nothing else moved:

- the base, `feat/jobs` at `f7977e96`;
- the shim branches, pyvvo/funcd-typescript at `461f2f8` and pyvvo/funcd-python at `7af8ec1`, so their round-1 checks
  still hold.

funcd ran against the shims through the `go.work`, in a separate checkout at `1c944088`.

Every finding of round 1 is fixed, and a test or a mutant proves each fix. The guide page is accurate: each claim
matches the code, and its output is real. Two sentences in the docs overstate the code. This gate stamped ADR-0348
`Implemented` and F138 `implemented` in `8b2950da`.

### Round 1's findings

| Finding | Fix | Evidence |
|---|---|---|
| Major 1, the egress lane's Job | `1c944088`, `e2e/egress.venom.yml:498-499` | `approval:` and `bypass: true` sit under the heredoc's `spec`, at the right depth. The lane was not run. |
| Major 2, `funcdctl types` | `1c944088`, `pkg/sdk/types_gen.go` | `funcdctl types` now writes `phase: "run" \| "preview"`, an `Approval` type and `approval: Approval \| null` in TypeScript. Python gets the same, with `approved_by`, and the generated file parses. `TestGenerateJobTypes` pins both languages, and mutants m12 to m14 fail it. |
| Minor 1, /dev/null | `ac45c0a6`, `cmd/funcdctl/job.go:347` | `isatty` decides whether stdin is a terminal. `TestCLIJobApproveAndReject` opens `os.DevNull` and expects the `--digest` error. Mutant m11, which restores the character-device test, fails it. |
| Minor 2, the kind of the 403 check | `1c944088`, `TestJobRunApprovalAsksUpdateOnTheJob` | A stub authorizer records the request: update on the Job, in the run's namespace. Mutant m8b now fails the test. |
| Minor 3, the cancel row | `1c944088`, `docs/behavior/jobs.md:53` | The row now says that only a Running run's handler may have done part of its work. |

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The guide's Cancelled row says that the handler never ran** · model ·
  `docs/guide/develop/workloads/job/approval.md:227`. The row says: "The run was cancelled, or its Job was deleted or
  created again while the run waited. The handler never ran." The same table lists `Running`. A Running run that is
  cancelled, or whose Job is deleted, also ends `Cancelled`, and its handler may have done part of its work. ADR-0347
  Decision 6, `docs/behavior/jobs.md:53` and the Job page's Status table all say so. Fix: say this only of a run that
  was waiting, or drop the last sentence.
- **The shortened old-preview row overstates** · model · `docs/behavior/jobs.md:62`. The row says that "an update
  that keeps or names the old one is refused". But a cancel or a rejection of such a run also keeps the old
  `spec.approve`, and admission takes it: see `JobRunApproves` (`internal/controlplane/admission/jobrun.go:152`) and
  `TestJobRunApprovalAdmission`, which the row no longer cites. The round-1 row said so. Fix: add "but a cancel or a
  rejection is taken". The chapter's written part holds about 14.6 KB of its 16 KiB cap.

### Routed (not this work's)

- **Carried over from round 1, unchanged:**
  - the cause's name, `job.not-a-job-worker` against the Contract's `job.notAJobWorker`;
  - an update that keeps an unrecorded approval;
  - 512 bytes against 512 characters.
- **What the digest can be checked against** · `uncovered`, a correction to round 1. Round 1 said that a Python
  shim's answer has spaces. It has none: `jsonwire.encode` writes the separators that `JSON.stringify` writes, so
  both shims answer the same bytes. The guide's TypeScript and Python runs show the same digest for the same
  document: `sha256:9863a692…` is the sha256 of `{"month":"2026-10","rows":31}`. The mismatch comes from the store
  instead. Its `json.Marshal` (`internal/store/store.go:656`) escapes `<`, `>` and `&`, so a preview of
  `{"plan":"a -> b"}` is stored as `{"plan":"a -> b"}`, and the sha256 of the stored bytes is not the digest. The
  question for the decider stands: should a reader be able to recompute the digest from `status.preview.document`?
- **A fix that `feat/jobs` will also make** · elsewhere: the integration step. `1c944088` also turns the generated
  Python `RunInfo` into a `TypedDict` with the key `started_by`. That is the fix of ADR-0347's generator that
  `feat/jobs` is about to receive. Expect a conflict in `pkg/sdk/types_gen.go` and `pkg/sdk/contract_file_test.go` on
  the rebase, and keep one copy.

### Outside this gate, before the PR merges

- **The shims' release and the go.mod bump** (plan step 5). CI has no `go.work`, so it builds with the pinned shims,
  v0.34.0 and v0.29.0. Those shims call the handler in the preview call and have no `context.approval`. ADR-0348's e2e
  scenarios would therefore fail in CI. `TestScenarioGuideIncludesResolve` would fail too, since the `#preview`
  regions exist only on the shim branches. So the bump must land on this branch before the PR merges. If the released
  shims differ from `461f2f8` and `7af8ec1` in ADR-0348's parts, send me the diff.
- **The gate**: `just ci-full`, after the bump.
- **The feature issue**: the PR carries `Fixes #1016`.
- **ADR-0347**: the Done-when names it. It is still `Reviewing` on `feat/jobs`, and its code is in place on the base.

### ✅ Verified correct (keep it)

**The guide page** (`docs/guide/develop/workloads/job/approval.md`).

- Each claim matches the code:
  - the preview call and the contract checks on it;
  - `--digest`, and the error when there is no terminal;
  - the 409 that names the current digest;
  - the phases in which a run can be rejected, and the 512-byte limit on the reason;
  - the keys of `context.approval`, and when it is null;
  - HandlerFailed when the shim cannot read the approval;
  - the expiry, checked on the approval and when the slot frees;
  - the slot and restart rules;
  - admins and developers as the approvers.
- The page's output is real. Each `describe` listing has `renderJobRunDescribe`'s format. Each digest is the sha256
  of the document shown with it: `f854072e…` for November, and `34356905…` for December.
- Every link's anchor exists. Every `::include` resolves through the `go.work` (`TestScenarioGuideIncludesResolve`),
  and `approval.yaml` applies (`TestScenarioGuideExamplesApply`).

**The Job page** names `approval.bypass`, the new phases and Rejected. `job.payloadLimit` now names the preview in the
config's doc, in `examples/funcdconfig.yaml` and in the generated reference.

**go-isatty**:

- its license is MIT;
- it was already in the module graph and is now a direct dependency;
- `go mod tidy` without the `go.work` leaves `go.mod` and `go.sum` unchanged;
- the bloat audit hard-flags only a module that is new to `go.mod` (`scripts/agent/audit.py:657`), so this promotion
  passes;
- no other code in the repo checks for a terminal.

**The behavior rows** fit the cap, and `check-behavior` passes.

### Mutants

| Mutant | Edit | Result |
|---|---|---|
| m8b | admission authorizes update on JobRun, not on Job | killed (`TestJobRunApprovalAsksUpdateOnTheJob`) |
| m11 | `answerSource` again takes a character device for a terminal | killed (`TestCLIJobApproveAndReject`) |
| m12 | the TypeScript `RunInfo.phase` goes back to `"run"` | killed (`TestGenerateJobTypes`) |
| m13 | the TypeScript `JobContext` loses `approval` | killed (`TestGenerateJobTypes`) |
| m14 | the Python `JobContext` loses `approval` | killed (`TestGenerateJobTypes`) |

Each mutant was undone with `git checkout -- <file>`, and the tree ended clean. Round 1's 13 mutants cover code that
these commits do not change.

### What ran, on `1c944088`

**Build and modules**

- `go build ./...`: exit 0.
- `go mod tidy` with `GOWORK=off`: no change to `go.mod` or `go.sum`.

**The touched packages** are `cmd/funcdctl`, `internal/controlplane/admission`, `pkg/sdk`, `internal/platform/config`,
`api/types/v1alpha1` and `cmd/funcd`.

- `go vet` on them: exit 0. With `-tags dev` on `cmd/funcdctl`: exit 0.
- `golangci-lint run` on them and on `pkg/funcd`: exit 0, with 0 issues. With `--build-tags dev ./cmd/funcdctl`: exit
  0.

**Tests**

- `go test -race -count=1` on the touched packages and on `internal/function`, `internal/controlplane`,
  `internal/workernode/local`, `internal/contractcheck` and `internal/invoke`: exit 0.
- `go test -tags dev -run 'Job|CommandSpec' ./cmd/funcdctl/`: exit 0.
- `go test ./scripts/`, the lanes' guards: exit 0.
- `go test -race -run` on the guide tests, `TestJobCauseMatrix` and `TestEveryDeclaredCauseHasARow` in `pkg/funcd`:
  exit 0, in 76 s.
- `go test -tags e2e -race -count=1 -v` on ADR-0348's 11 scenarios and `TestScenarioJobRunsHaveTheirOwnSockets`: exit
  0, at 1.3 to 2.6 s each.

**The repo's checks**

- `just check-behavior`, `just check-hygiene` and `just check-specs`: each exit 0. `check-specs` reports 27 specs with
  none failing, and reused its cached result for the unchanged `approval.fizz`.
- `just generate`: exit 0, and the tree stayed unchanged.
- The stamp commit's hooks, the behavior check and the commit-message check: passed.

**Not run**:

- `just ci-full`, the whole e2e suite and `scripts/agent/gate.sh`;
- the Lima lanes and `just test-linux`;
- the shims' checks, since the shims did not change.

### Definition of Done

17 of the 19 items hold:

- **The ADR's Review checklist**: 6 of 6 hold.
- **The ADR's plan and its Done-when**: 3 of 5 hold, namely `check-specs`, the behavior rows and the guide page. The
  shims' release and the go.mod bump, and `just ci-full`, are still open. Both are outside this gate, and neither
  counts against the model.
- **The generic DoD**: 8 of 8 hold.

### Stamp

The stamp is `8b2950da docs(adr): ADR-0348 Implemented, and its feat row F138 implemented`, on `feat/jobs-approval`
over `1c944088`. It edits two lines:

- the ADR's Status line, which now begins `Implemented (2026-11-24), review passed in round 2 with two Minors; Accepted
  (2026-11-23)`;
- F138's status cell, which is now `implemented`.

Over the whole branch, that Status line is the ADR's only change.

When `feat/jobs` moved to `017495fc` (ADR-0347 Implemented), the branch was rebased onto it, and the stamp became
`dce8f134`. Five commits followed it, `491b96a3` to `5fdfa7c8`, and this review does not cover them. None of them
touches the ADR. `491b96a3` fixes both Minors of this round: `docs/behavior/jobs.md:62` now ends with "unless it
cancels or rejects the run", and the guide's Cancelled row now says that a Running run's handler may have done part of
its work.

### The released shims (checked after the stamp)

The released shims keep ADR-0348's parts. funcd-typescript v0.35.0 is `6c3ea11` plus version files only, and
funcd-python v0.30.0 is `210aae8` plus version files only.

In ADR-0348's path, the range changes one helper: `refusal` in `shim/src/job.ts` and `_refusal` in
`shim/src/funcd_shim/job.py`. A JSON refusal with no `detail` now keeps the body's text as its detail, where the detail
was empty before. This does not change ADR-0348's behavior:

- `context.approval`'s read of `GET /job/run` still throws on any status but 200, so a run whose approval cannot be
  read still fails before its handler, with `HandlerFailed`;
- funcd's 403 problem carries a detail, so its message is unchanged;
- the committed TS bundles carry the same change, and they match a fresh esbuild build.

What ran, each exit 0:

- at `6c3ea11`: the TS shim's lint, typecheck and tests, with 243 passing;
- at `210aae8`: the Python shim's `just check` and `just go-check`;
- at the branch tip `5fdfa7c8`, with the `go.work` on those two commits: ADR-0348's 11 e2e scenarios and
  `TestScenarioJobRunsHaveTheirOwnSockets` under `-race`, and `TestScenarioGuideIncludesResolve`.

### Model scorecard

Not recorded: the batch's ledger rows go in one commit. The arguments:

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0348 --phase implementation --model claude-opus-5-5 \
  --verdict pass \
  --blockers 0 --majors 0 --minors 2 \
  --model-attributed 2 \
  --dod-passed 17 --dod-total 19 \
  --report docs/reviews/adr-0348-implementation-claude-opus-5-5-2.md \
  --notes "round 2 pass; round 1's 2 Majors and 3 Minors fixed, each proven (mutants m8b, m11-m14 killed); guide page checked claim by claim, real output; Minors (model): the guide's Cancelled row says the handler never ran, the shortened old-preview row omits that a cancel or a rejection is taken; routed: digest vs the store's re-encoded document (corrected: both shims send compact JSON), the started_by generator fix will meet feat/jobs; open outside the gate: shim release and bump, ci-full"
```

### Recommendation

Nothing has to go back to the builder. Each Minor is one sentence, and the builder can fix both on this branch before
the PR. Before the PR merges, release the shims, bump go.mod and run the gate. The PR carries `Fixes #1016`.
