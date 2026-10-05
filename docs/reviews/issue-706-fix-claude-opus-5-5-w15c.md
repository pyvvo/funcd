## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #706 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i706`, commit ec7404ca `fix(runtime): send the containerd worker's CNI DEL while its netns exists`
(`internal/runtime/containerd/containerd_linux.go`, `internal/runtime/containerd/cniid_linux_test.go`).

The person's decision for this issue was "prove first": the regression test must fail on current `origin/main` with no
fix, for the issue's reason, before any production change. That holds (Step 2.1 below). `internal/runtime/` is identical
between the branch's base and current `origin/main` (`git diff --stat` empty), so the overlay of `origin/main`'s
`containerd_linux.go` is the current main.

All tests ran on Linux (the package is `_linux`) in Docker `golang:1.26.4`, offline against the host module cache, as the
issue's repro did; vet and lint ran on the host through `scripts/agent/d`.

### Minor 1 — no test covers the DEL on the task-not-found branch  ·  attribution: model
Mutant M3 moved the `d.cni.Remove` call inside `if err == nil` (so a Stop whose task is already gone sends no DEL and
the host-local lease leaks). It survived: `go test -race -overlay m3.json -run 'TestIssue706|TestScenario_|Stop|CNI|Close|CreateFail'`
→ `ok github.com/pyvvo/funcd/internal/runtime/containerd 7.495s`. The fix rewrote the `switch` that held this branch, so
the behavior it kept (DEL on `errdefs.IsNotFound` from `container.Task`) is now untested. Fix: a second case in
`TestIssue706_…` (or a sibling test) that stops a worker whose task is not found and asserts one DEL.

### Minor 2 — the worker loses its network for the SIGTERM grace window  ·  attribution: issue
The DEL now runs before SIGTERM, and the bridge plugin's DEL removes the worker's `eth0`. For up to `stopGrace` (10 s)
the worker shuts down with no network: a shutdown hook that calls out (KV, bus, another function) fails. Inbound calls
are drained before `function.retire` (`idle`, ADR-0143), so the visible cost is outbound work during shutdown. ADR-0011
asks only for SIGTERM→grace→SIGKILL, so no Contract is broken, and the issue itself named "DEL before SIGTERM" as the
minimum fix and netns pinning (bind mount at Create, as CRI does) as the complete one. The pinned netns would also cover
the two cases this fix leaves open, both named in the issue as out of the minimum: a task that crashed before Stop
(its `/proc/<pid>/ns/net` is already gone) and the PID-reuse hazard. Recommend a follow-up issue for netns pinning; the
commit message does not mention the trade-off.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason** (overlay of `origin/main`'s `containerd_linux.go`, test kept):
  `--- FAIL: TestIssue706_StopSendsCNIDelBeforeTaskStops` — `expected: [][2]bool{[2]bool{false, false}}`,
  `actual: [][2]bool{[2]bool{true, true}}`, "Stop must send the CNI DEL before it kills and deletes the task". This is
  the issue's own observation (`killed=true deleted=true`).
- **Passes with the fix under `-race`**, un-skipped: `--- PASS: TestIssue706_StopSendsCNIDelBeforeTaskStops`; the whole
  package `go test -count=1 -race ./internal/runtime/containerd/` → `ok … 7.662s`.
- **Mutants on the key line**: M1 (drop the DEL) fails `TestIssue706_…`, `TestScenario_UnrevisionedCNIIDsDistinctAcrossNamespaces`
  and `TestClose_StopsEveryWorker`; M2 (DEL after SIGTERM) fails `TestIssue706_…` with `actual: {true, false}` — the
  test checks "before the kill", not only "before the delete". M3: see Minor 1.
- **Cause, not symptom**: the DEL now reaches the bridge plugin while `/proc/<pid>/ns/net` exists, so `cmdDel` gets past
  the missing-netns early return to the masquerade teardown. No retry, timeout or swallowed error added; the DEL error
  was already ignored before.
- **Error semantics kept**: a task load error other than NotFound still returns before any DEL (the worker may still
  run, as the `stop` doc comment says); NotFound still sends the DEL and deletes the container.
- **Siblings**: `Close` goes through `stop` (covered, and `TestClose_StopsEveryWorker` still passes). The Create-failure
  path already sends its DEL with the task alive. `discard` passes netns `""`, which ADR-0179 accepts. No other Stop-time
  DEL site.
- **Scope**: two hunks, both for the issue; no test weakened or deleted.
- **Reuse**: the test reuses `newCNIIDFixture`, `createdTasks` and embeds `recordingCNI`; `taskStateCNI` adds only the
  ordering record. No new helper or dependency.
- **Conventions**: the one code comment states the why (netns lifetime vs the bridge DEL); test comment one line;
  idiom matches the surrounding driver.
- **ADRs**: no ADR file touched; ADR-0011 (Stop SIGTERM→SIGKILL, idempotent), ADR-0056 (`ipMasq`) and ADR-0179 hold.
- **Checks** (touched package): `gofmt -l` clean; `go vet` host and `GOOS=linux` clean; `golangci-lint run
  ./internal/runtime/containerd/` → `0 issues.`; Linux tests green with `-race`. Linux lint, e2e and the lanes are left to
  the group gate. Worktree left clean.
- **Shape**: `fix(runtime):` subject, `Fixes #706`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Optionally add the task-not-found DEL case to the test before the PR (Minor 1), and file a follow-up for netns
pinning (Minor 2).
