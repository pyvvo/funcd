# ADR-0347: A Job runs its handler once per JobRun, in a worker of its own

- **Status**: Accepted (2026-11-23) on the decider's go, after two judge rounds with every Blocker and Major folded
- **Amended in place**: 2026-11-23, by the decider on [#1015](https://github.com/pyvvo/funcd/issues/1015#issuecomment-7113370643): an image with no contract holds the Job with ContractMissing (Decision 2)
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: jobs, runtime, control-plane, revisions, cli
- **Realizes**: [FEAT-0012/F137](../feat/0012-feat-infrastack.md) (Jobs: work that runs to completion)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0003](0003-resource-model-and-api-typing.md)'s
  Revision row (`spec.function` is no longer required: a Job's Revision names its Job in `spec.job`, Decision 2);
  [ADR-0306](0306-workload-hooks-check-a-functions-bindings-and-give-its-workers-their-env.md)'s hook signatures
  (they take the fields a Job shares and the subject's kind, Decision 5); [ADR-0206](0206-restore-and-held-boot.md)
  Decision 2 step 4 (a restore also ends the restored JobRuns that have not ended, Decision 7);
  [ADR-0262](0262-function-owned-volumes.md)'s volume `ID` (it names its owner's kind, Decision 1)
- **Relates to**: ADR-0094 · ADR-0146 · ADR-0190 · ADR-0172 · ADR-0011 · ADR-0152 · ADR-0302 · ADR-0303 ·
  ADR-0304 · ADR-0059 · ADR-0170 · ADR-0290 · ADR-0344 · ADR-0346 · ADR-0148 · F138, F140, F257, F271 (own ADRs)

## Context & Need

A stack update, a backfill or a migration runs once, for minutes or hours, and must not run twice after a crash. No
kind does that today: a Function serves calls bounded by a deadline (ADR-0151), and a workflow step is such a call,
sent at least once (ADR-0094). F137 decided the shape: a `Job` is a definition built as a Function is, from its
fields, its image and its contract; each execution is a `JobRun`, created through the API as a WorkflowRun is; a run
cut by a restart ends failed and never starts again on its own. #1015's inputs (2026-11-23) settle the rest.

## Scenarios

- `scenario: job-run-succeeds`: Given a Job `hello` whose handler returns `{"greeting": "hi Ada"}`; when `funcdctl job
  run hello --input '{"name":"Ada"}'`; then it prints `hello-<id>`, the run ends `Succeeded` with that `result` and
  `jobRevision` `hello-v1`, and `funcdctl job logs` shows the handler's log line.
- `scenario: job-input-refused`: a run created with `{}` against a contract requiring `name` gets 422, naming the path.
- `scenario: job-runs-wait-their-turn`: Given `concurrency: 1`; when two runs are created; then the second stays
  `Pending` with `Queued` until the first ends, then runs.
- `scenario: job-cancel-stops-run`: Given a `Running` run; when `funcdctl job cancel <run>`; then its worker stops
  within the driver's grace, the run ends `Cancelled`, and a late answer changes nothing.
- `scenario: job-restart-interrupts`: Given a `Running` run; when funcd restarts; then the run is `Failed` with
  `Interrupted`, no worker of it runs, and its handler was called once.
- `scenario: job-timeout`: Given `timeout: 2s` and a handler that sleeps 10 s; then the run ends `Failed` with
  `RunTimedOut` and its worker is gone.
- `scenario: job-run-pins-revision`: Given a run `Running` on `hello-v1`; when the Job's image changes; then the run
  ends on `hello-v1`, the next run pins `hello-v2`, and `hello-v1` is deleted once no open run pins it.
- `scenario: job-handler-fails`: a handler that throws ends the run `Failed` with `HandlerFailed` and its first line.
- `scenario: job-deleted-cancels-run`: Given a `Running` run; when its Job is deleted; then the run ends `Cancelled`
  with `JobDeleted` and stays listed until `job.retention`.
- `scenario: job-start-waits-while-held`: Given the run's namespace is held (ADR-0290); when a run is created; then it
  stays `Pending`, its status untouched, until the release, then runs.

## Scope

In: the `Job` and `JobRun` kinds; a Job's revisions; a run's admission, start, worker, call, end, timeout, cancel,
holds, restart, restore and retention; the handler's event and context types; `funcdctl job`; three config keys.
Out: approval and preview (F138); the Job as its own principal, its egress and a policy that names a Job (F140); a
Function starting a run (F257); an App's `jobs` (F271); the InfraStack add-on (F141); re-running a run; retries.

## Constraints & Decision drivers

- Never call a handler twice for one run, whatever stops funcd (F137).
- A Job is built as a Function is and reuses what Functions have (F137, 2026-11-02 and 2026-11-23).
- One sandbox per run, on both runtime drivers and under `funcdctl dev` (F137, F139).
- Nothing on a Job carries a Function's name (the decider, #1015, 2026-11-23).

## Alternatives considered

- **A run as a hidden Function called once**, as a workflow image step materializes one: a Function's call is bounded
  by an hour (ADR-0151), shares warm workers and runs as a Function principal, which F140 rules out. Rejected.
- **A one-step Workflow**: the engine sends in-flight steps again after a restart (ADR-0094). Rejected by F137.
- **A `JobRevision` kind**, as AppRevision and AddonRevision: ADR-0344's image pin and fill, ADR-0172's read-only rule
  and `funcdctl get revisions` serve `Revision`. Rejected for the shared kind, with names of its own (Decision 2).
- **`spec.function` with `kind: Job` and `<job>-<n>` names**: a Function's name on a Job's object, and ADR-0172
  Decision 5 refuses a `revOther`, so whichever of a Function `x` and a Job `x` stamps second fails. Rejected.
- **Restarting a cut run that never reached its handler**: funcd cannot tell a cut call from a received one. Rejected.
- **The reconcilers in a package of their own**: they would import `internal/function`, a sibling feature (ADR-0002),
  or copy its stamping, gates and worker building. Rejected: they live in `internal/function`.

## Decision

1. **The Job.** A namespaced, status-bearing kind. Its spec holds a Function's fields with their meaning and checks:
   `runtime`, `handler`, `image`, `imageDigest`, `resources`, `kv`, `buckets`, `sql`, `lakes`, `configMaps`,
   `secrets`, `volumes` and `bindings`. It adds `timeout`, the bound on each call of a run, 0s to 168h, 0s or unset
   none, as a Workflow run's (a call, not the whole run as #1015 put it: F138's wait for a person must not count); and
   `concurrency`, how many of its runs may hold a worker at once, 1 to 15, unset 1, and 1 with a local volume, as a
   Function's replicas. It has no `scaling`, `replicas`, `pooling`, `protocol` or `ingress`. What it binds is in use
   (`gc.InUse`), and its local volumes are keyed by kind and name, so a same-named Function never shares one. F138
   adds `approval`; the run below is that of a Job with `approval.bypass`, or of an approved run.
