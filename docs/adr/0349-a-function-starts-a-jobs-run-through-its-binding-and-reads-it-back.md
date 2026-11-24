# ADR-0349: A Function starts a Job's run through its binding and reads it back

- **Status**: Reviewing (2026-11-24, implemented); Accepted (2026-11-23) on the decider's go, after two judge rounds with every Blocker and Major folded
- **Amended in place**: 2026-11-24, by the decider on [#1492](https://github.com/pyvvo/funcd/issues/1492#issuecomment-7119567941): an input off the Job's contract answers 422 as a Function's call does, not as a declared cause (Decision 4)
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: jobs, functions, bindings, local-api, sdk
- **Realizes**: [FEAT-0012/F257](../feat/0012-feat-infrastack.md) (a Function starts a Job run)
- **Relates to**: [ADR-0347](0347-a-job-runs-its-handler-once-per-jobrun-in-a-worker-of-its-own.md) (the run and its
  admission) · [ADR-0348](0348-a-run-waits-for-a-persons-approval-of-its-preview-unless-its-job-says-bypass.md)
  (`local.JobFacet`) · [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) (a binding that is its Cedar
  grant) · ADR-0246 (bindings) · ADR-0116 (the capability registry) · ADR-0074 · ADR-0310 · ADR-0330 · ADR-0064

## Context & Need

Work such as provisioning a node or a backfill starts from an event or a schedule that a Function handles, not only
from a person, and the InfraStack add-on's Function starts a run for every change of a stack (F141). F257 decided that
a Function's bindings name a Job it may start, that its code starts a run and gets the run's name back without waiting,
then reads the run's phase and result, and that the run checks the same rights as a person's `funcdctl job run`.
#1015's inputs (2026-11-23) give the shape: `spec.bindings.job`, `ctx.job.start` and `ctx.job.get`, the answers 403,
404 and 422, and no direct Job action for a Sensor, a schedule or a Workflow step.

## Scenarios

- `scenario: function-starts-job-run`: Given a Function `planner` with `bindings.job: [{alias: stack, job: tofu}]`;
  when its handler calls `ctx.job.start("stack", {"op": "plan"})`; then it gets `tofu-<id>` back at once, and that run
  exists with `startedBy` `Function::"team/planner"`.
- `scenario: function-reads-job-run`: When the handler later calls `ctx.job.get("stack", "tofu-<id>")`; then it gets
  the run's phase, reason and, once `Succeeded`, its result.
- `scenario: job-start-without-binding`: Given no binding with the alias `other`; when `ctx.job.start("other", {})`;
  then 403, and no run is created.
- `scenario: job-start-forbidden-by-policy`: Given a namespace Policy that forbids `job::start` to `planner`; then 403.
- `scenario: job-start-input-refused`: Given an input off the Job's contract; then 422 naming the path.
- `scenario: job-start-named-twice`: When the handler starts the run `tofu-gen-7` twice with one input, as a retried
  call does; then both calls answer `tofu-gen-7`, and one run exists.
- `scenario: job-start-name-taken`: Given a run `tofu-gen-7` of another Job, or with another input; then 409.
- `scenario: job-start-job-gone`: Given the bound Job is deleted; then `ctx.job.start` answers 404.
- `scenario: job-get-other-job`: When the handler reads a run of a Job it does not bind; then 404.

## Scope

In: `spec.bindings.job`; `ctx.job.start` and `ctx.job.get` on both shims; the local API's `POST /job/{alias}` and
`GET /job/{alias}/{run}`; their Cedar actions, the `Job` resource type and the answers.

Out: waiting for a run inside a call (a Function polls, or reacts to a later event); a Sensor, schedule or Workflow
action that starts a run directly (none: a Function does it); typing a Job's input per alias in the SDK; a Job
starting a run (a Job's spec refuses `bindings.job`); approving a run from code (F138's approvers are people); how an
App's part binds a Job part (F271).

## Constraints & Decision drivers

- The binding is the grant, as every binding of a Function is (ADR-0246), and a forbid Policy revokes it.
- A run a Function starts passes the admission a person's does (ADR-0347 Decision 3).
- A caller that sends a start again, as an at-least-once delivery does, starts no second run.

## Alternatives considered

- **A Sensor or Workflow action that starts a run**: a second way in with its own rights and answers. Rejected by
  the decider's input: a Function does it.
- **A link to the Job, as `bindings.links`**: a link calls a Function's worker and waits for its answer; a run is no
  call and takes minutes. Rejected for a binding of its own.
- **`ctx.job.start` that waits for the run**: a Function's call is bounded (ADR-0151), a run is not. Rejected.
- **Run names only the server picks**: a start sent again would make a second run. Rejected: the caller may name it.
- **409 for a start sent again**: the shims raise it, so a retried Function fails until its event dead-letters,
  though its run started. Rejected for answering the run the start already made.

## Decision

1. **The binding.** `spec.bindings.job` holds entries of `alias`, a DNS-1123 label unique in the list, and `job`, a
   Job of the Function's namespace. The capability registry (ADR-0116) gives the Function `job::start` and
   `job::read` on that Job, a resource of the new Cedar entity type `Job` (F140's ADR also makes it a principal), and
   a namespace Policy's forbid revokes either. A missing Job holds nothing: a call on it answers 404. A Job's spec
   refuses `bindings.job`.
2. **A start.** `ctx.job.start(alias, input, name?)` sends `POST /job/{alias}` with `{"input": …, "name": …}` to the
   local API's `JobFacet`. Once Cedar allows `job::start` to the caller on the bound Job, the facet creates the run
   through a `JobRunStarter` that applies the JobRun admission of ADR-0347 Decision 3, with `status.startedBy` the
   caller's Cedar principal, `Function::"<ns>/<name>"`, and answers 201 `{"run": "<name>"}` without waiting. With no
   `name`, the run is `<job>-<id>`. A name that a run of the bound Job already holds, with an input equal as a JSON
   value, answers 200 with that name, so a start sent again starts nothing; a name another run holds answers 409,
   which tells the caller no more than that its namespace holds the name.
3. **A read.** `ctx.job.get(alias, run)` sends `GET /job/{alias}/{run}`. Once Cedar allows `job::read`, the facet
   answers 200 with the run's `name`, `phase`, `reason`, `startedAt`, `endedAt` and, once `Succeeded`, `result`; a
   run of another Job, or one that `job.retention` removed, answers 404.
4. **The answers.** 403 when the Function has no binding of that alias or Cedar denies the action; 404 when the Job
   or the run is gone; 409 for a name another run holds; 413 over `job.payloadLimit`; 422 for an input off the Job's
   `status.contract`, answered as a Function's call answers it: the contract's `{"error", "details"}` body, relayed as
   is (amended 2026-11-24). Each other answer is a declared cause (ADR-0330).
5. **The shims.** Both shims add `ctx.job.start(alias, input, name=None) -> str` and `ctx.job.get(alias, run) ->
   JobRunState` (`name`, `phase`, `reason`, `started_at`, `ended_at`, `result`; camelCase in TypeScript), raising the
   shim's error for each answer above, as `ctx.addon.fetch` does. `kindref` lists `job.start` and `job.get` among the
   ctx methods.

## Design model

- **Spec**: [`docs/design/job/start.fizz`](../design/job/start.fizz) · state machine
  [`start.md`](../design/job/start.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AtMostOneRun` | always | `scenario: job-start-named-twice` | `TestScenarioJobStartNamedTwice` |
  | `CallerSucceeds` | eventually always | `scenario: job-start-named-twice` | `TestScenarioJobStartNamedTwice` |
- **Bounds**: one start, its answer lost once, 10 actions: the smallest retry.
- **Faults and environment**: the call's answer is lost once and the caller sends it again; starts reach the facet
  concurrently, and the store creates a name at most once.
- **Mutants**: `unnamed-retry` fails `AtMostOneRun`; `conflict-on-retry` fails liveness.
- **Result**: pass, 6 states, 0.2 s, FizzBee v0.5.4; wider (the answer lost twice, 14 actions): pass, 9 states.
- **Left to tests**: a name held by another Job's run or another input (409), and the admission's answers.

## Behavior rows

- **jobs.md**: a Function's start and read, and their answers. **functions.md**: `bindings.job` among the bindings.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1: FunctionBindings gains Job []FunctionJob `json:"job,omitempty"`; a Job's spec refuses it.
type FunctionJob struct {
	Alias string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	Job   ObjectName `json:"job"`
}

// internal/auth, and the Cedar entity type "Job" in internal/auth/cedar
const (
	ActionJobStart Action = "job::start" // Resource = a Job
	ActionJobRead  Action = "job::read"  // Resource = a Job
)

// internal/workernode/local: JobFacet (ADR-0348) takes a starter beside its reader.
type JobRunStarter interface {
	// Start creates a run of job through ADR-0347's admission, named name or <job>-<id>, started by caller. A name a
	// run of job with the same input holds returns that run with created false; one another run holds is fault.Conflict.
	Start(ctx context.Context, caller Ref, job v1alpha1.ObjectName, name v1alpha1.ObjectName,
		input json.RawMessage) (run v1alpha1.ObjectName, created bool, err error)
	// Get returns run when it is a run of job; fault.NotFound otherwise.
	Get(ctx context.Context, job, run v1alpha1.ObjectName) (v1alpha1.JobRun, error)
}
```

| Endpoint | Answers |
|---|---|
| `POST /job/{alias}` with `{"input": …, "name": …}` | 201 or 200 `{"run": "<name>"}`; 403, 404, 409, 413, 422 |
| `GET /job/{alias}/{run}` | 200 `{"name", "phase", "reason", "startedAt", "endedAt", "result"}`; 403, 404 |

| Dependency & I/O | Direction | Shape |
|---|---|---|
| `spec.bindings.job` | consumes | the aliases a Function's code may start and read |
| `job::start`, `job::read` on `Job` | consumes | Cedar actions, granted by the binding |
| `JobRunStarter` | consumes | implemented in `pkg/funcd` over ADR-0347's admission and the store |

## Implementation plan

1. `api/types/v1alpha1`: `FunctionJob` in `FunctionBindings`, its checks in `validateBindings`, its refusal on a Job.
2. `internal/auth` and `internal/auth/cedar`: the two actions and the `Job` entity type, granted by the binding in the
   capability registry, as `InvokeCapability` grants a link.
3. `internal/workernode/local`: the two endpoints on `JobFacet` and their causes; `pkg/funcd`: the `JobRunStarter`.
4. The shims' `ctx.job`, released and bumped; `internal/controlplane/kindref`: the two methods. Docs: the rows.

Tests, each written to pass: the binding's checks; the capability registry's grant and a forbid; the endpoints'
answers; one `TestScenario…` per scenario in `pkg/funcd`'s e2e suite, the oracle under
`// oracle: docs/design/job/start.fizz#<Assertion>`; the shims' `ctx.job`. Done when `just ci-full` and `just
check-specs` pass with ADR-0347 implemented.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`, and every assertion its oracle marker.
- [ ] A start runs ADR-0347's admission and records the caller's Cedar principal in `startedBy`.
- [ ] A start sent again with its name and input answers 200 with the run; another holder of the name answers 409.
- [ ] Both actions come from the binding alone, and a forbid Policy revokes each; a Job's spec refuses the binding.
- [ ] A read answers only a run of the bound Job, and every answer but the contract's 422 is a declared cause (amended 2026-11-24).

## Consequences

- A Function can start any amount of runs of a Job it binds; the Job's `concurrency` and `job.maxRunning` bound what
  runs, and a run without `bypass` still waits for a person.
- A start with no name is not safe to send again: a Function called at least once names its run from its event's
  `id`. A Function learns a run's end by reading it; there is no callback.

## Open questions

None.

## References

- Feature issue #1492 (FEAT-0012 F257), under tracker #1028; #1015's decision inputs of 2026-11-23.
- ADR-0347, ADR-0348, ADR-0338, ADR-0246, ADR-0116, ADR-0074, ADR-0310, ADR-0330, ADR-0151, ADR-0064.
