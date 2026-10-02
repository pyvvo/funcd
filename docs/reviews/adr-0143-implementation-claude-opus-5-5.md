# ADR-0143 Implementation Review — Redeploy by revision switch (F13)

**Verdict**: **changes requested**. The switch, the drain, the runtime port and the call counting are correct and
well tested at the unit level, but the Definition of Done is not met:
- the existing env-echo Lima lane now fails, because its crash case looks for the container by the name that the
  new naming rule replaced (model);
- the e2e half of the Implementation plan (the `pkg/funcd` scenarios and the env-echo redeploy case) is not
  written yet, at the decider's direction (sequencing, not scored).

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0143 Contracts / Scenarios / Implementation plan / Review checklist / Done-when ·
ADR-0011 (the runtime port) · ADR-0016 and ADR-0033 (the resolver and the activator) · ADR-0020 · ADR-0142 ·
ADR-0047 · ADR-0002 conventions · the blueprint · FEAT-0000/F13

The work is uncommitted on `fix/redeploy-revision-switch`, on top of the #15 fix (e25836e, treated as merged).
`pkg/funcd/redeploy_probe_e2e_test.go` is an old untracked probe, not part of the work; it was skipped
(`-skip TestProbe`).

## Verdict: changes requested — 2 blockers, 0 majors  (ADR-0143 implementation, model: claude-opus-5-5)

### 🔴 Blocker 1 — the env-echo Lima lane fails: its crash case uses the old container name  ·  attribution: model
`nix develop -c just lima-example-all`: the env-echo lane ends `final status: FAIL`. Seven of its eight cases pass
on containerd. The ADR-0142 case `crashed-function-worker-restarts` fails:

```
Assertion "result.systemout ShouldMatchRegex \"PID_BEFORE=[0-9]+\"" failed. value PID_BEFORE=
expected 'REPLACED=no PHASE=Ready ( -> )' to contain 'REPLACED=yes'
```

The case finds and kills the worker by the container ID `env-echo-r0` (`e2e/env-echo.venom.yml:206-207` and
`:232`). The Contracts give a revisioned worker the container ID `<revision>.r<replica>`
(`internal/runtime/containerd/containerd_linux.go:331`), so the worker is now `env-echo-1.r0` and the case finds
nothing. The implementation changed the name as the ADR requires, but it did not update the suite that depends on
the old name, and Done-when requires all nine lanes to pass. The duckdb case that kills `lake-r0` still passes,
because a CatalogService engine has no revision and keeps the old name.

Not verified: whether the crash replacement itself works under the new names on containerd. A rerun with a
corrected lookup was prepared but not run, so that the verdict would not be delayed.

**Fix (builder)**: make the crash case find the worker by its revisioned name (for example, match
`^env-echo-[0-9]+[.]r0$` in `ctr task ls`, or select by the `funcd/name` label). Do this in the same change as the
planned env-echo redeploy case, and rerun the lane.

### 🔴 Blocker 2 — the e2e tests of Implementation plan step 7 are not written yet  ·  attribution: sequencing (the decider's direction; not scored)
The decider asked for this review before the e2e suites. Two parts are missing:
- the `pkg/funcd` e2e tests `redeploy-switches-to-new-revision` (a caller that loops through the switch and counts
  0 failures), `failed-revision-keeps-old-serving` (an unresolvable tag and an unloadable handler) and
  `in-flight-call-finishes-on-old-revision`;
- the env-echo case `redeploy-switches-to-new-revision`.

Until they exist, the following stays unverified:
- on containerd: two revisions of one replica running side by side (two CNI attachments), and `Remove` after
  `Stop`;
- the 0-failed-calls claim on the real push → apply → invoke path;
- the `pkg/funcd` wiring of the tracker into the Sensor invoker and the workflow dispatcher, which no test covers
  (see Minor 2).

This is recorded like an `env` finding: it does not count against the model.

