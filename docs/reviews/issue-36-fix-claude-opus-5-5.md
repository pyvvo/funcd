## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #36 fix, model: claude-opus-5-5)

Fix under review: commit `636363d` on `fix/205-pooling`, "fix(function): serve pooled Functions in the daemon and
address a solo-run opt-in at its root". Reviewed at the group HEAD `2500327`. Only #36's commit was judged; the other
commits on the branch belong to other issues of the pooling group.

The issue has two causes, and the fix removes both:

1. `cmd/funcd/main.go` process mode never registered the embedded node pool host (`shim.Pool`, `pool.mjs`), so
   `poolKeyFor` returned false for every `node*` Function and pooling never happened (ADR-0046 Implementation plan
   step 4 was not wired into the daemon).
2. `internal/dataplane/dataplane.go` chose the pooled path `/function/<name>` from `spec.pooling.worker` alone, not
   from whether the reconciler pooled the Function. A Function that opted in but ran solo was therefore Ready and
   answered 404 to every call. This applies to node before this fix, to python on a Python older than 3.14, and to
   every runtime in containerd mode.

The fix registers `WithPoolShim(node, pool.mjs)` in process mode. It also moves the pooled-path decision to the place
where pooling is decided: `endpoints.Upstream` in `internal/function/function.go` appends `/function/<name>` when
`poolKeyFor` is true, which is the same predicate `upstreamForFn` uses to choose the pool worker. The activator's
`forward` then addresses a request relative to the upstream's path, and `rebase` maps the root to the path itself,
because the pool serves `/function/<name>` and not `/function/<name>/`. The data plane no longer reads
`spec.pooling.worker`.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The new test fake repeats the port-registration block** · attribution: `model` ·
  `internal/function/pool_test.go` `serveCalls` repeats the `httptest.NewServer` → `net.SplitHostPort` →
  `strconv.Atoi` → `f.revPort[rev] = port` tail that `serveRevision` (`internal/function/shim_test.go`) and
  `serveBlockingRevision` (`internal/function/switch_test.go`) already contain. This is the third copy. The new
  route-aware handler is needed, because `serveRevision` answers 200 on every path and cannot tell `/` from
  `/function/<name>`. Only the registration tail is duplicated. Fix (optional): factor a small
  `registerRevPort(t, rev, srv)` helper on `fakeRuntime` and use it in all three helpers. This is trivial test-fake
  boilerplate that follows the surrounding idiom, and it does not block sign-off.

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reason.** `git revert --no-commit 636363d` conflicted
  only on `internal/function/pool_test.go`, which later commits modified. The review therefore reverse-applied only
  #36's non-test hunks (`cmd/funcd/main.go`, `internal/activator/activator.go`, `internal/dataplane/dataplane.go`,
  `internal/function/function.go`) onto HEAD and kept HEAD's tests:
  - `TestIssue36_DaemonPoolsNodeFunctions` failed with `expected: 200 actual: 404 … node-a answers its calls: 404 Not
    Found`. This is the issue's symptom.
  - `TestIssue36_SoloRunPoolingOptInIsServed` failed with `expected: 200 actual: 404 … the data plane reaches the solo
    shim: 404 page not found`.
  - `TestForwardUnderUpstreamPath` failed because `/` was forwarded as `/function/svc/`.
- **The tests pass with the fix under `-race`.** After `git reset --hard 2500327`, the tests
  `TestIssue36_DaemonPoolsNodeFunctions`, `TestIssue36_SoloRunPoolingOptInIsServed`,
  `TestPooledFunctionIsServedThroughDataPlane` and `TestForwardUnderUpstreamPath` all ran with `-race -count=1 -v`.
  Every test printed `--- PASS`, and none was skipped (node was on PATH).
- **The user-visible behavior is fixed on a real daemon.** The review built `funcd` and `funcdctl`, ran the daemon
  in process mode on ports 30360/30361 with in-memory storage, pushed an OCI-layout artifact, and applied `node-a` and
  `node-b` (`pooling.worker: agents`) and `node-solo`. All three Functions were `Ready`. Every call
  (`POST /function/<name>`) returned 204, three times for each Function. Before the fix, the issue reported 404 for
  the pooled Functions. The daemon's child processes were one `node pool.mjs` and one `node shim.mjs`, so the two
  pooled Functions now share one pool process. The daemon was stopped by its PID, and no children remained.
