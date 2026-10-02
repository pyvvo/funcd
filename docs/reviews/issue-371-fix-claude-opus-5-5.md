## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #371 fix, model: claude-opus-5-5)

Change: branch `fix/i371`, commit `0831479` — `fix(function): fail a python Function with RuntimeUnavailable when
no python shim is registered`. Touched: `internal/function/function.go`, `internal/function/pool.go`,
`internal/function/dispatch_test.go`.

### 🟡 Minor
- **Endpoint-mode guard of the runtime gate is not pinned by a test** · attribution: `model` · evidence: mutant M3
  removed `r.endpointMode != EndpointLoopback ||` from `runtimeUnavailable` (`internal/function/function.go`), and
  `go test -count=1 ./internal/function/` stayed `ok`. With that guard gone, a container-mode python Function on a
  daemon that registered no python process shim would be failed with `RuntimeUnavailable` although its curated
  image carries the shim. The code is correct today; only the guard is untested (a container lane would catch a
  regression, the unit tests would not). Fix: add a container-mode (`EndpointNetnsFixedPort`) case to the
  `TestIssue371_…` table asserting the python Function is not gated.
- **`RuntimeUnavailable` is new status vocabulary not recorded in an ADR** · attribution: `adr` (not scored) ·
  evidence: no file under `docs/adr/` names the reason; the sibling gate reasons (`ArtifactUnresolved`,
  `NoMatchingPlatform`, `CatalogNotReady`) are each ADR-decided. ADR-0049 Decision 7 states the dispatch rule
  (`python*` → python shim, else node) but leaves the "python shim absent" case undecided, so the fix fills a gap
  rather than contradicting the ADR. Fix: name the reason in the next ADR that touches per-family dispatch.

### ✅ Verified correct (keep it)
- **Fails without the fix**: `git revert --no-commit 0831479`, test file restored from HEAD, then
  `go test -run TestIssue371_ ./internal/function/` → both subtests FAIL with `expected: "Failed"`,
  `actual: "Ready"`: the python Function was launched (under the node shim) instead of being rejected, which is the
  reported behavior. `git reset --hard 0831479` restored the worktree; it is clean at that HEAD.
- **Passes with the fix**: `go test -race -count=1 -v -run TestIssue371_` → `solo` and `pooled` PASS; the whole
  package under `-race` → `ok`.
- **Mutants**: M1 (drop the `isPythonFamily` guard in `poolHostFor`) → `TestIssue371_…/pooled` FAILS. M2 (drop
  `|| r.familyShim(rt) != nil` in `runtimeUnavailable`) → `TestScenarioRuntimeSelectsShim` FAILS. M3 survived
  (Minor above). Each mutant was restored with `git checkout`.
- **Root cause, not symptom**: the issue names `shimFor`'s unconditional fallback to the node shim. The fix adds a
  gate before pooling's assign that refuses a process-mode python-family Function when no python runtime shim
  (and, for a pooled member, no python pool host) is registered, and stops `poolHostFor` from handing a python
  runtime the node pool host — the second path to the same defect. The node default for node-family runtimes is
  unchanged. The new status names the runtime, as the issue's expected behavior asks.
- **Scope**: three files, every hunk serves #371; no test weakened or deleted; no ADR file touched.
- **Reuse**: `familyShim` is extracted from `shimFor`'s existing longest-prefix loop rather than copied; the gate
  reuses `gateFailed`/`gateFailure` (with `readyMessage` and `zeroReplicas`, matching the sibling gates),
  `isPythonFamily` and `poolKeyFor`; the test reuses `newShimHarness`, `h.condition` and the fake runtime's
  `counts()`. No new dependency, type or harness.
- **Conventions**: placement mirrors the platform gate (ADR-0145) and shape gate ordering; comments state the why
  and cite the issue; imports at top level; no YAML touched. ADR-0049 Decision 8 (a python function never co-pools
  on the node host) is now enforced in `poolHostFor` even when no python runtime shim is registered.
- **ADRs**: no Accepted/Implemented ADR's Decision or Contracts contradicted (ADR-0049 Decisions 7–8, ADR-0143
  Decision 4.6 gate semantics: a serving revision keeps serving through `gateFailed`).
- **Checks (touched package)**: `go build ./...` OK, `go vet ./internal/function/` OK,
  `golangci-lint run ./internal/function/...` → 0 issues, `go test -race ./internal/function/` → ok.
- **Shape**: `fix(function):` subject, `Fixes #371`, attribution trailer, one issue in one commit.

### Definition of Done
9 / 10 items hold (item 8's Linux lint, e2e and lanes are deferred to the group gate and not counted). Miss:
item 4 is partial — revert and two of three mutants fail a test, one guard mutant survives (Minor, `model`).

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #371 (fix) → pass, 0/0/2, 1 model-attributed, DoD 9/10.

### Recommendation
Pass. Optionally add the container-mode case to the regression table before the PR; route the
`RuntimeUnavailable` vocabulary to the next dispatch ADR.
