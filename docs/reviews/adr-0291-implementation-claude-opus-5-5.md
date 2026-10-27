## Verdict: pass — 0 blockers, 0 majors, 4 minors  (ADR-0291 implementation, model: claude-opus-5-5)

This review covers 31bd5849 on `feat/adr-0291-workload-limits` against ADR-0291's Decisions, Contracts, Scenarios,
Review checklist and done-when line. ADR-0291's work is 3854d12b (with the provider and SQLDatabase changes it makes),
on top of the acceptance commit 4c0f382d. The checks ran in a worktree detached at 31bd5849. `scripts/agent/gate.sh`
and the Lima lanes run once on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`: ok.
- `go test -race -count=1` on the 28 packages the branch touches: every package passes except `pkg/funcd`, where
  `TestScenarioResourcesChangeRestartsEngine` fails. That failure is ADR-0292's work and is its report's Blocker. Every
  ADR-0291 test passes, in `internal/function`, `internal/runtime`, `api/types/v1alpha1`, `internal/platform/config`
  and `internal/provider`.
- `go vet` on the touched packages, plain, `-tags dev` (`internal/lake/devengine`, `cmd/...`), `-tags e2e`
  (`pkg/funcd`, `cmd/funcd`) and `GOOS=linux` (`internal/runtime/...`, `internal/function`, `internal/provider`): ok.
- `golangci-lint run` on the touched packages, and on `internal/lake/devengine` with `--build-tags dev`: 0 issues.
  `gofmt -l` on the changed Go files lists nothing.
- `just check-hygiene check-specs`: ok (13 specs, 0 failing).
- A mutant, run with `go test -overlay`: `resourcesGate` always returning nil fails `TestScenarioPastAdmissionBlocked`
  (a worker is created, and the reconcile names no field).

### Minor
- **`EnforcesLimits` sits between `StopGrace`'s doc comment and `StopGrace`** (`model`):
  `internal/runtime/containerd/containerd_linux.go:656-660`. `StopGrace` loses its comment, and `EnforcesLimits`
  carries both. Move the method above the `StopGrace` comment.
- **The `functions:` block sits under the `site:` section header** (`model`): `examples/funcdconfig.yaml:261-266`
  opens `# --- site: …`, then lists `functions:` before `site:`. Give it its own `# --- functions: …` header line.
- **`LakeSpec.Resources` still says an unparsable value is recorded forward-compat** (`model`):
  `api/types/v1alpha1/lake.go:35-36`. `Lake.Validate` now refuses that value through `ResourceSpec.Validate`
  (Decision 2), and `runtime.LimitsFrom` maps it.
- **No `runtimecontract` case for `LimitEnforcer`** (`model`): the Implementation plan asks for one. `LimitsFrom` and
  `LimitWarner` are tested in `internal/runtime/limits_test.go`, with a fake enforcing runtime. Nothing in the
  contract suite pins which drivers claim the capability: containerd does, and the process driver does not.

### Routed (not this work's)
- **The changelog's note on the default** · home: elsewhere · destination: the PR's squash message (the one-PR batch
  mode's `BEGIN_COMMIT_OVERRIDE`). The ADR's Negative says the changelog tells owners that existing solo Functions get
  1 CPU and 512 MiB at upgrade. release-please builds the changelog from subjects and footers, and 3854d12b says it
  only in its body.

### ✅ Verified correct (keep)
- **One check and one mapping**: `ResourceSpec.Validate` (`api/types/v1alpha1/resources.go`) is called by Function,
  Lake and SQLDatabase. SQLDatabase's inline regex and float checks are gone, and its memory now takes `ByteSize`'s
  grammar. `runtime.LimitsFrom` replaces the provider's `limitsFrom`. No other `ResourceSpec` check or limits mapping
  remains.
- **The port**: `runtime.LimitEnforcer` beside `StopGracer`. Containerd implements it. `LimitWarner` logs once per
  owner, only for a set limit under a driver without the capability, for the Function reconciler (`limitsOf`) and the
  provider runtime (`Converge`) alike.
- **The default**: `functions.defaultResources` (1 CPU, 512Mi). An empty field sets no default for that field. The
  default is checked at load (`CheckFunctions`) and by `WithFunctionDefaultResources`, and wired from `cmd/funcd`
  through `function.Deps.DefaultResources`. Each empty spec field takes the default independently.
- **Pools and the second layer**: `FunctionSpec.PooledWithResources` is the one predicate. `FunctionSpec.Validate`
  refuses with 422 naming the field, and `resourcesGate` runs at step 3b, before pooling placement, with the
  `ResourcesInvalid` and `ResourcesWithPooling` reasons. The test's assertion follows the workaround the ADR names
  (#1317): the reconcile error names `spec.resources`, and no worker starts.
- **Contracts**: every signature matches. `FunctionSpec.Resources` is `omitzero`, so an unsized Function renders no
  `resources: {}`.
- **Tests**: one per scenario. `TestScenarioFunctionLimitsApplied` covers container and process mode.
  `TestScenarioFunctionDefaultLimits` covers a field-by-field default and no default. `TestScenarioInvalidResourcesRefused`
  covers both refusals, and `TestScenarioLimitsNotEnforcedLogged` checks three passes with one log line. The duckdb
  lane reads an unsized worker's cgroup and OOM-kills a process past 512 MiB while the worker keeps serving
  (`e2e/duckdb.venom.yml:1218-1260`).
- **Tracking**: the ADR at `Reviewing` with its substance unchanged since acceptance (only the status line moved),
  F206 at `reviewing`, and the blueprint's isolation bullet describes the limits.

### Definition of done
8 of 8. That is the three Review checklist items, plus the done-when line's checks that run per change: the tests with
`-race` (ADR-0291's own), build, vet (plain, dev, e2e, Linux), lint, and `just check-hygiene check-specs`. The line's
`scripts/agent/gate.sh` runs once on the branch in the integration session.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0291 (implementation) → pass, 0/0/4, 4 model-attributed, DoD 8/8. See
docs/reviews/model-scorecard.md.

### Recommendation
ADR-0291 meets its bar. Stamp it `Implemented` with F206 once the branch's ADR-0292 Blocker is fixed, so the three
stamps land in one commit. The four Minors are small and can ride along with that fix.

### Follow-up at cdcba094
The integration session folded the four Minors in cdcba094, and I checked them in a worktree at that commit:
1. `EnforcesLimits` now sits above `StopGrace`'s comment, with `var _ runtime.LimitEnforcer = (*driver)(nil)`.
2. `functions:` has its own `# --- functions:` header in `examples/funcdconfig.yaml`.
3. `LakeSpec.Resources` says `Validate` refuses a value that does not parse.
4. `runtimecontract.RequireLimits` is the contract case. It is called with false in
   `TestProcessDriverLeavesLimitsUnenforced`, and with true in the containerd integration contract, which runs on
   Linux.

The ADR-0291 changes pass the second round's checks: the tests with `-race`, vet plain, dev, e2e and Linux, and lint,
`GOOS=linux` included.

The routed changelog note is now in the integration session's `BEGIN_COMMIT_OVERRIDE`, as a `feat(function)!`
subject naming the upgrade default. Stamped `Implemented` with F206 at cdcba094.