- **Mutants: all 3 were killed (overlay).**
  - M1 removed the `/function/<name>` append in `endpoints.Upstream`. It failed `TestIssue36_DaemonPoolsNodeFunctions`,
    `TestPooledFunctionIsServedThroughDataPlane` and `TestIssue37_PooledFunctionReachableFromEveryInvoker`.
  - M2 dropped the root mapping in `rebase` (`u.Path == "/"`). It failed `TestForwardUnderUpstreamPath`,
    `TestPooledFunctionIsServedThroughDataPlane` and `TestIssue36_DaemonPoolsNodeFunctions`.
  - M3 removed `WithPoolShim` from `main.go`. It failed `TestIssue36_DaemonPoolsNodeFunctions`: the Functions now
    answer, but in two processes.
- **The root cause is fixed, not masked.** The path decision now uses the reconciler's own predicate (`poolKeyFor`),
  the same one that chooses the pool worker in `upstreamForFn`, so the routing and the placement cannot disagree. The
  fix adds no retry, no timeout and no swallowed error.
- **Every caller benefits.** All three consumers of `Endpoints.Upstream` (the activator and data plane, the workflow
  dispatcher in `internal/workflow/dispatch.go`, and the Sensor invoker in `internal/sensor/invoker.go`) now get the
  pool path from one place. The ADR-0143 call tracker keys on the host only (`hostOf`), so the path suffix does not
  change the drain accounting. The gateway route table is unchanged, and the gateway's handler is not mounted on the
  data plane (`internal/dataplane/dataplane.go`), so the change does not affect it.
- **Scope.** Every hunk serves #36. The `startDaemonPlatform` extraction in `cmd/funcd/main_test.go` is shared by the
  existing `TestDaemonExecutesFunction` and the new test, and it keeps that test's assertions unchanged. The edit to
  the `pool_dispatch_test.go` comment keeps it true, because the daemon now registers `WithPoolShim`. No test was
  weakened or deleted.
- **Reuse.** `rebase` is justified: `httputil.NewSingleHostReverseProxy` (and `url.JoinPath`) join `/` to
  `/function/<name>/`, which `pool.mjs` does not serve. The new `withNodePool` option replaces a closure that existing
  tests inline, so it does not duplicate one.
- **ADRs.** The fix matches ADR-0046 Decision 5 and its Review-checklist item: the pooled upstream is the pool worker
  address, `/function/<name>` is preserved, and the pool routes by name. The commit also completes ADR-0046
  Implementation plan step 4. `git diff origin/main...HEAD -- docs/adr` is empty, so no Accepted or Implemented ADR
  was edited. The living docs (the PROJECT-SUMMARY "Worker pooling — V1 (Node)" row and the blueprint's
  `shim.Pool` embedded) are now true.
- **Conventions.** The fix follows ADR-0002: `api/fault` errors, ctx-first calls, no `any` and no new dependency.
  Imports are at the top level, and the comments are short and give the reasons.
- **Checks.**
  - `gofmt -l` printed nothing.
  - `go build ./...` and `GOOS=linux go build ./...` printed ok.
  - `go vet` passed on the host and on Linux for the touched packages.
  - `golangci-lint` reported `0 issues.` on the host and on Linux for `cmd/funcd`, `internal/activator`,
    `internal/dataplane` and `internal/function`.
  - `go test -race -count=1` passed (`ok`) on `cmd/...`, `internal/activator/...`, `internal/dataplane/...`,
    `internal/function/...`, `internal/workflow/...`, `internal/sensor/...`, `internal/workernode/...` and
    `pkg/funcd/...`.
  - `go test -tags e2e -count=1 ./pkg/funcd/...` passed (`ok`, 110s).
  - `just check-hygiene` reported `hygiene: clean`.
  - The review did not run the Lima lanes, because the fix does not touch containerd, network or e2e lane paths, and
    the group's later stage owns the VM.
- **Shape.** The subject is `fix(function): …`, the body names the cause and the fix and lists both regression
  tests, and it carries `Fixes #36` and the Co-Authored-By trailer. The commit covers one issue.

### Definition of Done
11 / 11 items hold. The Minor finding is trivial test-fake boilerplate that follows the surrounding idiom. It does not
break item 10, because the logic the fix adds (the route-aware fake handler, `rebase` and the Endpoints path) exists
nowhere else.

### Model scorecard
For the later ledger stage: claude-opus-5-5 on issue #36 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. As an optional follow-up, the `fakeRuntime` port-registration tail could be factored into one helper. A
separate idea, not a defect of this fix: a Function that opts into pooling but runs solo, because no pool host exists
for its runtime, still gets no condition that says pooling is off. This would need a design decision, and the issue
did not require it.
