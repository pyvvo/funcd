## Verdict: changes requested — 0 blockers, 1 major (ADR-0349 implementation, model: claude-opus-5-5)

This report reviews `feat/jobs-binding` at c7bd2ca0, the six commits on top of `feat/jobs-approval`, and the ADR-0349
part of commit 0794b68 of pyvvo/funcd-typescript and commit a9860c1 of pyvvo/funcd-python (`ctx.job.start`,
`ctx.job.get`, `JobRunState`, `JobError`, the context and pool wiring and the `api.json` entries). The funcd tests ran
the shims under review through the worktree's uncommitted `go.work`. The binding, the two Cedar actions, the two
endpoints, the starter and both shims meet Decisions 1 to 5. The nine scenarios, the cause matrix and the design
model's oracles pass. One defect remains: a start sent again answers 409 when a string in its input holds `&`, `<` or
`>`. The 422 of Decision 4 needs the decider's ruling before the `Implemented` stamp (see Routed).

### 🔴 Blockers

None.

### 🟡 Major 1 — A start sent again answers 409 when a string in its input holds `&`, `<` or `>` · attribution: model

Decision 2 answers 200 when "a run of the bound Job already holds" the name "with an input equal as a JSON value, ... so
a start sent again starts nothing". The Alternatives reject a 409 for that case, because "a retried Function fails
until its event dead-letters, though its run started". The starter compares two spellings of one input:

- The store writes the run with `json.Marshal` (`internal/store/store.go:656`). `json.Marshal` writes `&`, `<` and `>`
  inside a `json.RawMessage` as `\u0026`, `\u003c` and `\u003e`, so the stored `spec.input` keeps that spelling.
- `held` compares the stored input with the sent input through `sameJSONValue` (`pkg/funcd/jobstart.go:135`).
  `sameJSONValue` compares a string by its compact text (`pkg/funcd/jobstart.go:228`), so `"a=1&b=2"` and
  `"a=1\u0026b=2"` differ, although they are the same JSON string.

A probe (a temporary test over `starterOver`, removed after the run) started `tofu-amp` with
`{"url":"https://x/?a=1&b=2"}`. The store held `{"url":"https://x/?a=1\u0026b=2"}`, and the same start sent again
returned `job.run-name-taken`. `{"cmd":"a > b"}` gave the same result, and `{"op":"plan"}` returned the run with
`created` false. Through the Node shim, `TestScenarioJobStartNamedTwice` with `"stack": "net&dev"` in both inputs (a
temporary edit, restored) failed: the call sent again got `context.job.start("stack") failed: 409`.
Neither shim escapes these characters (`JSON.stringify`, `json.dumps`), so a retried start fails whenever its input
holds a URL with a query string, a shell command or an HTML fragment. Both checks of the bug rule hold: the probe
reproduces it on the branch, where the code is new, and nothing accepts it, since Decision 2 and the row "A named start
sent again" (`docs/behavior/jobs.md`) say that an equal input answers the run.

Fix: compare decoded values. For example, decode both inputs with `UseNumber` into `any` and compare them with
`reflect.DeepEqual`, which also replaces the hand-written walk over objects and arrays. Add cases with `&`, `<` and `>`
to `TestSameJSONValue` and to the input of `TestScenarioJobStartNamedTwice`.

### Minor

- **The `job` field's description says that a read on a missing Job's alias answers 404** · attribution: model.
  `FunctionJob.Job` says "a start or a read on its alias answers 404" (`api/types/v1alpha1/function.go:121`), and the
  OpenAPI schema and the Function, App and Job Spec pages repeat it. A read of a run that the deleted Job left answers
  that run until `job.retention` removes it: the Cedar check passes on a missing Job, and `TestJobRunStarterStarts`
  pins the read (`pkg/funcd/jobstart_internal_test.go:80`). Fix: say that a start answers 404, and that a read answers
  the Job's runs until `job.retention` removes them.