### 🟡 Major / Minor
- **Minor · model — the process driver lets `Remove` forget a running instance after `Stop` and then `Start`.**
  `Start` (`internal/runtime/process/process.go:97-161`) does not reset `released`, so after `Stop` → `Start`
  the instance runs again with `released` still true. `Remove` (`:279`) then deletes it from the map and orphans
  its process. The port contract says that a running instance is `fault.Conflict`.
  - A probe (Create → Start → Stop → Start → Remove) returned `nil` where the contract requires Conflict.
  - No caller reaches this today. `retire` (`internal/function/function.go:955`) always stops before it removes,
    and the pool's restart (`restartPool`: Stop → Start on the same ID) never removes. containerd cannot restart a
    stopped instance, so only the process driver is affected.

  **Fix**: reset `released` in `Start`, and add a contract subtest (Stop → Start → Remove → Conflict). Also add the
  "created → Conflict" case: the contract names it but no subtest covers it. A probe showed the process driver
  already returns Conflict there.
- **Minor · model — 14 of 41 overlay mutants survive, so several Review-checklist behaviors have no failing test.**
  For each one, the code is correct when read; the two probes below show that the last two items work. The
  surviving mutants:
  - the switch on one ready C replica instead of all of them: no test has C with more than one replica;
  - the `HandOutSettle` floor after `drainingSince`, and the resolver's `HandedOut` call. These are the two guards
    against the gap between resolving an upstream and calling it;
  - stopping C's workers on a gate failure while S serves;
  - the steady state's `RevisionReady` check, which re-checks a failed gate every period;
  - the requeue at `HandOutSettle` after the switch, and the drain's `min(1 s, …)` requeue;
  - S keeping its listed replica indexes, and the `0 … status.replicas − 1` fallback after a restart;
  - `teardown` removing (and not only stopping) a deleted Function's workers;
  - S following C for a pooled member;
  - the tracker's upgrade (HTTP 101) branch, and the activator wrapping its transport.

  The planned e2e in-flight test would cover the activator wiring. The other items need unit tests. In addition,
  the fake runtime's `Remove` accepts an instance that exited on its own (`internal/function/shim_test.go`), which
  the real drivers reject, so the tests cannot catch a `Remove` without `Stop`.
- **Minor · adr — the pre-gate drain is skipped while `status.currentRevision` is empty** (`function.go:355`).
  This was one of the two changes outside the ADR's file list.
  - The ADR says "in every full pass" and does not define this case. With no current revision, every revisioned
    worker would read as "never served" and be retired at once, so the guard is sound.
  - The drain after the stamp (`function.go:372-379`) still runs in the same pass once a revision is stamped.
  - It is untested: the mutant that removes the guard survives.

  Record it in the next ADR that touches this path.
