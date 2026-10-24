## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd issue #1227 fix, model: claude-opus-5-5)

Branch `fix/1227-stop-network-after-exit`, commit 8b687361 `fix(runtime): a containerd stop keeps the worker's network
until its task exits`, on `origin/main` cd9ee0c9 (`git diff origin/main...HEAD`:
`internal/runtime/containerd/containerd_linux.go`, `internal/runtime/containerd/cniid_linux_test.go`,
`e2e/duckdb.venom.yml`, `e2e/fixtures/lake-redeploy.yaml`, `scripts/lanes.yaml`). Issue: "A Lake stop on containerd saves nothing: its network is removed before SIGTERM".

### Minor
- **The issue's delete case keeps a second cause** · attribution: model · evidence: the debug lane run deleted Lake
  `c` in namespace `a-b`, where no Function `c` exists, and both saves of its engine's stop got 403. The S3 gateway
  reads the store on each request (`lakeOwnsPrefix`), and the reconciler stops the engine only after the Lake is gone,
  so a delete still loses the commits since the last save, although the network now stays up. The fix is right for
  the cause the issue names, which every listed stop shared; the second cause needs a decision and is now
  [#1231](https://github.com/pyvvo/funcd/issues/1231) (needs-adr), filed before the merge, so `Fixes #1227` stands.

### Routed (not this work's)
- **A Function named like a Lake's prefix owner takes the prefix's write, and the Lake keeps acknowledging commits
  it cannot save** · destination: #1231, with the delete case (a decision; ADR-0175 accepted the risk until a typed
  owner lands) · evidence: the first lane run of the new case, placed after `same-name-function-and-lake-stay-ready`,
  failed with "Table with name issue1227_a does not exist". A debug build that copied the engine's output into the
  journal showed both of the stop's catalog PUTs answered 403 while Function `lake` existed:
  `lakeOwnsPrefix` (`internal/auth/cedar/capabilities.go:349`) makes a Lake the writer only while no Function holds
  the owner name. The Lake stayed Ready, and the three tables were inlined in the catalog, so nothing else was written.
  The case now runs before Function `lake` is created and says why.
- **A failed final save leaves no trace** · destination: the stop-path search report · evidence: `_on_term`
  (`images/runtime/duckdb/shim.py`) ends in `finally: raise SystemExit(0)`, so the stop's upload error is dropped
  and the engine exits 0; an engine's output is kept only as the worker's tail (ADR-0168), which goes with the worker
  at `Remove`.
- **The daemon's own stop takes the S3 gateway away before it stops the engines** · destination: the same report ·
  evidence: `p.s3gw.Run(ctx)` (`pkg/funcd/funcd.go:1655`) closes the gateway when Run's context ends, before the
  HTTP drain and before `Shutdown` closes the runtime. Another code path with another cause; this fix does not
  touch it.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `origin/main`'s `containerd_linux.go`
  overlaid, cross-compiled for linux/arm64 and run as uid 1000 in Alpine:
  `TestIssue1227_StopSendsCNIDelAfterTheTaskIsGone` fails at "Stop must kill and delete the task before the CNI DEL"
  (`[[false false]]`: the DEL saw a live task). With the fix it passes.
- **Mutants.** M1 (the DEL and the unpin right after SIGTERM, before the exit) fails the regression test the same
  way. M2 (the unpin before the DEL) fails `TestIssue706_ExitedWorkerCNIDelRunsInItsPinnedNetns` ("Stop's CNI DEL
  must name a netns that outlives the exited task"), so #706's protection still holds.
- **The behavior is fixed on the real platform.** The duckdb lane passes 20 of 20 cases. Its new case,
  `issue1227-redeploy-keeps-commits`, commits three tables, applies Lake `lake` at 3Gi (a spec change that replaces
  the engine, #1195), waits for a new engine pid and a Ready Lake, and reads `[[3]]` from the new engine, in 3.9 s.
  On unfixed `origin/main` cd9ee0c9, with only the case and its fixture copied in, the same case fails: the
  replacement takes 12 tries of 1 s (the old engine runs to its 10 s grace), and the new engine answers "Table with
  name issue1227_a does not exist" on all 21 tries. The debug run showed the stop end to end: SIGTERM, drain, the save after the drain (PUT 200) and the stop's own
  upload (PUT 200) within 80 ms, then the new engine's GET of that catalog.
- **The fix addresses the cause.** `stop()` sent the CNI DEL before `task.Kill(SIGTERM)`, which #748 had put there so
  that the bridge plugin frees the masquerade rules while the netns exists. The netns is pinned at `Create` since
  #706 (`netns_linux.go`), so the DEL now follows the task's exit and the delete, in the pinned netns, and the pin
  goes last. ADR-0011's `Stop` (SIGTERM, a bounded grace, then SIGKILL) now holds on containerd as on the process
  driver.
- **Scope and tests.** Every hunk serves the issue. `TestIssue706_StopSendsCNIDelBeforeTaskStops` asserted the
  defective order and is replaced by the #1227 test; #706's own guarantee, a DEL in a netns that outlives the task,
  keeps its test. The fixture is the lane's Lake (funcd-python `examples/lake-quack/lake.yaml`) with only
  `resources.memory` changed.
- **Siblings.** `Close` stops every worker through the same `stop()`, so the daemon's stop of the engines gets the
  fix. The boot sweep's `discard` kills first and releases the attachment after. A failed `Create` releases a network
  no task ever used.
- **Every case of the issue, for its cause.** A redeploy, a spec change, a delete and the key renewal all stop the
  engine through `provider.retire` → `Runtime.Stop`, so all four keep the network now; the delete's second cause is
  the Minor above.
- **ADRs.** The change restores ADR-0011's stop order and the environment ADR-0271 and ADR-0273 assume; no ADR file
  was edited.
- **Conventions.** A why-comment naming #1227 and #706, block-style YAML, the lane's mutations in the VM, `retry` as
  the wait. Subject `fix(runtime):`, `Fixes #1227`, the attribution trailer, one commit.
- **Checks.** `go vet ./internal/runtime/containerd/` clean on darwin and linux;
  `go test -run 'Lane' ./cmd/funcd/ ./internal/testkit/langmod/` ok; the package's Linux tests pass in Alpine as a
  non-root user (no `-race`: the Linux build is cross-compiled without cgo). `scripts/agent/gate.sh` at 8b687361: PASS (audit, ci-full in 687 s, the Linux build, vet and lint, the Linux tests, stress, a clean tree).

### Definition of Done
11 / 12 items hold. Item 12 holds only for the issue's own cause: the delete case's second cause is #1231 (Minor
above). Item 13 does not apply: the fix restores the environment `docs/design/lake/stop.fizz` assumes and changes no
modeled behavior.

### Model scorecard
Recorded: claude-opus-5-5 on #1227 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/12.

### Recommendation
Ready for the PR. #1231 takes the delete case's second cause and the same-name case; the other two routed findings go to the stop-path search report for the decider.
