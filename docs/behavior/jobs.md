# Jobs

Status words and how to read this: [the index](README.md).

Kinds: Job, JobRun.

```mermaid
stateDiagram-v2
    [*] --> Admitted : a JobRun's create within job.payloadLimit and its Job's input schema
    [*] --> Refused : over the limit, or off the input schema
    Admitted --> Kept : a replace that keeps spec.job and spec.input
    Kept --> Kept : spec.cancel turns true, once
    Admitted --> [*]
    Kept --> [*]
    Refused --> [*]
```

A Job is built as a Function is, from the fields it shares with one, and each of its executions is a JobRun, created
through the API as a WorkflowRun is (ADR-0347). This chapter holds what the API does with them: their checks, a run's
admission and who may act, the Job's pass, its revisions and its contract, then a run's start, its end, a restart and
its retention. The job.* keys are in [Limits](limits.md), and a restore's end of the open runs in
[Disaster recovery](disaster-recovery.md). New words: Admitted, Refused, Kept.

| Where | What happens | Time | You see | Status | Source |
|---|---|---|---|---|---|
| A Job's spec | A Function's fields with their checks, then `timeout`, 0s to 168h, and `concurrency`, 1 to 15 and at most 1 with a local volume. It has no scaling, replicas, pooling, protocol or ingress | `MaxJobConcurrency` 15 | 400 `spec.concurrency 2 with spec.volumes[0], a local volume` | decided | ADR-0347 Decision 1, `TestJobValidate` |
| A Job's Revision name | `<job>-v<generation>`, cut and hashed past 63 characters with -v<generation> last, so it never meets a Function's `<name>-<generation>`. A Revision names one of spec.function and spec.job | `maxRevisionName` 63 | 400 `spec sets both function and job` | decided | ADR-0347 Decision 2, `TestJobRevisionName`, `TestJobRevisionNameNeverEqualsRevisionName` |
| A JobRun's input over the limit | The create is refused. The input is fixed once the run exists, so an update never meets a lowered limit | `job.payloadLimit` 262144 | 413 `run input <n> bytes exceeds job.payloadLimit <limit>` | decided | ADR-0347 Decision 3, ADR-0148, `TestJobRunPayloadAdmission` |
| A JobRun's input off its Job's contract | The create is refused, naming each value under body.spec.input. A Job that is missing, or has no status.contract, leaves the input unchecked at the create | none | 422 `run input does not match the input schema of Job <job> (InputSchemaMismatch)` | decided | ADR-0347 Decision 3, `TestJobRunContractAdmission`, `TestJobRunAdmissionAnswers` |
| A JobRun's update | spec.job and spec.input are fixed, and spec.cancel may only turn true; any other change of the spec is refused | none | 400 `spec.job and spec.input are fixed once it is created`, `spec.cancel may only turn true` | decided | ADR-0347 Decision 3, `TestJobRunSpecAdmission` |
| A JobRun's create | The API server sets status.startedBy to the caller's identity subject, a record that grants nothing, and status.jobUID to its Job's UID when the Job exists. A body's status is dropped, and a replace keeps both | none | the run's status.startedBy | decided | ADR-0347 Decision 3, `TestJobRunCreateRecordsWhoStartedIt`, `TestCLIApplyJobAndJobRun` |
| Who may act on Jobs | The built-in roles treat a Job as a Workflow and a JobRun as a WorkflowRun: a developer of the namespace writes both, a viewer only reads them | none | 403 `create JobRun in "team-a" denied: viewer is read-only (verb "create")` | decided | ADR-0347 Decision 10, `TestJobKindsActAsWorkflowKinds` |
| A Job's apply | Its links, bindings and local volumes pass a Function's admissions. A Function a Job links to is not deleted while the link stays, and a local volume is the Job's own, apart from a same-named Function's | none | 400 `does not declare`; 409 `is linked by Job` | decided | ADR-0347 Decision 1, `TestJobAnswersMatrix`, `TestJobLinks`, `TestJobVolumesLiveApartFromAFunctions` |
| A Job's pass | It stamps its generation's Revision, then is Ready once the checks a Function passes before its first worker pass, with its reasons: platform, shape, runtime, resources, the workload hooks and DataMissing | none | `NoMatchingPlatform`, `ShapeInvalid` | decided | ADR-0347 Decision 2, `TestJobGatesOnTheFunctionsChecks`, `TestJobPlatformGate`, `TestJobDataMissingAfterLoss` |
| A Job's contract | Read by the revision's digest into status.contract. An image with none leaves a run's input unchecked, and one that does not compile holds the Job | none | `ContractInvalid` | decided | ADR-0347 Decision 2, `TestJobReadsItsContractByDigest` |
| A Job's old revisions | Deleted once neither the current spec nor a run that has not ended pins them; a run's change brings its Job's pass | none | the Revision gone from funcdctl get revisions | decided | ADR-0347 Decision 2, `TestJobKeepsTheRevisionsItsOpenRunsPin` |
| A run's start | A Pending run starts once its Job is Ready at its generation and a slot is free. One write stores it Running with jobRevision, jobUID, imageDigest, startedAt and traceId, and only then does its executor start | `controller.referentPollInterval` 2 s between tries | `JobNotFound`, `JobNotReady`; phase Running | decided | ADR-0347 Decision 4, `TestJobRunStartsOnceItsJobIsReady` |
| A run's slot | Fewer of its Job's runs Running than the Job's concurrency, and fewer of the node's than job.maxRunning. A freed node slot goes to the oldest waiting run whose Job has one free, by creation time then name, so a Job at its limit holds no other back | `job.maxRunning` 4 | `Queued` | decided | ADR-0347 Decision 4, `TestJobRunSlots` |
| A run's input at its start | Off the contract of the revision it pins, the run ends Failed: it is never stored Running and its handler is never called | none | `InputSchemaMismatch` | decided | ADR-0347 Decision 4, `TestJobRunInputOffTheContractFailsAtStart` |
| A held platform or namespace | The run reconciler starts, ends and deletes none of its runs and leaves their status as it is, until the release | until the release | the run's status unchanged | decided | ADR-0347 Decisions 4 and 8, `TestJobRunHeldActsOnNoRun`, `TestEveryRunnerConsultsHold`, `TestEveryRunnerConsultsHoldInNamespace` |
| A run's spec.cancel | A Pending run ends at once. A Running one has its executor stopped, and its worker with it, then ends; a late answer changes nothing, and the handler may have done part of its work | the driver's stop grace | `Cancelled` | decided | ADR-0347 Decision 6, `TestJobRunCancel` |
| A run's Job deleted | A run whose Job is deleted, re-created under another UID, or whose pinned revision is gone ends Cancelled, a Running one once its executor stopped. A run created before its Job has no jobUID and waits | none | `JobDeleted` | decided | ADR-0347 Decision 6, `TestJobRunJobDeleted` |
| A deleted JobRun | Its executor stops, and with it the run's worker | none | the worker gone | decided | ADR-0347 Decision 6, `TestJobRunDeletedStopsItsExecutor` |
| funcd starts | Every run stored Running ends Failed, after the drivers' sweeps and before any run starts, held or not: its handler may have run, and no run starts again on its own | at boot | `Interrupted` | decided | ADR-0347 Decision 7, `TestJobRunBootInterrupts`, `TestEveryRunnerConsultsHold` |
| An ended run | Deleted job.retention after its endedAt by a sweep, at most an hour apart, that skips held runs | `job.retention` 720h | the JobRun gone | decided | ADR-0347 Decision 8, `TestJobRunRetention` |

<!-- generated values, do not edit: just behavior-values -->
<details>
<summary>Jobs: values</summary>

| Value | Default | From |
|---|---|---|

</details>
<!-- end of generated values -->
