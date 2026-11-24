# ADR-0348: A run waits for a person's approval of its preview, unless its Job says bypass

- **Status**: Reviewing (2026-11-24, implemented); Accepted (2026-11-23) on the decider's go, after two judge rounds with every Blocker and Major folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: jobs, approval, control-plane, local-api, cli
- **Realizes**: [FEAT-0012/F138](../feat/0012-feat-infrastack.md) (approval of a run)
- **Relates to**: [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (the run, which
  this ADR extends with phases before `Running`) · [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md)
  and [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (a local API facet and its causes) ·
  ADR-0094 (declarative intents on a run) · ADR-0303 · ADR-0148 · F140 (the Job's identity) · F147 to F149

## Context & Need

A stack update or a backfill can destroy data, so a person must see the change before it runs, and the platform must
keep the record (F138). F138 decided the rule: a run shows what it would change and waits for a person's approval,
unless its Job says `bypass`; an approval names the preview it approves, so exactly what was previewed runs; a
person can reject a run, which ends it; a preview older than the Job's expiry is previewed again when someone approves
it, not on a timer; in the first slice whoever may update the Job approves its runs. #1015's inputs (2026-11-23) add
the shape: a `preview` export called with `funcd.job.preview`, its answer kept with its digest, and the approval
passed to the handler.

## Scenarios

- `scenario: job-run-waits-for-approval`: Given a Job `tofu` without `approval.bypass`; when a run is created; then it
  goes `Previewing`, then `AwaitingApproval` with `status.preview` (digest, document, time), and `handle` has not run.
- `scenario: job-approve-runs-the-preview`: When `funcdctl job approve <run> --digest <d>`; then the run ends
  `Succeeded`, and its handler's `context.approval` holds the approver, `d` and the preview's document.
- `scenario: job-approve-outdated-digest`: Given a run whose preview is now `d2`; when someone approves `d1`; then the
  API answers 409 naming `d2`, and nothing changes.
- `scenario: job-approve-at-creation-refused`: When a run is created with `spec.approve` set; then 422, and no run.
- `scenario: job-approve-refused-to-viewer`: Given a viewer of the namespace; when they approve or reject; then 403.
- `scenario: job-stale-preview-previewed-again`: Given a preview older than `approval.expiry`; when it is approved, or
  when its slot frees after the approval; then the run previews again, and runs when the new preview has the same
  digest, or waits for a new approval when it differs.
- `scenario: job-reject-ends-run`: When `funcdctl job reject <run> --reason "too wide"`; then the run ends `Rejected`
  with that reason and `rejectedBy`, and `handle` never ran.
- `scenario: job-bypass-runs-at-once`: Given `approval.bypass: true`; then the run calls no preview, runs at once,
  and its `context.approval` is none.
- `scenario: job-restart-previews-again`: Given a `Previewing` run; when funcd restarts; then the run previews again.
- `scenario: job-without-preview-export`: Given a module that exports no `preview`; then the preview is the input.
- `scenario: job-recreated-cancels-waiting-run`: Given an approved run waiting for a slot; when its Job is deleted
  and created again; then the run ends `Cancelled` with `JobDeleted`, and no handler is called for it.

## Scope

In: the Job's `approval`; the preview call and its record; approve, reject and expiry; the approval in the
handler's context and the local API's `GET /job/run`; `funcdctl job approve` and `reject`.

Out: policies on who may start, approve, reject or set `bypass`, and the params a run overrides (F147 to F149); a
person as a principal (F147); the record of who wrote each generation (F149); a reminder or a deadline for a waiting
run; the Workflow gates of FEAT-0005/F66.

## Constraints & Decision drivers

- Exactly what was previewed runs, a newer preview needs its own approval, and only a person approves (F138).
- Expiry is checked on an approval, never on a timer (F138).
- The approver is whoever may update the Job, checked by its roles, and the run records who (F138, first slice).
- A run's handler is called at most once (ADR-0347); a preview changes nothing the run applies.

## Alternatives considered

- **An approve endpoint** (`POST …/jobruns/{name}/approve`): a verb of its own beside the CRUD surface. Rejected for a
  declarative field, as a run's `cancel` and a workflow run's `paused` are (ADR-0094).
- **The approval as a boolean**: it would approve whatever the run previews by then. Rejected: it names a digest.
- **Expiry on a timer**, previewing again as the clock passes it: F138 rules it out. Rejected.
- **The preview and the apply in one worker**, kept alive while the run waits: a wait can last the whole expiry, and a
  worker holds memory and a slot. Rejected: each call gets its own worker, of the same pinned revision.
- **The approval in the event's data or the worker's env**: a preview of up to `job.payloadLimit` fits neither.
  Rejected for the local API (#1015's input).

## Decision

1. **The Job's approval.** `spec.approval` has `bypass`, default false, and `expiry`, 1m to 168h, unset 24h. With
   `bypass`, a run runs as ADR-0347 decides and its handler's `context.approval` is none. Without it, a run previews
   first and waits for an approval.
2. **The preview.** A `Pending` run whose Job has no `bypass` previews once a slot is free: a run `Previewing`, a phase
   #1015's list lacks since the slot and restart rules hang on it, holds a slot as a `Running` one does. One write
   stores it `Previewing` with ADR-0347's pin and a trace, before its worker exists; the preview and the apply run
   that one revision, in one trace. The executor calls a worker of it once, as ADR-0347 Decision 5 calls, but as
   caller `JobPreview` with the event type `funcd.job.preview`: the contract plugins check its input, and
   contract-output skips it, since a preview is no result. The shim calls the module's `preview(context, event)`
   export, or answers the run's input when the module has none. A 2xx answer within `job.payloadLimit` becomes
   `status.preview`: its `digest` (`sha256:` of the answer's bytes), its `document` and `previewedAt`; the run goes
   `AwaitingApproval` and the worker stops. Another answer ends the run `Failed` with `PreviewFailed`, a larger one
   with `PayloadLimitExceeded`. The Job's `timeout` bounds the call. A restart puts a `Previewing` run back to
   `Pending`, so the preview is called again; a cancel stops the preview's worker and ends the run `Cancelled`.
3. **An approval.** `spec.approve` holds a preview digest. Admission refuses it, `spec.reject` and `spec.rejectReason`
   on a run's creation (422). It takes an update that sets or changes `spec.approve` only while the run is
   `AwaitingApproval` and the digest is `status.preview.digest` (else 409, naming the current digest), and only from a
   caller the RBAC authorizer lets update the run's Job (`auth.VerbUpdate` on `Job`; else 403). The API server then
   sets `status.approvedAt`, and `status.approvedBy` to the caller's identity subject, empty with none. A pass acts only
   on an approval whose `approvedAt` admission wrote.
4. **The apply.** An approved run waits for a slot (`Pending`, `Queued`) and keeps its pin and its trace. On the
   approval and when the slot frees, a pass applies a preview within the Job's `approval.expiry` as ADR-0347 decides,
   and previews a stale one again in the slot, reason `PreviewExpired`: a document with the approved digest goes on
   to `Running` in that slot; another clears `approvedBy` and `approvedAt` and leaves the run `AwaitingApproval` for a
   new approval. A run that holds a pin
   (`Previewing`, `AwaitingApproval`, or approved and `Pending`) whose Job is deleted or re-created, or whose pin is
   gone, ends `Cancelled` with `JobDeleted` before any further call (ADR-0347 Decision 6), so an approval never runs
   another Job's code.
5. **A rejection.** `spec.reject`, one-way, with an optional `spec.rejectReason` of at most 512 bytes. Admission takes
   it from the same callers as an approval, while the run is `Pending`, `Previewing` or `AwaitingApproval`, and sets
   `status.rejectedBy`. A pass ends the run `Rejected`, reason `Rejected`, with the reason as its message, stopping a
   preview's worker. A rejection set before a pass wins over an approval. A cancel ends an `AwaitingApproval` run
   `Cancelled`; a `Running` run is cancelled, never rejected.
6. **The handler's approval.** In the apply call the shim fills `context.approval`, an `Approval` of `approvedBy`,
   `digest` and `preview` (the document), from the local API: `GET /job/run` answers the caller's own run (`name`,
   `job`, `revision`, `phase`, `startedBy`, `approval`) to a Job's worker only, 403 to any other caller, its causes
   declared (ADR-0330). The facet is `local.JobFacet` (ADR-0310), which F257 extends. `context.run.phase` is
   `preview` in the preview call.
7. **funcdctl.** `funcdctl job approve <run>` prints the preview's document and digest and, on a terminal, asks to
   confirm; `--digest <d>` approves `d` with no question, for scripts. `funcdctl job reject <run> [--reason <text>]`.
   `describe` shows the preview, `approvedBy` and `rejectedBy`.

## Design model

- **Spec**: [`docs/design/job/approval.fizz`](../design/job/approval.fizz) · state machine
  [`approval.md`](../design/job/approval.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AppliesWhatWasApproved` | always | `scenario: job-stale-preview-previewed-again` | `TestScenarioJobStalePreview` |
  | `NoStaleApply` | always | `scenario: job-stale-preview-previewed-again` | `TestScenarioJobStalePreview` |
  | `RejectedNeverRuns` | always | `scenario: job-reject-ends-run` | `TestScenarioJobRejectEndsRun` |
  | `ApprovedByAPerson` | always | `scenario: job-approve-at-creation-refused` | `TestScenarioJobApproveAtCreation` |
  | `NoApprovalStuck` | eventually always | `scenario: job-approve-runs-the-preview` | `TestScenarioJobApproveRuns` |
- **Bounds**: one run, two expiries, one restart, 20 actions: two previews again, each the same document or a new
  one, with an expiry while the approved run waits for its slot.
- **Faults and environment**: a restart, which calls a preview in flight again and ends an apply `Interrupted`; the
  controller runs one worker, so a pass is one step; the clock is the `Expire` action; a person shows, approves and
  rejects at any step, and a creator may set an approval at creation.
- **Mutants**: `apply-unmatched` fails `AppliesWhatWasApproved`; `apply-stale` and `no-slot-time-check` fail
  `NoStaleApply`; `approve-after-reject` fails `RejectedNeverRuns`; `approve-at-create` fails `ApprovedByAPerson`;
  `no-repreview` fails liveness.
- **Result**: pass, 168 states, 0.3 s, FizzBee v0.5.4; wider (three expiries, 26 actions): pass, 238 states, 1 s.
- **Left to tests**: other runs' slots and the apply's call (ADR-0347's model); admission's 409, 403 and 422; a
  re-created Job; the shims' `preview` export and `context.approval`.

## Behavior rows

- **jobs.md** (ADR-0347's chapter): the preview, an approval, expiry, a rejection, `bypass`, each with its reason.
- **funcdctl.md**: `funcdctl job approve` and `reject`.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/job.go (delta)
type JobApproval struct {
	Bypass bool     `json:"bypass,omitempty"`
	Expiry Duration `json:"expiry,omitempty"` // 1m to 168h; unset: 24h
}
// JobSpec gains Approval JobApproval `json:"approval,omitzero"`.

// api/types/v1alpha1/jobrun.go (delta)
// JobRunSpec gains:
//	Approve      string `json:"approve,omitempty" pattern:"^sha256:[a-f0-9]{64}$"`
//	Reject       bool   `json:"reject,omitempty"`
//	RejectReason string `json:"rejectReason,omitempty" maxLength:"512"`
// JobRunStatus gains Preview *JobRunPreview `json:"preview,omitempty"`, ApprovedBy string, ApprovedAt Timestamp and
// RejectedBy string, each under its camelCase JSON name.
type JobRunPreview struct {
	Digest      string          `json:"digest"` // sha256:<hex> of the answer's bytes
	Document    json.RawMessage `json:"document"`
	PreviewedAt Timestamp       `json:"previewedAt"`
}
// JobRunPhase gains Previewing, AwaitingApproval and Rejected.

// internal/invoke: the preview call's caller, after JobRun; internal/contractcheck's output plugin skips it.
const JobPreview Caller

// internal/workernode/local
func JobFacet(runs JobRunReader) Facet // GET /job/run

type JobRunReader interface {
	// Run returns the run whose worker ref is the caller's; fault.Forbidden for a caller that is no Job's worker.
	Run(ctx context.Context, ref Ref) (v1alpha1.JobRun, error)
}
```

`GET /job/run` answers 200 with this, or 403 (`job.notAJobWorker`):

```json
{"name": "tofu-7", "job": "tofu", "revision": "tofu-v3", "phase": "Running", "startedBy": "alice",
 "approval": {"approvedBy": "bob", "digest": "sha256:…", "preview": {"summary": "…"}}}
```

The shims: `Approval` (`approved_by`, `digest`, `preview`; `approvedBy` in TypeScript), `JobContext.approval:
Approval | None`, `RunInfo.phase` `preview` or `run`, and an optional module export `preview(context: JobContext,
event: CloudEvent[JobInput]) -> Json`.

| Reason | Phase | When |
|---|---|---|
| `PreviewFailed` | Failed | the preview answered an error |
| `PreviewExpired` | Previewing | the approved preview was past `approval.expiry` |
| `Rejected` | Rejected | `spec.reject` |

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `spec.approve`, `spec.reject`, `spec.rejectReason` | consumes | an update of the run, checked by admission |
| RBAC authorizer, `middleware.IdentityFrom` | consumes | the approver's right and name |
| `GET /job/run` | exposes | the worker's own run, to a Job's worker |
| `funcd.job.preview` | sends | the event type of the preview call |

## Implementation plan

1. `api/types/v1alpha1`: `JobApproval`, the run's fields and phases, `JobRunPreview`, their checks.
2. `internal/function/jobrun.go`: the preview phase on ADR-0347's executor, the approval pass and its slot-time
   expiry check, the rejection, the restart and cancel rules for `Previewing` and `AwaitingApproval`, a pinned
   waiting run's `JobDeleted`; slots count `Previewing` runs. `internal/invoke`: `JobPreview`, which
   `internal/contractcheck`'s output plugin skips.
3. `internal/controlplane`: the JobRun admission (no approval or rejection at creation; phase, digest, RBAC `update`
   on the Job, `approvedAt`, `approvedBy`, `rejectedBy`, one-way reject).
4. `internal/workernode/local`: `JobFacet` and its causes, wired for Job workers (F140's ref); `pkg/funcd` gives it
   a `JobRunReader` over the store.
5. `cmd/funcdctl/job.go`: `approve` and `reject`. The shims: `preview`, `Approval`, `context.approval`, released and
   bumped. Docs: the behavior rows and the guide page.

Tests, each written to pass: the types' checks; admission tests of 409, 403 and 422; one `TestScenario…` per scenario
in `pkg/funcd`'s e2e suite, each oracle test under `// oracle: docs/design/job/approval.fizz#<Assertion>`; the facet's
endpoint test; the CLI's; the shims'. Done when `just ci-full` and `just check-specs` pass with ADR-0347 implemented.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`, and every assertion its oracle marker.
- [ ] The pin and trace are stored with `Previewing` before the preview's worker exists; the apply keeps both.
- [ ] An apply starts only on an approval that admission recorded, naming the preview, within the expiry at the
      approval and when the slot frees.
- [ ] Admission refuses an approval or rejection at creation (422), another digest (409), a non-approver (403).
- [ ] A rejection wins over an approval; a pinned waiting run of a re-created Job ends `JobDeleted`.
- [ ] `GET /job/run` answers a Job's worker only, with its causes declared.

## Consequences

- Every Job without `bypass` waits for a person, so a Job for unattended work sets `bypass: true`.
- A run boots a worker twice, for its preview and for its apply, in one trace.
- A run waiting for approval holds no worker and no slot, and waits with no deadline until someone acts.

## Open questions

None. Policies on approvals come with F147 to F149.

## References

- Feature issue #1016 (FEAT-0012 F138), under tracker #1028; #1015's decision inputs of 2026-11-23.
- ADR-0347, ADR-0310, ADR-0330, ADR-0303, ADR-0094, ADR-0148; `docs/reports/infrastack-design.md` (Preview, approve,
  run).