2. **Its revisions.** The Job reconciler stamps a `Revision` per generation as the Function reconciler does: the digest
   resolved once (ADR-0035), runtime and handler from the spec, ADR-0344's resolution, gate, Addon watch and
   `runtimeImage`. Its `spec.job` names the Job, `spec.function` is empty, its controller ref is the Job, and
   ADR-0172's read-only rule holds. Its name is `<job>-v<generation>`, cut and hashed past 63 characters as ADR-0172
   Decision 4 with `-v<generation>` last: a Function's Revision name ends in `-<digits>`, so the two never meet. The
   reconciler reads the revision's contract by digest (`artifact.Inspect`) into `status.contract`; an image with none
   holds the Job with `ContractMissing`, as a cloudevent Function's (#1084; amended 2026-11-23). The Job is Ready once
   its revision is stamped and the checks a Function passes before its first worker pass (the shape gate, ADR-0149's
   runtime gate, ADR-0306's checks), with the Function's reasons.
   It keeps its current revision and those of runs not ended, and deletes the others; the owner collector (ADR-0170)
   removes a deleted Job's revisions and the stores it owns, as a Function's.
3. **A run.** A `JobRun` names its Job in `spec.job` and carries `spec.input`, both fixed at creation; `spec.cancel`
   may only turn true, and admission refuses any other change of the spec as invalid. `funcdctl job run` sets
   `generateName: <job>-`, so the server names it `<job>-<id>`. Admission refuses an input over `job.payloadLimit`
   with 413 (ADR-0148) and, when the Job has a `status.contract`, one off its input schema with 422 naming the path.
   At creation the API server sets `status.startedBy` to the caller's identity subject (`middleware.IdentityFrom`),
   empty with none (a record, not a grant), and `status.jobUID` to its Job's UID when the Job exists.
4. **The start.** A pass starts a `Pending` run once its Job is Ready (else `JobNotFound`, `JobNotReady`) and a slot is
   free: fewer of the Job's runs holding a worker than its `concurrency`, and fewer on the node than `job.maxRunning`
   (else `Queued`). A node slot goes to the oldest `Pending` run whose Job has a free slot, by creation time then name,
   so a Job at its limit holds no other back. One write stores the run `Running` with `jobRevision`, `jobUID`,
   `imageDigest`, `startedAt` and a `traceId`, before anything else, keeping the pin and trace a run already holds
   (ADR-0348); an input off that revision's contract ends it `Failed` with `InputSchemaMismatch` instead. Only then
   does its executor start, a goroutine keyed by the run's UID. While the platform (ADR-0206 Decision 6) or the run's
   namespace (ADR-0290) is held, the run reconciler acts on no run and leaves its status as it is, as for workflow runs.
5. **The worker and the call.** The executor creates one worker of the pinned revision through ADR-0306's hooks, given
   the Job's shared fields and the kind Job, as a solo Function worker of that runtime is built, on the driver such a
   worker uses (the process driver under `funcdctl dev`, ADR-0346's for an Addon's runtime). Its `WorkerSpec` has
   `Name` the run, `OwnerKind` `JobRun` (ADR-0152) and `Revision` the pin; drivers and executor treat it as a shim
   worker, not an engine. Its env adds `FUNCD_JOB`, `FUNCD_JOB_RUN`, `FUNCD_JOB_REVISION` and `FUNCD_JOB_STARTED_BY`,
   with no `FUNCD_FUNCTION`. Its rights and its egress are the Job's (F140), since ADR-0175 indexes a worker by its
   owner kind and an EgressPolicy grants Functions only. A worker that does not listen and become ready (ADR-0161)
   within `runtime.bootTimeout` ends the run `Failed` with `StartFailed`. The executor then calls it once through the
   invoke chain (ADR-0302), caller `JobRun`, `Target` the Job at its pinned revision: a POST of the CloudEvent below
   with the run's `traceparent`, and `X-Funcd-Timeout-Ms` set to the time left when the Job has a `timeout`. No data
   plane plugin applies to it; the contract plugins check it against the pinned revision's contract (ADR-0303), which
   `ContractOf` reads from `ref.Revision`, a Job's Revision by its `spec.job`. A 2xx ends the run `Succeeded` with the
   body as `status.result`, or `Failed` with `PayloadLimitExceeded` over `job.payloadLimit`; a 422 ends it `Failed`
   with `InputSchemaMismatch`; any other answer or a transport error, `Failed` with `HandlerFailed` and the cause's
   first line, cut at 512 bytes. The executor keeps the answer in memory and enqueues the run; the pass writes it while
   the run is stored `Running` under that UID, so the pass is the run's one status writer (ADR-0146 Decision 4). The
   worker is stopped and removed at every end of the run.
6. **Timeout, cancel and deletion.** The Job's `timeout`, from the worker's start, stops the worker and ends the run
   `Failed` with `RunTimedOut`. With `spec.cancel`, a pass ends a `Pending` run `Cancelled`; for a `Running` one it
   first cancels the executor and stops the worker (SIGTERM, the driver's grace, SIGKILL), reason `Cancelled`, and the
   handler may have done part of its work. A worker created under a cancelled executor is stopped at once. A run whose
   Job is deleted or re-created under a UID other than its `jobUID`, or whose pin is gone, is cancelled with
   `JobDeleted` before any further call, as ADR-0190 Decision 10 cancels a workflow run; one created before its Job
   has no `jobUID` and waits with `JobNotFound`. A deleted `JobRun` has its worker stopped. Runs stay listed.
7. **A restart and a restore.** At boot, after the drivers' sweeps have removed every leftover worker (containerd's
   `BootSweep`, the process driver's registry reap, ADR-0346's reap) and before the run reconciler starts any run,
   every run stored `Running` ends `Failed` with `Interrupted`, held or not, since this starts nothing. `funcd restore
   run <point>` ends every restored run not ended `Cancelled` with `Restored`, where it pauses each open workflow run
   (ADR-0206 Decision 2): the backup cannot say whether it ran since. No run is called twice or restarts on its own.
8. **Retention and logs.** The run reconciler deletes a run `job.retention` after its `endedAt`, and not while held.
   The executor sends the worker's output and records to funclog as a Function worker's, under the run; `funcdctl job
   logs <run>` reads them by the run's `traceId` (ADR-0106).
9. **The handler.** The shim calls the handler export as `handle(context, event)`, serves the call for as long as it
   runs and reads no contract (ADR-0304). The context is a `JobContext`: the `FunctionContext` plus `run`, a `RunInfo`
   of `name`, `job`, `revision`, `phase` (`run`) and `startedBy`, from the env. `pkg/sdk/types_gen.go` writes `JobInput`
   and `JobOutput`, with a typed `JobContext`, when the manifest beside the contract is a Job's.
10. **funcdctl and roles.** `funcdctl job` mirrors `funcdctl workflow`: `run <job> [run-name] --input <json|@file>`
    prints the run's name; `runs <job> [--phase] [-o json]`, newest first; `logs <run>`; `describe <run>` (phase,
    reason, revision, digest, `startedBy`, times, result); `cancel <run>`. The built-in roles treat Job as Workflow and
    JobRun as WorkflowRun; the kinds' reference groups them, with a Job's Revisions, among the workloads.

