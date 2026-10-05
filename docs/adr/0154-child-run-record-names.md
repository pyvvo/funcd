# ADR-0154: Child run record names — `<parentRun>.<step>`, never a WorkflowRun's name

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, sub-workflow, run-state, replay, naming
- **Realizes**: [FEAT-0005/F70](../feat/0005-feat-workflow-engine.md) (sub-workflows: the child run's durable record)
- **Supersedes (in part, scoped)** — ADR-0099 and ADR-0107 keep status `Implemented` and get a
  `Superseded in part by: ADR-0154` back-link at acceptance:
  - [ADR-0099](0099-sub-workflows.md) Decision §2, second bullet (lines 109–110): "a deterministic nested run name
    `<parentRun>-<step>`" — the separator becomes `.`.
  - [ADR-0107](0107-run-replay-from-step.md) Decision §5 (lines 153–154): "new child record `<replay>-<step>`" — it
    becomes `<replay>.<step>`.
  - ADR-0107 Scope (lines 79–80, a child run "replayable *standalone*") and Decision §5 (lines 158–159, replaying
    from inside a child): through the API, a child is re-run by replaying its parent from the `workflow:` step; only
    `Engine.Replay`, which has no production caller, still seeds from a child record. The child-record digest pin
    (§5 lines 155–157) stands.
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine-owned run-state store) ·
  [ADR-0104](0104-cross-subworkflow-trace-linking.md) (a child shares its parent's trace; the run `"parent-sub"` in
  its trace-shape example, line 161, now reads `parent.sub` — an illustration, so no back-link) ·
  [ADR-0146](0146-workflowrun-drive-model.md) (Proposed; inline children stay synchronous and are never registry
  entries — compatible)

## Context & Need