- **TypeScript's `JobRunState` types `startedAt` and `endedAt` as optional strings** · attribution: model.
  pyvvo/funcd-typescript `shim/src/job.ts:39` and `:41` say "absent before". funcd sends `null` before, since
  `local.JobRunState` has no `omitempty` and `TestJobReadAnswers` pins `"startedAt": null`, so a check for `undefined`
  misses it. Fix: type both as `string | null` and say "null before", as the Python shim's `str | None` does.
- **`jobGrants` reads the caller's bindings and builds its principal by hand** · attribution: model. `bound`
  (`pkg/funcd/jobstart.go:52-73`) and the requests in `Grant` and `Start` repeat `services.CallerBindings` and
  `services.Principal` (`internal/services/bindcache.go:89`, `:108`), which the KV, Bucket, SQL, Secret and add-on
  facets use. Fix: call them.
- **The scenario `job-start-name-taken` does not tell another Job's run from another input** · attribution: model. The
  run `tofu-gen-7` of `backfill` holds no input, and the planner sends `{}` (`pkg/funcd/job_start_e2e_test.go:201`,
  `plannerSrc`), so the start also differs in its input. A mutant that drops `run.Spec.Job == job` from `held`
  (`pkg/funcd/jobstart.go:135`) passed `TestScenarioJobStartNameTaken`, `TestJobCauseMatrix` and
  `TestJobRunStarterStarts`. Fix: give the other Job's run the input that the start sends.

### Routed (not this work's)

- **422 for an input off the Job's contract** · home: elsewhere, the decider. Decision 4, the Contracts' endpoint table
  and the scenario `job-start-input-refused` say 422. The local API answers every error through its interceptor
  (`internal/workernode/local/interceptor.go:87`, ADR-0330 Decision 3) from `api/fault`'s single status map. In that map
  `Invalid` is 400 (`api/fault/problem.go:28`), and ADR-0148 Decision 1 says "the local API included (no
  `Invalid→422`)". No kind answers 422. The kinds added after ADR-0002 came from the Contracts of their ADRs, such
  as ADR-0151's `DeadlineExceeded` and ADR-0320's `PreconditionFailed`, and ADR-0349 neither names one nor supersedes
  ADR-0148 Decision 1. The builder answers 400 with `job.input-refused`, records the row as `differs`
  (`docs/behavior/jobs.md:72`) and pins 400 in `TestScenarioJobStartInputRefused` (`pkg/funcd/job_start_e2e_test.go:170`).
  The ruling is either a partial supersession of Decision 4 and the scenario to 400, or a fault kind that answers
  422. What stays here: the test, the row and both shims' docs then state the ruling. Today the `start` docs of both
  shims say 422 (`shim/api.json`, `shim/src/job.ts`, `shim/src/funcd_shim/job.py`), so the Job Spec page shows 422 in
  the method's row and 400 in its cause table. The `Implemented` stamp waits for this ruling.
- **The shims are not released** · home: the integration step. `go.mod` pins funcd-typescript v0.34.0 and funcd-python
  v0.29.0, so `ctx.job`, the Job Spec page and the scenarios pass only through the uncommitted `go.work`. Step 4 of the
  Implementation plan ("released and bumped") and the Definition of done's `just ci-full` wait for the shims' PRs, their
  releases and the `go get`.
- **The Contracts' `JobRunStarter.Get` has no namespace** · home: elsewhere, the ADR's text; no change asked. The
  platform has one starter, so `Get` takes `ns` beside `job` and `run`. `JobFacet` also takes a third argument,
  `JobGrants`, for the binding and Cedar check that Decision 2 gives the facet, as the add-on facet's `Addon` does. Both
  are the smallest change, and the error kinds match the Contracts: `job.run-name-taken` is a Conflict, and
  `job.run-not-found` is a NotFound.
- **A read on the alias of a deleted Job** · home: elsewhere, the decider; no change asked. Decisions 1 and 4 can be read
  as "every call on a missing Job answers 404", while Decision 3 lists only a run of another Job and a run that
  `job.retention` removed. The facet follows Decision 3 and ADR-0347 Decision 6 ("Runs stay listed"), so a Function
  reads its run's end, `Cancelled` with `JobDeleted`.
