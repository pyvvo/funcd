## Verdict: changes requested — 0 blockers, 1 major, 5 minors (ADR-0347 implementation, model: claude-opus-5-5)

This report reviews ADR-0347's part of the branch `feat/jobs` at 4c46aeaa: the 18 commits after the docs commit
afe2468a, which main holds as the squash db7cb569 (#1551). ADR-0351's part, in the same commits, has its own report,
[adr-0351-implementation-claude-opus-5-5.md](adr-0351-implementation-claude-opus-5-5.md). The reviewer did not write
the work, and every check below was run, not read.

The code meets the ADR: each of the ten scenarios passes, each checklist item holds, and no finding in the code is
above Minor. The one Major is the guide page that the Implementation plan asks for: its first example cannot be run,
since the handler and the contract that it pushes are on no page and in no repository (P1 below). ADR-0347 stays
`Reviewing` until that page is fixed.

### 🔴 Blockers

None.

### 🟡 Major

- **P1: the guide page's first example cannot run** · attribution: model. See *Page critic*.

### Minor

- **`jobNeedsContract` is a constant `true` inside a condition** · model · `internal/function/job.go:26-29`, used at
  `:154` as `fault.KindOf(err) == fault.NotFound && jobNeedsContract && r.requiresContract()`. The decider's amendment
  of Decision 2 holds a Job whose image carries no contract with `ContractMissing`; the constant adds a switch that the
  decision does not have. Drop it; the comment on `contract` already records the amendment.
- **P2, P3, P5 and P7** of *Page critic* · model.

### Page critic (develop-page step 10)

The pages `docs/guide/develop/workloads/job/index.md` and `docs/guide/platform/workloads/job.md`, checked against the
code at 4c46aeaa, `.claude/skills/develop-page/template.yaml`, `.claude/rules/docs-guide.md` and the reader's path
through the page. The site build and the fresh reader come after the go.mod bump.

- **P1 (Major): the first example has no handler and no contract.** `index.md:18-26` pushes `report.mjs` with
  `report.contract.yaml` beside it and says what the handler does, but the page shows, includes or links neither file,
  and no repository holds them (the `report.mjs` of `scripts/lanes.yaml` is a step of the workflow lane). The 422 that
  `{"day":1}` gets (`:72-73`) and the logged line (`:83`) depend on that unseen schema and code. The template's first
  section asks for every input of a runnable example, and develop-page step 7 takes handler code from a lane example,
  marked by a region, each language under its own heading. No language repository has a Job example: the job lane runs
  funcd's fixture `e2e/fixtures/job-sleep.mjs`, and the three language-repository PRs of this change
  (funcd-functions#41, funcd-typescript#117, funcd-python#137) change owners only. The page needs a tested Job handler
  with its contract, under `### TypeScript` and `### Python`, that a lane runs.
- **P2 (Minor): StartFailed's message.** The Status row (`index.md:178`) says the message "names the last binding check
  that failed". It does so only when the worker's last readiness answer was a dependency report
  (`internal/function/jobexec.go:199-204`); a worker that ends first, a run name still taken after
  `runtime.bootTimeout`, or a bindings, materialize or create error gives another message. Scope the clause.
- **P3 (Minor): "two of its own".** `index.md:189-190` calls `ContractMissing` and `ContractInvalid` the Job's own
  reasons; both are a Function's reasons too (`reasonContractMissing` and `reasonContractInvalid` in
  `internal/function`, for a cloudevent Function's missing contract and for a contract that does not compile).
- **P5 (Minor): the order of a run's end.** `platform/workloads/job.md:13-14` says funcd "keeps the answer as the run's
  result; then the run ends and its worker stops". Only a successful answer becomes the result, and the worker is
  stopped and removed before the end is written (`jobexec.go:68-74`, `internal/function/jobrun.go:233-243`).
- **P7 (Minor): a run created before its Job.** `index.md:199` says that deleting a Job ends each of its runs that has
  not ended. A run created before its Job, and not started yet, holds no `jobUID` and waits with `JobNotFound` instead
  (`jobrun.go:157`, Decision 6), which `index.md:124` states for the creation; keep the deletion sentence to the runs
  that hold the Job's UID.

Not findings. The page runs its example on a server, not under `funcdctl dev` as the template's workload route says,
because `funcdctl dev` builds no Job's code from its folder (`cmd/funcdctl/dev_folder.go:157-191` builds Functions and
Workflow steps): the server is the route the code allows. The other facts hold: both describe outputs match
`renderJobRunDescribe` (`cmd/funcdctl/job.go:182-210`) and the run's messages (`jobrun.go:204`, `:219`); `started by:
dev` is the subject that `WithDevAuth` gives the built-in token (`pkg/funcd/options.go:967`); the limits and defaults
match `internal/platform/config/job.go` and `api/types/v1alpha1/job.go`; the Status phases and reasons match
`api/types/v1alpha1/jobrun.go:43-67`; the Access section matches ADR-0351's code and scenarios; Python's `started_by`
matches the pinned shim's `RunInfo`; the CLI error matches `pkg/sdk/sdk.go:386`. Neither page names an ADR or an
internal name, writes "you" or "we", or puts a manifest in a fence, and `TestDevelopPagesFollowTheTemplate`,
`TestScenarioGuideIncludesResolve` and `TestScenarioGuideExamplesApply` pass in `pkg/funcd`'s run below.

### Routed (not this work's)

- **The merge with main** · integration: `git merge-tree origin/main 4c46aeaa` stops on add/add conflicts in the two
  ADRs and the two feat docs, because main holds the docs commit as the squash db7cb569 while the branch keeps
  afe2468a. A rebase onto origin/main that drops afe2468a resolves them; the integration step then runs
  `scripts/agent/gate.sh`.
- **ADR-0346's driver and reap** for a Job on an add-on's runtime under `funcdctl dev` (Decisions 5 and 7) · elsewhere:
  ADR-0346 is Accepted and not built, so that part lands with its implementation.
- **The pinned example modules** · integration: they name owners by name alone until the go.mod bump to the releases
  of funcd-functions#41, funcd-typescript#117 and funcd-python#137. `TestExampleResourcesDecode` fails on the pinned
  tags (the bare owner of `examples/secret-writer/secret.yaml`) and passes with the three modules in an uncommitted
  go.work, as `pkg/funcd`'s guide-example tests do.

### ✅ Verified correct (keep it)

- **The checks**, each through `scripts/agent/d` on the 43 touched packages: `go build ./...` for darwin and linux;
  `go vet`, also for linux and with `-tags e2e` on `pkg/funcd`; `go tool golangci-lint run`, 0 issues, also with
  `--build-tags dev` on `cmd/funcdctl` and for linux on the containerd, quotafs, local, function and funcd packages;
  `go test`, exit 0, with `cmd/funcdctl` also under `-tags dev`. The e2e-tagged lint, which `just lint` does not run,
  reports two `gochecknoglobals` lines of e2e files that this branch does not change.
- **The scenarios**: `go test -race -tags e2e -count=1 -run 'TestScenarioJob' ./pkg/funcd/ -v` exits 0. Each of the ten
  scenarios has a test of its name in `pkg/funcd/jobs_e2e_test.go`, on the process driver and the real Node shim, with
  `t.Parallel()`; the longest is `TestScenarioJobTimeout` at 2.78 s, and the package takes 4.8 s.
- **The design model**: `python3 scripts/specs.py check docs/design/job/run.fizz` passes (5,463 states, 3.8 s), each
  of the seven mutants fails the assertion it names, and `run.md` is current. Each of the six assertions has its oracle
  marker on the test that the ADR names (`pkg/funcd/jobs_e2e_test.go:219`, `:248`, `:299`, `:301`, `:318`, `:338`).
- **The real platform**: the job lane's last result on containerd holds its three cases, job-run-succeeds,
  job-cancel-stops-run and job-restart-interrupts, with no failure (5.4 s). Its suite checks that the run's `<run>.run`
  container is gone after the answer, after the cancel, and after a `kill -s KILL` of funcd and its next start.
- **The checklist**, item by item in the code:
  - one write stores the run `Running` and only then does its executor start (`jobrun.go:123-130`); the executor keeps
    its answer in memory (`jobrun.go:521-527`) and the pass writes it while the run is `Running` (`jobrun.go:236-243`);
  - `Boot` ends every `Running` run `Interrupted` (`jobrun.go:405-426`) after the providers' start and before the
    controller's loop (`pkg/funcd/funcd.go:1384`); a restore ends the open runs `Restored` (`internal/restore/run.go:457`,
    `TestRestoreRun_EndsOpenJobRuns`);
  - the slots count the Job's runs and the node's, oldest first (`jobrun.go:296-363`, `TestJobRunSlots`); a held
    platform or namespace acts on no run (`jobrun.go:101-103`, `TestJobRunHeldActsOnNoRun`);
  - a Job's Revision names its Job in `spec.job` under `JobRevisionName` (`api/types/v1alpha1/revision.go:71`,
    `TestJobRevisionNameNeverEqualsRevisionName`); `revisionOf` calls it another's (`internal/function/function.go:2664`),
    the Function's held revisions read only its own (`ControlledBy`), the workflow pins skip it
    (`internal/gc/gc.go:537`), and `Pairs` holds the Job's (`gc.go:39`);
  - the run's call has caller `JobRun` and a nil Function, so only the contract plugins apply
    (`internal/contractcheck/contractcheck.go:47`; the data plane's apply to External and FnToFn callers), on the pinned
    revision's contract (`internal/function/contracts.go:130`, `:178`, `TestContractOfAJobRevision`);
  - the worker is named after the run, of kind `JobRun`, with the four `FUNCD_JOB*` variables and no `FUNCD_FUNCTION`
    (`jobexec.go:139-146`, `function.go:3323-3345`); containerd and the executor treat it as a shim worker
    (`internal/runtime/containerd/containerd_linux.go:736-748`, `pkg/funcd/executor.go:267`); it is removed at every end
    (`jobexec.go:68-74`, `:337-351`);
  - `startedBy` comes from the caller's identity at creation only (`internal/controlplane/handlers.go:1378`), and a
    replace keeps the stored status (`TestJobRunCreateRecordsWhoStartedIt`).
- **The contracts**: `JobSpec`, `JobStatus`, `JobRunSpec`, `JobRunStatus`, `JobRunPhase`, the `RevisionSpec` delta,
  `JobRevisionName` and `invoke.JobRun` match the ADR's; the call's body is the CloudEvent of the Contracts
  (`jobexec.go:268-280`); the reasons table and the three `job.*` keys with their bounds match.
- **The amendment**: the ADR's only change of substance is the decider's in-place amendment of Decision 2 (#1015, the
  decider's comment of 2026-11-23), with its header line and the marked sentence; the code holds such a Job as a
  cloudevent Function is held, where artifacts carry contracts (`job.go:154`, `TestJobWithoutContractIsHeld`).
- **The behavior rows**: `jobs.md`, its README row, `limits.md`, `funcdctl.md` and `disaster-recovery.md` hold the rows
  that the ADR lists, each citing a test that exists.
- **Tracking**: the ADR is `Reviewing` and F137 `reviewing`.

### Choices of the implementation, for the decider to confirm

- A 2xx whose body is not JSON ends the run `Failed` with `HandlerFailed`, since `status.result` is JSON; an empty 2xx
  body is `Succeeded` with no result.
- JobRun admission leaves the input to the run's start while the Job's Ready condition is of an earlier generation, as
  a Workflow's does (#756).
- A Job's local volumes live under `<job>.job` beside a Function's `<function>` directory, so no stored volume moves.
- `funcdctl job logs` reads the run's own lines and the lines of its trace, merged oldest first and cut to the limit.
- Under `funcdctl dev`'s file artifacts a Job whose image carries no contract is Ready with its input unchecked, as a
  cloudevent Function is not held there.

### Definition of Done

21 of 22 items hold: the 8 Review-checklist items; the four of the ADR's "Done when" (`just ci-full` through the
touched packages' checks above, which the gate runs in full next; the lane; `just check-specs`; ADR-0351 in the same
PR); the plan's behavior rows and Spec tabs; and the 7 generic items (behavior with no stub, contracts, the plan's
surface, ADR-0002 conventions, dependencies, scope, tracking). Miss: the guide page of plan step 6 (P1, model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0347 (implementation) → changes-requested, 0/1/5, 6 model-attributed, DoD 21/22.
See `docs/reviews/model-scorecard.md`.

### Recommendation

The builder gives the guide page a tested Job handler and its contract for the first example, with each language's
tab and a lane that runs them, and fixes the five Minors; a second round then stamps ADR-0347 `Implemented` and F137
`implemented`. Feature issue #1015 is the one that the implementing PR then closes. Nothing goes to a superseding ADR.
