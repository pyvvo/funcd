## Verdict: pass — 0 blockers, 0 majors, 1 minor (ADR-0350 implementation, model: claude-opus-5-5)

This report reviews the five commits of `feat/jobs-apps` over its base `feat/jobs` (ADR-0347 and ADR-0351, reviewed
separately), from `ddfd6cb9` (`feat(apps): an App declares Jobs as it declares Functions (ADR-0350)`) to `f0ea4332`
(the `Reviewing` bump). An independent reviewer in a subagent ran it, from the files alone. Every check below was run,
not read. The base has no drift: `feat/jobs` branches from `db7cb569`, the head of `origin/main`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **Two of Decision 1's entry rules hold for a Job entry, but no test pins them** · attribution: `model`. Decision 1
  says that each store, Lake, ConfigMap and Secret an entry binds is one the App's lists declare, as for a Function
  entry. The branch tests the Secret half (`TestAdmissionRefusesUndeclaredSecret`, cases "a Job" and "a declared Job
  Secret", `internal/app/config_test.go:405`, `:433`). It does not test a Job entry that binds a store the App does
  not declare, or the ConfigMap repoint of `api/types/v1alpha1/app.go:817-821` (ADR-0213 Decision 3). Both rules hold.
  A scratch test, added with `go test -overlay` and never written into the tree, got
  `admission.app-parts: spec.jobs[0] (Job/todo-migrate): admission.binding-declared: spec.bindings.blob[0]: Bucket "nowhere" is not declared in spec.buckets`.
  The same test passed with a declared Bucket. It also read the stored ConfigMap name in the written Job's
  `spec.bindings.config`, with the App's spec unchanged. Fix: add both cases next to the Function's.
  `TestScenarioAppDropBoundStoreRefused` (`pkg/funcd/app_store_e2e_test.go:105`) shows the refusal for a Function,
  and `TestAppConfigMapsWrittenFirst` (`internal/app/config_test.go:273`) shows the repoint.

### Routed (not this work's)

- **Decision 4, second sentence** (an App with engineless parts keeps its Jobs until those parts are gone) · home:
  elsewhere · destination: ADR-0345's implementation. ADR-0345 is Accepted but not built. Its Decision 8, its
  checklist item "An App's other parts outlive its engineless parts, on a deletion and on a prune" and its scenario
  `engineless-app-order` ("bunny-edge …, its Job and infrastack-reconciler") cover the rule. This branch has no
  engineless part to wait for. The `{App, Job}` pair sits in `gc.Pairs`' users group, the order that ADR-0345 will
  change.
- **Decision 5, last sentence** (a Function part's `bindings.job` names a Job part by its name) · home: elsewhere ·
  destination: ADR-0349's implementation. ADR-0349 is Accepted but not built, so `FunctionBindings` has no `job`
  field yet. A Job part keeps its entry's name, so nothing waits in the App for that field.

Neither rule is an item of ADR-0350's Review checklist or of its Done-when, so the `Implemented` stamp does not wait
for them.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`:
  - `go build ./...`: exit 0.
  - `go vet`: exit 0 on `api/types/v1alpha1/...`, `internal/app/...` and `internal/gc/...`, on `cmd/funcdctl` with
    `-tags dev` and on `pkg/funcd` with `-tags e2e`.
  - `go test -race -count=1` on `api/types/v1alpha1/...`, `internal/app/...` and `internal/gc/...`: exit 0.
  - `go test -tags dev -race -count=1 ./cmd/funcdctl/`, the whole package: exit 0, 273 s.
  - `go tool golangci-lint run` on the three trees, and with `--build-tags dev` on `cmd/funcdctl`: 0 issues.
  - With `--build-tags e2e`, `app_jobs_e2e_test.go` has 0 issues. The run exits 1 on two `gochecknoglobals` hits in
    e2e files this branch does not touch. `just lint` does not pass that tag.
  - `just check-hygiene`, `just check-behavior` and `just check-specs` (27 specs, 0 failing): exit 0.
  - The generator `just generate` starts with, run into a scratch copy of the repository layout, writes
    `api/openapi` and `docs/guide` byte-identical to the branch. `TestScenarioCommandSpecGenerated` passed in the
    `cmd/funcdctl` run.
- **The scenarios**: all six have a passing `TestScenario…`, un-skipped, under `-race -count=1`:
  - `TestScenarioAppDeclaresJob`: 0.70 s.
  - `TestScenarioAppJobRunsKeepTheirRevision`: 0.88 s.
  - `TestScenarioAppJobSelfHeals`: 0.48 s.
  - `TestScenarioAppDeleteRemovesJob`: 0.79 s.
  - `TestScenarioAppTemplatePinsJobImage`: 2.72 s.
  - `TestScenarioAppDevBuildsJob`: 1.27 s.

  Each calls `t.Parallel()`, polls the condition it waits for with `require.Eventually`, and raises no timeout.
  `app-dev-builds-job` runs in `cmd/funcdctl` with `-tags dev`, on the `startApp` harness that the other `funcdctl
  dev` App scenarios use, so the plan's "in `pkg/funcd`'s e2e suite" does not fit it. The tests check what the
  scenarios state:
  - The Job is not Ready on a runtime the rig lacks, and the App waits with the Job's `RuntimeUnavailable`.
  - The run writes and reads `state` through its binding.
  - An upgrade switches while the run is `Running`, and the run ends on `migrate-v1`.
  - A deleted App's run ends `Cancelled` `JobDeleted`, its worker gone, and the run stays listed.
- **The contracts**: `AppJob` matches the ADR's block (`api/types/v1alpha1/app.go:175`), and `Jobs` sits right after
  `Functions` in `AppSpec`. Both `entries` functions (`api/types/v1alpha1/app.go:817`, `internal/app/reconcile.go:201`)
  and the doc of `status.children` put it after `functions`. The entry's schema refuses `scaling`, and the root lists
  are refused at `spec.jobs[i]` (`app.go:557`).
- **Decision 2 through the shared paths**: the one pair `{App, Job}` (`internal/gc/gc.go:45`) reaches the prune
  (`sectionKinds`), the self-heal watch (`pkg/funcd/funcd.go:1148`) and `app delete`'s tree
  (`cmd/funcdctl/app.go:998`). None of them keeps a list of its own. A Job part is judged by `judge`'s
  Ready-at-generation path (`internal/app/status.go:89`) with no Job-specific code. `TestAppJobPart` pins these:
  - The write order and the controller reference.
  - `Progressing` until the Job is Ready.
  - A `Running` run neither adds a child nor changes the App's readiness.
  - An upgrade with current = latest, and the run's resourceVersion untouched.
  - The write-back, recorded in `lastSelfHeal`.
  - The prune, with the run kept.
- **No run holds the App**: nothing in `internal/app` reads a JobRun. The prune keeps only a Function that an open
  workflow run holds (`internal/app/reconcile.go:699`), so a dropped Job goes and its runs end `Cancelled`. This keeps
  F145's rule that no run holds a rollout.
- **Templates**: `imagePath` and `digestPath` route `jobs[i].image` and refuse `jobs[i].imageDigest`
  (`internal/app/template/render.go:48-49`; `TestRenderPinsJobImage`). A push packs the template and its lock and
  contacts no registry (ADR-0218 Decision 6), so the lock carries a Job entry's image as it carries a Function's.
- **Dev**: `probeImages` notes a Job part's image with its runtime (`cmd/funcdctl/dev_app.go:648`). Lookup, push
  and watch take the Function path, since nothing branches on `partJob`. `platformFolder` hands each built Job part to
  `builds()` for the shims (`dev_app.go:984`, `dev_folder.go:371`; `TestAppDevJobBuildPicksShims`).
- **The Scope's exclusion holds**: an App's backup still copies only its Functions' local volumes
  (`internal/backup/workload/workload.go:142`).
- **The behavior rows**: `docs/behavior/apps.md` gains the three rows of the ADR's *Behavior rows*, and
  `just check-behavior` passes.
- **The guide page** that the plan names exists. Its tested manifest `api/types/v1alpha1/examples/app/jobs.yaml`
  applies (`TestScenarioGuideExamplesApply/jobs.yaml`), and its include resolves
  (`TestScenarioGuideIncludesResolve`). Its own critic comes later.
- **Tracking**: the ADR's only change is its status line, the F271 row is at `reviewing`, and `go.mod` is unchanged.

### Definition of Done

12 of 12 items hold:
- the 3 Review-checklist items;
- the touched packages' checks (the gate runs `just ci-full`);
- the behavior rows;
- 7 generic items: real behavior, contracts, tree, conventions, no new dependency, no scope creep and tracking.

Not run here:
- `just ci` and `just ci-full`, the whole e2e suite and `scripts/agent/gate.sh` (the audit, the stress run and the
  Linux tests), which the parent runs once per branch;
- `go test` outside the touched packages, and `cmd/funcdctl` without `-tags dev`, since every file it changes is
  `//go:build dev`;
- no Lima lane, since the ADR names none.

### Model scorecard

To record in the batch's ledger commit: claude-opus-5-5 on ADR-0350 (implementation) → pass, 0 blockers, 0 majors,
1 minor, 1 model-attributed, DoD 12/12. See `docs/reviews/model-scorecard.md`.

```bash
python3 .claude/skills/adr-impl-review/scripts/scorecard.py record \
  --ledger docs/reviews/model-ledger.json \
  --adr 0350 --phase implementation --model claude-opus-5-5 \
  --verdict pass \
  --blockers 0 --majors 0 --minors 1 \
  --model-attributed 1 \
  --dod-passed 12 --dod-total 12 \
  --report docs/reviews/adr-0350-implementation-claude-opus-5-5.md \
  --notes "pass; six scenarios pass under -race (five e2e at 0.5-2.7 s, the dev one in cmd/funcdctl); Minor (model): no test pins a Job entry's undeclared-store refusal or its ConfigMap repoint, both shown to hold by an overlay test; routed: Decision 4's engineless order to ADR-0345, Decision 5's bindings.job to ADR-0349"
```

### Recommendation

Nothing goes back to the builder. The Minor is two test cases, which the builder can add on this branch if it wants.
This gate stamps ADR-0350 `Implemented` and F271 `implemented`. The PR that ships the batch names the feature issue
with `Fixes #1534`.
