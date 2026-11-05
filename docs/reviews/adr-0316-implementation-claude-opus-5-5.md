# ADR-0316 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0316 implementation, model: claude-opus-5-5)

This reviews the commit "a Function's /health/* paths answer 404 from the listener": the `health-paths` plugin, the export of
`RootedClean`, the tests, the behavior rows and the status moves. The three Minors need no rework before the merge, and one small
commit would close them. The caller may stamp the ADR and the feat row.

The three Minors were folded after this review: the row went to a commit of its own, the tests use `seedFnIngress`, and
`requireNoRoute` compares the whole phrase, which fails the mutant of Minor 3.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **Minor · model — a row on another subject came in with this commit.** `docs/behavior/calls.md:58`, `Another method on a Python
   worker` (`uncovered`), is not in the ADR's *Behavior rows*, and the commit message does not name it. The row itself is true. A scratch
   test on real workers answered 501 `Unsupported method ('PUT').` on Python and 404 on Node, and DELETE, PATCH, OPTIONS and HEAD gave the
   same statuses. `just check-behavior` takes the row. **Fix**: move the row to a commit of its own, or name it in this commit's message.
2. **Minor · model — the tests repeat a helper that exists.** `seedFnIngress` (`internal/dataplane/namespacehost_test.go:99`) seeds a
   Function with an `ingress`. `seedClosed` (`internal/dataplane/healthpaths_test.go:59`) and the `own` block
   (`internal/dataplane/healthpaths_test.go:179-186`) write the same steps by hand. **Fix**: call `seedFnIngress` in both places and drop
   `seedClosed`.
3. **Minor · model — no test pins the path in the 404.** The scenario's answer is `no route for "<path>"`. A mutant that names
   `c.Remainder` in place of `c.Request.URL.Path` survives all of `./internal/dataplane`, because `requireNoRoute`
   (`internal/dataplane/healthpaths_test.go:50`) and `TestScenarioFuncdsProbesReadTheWorker` look only for `no route for`. **Fix**: let
   `requireNoRoute` take the path and compare the whole phrase.

### Routed (not this work's)

- **F230 has no feature issue** · attribution: `adr` · home: elsewhere · destination: the session that drives this work, or the decider. A
  search for `F230` finds no issue, the feat row names none and the ADR's References cite none. This gate therefore names no `Fixes #N`
  for the PR. The ADR stays as accepted. The feat row is a living document and can name an issue once one is filed.
- **The README line of plan step 3** · home: elsewhere · destination: pyvvo/funcd-typescript#100. The PR is open and holds the one docs-only
  line the plan asks for. Its checks pass, and it needs no release and no `go get`.

### Not run here

`just ci`, `just ci-full`, `scripts/agent/gate.sh`, the Linux checks, the lanes and the whole e2e suite did not run in this review. The session
that drives this work runs one gate and one lane pass over the branch at the end. None of this is a finding.

### ✅ Verified correct (keep it)

**One named test per Scenario, each with `t.Parallel()`, un-skipped and passing under `-race`:**

| Scenario | Test | Time |
|---|---|---|
| `a-health-path-answers-404` | `TestScenarioAHealthPathAnswers404` | 0.04 s |
| `a-health-path-is-read-resolved` | `TestScenarioAHealthPathIsReadResolved` | 0.04 s |
| `the-stance-answers-first` | `TestScenarioTheStanceAnswersFirst` | 0.04 s |
| `other-paths-reach-the-worker` | `TestScenarioOtherPathsReachTheWorker` | 0.04 s |
| `another-function-keeps-the-path` | `TestScenarioAnotherFunctionKeepsThePath` | 0.04 s |
| `funcds-probes-read-the-worker` | `TestScenarioFuncdsProbesReadTheWorker` (real Node and Python shims) | 1.03 s |

The plan's three extra tests pass too: `TestPooledFunctionHealthPathAnswers404`, `TestHealthPathTakesNoTokenAndReadsNoFunction` and
`TestHealthPathInAHeldNamespaceAnswers404`. So does `TestPluginsKeepTheirCallers`.

**Commands, each through `scripts/agent/d`:**

| Command | Exit |
|---|---|
| `go test -race -count=1` of the named tests in `./internal/dataplane` | 0 |
| `go test -race -count=1 ./internal/dataplane ./internal/activator` | 0 |
| `go test -race -count=1 -tags e2e` of `TestScenarioFuncdsProbesReadTheWorker`, `TestScenarioAppDependencyCheck`, `TestScenarioHealthPoolMemberDependency` in `./pkg/funcd` | 0 |
| `go vet ./internal/dataplane ./internal/activator`, and `go vet -tags e2e ./pkg/funcd` | 0, 0 |
| `go tool golangci-lint run` on those two packages, and with `--build-tags e2e` on `./pkg/funcd/...` | 0, 0 (0 issues each) |
| `just check-behavior`, `just check-hygiene` | 0, 0 |

**The tests pin the rule.** With the change reverted by an overlay of `internal/dataplane/plugins.go`, these 7 tests of this ADR fail:
`TestScenarioAHealthPathAnswers404`, `TestScenarioAHealthPathIsReadResolved`, `TestScenarioTheStanceAnswersFirst`,
`TestPooledFunctionHealthPathAnswers404`, `TestHealthPathTakesNoTokenAndReadsNoFunction`, `TestHealthPathInAHeldNamespaceAnswers404` and
`TestPluginsKeepTheirCallers`. `TestScenarioFuncdsProbesReadTheWorker` fails on both languages, at `a/health/readiness from the listener`.
`TestScenarioOtherPathsReachTheWorker` and `TestScenarioAnotherFunctionKeepsThePath` pass, which is right: they pin the rule's boundary.

