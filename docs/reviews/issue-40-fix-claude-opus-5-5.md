## Verdict: pass (conditional on the lane stage) — 0 blockers, 0 majors, 2 minors  (issue #40 fix, model: claude-opus-5-5)

Commit under review: `b799e07` `fix(runtime): re-create a containerd worker that a previous funcd run left behind`
(branch `fix/206-restart-recovery`). The later commit on the branch (`c4cba86`, issue #95) was not reviewed.

The change is small and correct by reading: before `NewContainer`, `Create` now calls `reclaim`. When the driver
has no live instance under the worker's ID, `reclaim` deletes a leftover container that holds the name (with its
task, CNI attachment and snapshot) and then removes a leftover `<ctrID>-snap` snapshot. Sweep's per-container
teardown moved into a shared `discard` helper without any change in behavior. The regression is covered twice:
`TestIssue40_RestartReclaimsLeftoverWorker` (a Linux integration test, `FUNCD_IT=1`) and a new env-echo lane case,
`daemon-restart-recovers`.

**Condition:** this stage could not execute the regression. The test needs Linux, root, a running containerd, crun
and CNI. The host is macOS, and this stage may not run Lima lanes. No automated runner sets `FUNCD_IT=1`, so the
`daemon-restart-recovers` case in `just lima-example-env-echo` is the regression that actually runs. The later lane
stage must see it fail on the pre-fix code and pass with the fix. If it does not pass, this verdict does not hold.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors
- **The snapshot-only branch of `reclaim` has no test** · attribution: model · evidence:
  `internal/runtime/containerd/containerd_linux.go:352` (the `SnapshotService("").Remove` of `<ctrID>-snap` when no
  container exists). In `TestIssue40_…`, the leftover container exists, so `discard` deletes the snapshot through
  `WithSnapshotCleanup`, and `Remove` returns NotFound. A mutant that deletes the `Remove` call would survive the
  test. This branch handles a crash between snapshot prepare and container create, which is rare. Fix: add a case
  that creates only the snapshot and then calls Create, or accept the gap and say so.
- **Leftovers that are never re-created still leak, and the false Shutdown comment stays** · attribution: model ·
  evidence: `reclaim` acts only on the name being created. A worker the restarted run does not re-create stays
  running in containerd, with its IP and RAM, until something else removes it. An example is the old revision
  that was draining during an ADR-0143 switch when funcd died. The issue names this cause ("Sweep never wired into
  pkg/funcd startup"). The comment at `pkg/funcd/funcd.go:1201`, "Close the runtime first (stops instances …)",
  which the issue calls false for containerd, is unchanged. The issue's expected behavior is met: the Function
  serves again after a restart. Fix: correct the comment, and file a follow-up for a startup sweep of names that
  are not desired, or note it on #206.

### ✅ Verified correct (keep it)
- **The root cause is fixed, not masked.** The collision on the fixed name `<revision>.r<replica>-snap` is removed at
  its source. There is no retry, no longer timeout and no swallowed error. A failed load, delete or remove is
  returned through `mapErr` with the op and the container ID.
- **The snapshotter matches.** `SnapshotService("")` resolves the snapshotter the same way that
  `containerd.WithNewSnapshot` resolves it in `NewContainer` (both call `resolveSnapshotterName` with an empty name,
  containerd v2.3.1 `client/container_opts.go:244` and `client/client.go:720`). `reclaim` therefore removes the
  snapshot from the snapshotter that created it.
- **A live instance is not affected.** A live instance in the driver's map skips `reclaim`, so a duplicate Create
  still fails with Conflict, as it did before. A released instance (stopped but not removed) loads NotFound twice,
  so the `worker-recreate-after-stop` contract is unchanged. Create is called only from serialized reconcilers
  (`internal/function/function.go:901`, `internal/function/pool.go:275`, `internal/provider/runtime.go:113`), so the
  check-then-delete in `reclaim` does not race with another Create of the same ID in practice.
- **The CNI lease is released.** `discard` calls `cni.Remove(cniID, "")` with the ID it derives from the leftover's
  labels. That ID equals the ID the new worker sets up, so the host-local IP lease from the earlier run is freed
  before `cni.Setup`.
- **Reuse:** Sweep's inline teardown became the shared `discard` without any change in behavior. No new helper,
  type or dependency was added where an existing one could do the job.
- **The test fails before the fix (by reading).** With the pre-fix driver code under the new test, the test
  compiles for Linux with `-tags integration`; the compile exited 0. On that code, the second driver's Create
  reaches `NewContainer`, and `withNewSnapshot` prepares `issue40-1.r0-snap` while the first run's snapshot exists.
  That returns AlreadyExists, the same error the issue reports. `require.NoError` on the re-create then fails.
- **The lane case is sound.** It kills the main PID with `kill -KILL` and polls until the unit is no longer active.
  It then runs `reset-failed` and `systemd-run --unit funcd-demo /usr/local/bin/funcd --config /opt/lane/funcdconfig.yaml`,
  the same command as the lane bootstrap (`scripts/lima-lane.yaml:31`). After that it invokes the serving revision,
  with retry as the wait. The YAML is block style and parses (12 testcases).
- **Checks run in this stage** (all through `nix develop -c`, with exit codes):
  - gofmt clean.
  - `go build ./...` = 0 (host); `GOOS=linux GOARCH=arm64 go build ./...` = 0.
  - `go vet ./internal/runtime/...` = 0 (host); Linux vet of `internal/runtime/containerd` = 0, also with
    `-tags integration`.
  - golangci-lint `./internal/runtime/...` reported 0 issues on the host, on Linux, and on Linux with
    `--build-tags integration`.
  - `go test -race -count=1 ./internal/runtime/...`: every package ok.
  - `just check-hygiene`: clean.
  - The e2e suite `go test -tags e2e ./pkg/funcd/...` was not run. It uses the process runtime, so the containerd
    driver is not on its path.
- **Shape:** the subject is `fix(runtime):`, the body has `Fixes #40` and the attribution trailer, and the commit
  covers one issue. No Accepted or Implemented ADR was edited. The change agrees with blueprint.md's crash-only rule
  ("on restart, funcd rebuilds its world view … then lets the reconciliation loops converge"), ADR-0028 and
  ADR-0143. Worker names and the revision layout are unchanged.

### Not run here (env)
These items were not run in this stage, which records them against the environment:
- Checklist item 2: the fail-before run.
- Checklist item 3: the pass-with-fix run under `-race`.
- Checklist item 4: the overlay mutants.

The reasons:
- `TestIssue40_…` builds only on Linux with the integration tag (`//go:build linux && integration`) and skips
  unless `FUNCD_IT=1` is set. On this host it reports "no tests to run", both with and without the fix.
- The revert itself was done: `git revert --no-commit b799e07`, with the test file restored from HEAD. The test
  compiled, but it could not execute.

### Definition of Done
8 / 11 items hold. Items 1, 5, 6, 7, 8 (for the checks this stage owns), 9, 10 and 11 hold. Items 2, 3 and 4 were not
executed (env) and pass to the lane stage (`daemon-restart-recovers`).

### Model scorecard
Not recorded in this stage, as the batch instructs. Ledger fields: issue 40, phase fix, model claude-opus-5-5,
verdict pass, 0/0/2, 2 model-attributed, DoD 8/11.

### Recommendation
Sign off once the env-echo lane shows `daemon-restart-recovers` failing on the pre-fix code and passing with the
fix. Before merge, the fixer may also correct the `pkg/funcd/funcd.go` Shutdown comment and file a follow-up for a
startup sweep of names that are not desired.