## Design model

- **Spec**: [`docs/design/job/run.fizz`](../design/job/run.fizz) · state machine [`run.md`](../design/job/run.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AtMostOneRunning` | always | `scenario: job-runs-wait-their-turn` | `TestScenarioJobRunsWaitTheirTurn` |
  | `NeverCalledTwice` | always | `scenario: job-restart-interrupts` | `TestScenarioJobRestartInterrupts` |
  | `EndIsFinal` | transition | `scenario: job-cancel-stops-run` | `TestScenarioJobCancelStopsRun` |
  | `NoWorkerAfterEnd` | always | `scenario: job-timeout` | `TestScenarioJobTimeout` |
  | `PinnedRevisionKept` | always | `scenario: job-run-pins-revision` | `TestScenarioJobRunPinsRevision` |
  | `EveryRunEnds` | eventually always | `scenario: job-restart-interrupts` | `TestScenarioJobRestartInterrupts` |
- **Bounds**: two runs of one Job, concurrency 1, one stop, one edit, 16 actions: the smallest that holds a slot race,
  a stop between the store write and the call, and a late answer.
- **Faults and environment**: a stop at any step loses the calls in flight and the executors, not the store or the
  workers; crash on yield off; one controller worker (ADR-0146 Decision 8), so a pass, its `Answer` included, is a step.
- **Mutants**: `no-slot-check` fails `AtMostOneRunning`; `call-before-running` and `pending-after-restart` fail
  `NeverCalledTwice`; `late-answer-wins` fails `EndIsFinal`; `cancel-keeps-worker` fails `NoWorkerAfterEnd`;
  `gc-ignores-pins` fails `PinnedRevisionKept`; `running-after-restart` fails liveness.
- **Result**: pass, 5,463 states, 3.2 s, FizzBee v0.5.4; wider (three runs, 20 actions): pass, 134,013 states, 113 s.
- **Left to tests**: `job.maxRunning` over several Jobs, holds, a restore, the boot timeout, the payload limit,
  retention, a call past the shims' server timeouts, and the containerd stop and sweep (the lane).

## Behavior rows

- **jobs.md**, a new chapter with its README row: each rule of Decisions 3 to 8, with its reason. **limits.md**: the
  three `job.*` keys. **funcdctl.md**: `funcdctl job`. **disaster-recovery.md**: a restore ends the open runs.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/job.go and jobrun.go: each kind is TypeMeta, ObjectMeta, Spec and Status, as every kind.
type JobSpec struct {
	// Decision 1's FunctionSpec fields, with their types, JSON names and checks; then:
	Timeout     Duration `json:"timeout,omitempty"` // 0s to 168h; 0s or unset: none
	Concurrency int      `json:"concurrency,omitempty" minimum:"1" maximum:"15"` // unset: 1
}
type JobStatus struct {
	Status          `json:",inline"`
	CurrentRevision ObjectName        `json:"currentRevision,omitempty"`
	Contract        *WorkflowContract `json:"contract,omitempty"` // the current revision's, read by digest
}
type JobRunSpec struct {
	Job    ObjectName      `json:"job"`
	Input  json.RawMessage `json:"input,omitempty"`
	Cancel bool            `json:"cancel,omitempty"`
}
type JobRunStatus struct {
	Status      `json:",inline"` // its Ready condition carries the reason and the message
	Phase       JobRunPhase     `json:"phase,omitempty"`
	JobRevision ObjectName      `json:"jobRevision,omitempty"`
	JobUID      UID             `json:"jobUID,omitempty"`
	ImageDigest string          `json:"imageDigest,omitempty"`
	StartedBy   string          `json:"startedBy,omitempty"`
	StartedAt   Timestamp       `json:"startedAt,omitzero"`
	EndedAt     Timestamp       `json:"endedAt,omitzero"`
	Result      json.RawMessage `json:"result,omitempty"`
	TraceID     string          `json:"traceId,omitempty"`
}
type JobRunPhase string // Pending, Running, Succeeded, Failed, Cancelled; F138 adds three

// revision.go (delta): exactly one of Function and Job is set.
type RevisionSpec struct {
	Function ObjectRef  `json:"function,omitzero"` // was required
	Job      ObjectName `json:"job,omitempty"`
	// Number, Runtime, Handler, Image, ImageDigest unchanged; RuntimeImage as ADR-0344
}
func JobRevisionName(job ObjectName, generation int64) ObjectName // <job>-v<generation>, Decision 2

// internal/invoke: the run's caller, after AddonSettings.
const JobRun Caller
```

The call's body, with `Content-Type: application/json`:

```json
{"specversion": "1.0", "type": "funcd.job.run", "source": "funcd://<namespace>/job/<job>",
 "id": "<the run's uid>", "data": <spec.input, or null>}
```

| Reason | Phase | When |
|---|---|---|
| `JobNotFound`, `JobNotReady`, `Queued` | Pending | no Job of that name; the Job not Ready; no slot (Decision 4) |
| `StartFailed` | Failed | the worker did not listen and become ready within `runtime.bootTimeout` |
| `InputSchemaMismatch` | Failed | the input off the pinned revision's contract, at the start or in the call |
| `HandlerFailed`, `PayloadLimitExceeded` | Failed | an error answer; a result over `job.payloadLimit` |
| `RunTimedOut`, `Interrupted` | Failed | the Job's timeout; funcd stopped while the run was `Running` |
| `Cancelled`, `JobDeleted`, `Restored` | Cancelled | `spec.cancel`; the Job or the pin gone; a restore (Decision 7) |

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `job.maxRunning`, `job.payloadLimit`, `job.retention` | config | 4 (≥ 1); 262144 bytes (≥ 1); 720h (> 0) |
| `FUNCD_JOB`, `FUNCD_JOB_RUN`, `FUNCD_JOB_REVISION`, `FUNCD_JOB_STARTED_BY` | worker env | strings |
| `artifact.Inspect`, `runtime.Runtime`, `invoke.Chain`, ADR-0306's hooks, hold gates | consumes | as Functions |

## Implementation plan

1. `api/types/v1alpha1`: `job.go`, `jobrun.go` (`Validate` with the Function spec's helpers over the shared fields),
   `revision.go` (`job`, one subject, `JobRevisionName`), `metadata.go` (the two kinds in `Kind.Validate`,
   `NewObject`, `AllKinds` and its count).
2. `internal/function`: `job.go`, the Job reconciler, on the Function's stamping and gates given a subject; `jobrun.go`,
   the run reconciler with the executor, the hold gates, `Boot` and `SweepExpired`; `workerSpec` and ADR-0306's hooks
   take the shared fields and a kind; `ContractOf` reads a Job's pinned Revision; `revisionOf` and the Function GC
   skip a Revision whose `spec.job` is set. `internal/gc`: a Function's pairs, Route aside, and `InUse` for a Job;
   `internal/volume`: `ID` names its owner's kind, and one with none is refused.
3. `internal/invoke`: `JobRun`. `pkg/funcd/executor.go` and the containerd driver treat a `JobRun` worker as a shim
   worker (output to funclog, no `FUNCD_REPORTFILE`).
4. `internal/controlplane`: the CRUD blocks and `stampTypeMeta` cases, `routes.go`, `routes_rest.go`, `stubs.go`, the
   JobRun admission (413, 422, fixed fields, one-way cancel, `startedBy`), the role tables, `kindref`'s groups.
5. `internal/platform/config`: the three keys. `pkg/funcd`: the options, Decision 7's boot order and restore step.
   `pkg/sdk`: `kinds.go` and `types_gen.go`. `cmd/funcdctl/job.go`.
6. pyvvo/funcd-python (`funcd_shim/types.py`) and pyvvo/funcd-typescript: `JobContext`, `RunInfo`, the event, a long
   call; released, then `go get` of both tags. Docs: the behavior rows, the Spec tabs (`just generate`), a guide page.

Tests, each written to pass: the types' contract tests (`JobRevisionName` never equals a `revisionName`); one
`TestScenario…` per scenario in `pkg/funcd`'s e2e suite, process driver, `t.Parallel()`, short pacing, each oracle
under `// oracle: docs/design/job/run.fizz#<Assertion>`; a restore test; each `funcdctl job` command; the shims'
`JobContext` and a long call; the Lima lane case `lima-example-job` on containerd (a cancel stops the container within
the grace; a restart leaves the run `Interrupted`, no container). Done when `just ci-full`, the lane and `just
check-specs` pass, with F140's ADR in the same PR, so no Job worker starts without its principal.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`, and every assertion its oracle marker.
- [ ] One write stores a run `Running` before its worker exists; the pass alone writes the answer, while `Running`.
- [ ] Boot ends every `Running` run `Interrupted` after the drivers' sweeps; a restore ends open runs `Restored`.
- [ ] Slots count the Job's runs and the node's; a held platform or namespace starts nothing.
- [ ] A Job's Revision has `spec.job` and its `-v` name; no Function adopts, drops or collects it; the GC has the pairs.
- [ ] The contract plugins check a `JobRun` call on its pinned revision; no other plugin applies to it.
- [ ] The worker is a shim worker with the four `FUNCD_JOB*` vars and no `FUNCD_FUNCTION`, removed at every end.
- [ ] `startedBy` comes from the caller's identity only; the lane case ran on containerd and passed.

## Consequences

- A handler is called at most once per run; a cut, a timeout or a cancel may leave its work half done or done, so a
  new run must be safe (OpenTofu's state lock covers F141's). With no `timeout`, a hung run holds its slots.
- Each run boots a worker and pays its runtime's cold start.
- `funcdctl get revisions` lists Functions' and Jobs' revisions together, told apart by `spec.job` and the `-v` name.
- A Job's runs outlive it until `job.retention`; a restore cancels the runs it brings back open, for a person to rerun.

## Open questions

None. F138, F140, F257 and F271 decide their parts in their own ADRs, from #1015's inputs.

## References

- Feature issue #1015 (FEAT-0012 F137, tracker #1028) and its decision inputs of 2026-11-23; the ADRs above, ADR-0097,
  ADR-0161, ADR-0175, ADR-0058, ADR-0035, ADR-0106 and ADR-0149; `docs/reports/infrastack-design.md` (One Job kind).