- **The security page's list of Policy actions** · home: this ADR's guide step, which comes later.
  `docs/guide/develop/security-and-networking/security/index.md:158` lists the actions and entities that a Policy
  names, without `job::start`, `job::read` and `Job`. It already lacked `addon::call`.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`, all exit 0:
  - `go vet` on `api/types/v1alpha1`, `internal/auth`, `internal/auth/cedar`, `internal/workernode/local`,
    `internal/controlplane/kindref` and `pkg/funcd`, and with `-tags e2e` on `pkg/funcd`.
  - `go test -race -count=1` on the first five packages.
  - In `pkg/funcd` without tags, with `-race`: `TestJobCauseMatrix` (13 Job rows, 12 of them this ADR's),
    `TestEveryDeclaredCauseHasARow`, `TestScenarioFacetsMatch`, `TestJobRunStarterStarts` and `TestSameJSONValue`.
  - `go tool golangci-lint run` on the six packages: 0 issues.
  - `just check-behavior`, `just check-specs` (27 specs, 0 failing) and `just check-hygiene`.
  - specgen over the tree, then `scripts/check-generated.sh`: the OpenAPI schema and the Spec pages equal the commit.
- **The nine scenarios**, `-tags e2e -race -count=1`, each with `t.Parallel()` and each under 2.1 s:
  `TestScenarioFunctionStartsJobRun`, `TestScenarioFunctionReadsJobRun`, `TestScenarioJobStartWithoutBinding`,
  `TestScenarioJobStartForbiddenByPolicy`, `TestScenarioJobStartInputRefused`, `TestScenarioJobStartNamedTwice`,
  `TestScenarioJobStartNameTaken`, `TestScenarioJobStartJobGone` and `TestScenarioJobGetOtherJob`.
- **The shims.** TypeScript: 193 tests pass, `tsc --noEmit` passes, and `biome ci shim` passes, its 5 warnings in files
  that this work did not touch; the committed `shim.mjs` and `pool.mjs` equal a fresh esbuild of `src/`. Python:
  `just check` exits 0, with ruff, mypy and 525 shim tests; `test_api.py` and `api.test.ts` hold `api.json` current.
- **The design model.** `just check-specs` passes `docs/design/job/start.fizz` with 6 states in 0.3 s, as the ADR says;
  `unnamed-retry` fails `AtMostOneRun`, `conflict-on-retry` fails liveness, and `start.md` is current. The oracle
  markers of both assertions sit on `TestScenarioJobStartNamedTwice`, and `AtMostOneRun` also on
  `TestJobRunStarterStarts`.
- **The mutants**, each a one-line edit restored with `git checkout`:

  | Mutant | Caught by |
  |---|---|
  | `unnamed-retry`: the starter names every start anew | `TestScenarioJobStartNamedTwice`, `TestJobRunStarterStarts` |
  | `conflict-on-retry`: a held name answers 409 | `TestScenarioJobStartNamedTwice`, `TestJobRunStarterStarts` |
  | Cedar's deny is ignored | `TestScenarioJobStartForbiddenByPolicy`; the matrix's two `job.denied` rows |
  | Any alias resolves to a binding | `TestScenarioJobStartWithoutBinding`; the matrix |
  | A read skips the run's Job | `TestScenarioJobGetOtherJob`, `TestJobRunStarterStarts` |
  | A start skips the admission's refusal | `TestScenarioJobStartInputRefused`; the matrix's `job.input-refused` and `job.input-too-large` rows |
  | `startedBy` is the bare name | `TestScenarioFunctionStartsJobRun`, `TestJobRunStarterStarts` |
  | Inputs are compared byte by byte | `TestScenarioJobStartNamedTwice`, `TestSameJSONValue`, `TestJobRunStarterStarts` |
  | A Job's spec takes `bindings.job` | `TestFunctionJobBindingValidate` |
  | A read checks `job::start` | `TestJobReadAnswers` |
  | A read answers the result in any phase | `TestJobReadAnswers` |
  | `held` skips the run's Job | survives: the fourth Minor |

- **Decision 1.** `FunctionJob` has the Contracts' shape, and `validateBindings` checks the alias as a unique DNS-1123
  label and the Job as a name (`TestFunctionJobBindingValidate`). A Job's spec refuses `bindings.job`. `JobCapability`
  materializes `jobBindings` and grants both actions through `builtin_job.cedar`; a Job principal, which binds nothing,
  is denied (`TestJobBindingGrantsStartAndRead`). A Policy that forbids one action leaves the other
  (`TestPolicyForbidRevokesAJobAction`). The capability is in both the default registry and the platform's.
- **Decision 2.** The starter admits the run through `NewValidateAdmission` and the control plane's own admissions
  (`pkg/funcd/funcd.go`, the list that `internal/controlplane/server.go:88` builds), sets `startedBy`
  `Function::"<ns>/<name>"` and `jobUID`, and takes the Job's resource group. A start answers 201, and a start sent
  again with an equal input answers 200 (`TestJobStartAnswers`). A start with no name gets `<job>-<id>`. A missing Job
  answers 404 before admission.
- **Decision 3.** `stateOf` answers the name, the phase (`Pending` before any pass), the Ready reason, the times and,
  only once `Succeeded`, the result (`TestJobReadAnswers`). A run of another Job answers 404.
- **Decision 4.** Each endpoint declares its causes (`TestJobStartIsDescribed`), and the matrix provokes each one.
- **Decision 5.** Both shims send `POST /job/{alias}` and `GET /job/{alias}/{run}` with the pool's member, return the
  run's name and the run's state (camelCase in TypeScript, snake_case in Python), and raise `JobError` with funcd's
  status and problem. `kindref` maps `job.start` and `job.get` for both languages.
- **The behavior rows**: five in `docs/behavior/jobs.md` and one in `docs/behavior/functions.md`, as the ADR's
  Behavior rows section says. The row on the 422 says `differs`, as it should.
- **Tracking.** The ADR's diff is its Status line alone (`Accepted → Reviewing`), and F257 reads `reviewing`.

### Definition of Done

12 of 15 items hold: 3 of the 5 Review-checklist items, `just check-specs`, the touched packages' checks (the gate runs
`just ci-full`), the behavior rows, the shims' `ctx.job` with their tests, and 5 of 6 generic items (real behavior, the
tree, the conventions, no scope creep or new dependency, tracking). Misses: checklist item 1, whose scenario
`job-start-input-refused` asserts 400 against the ADR's 422 (`adr`); checklist item 3, Major 1 (`model`); and the
generic "Contracts honoured" item, for the same 422 (`adr`).

### Model scorecard

To record: claude-opus-5-5 on ADR-0349 (implementation) → changes-requested, 0 blockers, 1 major, 4 minors, 5
model-attributed, DoD 12/15. See `docs/reviews/model-scorecard.md`.

### Recommendation

The builder fixes Major 1 and the Minors, and sends the branch back to this reviewer. The decider rules on the 422
(Routed, first item); the `Implemented` stamp waits for that ruling and for a pass. The PR that ships ADR-0349 names
issue #1492.

### Ledger record

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0349 --phase implementation --model claude-opus-5-5 \
  --verdict changes-requested \
  --blockers 0 --majors 1 --minors 4 \
  --model-attributed 5 \
  --dod-passed 12 --dod-total 15 \
  --report docs/reviews/adr-0349-implementation-claude-opus-5-5.md \
  --notes "major (model): a named start sent again answers 409 when its input holds an ampersand or an angle bracket, since the store escapes them and sameJSONValue compares text; minors (model): the job field's doc says a read on a missing Job answers 404, the TS JobRunState times are typed optional though funcd sends null, jobGrants repeats services.CallerBindings and services.Principal, and job-start-name-taken's other-Job case also differs in input (a held-any-job mutant survives); routed (adr): Decision 4's 422 against ADR-0148 Decision 1's 400 on the local API, so the stamp waits for the decider's ruling; routed (integration): the shims are unreleased"
```