**Overlay mutants of `internal/dataplane/plugins.go`:**

| Mutant | Tests that fail |
|---|---|
| The plugin at Before in place of Admit | 5: `TestPluginsKeepTheirCallers`, `TestScenarioTheStanceAnswersFirst`, `TestHealthPathTakesNoTokenAndReadsNoFunction`, `TestScenarioAHealthPathAnswers404`, `TestHealthPathInAHeldNamespaceAnswers404` |
| `pathx.HasPrefix(p, "/health")` | 2: `TestScenarioOtherPathsReachTheWorker`, `TestPooledFunctionHealthPathAnswers404` |
| The raw remainder, with no `RootedClean` | 4: `TestScenarioOtherPathsReachTheWorker`, `TestScenarioAHealthPathIsReadResolved`, `TestPooledFunctionHealthPathAnswers404`, `TestScenarioAHealthPathAnswers404` |
| `invoke.External` dropped from `Applies` | 2: `TestScenarioAnotherFunctionKeepsThePath`, `TestPluginsKeepTheirCallers` |
| The plugin first in the chain, ahead of authn | 2: `TestPluginsKeepTheirCallers`, `TestScenarioTheStanceAnswersFirst` |
| `c.Remainder` in the 404's detail | none (Minor 3) |

**Real workers.** A scratch e2e test, not in the tree, sent 68 spellings of a remainder as raw request targets, by name, to a solo and a pooled
Function on the real Node and Python shims: 272 requests. 172 answered the edge's `no route for`. The other 100 are spellings the rule leaves
to the worker, such as `/health`, `/health/.` and `/healthz`, and none of them got a shim's health answer. GET, PUT, DELETE, PATCH, OPTIONS
and HEAD on `/health/readiness` each answered 404 on all four Functions.

**Review checklist and Contracts.**
- `healthPathsPlugin` (`internal/dataplane/plugins.go:185-194`) implements only Admit, comes last in `dataplane.Plugins` and applies only to
  `External` calls. `pkg/funcd/funcd.go:881` builds the real chain from that list, and the e2e test runs it.
- `Applies` reads the remainder through `activator.RootedClean` and the prefix `/health/`, so `/health` and `/healthz` pass and `/health/` does
  not. The plugin holds no store and reads none. `internal/invoke/invoke.go` asks a plugin's `Applies` only at a point it implements, so an
  external call pays for one path clean, on each of the by-name and Route forms.
- The signatures and the answer match the ADR's Contracts: `fault.NotFoundf(callOp, "no route for %q", c.Request.URL.Path)`. `RootedClean` changed
  only its name.
- `TestHealthPathTakesNoTokenAndReadsNoFunction` shows no token taken and no read of a missing Function on the Route form.
  `TestHealthPathInAHeldNamespaceAnswers404` shows no hold asked. The Scenario tests assert no call at the worker and no scale.

**Tree, tests and tracking.**
- The change touches the files the plan names and no others among the code. `internal/function`, `internal/provider`, `go.mod`, `go.sum` and the
  pinned shims are unchanged, so no probe, shim or dependency moved.
- `TestPluginsKeepTheirCallers` is the only existing test changed. It gains the plugin and a `Remainder`, and no assertion is weakened.
  `pkg/funcd/health_e2e_test.go` only gains its new test, with short pacing and a run of about 1 s.
- `docs/behavior/calls.md` holds the rows `A path under /health/`, `An http app's route under /health/` and `A health path over a link`, the edge
  label `a health path`, and the row `A GET or another path on a cloudevent Function` with `/health/` left out. `docs/behavior/health.md` holds
  `Who reads a worker's health paths`. Each reads as *Behavior rows* says.
- The ADR's diff in this commit is its status line alone. It reads `Reviewing` and the F230 row reads `reviewing`. ADR-0110 and ADR-0243 carry
  their `Superseded in part by` lines, and the blueprint carries its note.

**The cost statement is accepted.** The bench the done line asks for ran a 25-byte stub worker through `dataplane.Handler` in alternating builds, 6
rounds at 1 CPU and at 8 callers, on a busy host. It shows the cost as one allocation per call, in all 12 runs with the plugin (243 allocs/op),
and no time the rig can resolve: the medians with the plugin are 55.8 µs at 1 CPU and 24.2 µs at 8 callers, and the rounds spread from 53.0 to
67.0 µs and from 22.8 to 38.5 µs. Its remainder `/` is the cheapest case, so a micro-benchmark of the check's expression gives the rest, on a
2,000,000-iteration run: 23 ns and 1 allocation for `/`, 30 to 49 ns and 3 allocations for a plain path, and 55 ns and 4 allocations for a path
with a dot segment and a trailing slash. That is the size the ADR's Consequences state, plus one allocation in the last case.

### Definition of Done

14 of 15 hold: the five lines of the ADR's Review checklist; three clauses of plan step 4 (each Scenario's test passes, the bench shows the cost, no
shim, probe or other existing test changes); and seven generic items (real behavior, Contracts honored, tree, conventions, dependencies, scope,
tracking). Miss: scope, by Minor 1 · model. `just ci` and the gate are not counted, because they did not run in this review. No contract suite
applies, because no port changed.

### Model scorecard

claude-opus-5-5 on ADR-0316 (implementation): pass, 0 blockers, 0 majors, 3 minors, 3 model-attributed, DoD 14/15.

### Recommendation

The caller may stamp ADR-0316 `Reviewing → Implemented` and F230 `reviewing → implemented`, and record the row. The three Minors go back to the builder
as one small commit and do not hold the stamp. The branch merges after the driving session's one gate and lane pass.
