## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #37 fix, model: claude-opus-5-5)

Issue #37: a Workflow step (`function.ref`) or a Sensor function action that targets a pooled Function gets a 404,
because `internal/workflow/dispatch.go` and `internal/sensor/invoker.go` POST to the bare upstream while the pool host
serves a member only at `/function/<name>`.

Reviewed commit: `74df82b` (`test(function): cover Workflow steps and Sensor actions calling a pooled Function`) on
branch `fix/205-pooling`. The commit is test-only. The code change that resolves #37 is in the preceding commit
`636363d` (issue #36): `endpoints.Upstream` in `internal/function/function.go` now appends `/function/<name>` to a
pooled member's upstream, so every caller of `activator.Endpoints` gets the routed path. The data plane stopped adding
the path itself, and the activator rebases a forwarded request onto the upstream's path.
`internal/activator/activator.go` and `internal/dataplane/dataplane.go` are the files that carry those two changes.

### Revert check (adapted)

Reverting `74df82b` alone only deletes the test (it also conflicts on `internal/function/pool_test.go` with later
commits), so it proves nothing. Instead I ran `git revert --no-commit 636363d` and restored every `_test.go` file at
HEAD. This reverts the non-test files `cmd/funcd/main.go`, `internal/activator/activator.go`,
`internal/dataplane/dataplane.go` and `internal/function/function.go` to their pre-fix state. The revert applied cleanly
for those files.

- Without the fix: `go test -race -count=1 ./internal/function/ -run TestIssue37_` → **FAIL**. The output had
  `workflow.dispatch: default/svc rejected the step (404): 404 page not found` (pool_test.go:115) and
  `sensor: function default/svc returned status 404` (pool_test.go:120). These are the issue's two symptoms.
- With the fix (`git reset --hard 2500327`): `-race -count=5` → 5/5 PASS.

### User-visible behavior (real pool.mjs)

I wrote a scratch e2e probe (overlay, not committed). It uses the existing `newPoolHarness` from
`pkg/funcd/pooling_e2e_test.go`, which runs the real node shim and `pool.mjs` through `WithPoolShim`. The probe applies a
pooled Function `svc` (`pooling.worker: services`), calls it through the data plane, and then runs a Workflow whose only
step is `function.ref: svc`.

- Without the fix (same revert as above): `run phase=Failed … Error:workflow.dispatch: default/svc rejected the step (404): 404 Not Found`.
  This is the issue's exact text.
- With the fix: `run phase=Succeeded steps=[{Name:call Phase:Succeeded Attempts:1 …}]`.

### Mutants (overlay on `internal/function/function.go`, full `./internal/function/` package)

| Mutant | Killed by |
|---|---|
| M1 `up += "/function/"` (drop the name) | TestIssue37_…, TestPooledFunctionIsServedThroughDataPlane |
| M2 `!pooled` (append for solo instead) | TestIssue37_…, TestIssue36_SoloRunPoolingOptInIsServed, TestPooledFunctionIsServedThroughDataPlane, 2 redeploy scenarios |
| M3 `… + string(f.Name) + "/"` (trailing slash) | TestIssue37_…, TestPooledFunctionIsServedThroughDataPlane |

All three mutants are killed. There are no survivors.

### Minor 1 — no `fix:` commit or changelog line names #37's symptom  ·  attribution: issue
`74df82b` is `test(function): …` rather than the `fix(<scope>):` shape that `/fix` Step 6 asks for. The type is honest
for a test-only commit, because the root cause is shared with #36 and was fixed there. The #36 subject,
`serve pooled Functions in the daemon and address a solo-run opt-in at its root`, says nothing about Workflow steps or
Sensor actions. As a result, release-please will produce no release-note line for #37's user-visible fix. This needs no
code change. When the PR is opened, the PR description should state that #37 is fixed by `636363d`.

### Minor 2 — `testify/assert` breaks the repo's test idiom  ·  attribution: model
`74df82b` adds `github.com/stretchr/testify/assert` to `internal/function/pool_test.go`. The repo uses `require` in
about 200 test files, and `assert` appears in only two files, both added by this fix wave
(`internal/function/pool_test.go`, `cmd/funcd/main_test.go`). Using `assert` lets the test report both invokers when
both fail, so the choice has a reason, but it diverges from the surrounding code. Two `require` subtests, or `require`
calls in sequence, would match the idiom. This is cosmetic.

### ✅ Verified correct (keep it)
- **Root cause, not symptom.** The upstream path is decided in `endpoints.Upstream`, where pooling is decided
  (`poolKeyFor`). Each invoker no longer has to know about pooling. `grep '\.Upstream('` finds exactly four production
  callers: the activator's `Wake` and its ready-poll, the workflow dispatcher, and the Sensor invoker. All four now get
  the routed path. `Wake` returns that same `Upstream`, so the cold-start path is covered too. This conforms to ADR-0046
  Decision 5 (the path `/function/<name>` is preserved and the pool routes by name).
- **ADR-0143 drain tracking is not disturbed.** `CallTracker.HandedOut` and `Idle` key on `url.Host`
  (`internal/activator/calltracker.go` `hostOf`), so the appended path does not split a worker's call count.
- **The test is in the right place and reuses the existing harness.** `TestIssue37_PooledFunctionReachableFromEveryInvoker`
  uses `newShimHarness`, `withNodePool` and `fakeRuntime.serveCalls`. The fake pool in `serveCalls` serves only
  `POST /function/<name>`, which mirrors `pool.mjs`. The test drives the real `workflow.NewHTTPDispatcher` and
  `sensor.HTTPInvoker` against `h.r.Endpoints()`. It adds no new helpers.
- **Scope.** One file changed, with 28 added lines. No test was weakened or deleted. No ADR or doc was edited.
- **Conventions.** The imports are at the top level, there is no comment bloat (one short doc comment), and the commit
  has `Fixes #37` and the attribution trailer.
- **Checks (run in the review worktree, through `nix develop -c`).**
  - `gofmt -l internal/function` → empty.
  - `go build ./...` and `GOOS=linux go build ./...` → ok.
  - `go vet` on host and Linux (function, workflow, sensor) → ok.
  - `golangci-lint` on host and Linux (same packages) → `0 issues.`
  - `just check-hygiene` → ok.
  - `go test -race -count=1` for `internal/function/...`, `internal/workflow/...`, `internal/sensor/...`,
    `internal/activator/...` and `internal/dataplane/...` → all ok.
  - `go test -tags e2e -count=1 ./pkg/funcd/...` → `ok … 111.080s`.
  - Env note, not scored: the first e2e run used a long `TMPDIR` and failed 4 tests with
    `FUNCD_INVOKE_SOCKET unset`, because the unix-socket path got too long. With the default `TMPDIR`, those 4 tests and
    the full suite pass.
  - No Lima lane was run. The change does not touch the containerd, network or lanes paths.

### Definition of Done
11 / 11 items hold. Item 9 (conventions) holds with the cosmetic Minor 2. Item 11 (shape) holds with Minor 1: the
subject is a correct conventional type for a test-only commit, and it carries `Fixes #37` and the trailer.

### Model scorecard
Not recorded by this gate (batch run). Ledger fields: issue 37, phase fix, model claude-opus-5-5, verdict pass,
0/0/2, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. Optionally swap `assert` for `require` to match the repo idiom. In the PR description, state that #37 is fixed by
`636363d` (the shared root cause with #36) and guarded by `TestIssue37_PooledFunctionReachableFromEveryInvoker`.
