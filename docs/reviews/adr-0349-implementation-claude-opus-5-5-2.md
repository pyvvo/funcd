## Verdict: pass — 0 blockers, 0 majors, 2 minors (ADR-0349 implementation, round 2, model: claude-opus-5-5)

This report reviews `feat/jobs-binding` at 9d673730 in a review checkout of its own, detached at that commit, with a
git-ignored `go.work` over the shims' `feat/job-approval-start` branches: pyvvo/funcd-typescript at 6c3ea11 and
pyvvo/funcd-python at 210aae8. `git range-diff` shows the six round-1 commits unchanged by the rebase onto 2c24504b.
0775531a and 19efcb07 answer round 1, and 9d673730 adds the page "Start a run from a Function". Every round-1 finding
is fixed, and a test or a mutant proves each fix. The start answers an input off the Job's contract as the decider
ruled on 2026-11-24. The page's console output is real: I reproduced it on a local funcd, apart from one prerequisite
command (Minor 1). At 9d673730, `just check-behavior` failed on the Jobs chapter's size cap, which the rebase pushed
over, and lefthook's pre-commit `behavior` hook refused the stamp (Routed, first item). f1270455 trims two rows and
amends ADR-0349 in place for the ruling. At f1270455 the check passes and the amendment reads as the code does. The
stamp is committed as c4d24d23 on `feat/jobs-binding`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The page's server command fails for a user who is not root** · attribution: model. The prerequisites name
  `FUNCD_DATA_PLANE_ADDR=127.0.0.1:8081 funcd --memory` (`docs/guide/develop/workloads/job/start-from-a-function.md:19`).
  Run that way, funcd stopped at once: `create data dir /var/lib/funcd: mkdir /var/lib/funcd: permission denied`. The
  Job page explains why and sets `FUNCD_DATA_DIR="$(mktemp -d)"` (`docs/guide/develop/workloads/job/index.md:20-26`),
  and the walkthrough below ran with that variable. Fix: set `FUNCD_DATA_DIR` as the Job page does, or send the reader
  to the Job page's prerequisites, as the "Approve a run" page does.
- **The run's name in Python** · attribution: model. "The handler names the run after the month, `report-<month>`"
  (`start-from-a-function.md:24`) holds for TypeScript only. The Python handler names the run `pyreport-<month>`
  (pyvvo/funcd-python `examples/job-planner/src/main.py`), as the page's own Python output shows. Fix: add "or
  `pyreport-<month>` in Python".

### Routed (not this work's)

- **`just check-behavior` failed at 9d673730; fixed in f1270455** · home: the integration step, the rebase onto
  2c24504b. The check reported "docs/behavior/jobs.md: the written part is 16412 bytes, over the cap of 16384". Before
  the rebase, d239df80 kept the chapter under the cap. The new base adds 38 bytes to the chapter, from ADR-0348's
  round-2 rows (491b96a3), and the round-1 commits came through the rebase unchanged. The overflow therefore came from
  the two ADRs' rows together. The pre-commit `behavior` command refused the stamp, so I restored both files. f1270455
  words two rows shorter. At f1270455, `just check-behavior` and `just check-hygiene` exit 0, and each trimmed row says
  what it said before.
- **The decider's ruling of 2026-11-24, at first recorded only in a behavior row's citation; amended in f1270455** ·
  home: elsewhere, the decider. Decision 4 said that each answer is a declared cause (ADR-0330), while the ruling relays
  the 422 outside the causes. f1270455 amends ADR-0349 in place, in the form of ADR-0347's and ADR-0290's amendments.
  It adds a `- **Amended in place**` header line that links the ruling recorded on #1492 (comment 7119567941, by
  green-0-rabbit). It also rewrites Decision 4's 422 sentence and the checklist line on declared causes, each marked
  "(amended 2026-11-24)". The amendment reads as the code does, as the ✅ section says.
- **The shims are not released** · home: the integration step, as in round 1. `go.mod` pins funcd-typescript v0.34.0
  and funcd-python v0.29.0. Until the shims' PRs merge and the pins move, the page's `::include` lines resolve only
  through the `go.work`, and its `git clone` of `main` and its Examples links do not reach `examples/job-planner`.
- **The Contracts' `JobRunStarter.Get` has no namespace** · home: elsewhere, the ADR's text; no change asked, as in
  round 1.

### ✅ Verified correct (keep it)

- **Round 1's findings**, each fixed and proven:
  - Major 1: `sameJSONValue` compares strings by their decoded text (`pkg/funcd/jobstart.go:218`).
    `TestSameJSONValue` gains escaped cases, `TestJobRunStarterStarts` starts an input with `&`, `>` and `<b>` twice,
    and `TestScenarioJobStartNamedTwice` sends `net&dev <a>`. The mutant that compares strings as text fails all three.
  - The `job` field's description says that a start answers 404 and a read answers the runs that a deleted Job left
    (`api/types/v1alpha1/function.go:121`). The generated pages match.
  - TypeScript's `JobRunState` types `startedAt` and `endedAt` as `string | null`, and `result` is null before
    (6c3ea11).
  - `jobGrants` reads the bindings through `services.CallerBindings` and names the principal through
    `services.Principal` (`pkg/funcd/jobstart.go:42`, `:57`, `:106`).
  - The other Job's run in `TestScenarioJobStartNameTaken` holds the start's input. The mutant that drops
    `run.Spec.Job == job` now fails it.
  - The security page names `job::start`, `job::read` and `Job`.