- **Minor · adr — the drain retires an unrevisioned worker that shares the Function's name.**
  - The drain keeps only workers of S, C and D (`function.go:924`). An empty revision counts as "another
    revision", so such a worker is stopped and removed.
  - A CatalogService engine is named after its CatalogService and has no revision
    (`internal/provider/runtime.go:186`). If a Function and a CatalogService share a name in one namespace, every
    full pass of the Function kills the engine. A probe confirmed the removal.
  - Lookup by name across kinds predates this ADR (the provider's lookup and the old converge each mistake the
    other's workers for their own), and the ADR scopes CatalogService engines out. Read literally, Decision 4.1
    produces this, while Decision 1 and checklist item 13 say unrevisioned workers keep their old behavior.

  **Fix**: file an issue to scope runtime lookups by kind. A drain guard that skips an empty revision would restore
  item 13.
- **Minor · env — the containerd contract subtests were not run.** `worker-revision-round-trips`,
  `two-revisions-of-a-replica-coexist` and `worker-remove-after-stop` run only on Linux as root with `FUNCD_IT=1`.
  - The containerd half builds and vets for Linux (also with `-tags integration`) and lints clean.
  - The lanes give indirect evidence: revisioned container and CNI names work on real containerd, and `List`
    reports the revision (otherwise converge could not find its own workers and the Functions would not settle
    Ready).
  - No existing lane exercises `Remove` on containerd.
- **Minor · env — there is no committed Accepted ADR text to diff**, because the ADR file is untracked.
  - The Reviewing note in the Status line follows the precedent of ADR-0141 and ADR-0142.
  - The Decision and Contracts match the code.
  - The implementer's own guard (the Minor above) is absent from the ADR. This suggests the ADR was not rewritten
    to fit the code.

### The two changes outside the ADR's file list
- **`ensureFunction` keeps the stored status** (`internal/workflow/reconcile_workflow.go:280`): sanctioned and
  correct. It is the same class of defect as #15, which the ADR lists as a prerequisite: the status is
  server-owned, and the Workflow materializer must not wipe what the Function reconciler wrote. The change is one
  line. `TestMaterializeKeepsFunctionStatus` guards it (the mutant that removes the line fails it), and the
  workflow lane passes on containerd. Not verified: the builder's report that two workflow e2e tests fail without
  it (that overlay run was not done).
- **The drain is skipped with an empty `currentRevision`**: sound, see the Minor above.

### ✅ Verified correct (keep it)
- **Checks**, all exit 0:
  - `go build ./...`, also with `GOOS=linux`;
  - `go vet` on the host, on Linux, and on Linux with `-tags integration` for `internal/runtime/...`;
  - `gofmt -l` (no files);
  - `golangci-lint` (0 issues; also on Linux for `./internal/...`);
  - `go test -count=1 ./...` (79 packages ok);
  - `go test -race` on the changed packages (12 packages ok, 0 skipped);
  - `go test -tags e2e -count=1 -skip TestProbe ./pkg/funcd/...` (ok, 59.9 s);
  - `go mod verify`, `go mod tidy -diff` and `just check-hygiene`.

  The regenerated OpenAPI spec is byte-identical, and `go.mod`/`go.sum` are unchanged. The known
  `TestScenarioSiteArtifactRoundtrip` flake did not occur.
- **Every scenario test exists under its name and passes under `-race`, un-skipped.** This covers the eight
  scenarios and the plan's extra tests (a gate failure for six gate classes; pool admission has no case, but only
  a solo-to-pooled change, which the ADR scopes out, can reach it with S serving; a newer apply that does not extend a
  drain, a drain while a gate fails, the `DrainGrace` bound, a failed C that requeues after the period, a crashed S
  worker that is replaced and not removed, quiescence after a drain), plus the three contract subtests on the
  process driver and the tracker, dispatcher and invoker tests. Thirty repetitions under `-race`, run while a Lima
  VM loaded the machine, all passed, so the 30–50 ms drain windows did not flake.
- **Mutation evidence** (overlay mutants; the repository was untouched): 27 of 41 mutants are killed. Each of the
  following behaviors has a failing test when broken:
  - the drain before the gates, and the drain after a new revision is stamped;
  - no switch while D drains;
  - the revision filter in the resolver and in readiness;
  - the gate rule (S keeps serving; the requeue after the period);
  - the steady state's D check;
  - the drain's Stop + Remove, and the switch that demotes S to D;
  - the idle check;
  - S's replacement from S's Revision;
  - `RevisionReady` `Progressing`, the 200 ms poll of a booting C, a failed C that is kept, and `ShapeValid`
    reporting a failed C;
  - C's stopped replicas staying listed at scale-to-zero (an ADR-0142 test kills that mutant);
  - the process driver's `released` rules, revision reporting and IDs;
  - the tracker counting until the body closes, the end of a failed round trip, and the hand-out settle;
  - the materializer keeping the status.
- **Two probes cover what the shipped tests do not** (both pass under `-race`):
  - A call through `activator.New` with `Calls` set counts while it is in flight.
  - An upgraded (HTTP 101) connection tunnels through the counting transport and counts until it closes. Without
    the upgrade branch, `httputil.ReverseProxy` fails with "101 switching protocols response with non-writable
    body".