The engine records an inline sub-workflow child run in the same run-state keyspace as top-level runs
(`run/<ns>/<name>`, `internal/workflow/runstate/badger/badger.go:66`), under `<parentRun>-<step>`
(`internal/workflow/subworkflow.go:48`). A WorkflowRun name may contain `-`, so that name can be a user's run or
another child ([#117](https://github.com/pyvvo/funcd/issues/117)). Reproduced on main 1193be6 with the real
`RunReconciler` and in-memory Badger:

- **A** — parent `p` (step `sub` runs workflow `childwf`) finishes; a user then applies WorkflowRun `p-sub` of
  workflow `other`. The reconciler resumes the child's terminal record: `p-sub` reports `Succeeded` with `childwf`'s
  steps, and `other` never runs.
- **B** — the user's `p-sub` finishes first; parent `p` then overwrites record `p-sub` (workflow `childwf`, empty
  `RunUID`). The status stays right, but a replay of `p-sub` now fails `SeedInvalid` (workflow mismatch).
- **C** — run `a` (step `b-c`) and run `a-b` (step `c`) both write `a-b-c`; one child record is lost.

The run-UID guard (`Record.RunUID`, the [#307](https://github.com/pyvvo/funcd/issues/307) fix) does not catch them:
a child record has an empty `RunUID`, and `foreignRecord` (`internal/workflow/reconcile_run.go:256-258`) matches an
empty uid by name. For the same reason the retention sweep's `deleteRun` (`reconcile_run.go:480-491`) would delete a
user's WorkflowRun when the same-named child record expires (read from the code, not probed).

Purpose: a child run's record name stays deterministic — a resumed parent re-runs the child under it (ADR-0099 §5),
and traces and logs carry it — and it can never equal a WorkflowRun's name or another child's.

## Scenarios

- `scenario: user-run-keeps-its-own-record` — Given parent run `p` whose `workflow:` step `sub` ran `childwf` to
  `Succeeded`, When a WorkflowRun `p-sub` of workflow `other` (steps `x` → `y`) is created, Then `p-sub` dispatches
  `other`'s steps once each, its status lists `other`'s steps, and records `p.sub` and `p-sub` both exist.
- `scenario: parent-never-overwrites-a-user-record` — Given a finished WorkflowRun `p-sub` of `other`, When parent
  run `p` runs, Then record `p-sub` still holds workflow `other` and `p-sub`'s uid, and a replay of `p-sub` from step
  `y` ends `Succeeded`.
- `scenario: child-names-never-collide` — Given run `a` with `workflow:` step `b-c` and run `a-b` with `workflow:`
  step `c`, When both finish, Then records `a.b-c` and `a-b.c` both exist, each holding its own child workflow.
- `scenario: nested-child-names` — Given parent `p` whose step `sub` runs a child whose step `inner` runs a grandchild,
  When `p` finishes, Then the records are `p`, `p.sub` and `p.sub.inner`.
- `scenario: restart-reruns-child-under-its-dotted-name` — Given parent `p` stopped by a crash while its step `sub`
  is running and record `p.sub` is not terminal, When `p` is resumed, Then the child re-runs from its first step
  (ADR-0099 §5), record `p.sub` ends `Succeeded`, and the store holds only records `p` and `p.sub`.
- `scenario: replay-child-uses-dotted-name` — Given source run `src` whose `workflow:` step `sub` recorded child
  `src.sub`, When replay `rep` re-runs from `sub`, Then the fresh child is recorded as `rep.sub`, and `src.sub` is
  unchanged.
- `scenario: child-expiry-keeps-user-run` — Given parent `p` ran (the engine wrote its child record), then WorkflowRun
  `p-sub` of `other` ran, and the clock passes the child record's retention but not `p-sub`'s, When the retention
  sweep runs, Then record `p.sub` is deleted, and WorkflowRun `p-sub` and its record remain.
- `scenario: child-name-never-admitted` — Given a WorkflowRun named `p.sub`, or one whose `spec.replay.run` is
  `p.sub`, When it is validated at admission, Then it is rejected as invalid.

## Scope

**In**: the record name of an inline child run (under a top-level run, under another child, under a replay); the
three tests that hard-code it; the three superseded clauses.

**Out**:
- The CloudEvent `id` `<run>-<step>-<attempt>` (`internal/workflow/dispatch.go:44`): CloudEvents requires `source` +
  `id` to be unique; `source` is `<ns>/<run>`, and within one run the attempt after the last `-` keeps the id
  unique. Unchanged.
- The owned Function name `<workflow>-<step>` (`materializedStepName`, `reconcile_workflow.go:194`) — Open question 1.
- Standalone child replay through the API (a `spec.replay.run` naming a child record): its own topic (a new field
  type, ADR-0107 §6 superseded, a `funcdctl workflow replay` change); a board card (`/project-management`) at
  Proposed, a follow-up ADR if picked up.
- Migrating records: no production deployment exists (Decision 4).

## Constraints & Decision drivers

- Deterministic: a parent resumed mid-child re-runs the child (ADR-0099 §5), which must land on the same record.
- No new admission or engine check, and no new keyspace (decider).
- A WorkflowRun name, a step name and `spec.replay.run` are DNS labels (`DNSLabel`, `api/types/v1alpha1/ids.go:19`),
  which hold no `.`: `ObjectMeta.Validate` (`metadata.go:154`), `Workflow.Validate` (`workflow.go:249`),
  `WorkflowRun.Validate` (`workflowrun.go:119`). The store runs `Validate` on every write (`internal/store/store.go:294`
  create, `:356` update), so a run created without API admission (a Sensor action, ADR-0109) is held to it too.
- `Config.MaxSubworkflowDepth` (default 8) bounds a child name at 9 × 63 + 8 = 575 bytes, far below Badger's
  65000-byte key limit (badger v4 `txn.go`, `maxKeySize`).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **`<parentRun>.<step>`** | Never a WorkflowRun name, never another child's; one production line; no new check | Names change shape; a WorkflowRun cannot name a child as its replay source | **chosen** |
| Separate keyspace for child records | A user run and a child never share a key | More churn: listing, describe, replay lookups and the sweep each learn a second keyspace; C (child against child) still needs an unambiguous name | rejected |
| Collision policy: admission refuses a WorkflowRun named like a child, or the engine refuses a child whose record exists | Keeps the `-` form | Races (a parent can run after the admission check); a parent fails because of someone else's name | rejected |
| Keep `-`; `foreignRecord` also compares `Workflow`/`Depth` | Small | One key still holds two runs: B and C overwrite the record whoever reads it later | rejected |

## Decision

1. **The name.** An inline child run's record is named `<parent record name>.<step>`: `p.sub`, nested `p.sub.inner`;
   a replay `rep` that re-runs `workflow:` step `sub` records its child as `rep.sub`. One new helper, `childRunName`
   (no such name on main), builds it. `runChild` is its only production caller; the replay path reaches it through
   the same `runChild`.
2. **No new check.** The name is never a WorkflowRun's (Constraints), and two (parent, step) pairs never give the
   same name (the step is the label after the last `.`, the parent is the rest). Admission, the engine, the reconciler
   and `foreignRecord` stay as they are: `Get`, `Resume`, `Pause` and `Cancel` look up a WorkflowRun's own name, which
   never reaches a child record; `deleteRun` looks up a WorkflowRun under an expired record's name, which for a child
   record finds none.
3. **A record name, not an object name.** `runstate.Record.Name` stays typed `v1.ObjectName`, but a child's name is
   never validated as one and may exceed 63 bytes; no consumer validates, parses or truncates it (Contracts). ADR-0107
   §6 stands, so no WorkflowRun names a child record as its replay source (the third superseded clause).
4. **No migration.** No production deployment exists. On an install upgraded from a released build, a child record
   written before the upgrade keeps its `-` name. Until the retention sweep expires that record, a WorkflowRun named
   `<parent>-<step>` can still resume it, and the sweep can delete that WorkflowRun when the record expires.

## Temporary workarounds

None.

## Contracts

```go
// internal/workflow/subworkflow.go — runChild: childRun := childRunName(parent.Name, n.name)

// childRunName names the record of the inline child run that step of parent runs (ADR-0154): the parent record's
// name, ".", the step name. A WorkflowRun name and a step name are DNS labels, which hold no ".", so the result is
// never a WorkflowRun's name, and the step after the last "." makes it unique per (parent, step). It is a run-record
// name, not an API object name: it may exceed 63 bytes and is never validated as an ObjectName.
func childRunName(parent, step v1.ObjectName) v1.ObjectName {
	return parent + "." + step
}
```

Every site that builds, reads or validates a run name (main 1193be6):

| Site | Use | With `<parent>.<step>` |
|---|---|---|
| `subworkflow.go:48`, `:53` | builds the name, passes it to `execute` | `childRunName(parent.Name, n.name)` |
| `engine.go:228`, `:247`; `:262`, `:268`, `:275`, `:1004` | `rec.Name`; error text | text only |
| `engine.go:1071` → `badger.go:66-67` `recordKey`, `:88-90` `Put`, `:116-145` `List` | key `run/<ns>/<name>`; `Put` checks non-empty only; `List` scans the `run/<ns>/` prefix | no parse; no length check below 65000 bytes |
| `engine.go:302` `Resume`, `:497` `Pause`, `:516` `Cancel`, `:1011`; `reconcile_run.go:178` `applyRequest`, `:239-258` `started`/`foreignRecord` | `Get` by the WorkflowRun's (or the run's own) name | a WorkflowRun's name never reaches a child record (fixes A) |
| `engine.go:326` `Replay`, `:333`, `:386`; `reconcile_run.go:226` | replay source read by `seed.Run`, recorded as `SourceRun` | the reconciler's `seed.Run` is a DNS label; `Engine.Replay` has no production caller (a probe replayed `p.sub` to `Succeeded` through it); `funcdctl workflow replay` reads the source WorkflowRun first (`cmd/funcdctl/workflow.go:61`) |
| `engine.go:470-490` `SweepExpired`; `reconcile_run.go:480-491` `deleteRun`, `:462` `recordClosedRuns` | lists every record; looks up the WorkflowRun under a record's name | the lookup of `p.sub` is NotFound: a user run is never deleted |
| `reconcile_run.go:336-338` `mirrorTransition` (ADR-0146, if it lands first, replaces it with `syncStatus`, which reads the record by the WorkflowRun's own name, like `applyRequest`/`started`) | skips records not named like the run | unchanged: a dotted child name never equals a WorkflowRun name |
| `engine.go:777`, `:822`, `:980`; `dispatch.go:43-44`, `:110-111` | CloudEvent `source` `<ns>/<run>`, `id` `<run>-<step>-<attempt>`; log, error | `default/p.sub`, `p.sub-double-1`; text |
| `engine.go:965` `failureContext.Run` | the onFailure input's `run` | `"string"` in `failureContextSchema` (`engine.go:954`), no pattern |
| `reconcile_run.go:63`, `:65`, `:78`; `funclog/tracesink.go:217-218`, `:282-288` | span attribute `funcd.run`; `Resource.Replica`; trace key `traces/<ns>/<fn>/<date>/<unixnano>-<name>.otlp.jsonl` | no code parses `traces/` keys (`compact/keys.go` parses `logs/` only); a name over 63 bytes is bounded in the key (PR #608) |
| `ids.go:19`, `:124`; `metadata.go:154`; `workflowrun.go:119`; `workflow.go:249`; `store.go:294`, `:356` | the DNS-label checks (Constraints) | never applied to a child name |
| `cmd/funcdctl/workflow.go` (`replay`, `runs`, `describe`); `internal/controlplane/logs.go:107-121` | read WorkflowRun objects | never show a child name |
| metrics | no metric carries a run name | — |

| Consumes | Exposes |
|---|---|
| ADR-0099 `runChild`; the ADR-0094 run-state port | Child record names `<parent>.<step>` in run-state, the `funcd.run` span attribute, trace keys, the CloudEvent envelope and the onFailure input |

## Implementation plan

1. `internal/workflow/subworkflow.go`: add `childRunName`; `runChild` calls it at line 48.
2. `internal/workflow/subworkflow_test.go`: `run-p-sub` → `run-p.sub` (lines 100–101, 310, 381).
3. One test per scenario, each under a `// scenario: <name>` comment, asserting the literal dotted names (they pin the
   format, so they never call `childRunName`):
   - `internal/workflow/reconcile_run_test.go` (its `seedWorkflow`/`seedRun`/`reconcileRun` harness, the engine's
     `Children` = `fakeChildren`): `TestScenarioUserRunKeepsItsOwnRecord`,
     `TestScenarioParentNeverOverwritesAUserRecord`, `TestScenarioChildExpiryKeepsUserRun`
     (`RunReconciler.SweepExpired` with the clock past retention).
   - `internal/workflow/subworkflow_test.go` (`childEngine`): `TestScenarioChildNamesNeverCollide`,
     `TestScenarioNestedChildNames`, `TestScenarioRestartRerunsChildUnderItsDottedName` (extend `crashAt`,
     `engine_test.go:485-509`, to keep both records `p` and `p.sub` as they stand at the child's first dispatch; put
     them in a fresh store; `Resume` `p`).
   - `internal/workflow/replay_test.go`: `TestScenarioReplayChildUsesDottedName`.
   - `api/types/v1alpha1/workflowrun_test.go`: `TestScenarioChildNameNeverAdmitted`.
4. `scripts/agent/d go test -race ./internal/workflow/... ./api/types/v1alpha1/` and
   `scripts/agent/d go tool golangci-lint run ./internal/workflow/... ./api/types/v1alpha1/`; `scripts/agent/gate.sh`
   runs the repo-wide checks once for the PR, which carries `Fixes #117`.
5. Docs: at Draft, the F70 row's ADR column becomes `[ADR-0099] (+ [ADR-0154] — child record names)` and its status
   `implemented · child record names: adr`, which then advances with this ADR (split-status precedent: FEAT-0000/F13);
   the F71 row stays unchanged (its what/why does not promise child replay). At acceptance, the `Superseded in part
   by: ADR-0154` back-links in ADR-0099 and ADR-0107.
6. Release note: after an upgrade, do not create a WorkflowRun named `<parent>-<step>` until the old run records have
   expired, or clear the run state (Decision 4).
7. Definition of done: each scenario has one named, passing test; the three updated tests pass; no other production
   line changes; the gate is green.

## Review checklist

- [ ] `childRunName` is the only builder of a child record name; no `"-" +` builds a run name in `subworkflow.go`.
- [ ] No new admission, engine or reconciler check; `foreignRecord`, `started` and `deleteRun` are unchanged.
- [ ] No code validates, parses or truncates `runstate.Record.Name` as an `ObjectName`.
- [ ] A WorkflowRun name and `spec.replay.run` still reject `.` (ADR-0107 §6 unchanged).
- [ ] Each scenario has one named, passing test that asserts literal dotted names; the three old tests use
      `run-p.sub`.
- [ ] ADR-0099 and ADR-0107 receive only the back-link line; the F70 row is updated.

## Consequences

- Positive: A, B, C and the sweep deleting a user's WorkflowRun cannot happen for records written under the dotted
  name, with no new check, keyspace or API field (pre-upgrade `-` records: Decision 4).
- Negative: a child's name changes shape wherever it surfaces: run-state listings, the `funcd.run` span attribute and
  trace keys, the CloudEvent `source`/`id` a child step's function receives, the `run` field of a child's onFailure
  input, logs and error text. funcdctl output is unaffected (it reads WorkflowRun objects).
- Risks accepted: `Record.Name` is typed `v1.ObjectName` but holds a non-ObjectName for a child; a future caller
  that validates it would refuse children. The helper's doc comment and the Review checklist name this.

## Open questions

1. **Owned Function names** `<workflow>-<step>` (`materializedStepName`) keep the `-` separator: a Function name must
   be a DNS label, so `.` is not available. Their naming is out of scope here.
2. **Trace key length** — answered by PR [#608](https://github.com/pyvvo/funcd/pull/608), found while drafting this
   ADR: a child record name of 204 bytes or more (three nested 63-byte names) made the trace write fail on the
   `file://` driver, losing the run-root span. The trace key now writes a replica longer than 63 bytes as its first
   46 bytes, `-` and 16 hex digits of its SHA-256; the full name stays in the `replica` resource attribute. The
   separator change here does not alter lengths.

## References

- Issue [#117](https://github.com/pyvvo/funcd/issues/117); issue [#307](https://github.com/pyvvo/funcd/issues/307)
  (the run-UID guard, PR #407); the probes ran with `go test -overlay` from a scratch directory and are not
  committed.
- [ADR-0094](0094-workflow-engine-core.md), [ADR-0099](0099-sub-workflows.md),
  [ADR-0104](0104-cross-subworkflow-trace-linking.md), [ADR-0107](0107-run-replay-from-step.md),
  [ADR-0109](0109-sensor-event-action-binder.md), [ADR-0146](0146-workflowrun-drive-model.md).
- CloudEvents v1.0 specification, `id` attribute (`source` + `id` unique per event); RFC 1123 §2.1 (host name
  labels); badger v4.9.2 `txn.go` (`maxKeySize = 65000`).