- **The ruling.** An input off the Job's contract answers 422 with a `ContractViolation`, `{"error", "details"}`, as
  `application/json`. That is the status, body and header that a Function's call answers
  (`internal/contractcheck/contractcheck.go:139-156`). The details come from the admission's own check, which the
  admission and the starter share through `admission.JobRunInputDetails` (`internal/controlplane/admission/jobrun.go:100`).
  The facet relays the 422 as invoke relays a target's (`internal/workernode/local/jobstart.go:107-115`).
  `job.input-refused` is gone from the causes and the Spec page. Both shims keep the body's text as `JobError`'s
  detail, as their `ctx.invoke` errors do. `TestJobStartOffTheContractAnswersAsACall` pins the 422, the message, one
  detail at `/name` and no run; the scenario pins the same through the Node shim. A person's create keeps its 422
  (`TestJobRunContractAdmission`, `TestScenarioJobInputRefused`).
- **The amendment of f1270455** reads as the code does:
  - The header line and Decision 4 say that the 422 answers as a Function's call does, with the contract's
    `{"error", "details"}` body relayed as is. The starter builds that body as a `contractcheck.ContractViolation`
    (`pkg/funcd/jobstart.go:199`), and the facet writes its status and bytes unchanged.
  - "Each other answer is a declared cause" holds: `job.input-refused` is gone, and every other answer of both
    endpoints is declared (`TestJobStartIsDescribed`, `TestEveryDeclaredCauseHasARow`).
  - The checklist line says the same.
  - The ruling's comment on #1492 says it in the same terms. The header line has the form of ADR-0347's.
  - The two trimmed rows keep their facts, and the input row now cites "ADR-0349 Decision 4 (amended 2026-11-24)".
- **The checks**, through `scripts/agent/d`, exit 0 unless noted:
  - `go build ./...`; `go vet` on the touched packages and `pkg/funcd`, also with `-tags e2e`.
  - `go test -race -count=1` on `api/types/v1alpha1`, `internal/auth`, `internal/auth/cedar`,
    `internal/controlplane/admission`, `internal/workernode/local` and `internal/controlplane/kindref`.
  - In `pkg/funcd` without tags, with `-race`: `TestJobCauseMatrix` (12 Job rows), `TestEveryDeclaredCauseHasARow`,
    `TestScenarioFacetsMatch`, `TestJobRunStarterStarts`, `TestSameJSONValue`, `TestJobStartOffTheContractAnswersAsACall`
    and `TestScenarioGuideIncludesResolve`; `TestScenarioGuideExamplesApply`, which applies both planner manifests.
  - `go tool golangci-lint run` on the seven packages: 0 issues.
  - `just check-specs`: 27 specs, 0 failing. `start.fizz` passes with 6 states, and its two mutants fail as named.
  - `just check-hygiene`; specgen over the tree, then `scripts/check-generated.sh`: no change.
  - `just check-behavior`: exit 1 at 9d673730 (Routed, first item), and exit 0 at f1270455, also as the pre-commit hook
    of the stamp.
