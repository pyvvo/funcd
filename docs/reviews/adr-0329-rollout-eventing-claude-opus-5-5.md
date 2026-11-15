## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0329 rollout, the events and Workflow features, model: claude-opus-5-5)

The work is the events and Workflow item of ADR-0329's rollout, commit `910e459f` on `94f1821f`. It describes the 25
operations of EventSource, Sensor, Workflow, WorkflowRun (with `getWorkflowRunLogs`) and the dead letters, and it proves
their cases with `TestEventsAnswersMatrix` (117 rows) and `TestWorkflowAnswersMatrix` (107 rows) in
`pkg/funcd/eventing_answers_test.go`. Every check below ran on branch `feat/rollout-0329-eventing` at `910e459f`.
ADR-0329 is already `Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: the replay's hold case names the platform's release for a namespace's hold** · attribution: model.
  - The 503 case of `replayDeadLetter` covers both holds: "The platform or the namespace is held, so funcd delivers no
    event." Its fix is "Retry once the hold is released, which funcdctl hold release does."
    (`internal/controlplane/deadletters.go:141`).
  - `funcdctl hold release` releases only the platform's hold. When only a namespace is held, it answers 409 `the
    platform is not held` (`internal/controlplane/hold.go:254`). A namespace's hold takes `funcdctl hold release
    --namespace` (`cmd/funcdctl/hold.go:43`, `:55`), and a hold that a Restore keeps is released only once that
    Restore ends (`internal/controlplane/hold.go:297`).
  - The matrix row provokes a namespace hold, which is the half that the fix gets wrong.
  - The fix copies the raise site's message, `nothing is delivered until funcdctl hold release`
    (`internal/sensor/sensor.go:720`), which predates this work. The Function feature's two hold cases of
    `invokeFunction` name `--namespace` and the end of the Restore.
  - Fix: name both releases, for example "Retry once the hold is released: funcdctl hold release releases the
    platform's hold, and funcdctl hold release --namespace a namespace's, once the Restore that holds it has ended."
- **Minor 2: the description of a dead letter's payload says that the action delivers it** · attribution: model.
  - `payload` reads "The CloudEvent that the action was to deliver, as JSON. A replay delivers it again."
    (`internal/eventing/deadletter/deadletter.go:33`).
  - funcd stores the CloudEvent that fired the action (`internal/sensor/sensor.go:370`). A Function action delivers a
    copy whose data is the action's input, projected over that event (`sensor.go:324`). A `workflow:` action starts a
    WorkflowRun instead, and a replay projects the input of the Sensor's current action again.
  - The description holds only for a Function action with no input. A reader who compares the payload with what the
    Function received finds other data.
  - Fix: for example "The CloudEvent that fired the action, as JSON. A replay runs the Sensor's current action on it
    again."

### Routed (not this work's)

- **A storage failure answers a 500 that no operation lists** · home: elsewhere · destination: the decider ·
  attribution: adr.
  - A failure of the metastore, the authorizer or the dead-letter store answers 500 on each of the 25 operations, and
    a request can reach it. Decision 6 says that a described operation's "listed answers are all it answers".
  - No described control-plane operation lists that 500. The pilot's KVStore operations and the Function feature's
    leave it out too, and both passed review. The checklist's bar is "each status that its tests get", and the
    Consequences accept that "an untested path can still return an unlisted status".
  - This item follows that reading. To list the 500 would be one decision for every described operation.
- **The shared 409 of `createWorkflowRun` tells the caller to replace the run** · home: elsewhere · destination: the
  shared verb cases (`internal/controlplane/describe.go:269-270`), the decider · attribution: adr.
  - The fix of the shared case is "Choose another name, or replace the existing WorkflowRun." A replace that changes
    the run's workflow, input or replay answers 409, because those fields are fixed once the run is created
    (`internal/controlplane/admission/workflowrun.go`). Its second half helps only a pause or a cancel.
  - Decision 1 gives each described kind the shared set as it stands, and the helper has no way for a kind to reword
    a shared case.

### Not checked yet (behavior that predates this work)

The implementer marked four behaviors as surprising. Each predates this work, and the work documents each one as the
code answers it, so none is a finding here. No one has run the two checks of the bug rule on them. The evidence found
on the way:

- Discard answers 204 for a missing ID, but get and replay answer 404. The port contract pins an idempotent delete
  (`internal/eventing/deadletter/contract.go:107`, `delete-is-idempotent`), which runs on the memory driver and on
  both Badger drivers. `discardDeadLetter` lists no 404, which matches.
- Replay and discard are authorized as a delete on Sensor. The comment of `authorizeDeadLetters` gives the reason: a
  dead letter is a failed Sensor action (`internal/controlplane/deadletters.go:148`). The 403 cases say "delete
  Sensors in the namespace", which matches.
- A replay in a held namespace answers 503 even for a missing ID: `Replay` checks the hold first
  (`internal/sensor/sensor.go:409`). Both cases are listed, and the 404's When claims no order.
- `step` is ignored before a run starts: `RunLogs` returns before it resolves the step while `status.traceId` is
  empty (`internal/controlplane/logs.go:148`). The two `step` cases begin "The run has started", which matches.
- Found in this review: the raise site's own message names only the platform's release
  (`internal/sensor/sensor.go:720`; see Minor 1).

### ✅ Verified correct (keep it)

- **No wire change.**
  - **The spec.** A Python diff of the spec at `94f1821f` and at `910e459f` finds 168 operations on both sides and 25
    that changed, exactly the item's.
    - Unchanged: operation IDs, paths, methods, tags, request bodies, success responses, and each parameter's name,
      place, required flag, schema, explode and style (descriptions aside).
    - In the components, only DeadLetter and DeadLetterListOutputBody change, and in their descriptions only.
  - **The live answers.** The commit's own 224 rows ran on an archive of `94f1821f` and on `910e459f`, with
    `runMatrix` replaced through `go test -overlay` by a recorder.
    - On every row the status, the Content-Type and Allow headers, and the problem's type, title, detail and errors
      are the same, with IDs, times and durations masked. Each row's detail appears on both commits.
    - `rejectUnknownQuery` gives every write its 422 for an unknown query parameter, whether the write is described
      or not (`internal/controlplane/dryrun.go:45`). The 422 of replay and discard is older than this work.
- **The shared change.**
  - `getFunctionLogs` is byte-identical in the spec, and `TestFunctionAnswersMatrix` passed in the full run.
  - `getWorkflowRunLogs` keeps its parameters. Only their descriptions change, and they now say what the shared
    reader does: it clamps `limit` (`internal/funclog/logread/logread.go:93-98`), so no limit case is missing.
  - A mutant that drops the `since` case from `logFilterCases` fails both features' matrices:
    `getFunctionLogs: one case holds "The since parameter"`, and the same for `getWorkflowRunLogs`.
- **The cases left out on purpose.**
  - Replay's 400 `stored payload is not a CloudEvent` (`internal/sensor/sensor.go:438`). Only funcd writes a dead
    letter: the park and the re-park write a CloudEvent struct that it marshalled (`sensor.go:370`), and no API
    writes one. Only a corrupted or hand-edited store gives this answer, and the 400 is listed anyway. It may stay
    out.
  - Replay's 503 `dead-lettering is not enabled` (`sensor.go:413`). The route exists only with a dead-letter store
    (`internal/controlplane/server.go:96`). The reconciler holds that same store (`pkg/funcd/funcd.go:1115-1116`,
    `pkg/funcd/binding_providers.go:750`, `:768`), and the event store always gives one
    (`internal/eventing/eventstore/eventstore.go:70`). No request reaches this answer.
  - The run logs' 404 when a started run has no record and its Workflow is gone (`internal/controlplane/logs.go:181`).
    The engine pins the spec in every record that it writes (`internal/workflow/engine.go:283`, `:425`), and the
    retention sweep deletes the WorkflowRun before its record (`internal/workflow/reconcile_run.go:842-858`). No
    production path that I found leaves a started run without its record. The 404 is listed for the missing run.
  - Storage 500s: a request can reach them. They are routed above.
- **The choices that the ADR does not state.**
  - EventSource and Sensor have no case of their own. No admission handles either kind
    (`internal/controlplane/admission/`), their handlers are the generic ones
    (`internal/controlplane/handlers.go:860-874`, `:1262-1278`), and the control plane checks no hold on a write.
    Their `v1.Validate` refusals are the shared 400.
  - One 503 case covers every failed replay delivery.
    - The Sensor's invoker answers Unavailable for a refusal, a transport failure and every answer at or above 400
      (`internal/sensor/invoker.go:99-126`). A wake of a missing, Failed or slow Function is Unavailable too
      (`internal/activator/activator.go:272`).
    - Only the HTTP target (409, `internal/activator/protocol.go:24`) and the input (400) keep their own kinds, and
      each has its case. `internal/expr` maps a resolver's NotFound to Invalid (`internal/expr/check.go:446`).
    - A mutant that answers every failed delivery with 503 fails both rows.
  - `discardDeadLetter` lists no 404: the port contract above pins the idempotent delete.
  - The `step` 400s say "The run has started", which is true to `logs.go:148`.
  - The dead letters are real. A 100 ms timer fires on Sensors whose Function does not exist, with
    `eventing.deliveryAttempts` 1, so the rows run the real park and replay paths on the Badger driver.
    - Each row takes a dead letter of its own.
    - The Sensors that a replay calls listen to an EventSource whose next event is an hour away.
    - A delivery in flight keeps the action that it was queued with.
  - The contract resolver.
    - `WithWorkflowContractResolver` is an option of the base (`pkg/funcd/options.go:524`), which `funcdctl dev`
      uses.
    - The admission under test reads the Workflow's cached `status.contract`, which the real reconciler derives
      through the resolver.
  - DeadLetter's descriptions.
    - "Newest first" matches the port's contract (`internal/eventing/deadletter/deadletter.go:47`, the
      `list-newest-first-and-namespace-scoped` subtest).
    - `attempts`, `reason` and `failedAt` match the park and the re-park (`internal/sensor/sensor.go:441-453`,
      `internal/sensor/retry.go:160-170`).
    - `payload` is Minor 2.
- **Every other case's text matches its raise site.**
  - The Workflow's undeclared binding (`internal/controlplane/admission/bindingdeclared.go:269-317`): image steps
    only, against the Workflow's declarations or its App's.
  - The run input over `workflow.payloadLimit` (256 KiB by default, `internal/platform/config/config.go:35`).
  - The run input off the contract, and the fixed fields.
  - The dead letters' 404 and 409 cases, and the run logs' 404 and step cases.
  - `funcdctl eventing dlq list` and `funcdctl hold release` exist.
- **The matrices.**
  - `runMatrix` checks every row, with no exemption, and each of the 117 and 107 subtests passed by name.
  - **Deterministic.**
    - 14 runs under `-race` alone (3, then 1 verbose, then 10) and one in the full run, all ok. The 10 runs took
      15.3 s on a host at a load of 4 to 10.
    - The replay in progress holds its claim on a worker that signals when it receives the call, under a 30 s
      delivery timeout.
    - The hold marker is read live (`internal/platform/hold/namespace.go:55`).
  - **Four mutants failed a test.**
    - Dropping the `since` case fails both matrices.
    - Answering every failed delivery with 503 fails the 400 and 409 rows: `expected: 400`, `actual: 503`.
    - Making the fixed-field refusal a 400 fails its row: `expected: 409`, `actual: 400`.
    - Adding a replay case that no row provokes fails the matrix: `cases with no row: replayDeadLetter 503`.
  - The tests sit in a file of their own and build outside e2e. Both call `t.Parallel()`, and no shared helper changes.
- **Conventions.**
  - The bloat audit (`scripts/agent/audit.py --base 94f1821f --report-only`) passes with no flag: no lint delta,
    clone, masking pattern or dependency.
  - Comments: 7 to 97 code lines in production, 16 to 325 in tests. No `nolint` is new.
  - The rollout list goes from 122 to 97 lines, with none added, and `TestEveryOperationIsDescribed` passes.

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=3` of the two matrices alone | ok, 7.5 s |
| the same with `-count=1 -v`, then with `-count=10` | 117 and 107 subtests pass; ok, 15.3 s |
| `go test -race ./internal/controlplane/... ./internal/eventing/deadletter/...` | ok |
| `go build ./...`; `go vet` on the touched packages and `pkg/funcd`, on the host and with `GOOS=linux` | exit 0 |
| `golangci-lint` on the touched packages and `pkg/funcd` | 0 issues |
| `go test -race -count=1 ./pkg/funcd`, no filter: the two-sided guard | ok, 97.3 s |
| `scripts/agent/d just generate` on the clean tree | exit 0; the tree stayed clean |
| The spec diff, and the 224 rows on both commits | identical where it must be |
| Four overlay mutants | each failed a test |
| `scripts/agent/audit.py --base 94f1821f --head HEAD --report-only` | PASS, 0 hard flags |
| `just ci` and the e2e suite | not run: the batch gate runs them once |

### Definition of Done

15 of the 15 items hold: ADR-0329's 9 checklist items and its Definition of done, applied to this feature, except
`just ci`. The batch gate runs `just ci`, and its parts ran here: build, vet, lint, the tests and `just generate`.

### Model scorecard

To be recorded by the batch's last commit: claude-opus-5-5 on ADR-0329 (implementation, the events and Workflow
rollout) → pass, 0/0/2, 2 model-attributed, DoD 15/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can reword one fix and one description (Minors 1 and 2) in this pull request, then run
`just generate`. The storage 500s and the shared 409's fix go to the decider. Nothing is stamped: ADR-0329 is already
`Implemented`.