- **The runtime port and drivers match the Contracts**:
  - `NewInstanceID`, `WorkerSpec.Revision`/`Instance.Revision`, and the doc comment on `Remove`;
  - the containerd naming table (`workerNames`), with `funcd/revision` set only for a revisioned worker;
  - `Sweep` rebuilds CNI IDs with the same rule;
  - both `Stop` branches mark an instance released, and `Remove` is Conflict before that;
  - unrevisioned names are unchanged.
- **One tracker for every caller**: `pkg/funcd/funcd.go:583` builds the tracker and passes it to the reconciler
  (`:586`), the activator (`:620`), the Sensor invoker (`:684`) and the workflow dispatcher (`:795`). These three
  callers are the only users of `Endpoints.Upstream`, and the gateway's own proxies are not mounted
  (`internal/dataplane/dataplane.go:67`). `Wrap(nil)` wraps `http.DefaultTransport`, so the Sensor and workflow
  clients keep their transport.
- **The reconciler follows Decisions 3–9**:
  - the steady state calls only `Status` (RV unchanged, no `List`);
  - converge runs per revision with ADR-0142's per-replica table;
  - the switch condition and the S handling of Decisions 4.3–4.5;
  - the gate rule of 4.6. Its "as today" writes match the old code gate by gate: the phases, the `Ready` reasons
    and messages, `status.replicas` 0 where the old gate wrote it, and the 2 s requeues;
  - the requeue rule of 4.7;
  - with desired 0, S, D and `drainingSince` are cleared;
  - the resolver hands out S only, and records a hand-out only for a ready upstream;
  - `teardown` retires every worker.

  The pooled path is equivalent to the old `convergeFor`/`readyFor`.
- **Existing tests were changed only where the ADR sanctions it.** `TestScenarioScaleChangesReplicas` now
  reconciles until the drain ends (Decision 9), and its assertions are unchanged. The rest is harness plumbing:
  revisioned IDs, a fake `Remove`, `exit()` by name, and the new `readyReplicas` signature. The ADR-0142 suite
  (`supervision_test.go`) is unchanged and passes.
- **Conventions**: no goroutine, `any`, `panic` or non-slog logging was added; errors use `api/fault`; the code is
  ctx-first; new settings live in deps structs; there is no shim change.
- **Tracking**: the ADR is `Reviewing`; the F13 row reads `redeploy: reviewing`; the blueprint is synced in two
  places; there is no roadmap placeholder to reconcile; the board card is In Progress.
- **Lima lanes** (`just lima-example-all`, exit 1): eight of nine pass on containerd with revisioned Function
  workers: fn-to-fn, duckdb, s3, kv, workflow, funclog, egress and metastore. Only env-echo fails, on the stale
  container name (Blocker 1). The duckdb engine-crash case, which kills the unrevisioned `lake-r0`, passes.

### Definition of Done
12 / 17 items hold (the 16 Review-checklist items plus Done-when). The misses:
- item 3: the process driver's Conflict rule after a restart (model, Minor);
- item 13: an unrevisioned worker with a Function's name is now retired (adr, Minor);
- item 15: the e2e redeploy test is not written (sequencing);
- item 16: the env-echo lane fails (model, Blocker 1), and its redeploy case is not written (sequencing);
- Done-when: the same lane and e2e gaps.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0143 (implementation) → changes-requested, 2/0/6, 3 model-attributed,
DoD 12/17. See docs/reviews/model-scorecard.md.

### Recommendation
Loop back to `adr-impl`, then write the deferred e2e suites:
1. Update the env-echo crash case to the revisioned container name and rerun all nine lanes.
2. Reset `released` in the process driver's `Start`, with its contract subtest.
3. Add unit tests for the surviving mutants, at least the hand-out settle, the hand-out record and the
   all-replicas switch.
4. Write the `pkg/funcd` scenarios and the env-echo redeploy case.

The ADR stays `Reviewing`. The two `adr` Minors go to the next ADR that touches this path and to an issue about
lookups by name across kinds. Delete the old untracked probe before committing.