- **The scenarios**: the nine of ADR-0349 and ADR-0347's `TestScenarioJobInputRefused`, with `-tags e2e -race
  -count=1`, each under 1.7 s.
- **The shims.** TypeScript: `just test` (11 workspaces, the shim's 194 tests and the planner's 2), `just typecheck` and
  `just lint`; the committed `shim.mjs` and `pool.mjs` equal a fresh esbuild. Python: `just check`, with 526 shim tests,
  the planner's 2 tests and mypy on the planner.
- **The mutants**, each a one-line edit restored with `git checkout`, all caught:

  | Mutant | Caught by |
  |---|---|
  | `unnamed-retry`: the starter names every start anew | `TestScenarioJobStartNamedTwice`, `TestJobRunStarterStarts` |
  | `conflict-on-retry`: a held name answers 409 | `TestScenarioJobStartNamedTwice`, `TestJobRunStarterStarts` |
  | `held` skips the run's Job | `TestScenarioJobStartNameTaken` |
  | Strings are compared as text | `TestScenarioJobStartNamedTwice`, `TestSameJSONValue`, `TestJobRunStarterStarts` |
  | The refusal answers 400 | `TestScenarioJobStartInputRefused`, `TestJobStartOffTheContractAnswersAsACall` |
  | The refusal names no value | `TestScenarioJobStartInputRefused`, `TestJobStartOffTheContractAnswersAsACall` |
  | The facet relays nothing | `TestScenarioJobStartInputRefused`, `TestJobStartAnswers` |
  | The admission skips the contract | `TestScenarioJobInputRefused`, `TestScenarioJobStartInputRefused`, `TestJobRunContractAdmission` |
  | Cedar's deny is ignored | `TestScenarioJobStartForbiddenByPolicy`; the matrix |
  | Any alias resolves to a binding | `TestScenarioJobStartWithoutBinding`; the matrix |

- **The guide page, claim by claim.** Besides the two Minors, each claim matches the code:
  - the binding, the run's name returned at once, the read's fields in both languages, and the result only once
    `Succeeded`;
  - the start named again with an equal input, and the 409 for a name of another Job or another input;
  - a run name is a DNS-1123 label (`ObjectName.Validate`);
  - the admission's `job.payloadLimit` and contract checks;
  - the Node.js and `python3` rules: Python 3.12 runs a Function, and pooling needs 3.14 (`cmd/funcd/main.go:1728-1757`);
  - the at-least-once delivery that the events page describes;
  - the built-in policy, identical to `internal/auth/cedar/builtin_job.cedar` without its comment;
  - the 403 of a forbid, and the errors table, its 422 included;
  - a deleted Job: a start answers 404, and its runs stay readable;
  - a run without `approval.bypass` that waits for a person.

  Every link and anchor resolves. The nav entry and the Job page's link are in place.
- **The page's console output**, run on a local funcd built at 9d673730, with its API on 127.0.0.1:18480, its data
  plane on 127.0.0.1:18481 and `FUNCD_DATA_DIR` a temporary directory, from copies of both branches' examples:
  - Reproduced:
    - the four push digests, `aff19a7a…`, `8696a1f3…`, `06645d88…` and `27a4fcc1…`;
    - every `applied …` line;
    - both answers of `planner`, `{"run":"report-2026-10","phase":"Running"}`, and the answer of `pyplanner`;
    - both `job describe` listings, which differ only in their times, duration and trace id;
    - the `job runs report` listing;
    - the 403 text, word for word, with the forbid in a Policy named `no-planner-start`, a name that the page does not
      give.
  - Checked beyond the page: with the Job deleted, the start answered `404 ... no Job "report" in namespace
    "default"`, and `job describe report-2026-10` still read the run.
  - Not reproduced as written:
    - the server command (Minor 1);
    - the `git clone` of `main`, which does not hold `examples/job-planner` yet;
    - ports 8080 and 8081, which I did not take on the shared host.

    The push paths in the output are the page's illustrative `/home/dev`.
- **Tracking.** At 9d673730 the ADR's text had not changed since round 1. f1270455 amends only its header, Decision 4's
  422 sentence and one checklist line. The stamp, c4d24d23, moves the status `Reviewing → Implemented` and keeps the
  Amended-in-place line under it. It also moves F257 `reviewing → implemented`.

### Definition of Done

18 of 18 items hold at f1270455:

- the 5 Review-checklist items, the last as amended;
- `just check-specs`;
- `just check-behavior`, which failed at 9d673730 for the integration step's reason and passes since f1270455;
- the touched packages' tests, vet and lint (the gate runs `just ci-full`);
- hygiene and the generated files;
- the content of the behavior rows;
- the guide page;
- the shims' `ctx.job` with their tests;
- 6 generic items: real behavior, the Contracts, the tree, the conventions, no scope creep or new dependency, and
  tracking.

At 9d673730, 17 of the 18 held. This report's first version said 16 of 17, because it miscounted the same list.

### Model scorecard

To record: claude-opus-5-5 on ADR-0349 (implementation, round 2) → pass, 0 blockers, 0 majors, 2 minors,
2 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

ADR-0349 is stamped `Implemented` in c4d24d23 on `feat/jobs-binding`, and F257 is `implemented`. The two Minors, both
page wording, go into the separate commit after the stamp, with the guide critic's findings. The PR that ships
ADR-0349 names issue #1492.

### Ledger record

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0349 --phase implementation --model claude-opus-5-5 \
  --verdict pass \
  --blockers 0 --majors 0 --minors 2 \
  --model-attributed 2 \
  --dod-passed 18 --dod-total 18 \
  --report docs/reviews/adr-0349-implementation-claude-opus-5-5-2.md \
  --notes "round 2 pass; round 1's Major and 4 Minors fixed and proven (strings-as-text and held-any-job mutants killed); the 422 answers as a Function's call per the decider's ruling of 2026-11-24, amended in place in f1270455 and read against the code, with 10 mutants killed; page checked claim by claim and its output reproduced on a local funcd; minors (model): the page's server command lacks FUNCD_DATA_DIR and fails for a user who is not root, and its run name omits Python's pyreport-<month>; routed (integration): check-behavior failed on the Jobs chapter's cap after the rebase, fixed in f1270455; stamped in c4d24d23; the shims are unreleased"
```
