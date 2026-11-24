## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0346 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/addon-dev-workers` at 22c299fc: nine commits, one per plan phase, on origin/main
1ccda7f5, with funcd-typescript v0.36.0 and funcd-python v0.31.0 pinned in 2aa84a1c. The reviewer did not write the
work, and every check below was run, not read. The shipped code does what the Decisions say. The changes requested
are about evidence: the Linux run that plan step 8 asks for cannot happen in CI as the tests stand, one behavior row is
not updated, and four rules have no test. ADR-0346 stays `Reviewing`.

### 🔴 Blockers

None.

### 🟡 Major 1 — the real-engine dev scenarios skip on CI's Docker, so no Job runs there · attribution: model

- **The skip.** `workerImage` builds the test's Addon runtime image on the engine and skips the test when the engine
  keeps no digest for an image that it built (`cmd/funcdctl/dev_worker_test.go:83-91`). Five of the six scenario
  tests start with it: `TestScenarioDevAddonWorker`, `…Bindings`, `…Output`, `…Reap` and `…Linux`.
- **CI's engine.** CI's `ci` job runs `just ci` on `ubuntu-latest`, which is Ubuntu 24.04. The runner image installs
  docker-ce 28.0.4 (actions/runner-images `images/ubuntu/toolsets/toolset-2404.json`), and its `install-docker.sh`
  writes no `daemon.json`. Docker 28 then uses its classic image store, which gives an image built on the host no
  RepoDigest, so the test reaches the skip that the builder wrote for that case.
- **What it costs.** Two items of the Definition of done then fail. Plan step 8's "a Job runs on an Addon's runtime
  under dev on Docker … on Linux in CI" is not met. `scenario: dev-addon-worker-linux` passes in no environment:
  macOS skips it by `GOOS`, CI skips it at the image, and `just test-linux` builds no `-tags dev` package. Because
  `just ci` runs `go test` without `-v`, a green CI would not show these skips.
- **Not checked yet.** These tests did not run on CI, because the branch has not been pushed. On colima's Docker 29,
  which uses the containerd image store, all five ran and passed (below).
- **The fix (builder).** Give the image a digest on any image store. For example, push it to a throwaway registry
  container on 127.0.0.1 and name it by the digest that the push returns, or enable the containerd image store in
  `ci.yml`. Under `CI`, fail rather than skip, so that a lost run shows. Then the PR's CI runs the Linux scenario and
  the Job on Docker.

### Minor 1 — "The add-on runtime under dev" row does not name the workers · attribution: model

The ADR's Behavior rows ask that `docs/behavior/funcdctl-dev.md`'s "The add-on runtime under dev" (`:51`) also name
the workers it runs. The row is unchanged: it still names only the engines, and it cites ADR-0334 alone. The fix is to
add the workers on an Addon's runtime to its rule, citing ADR-0346 Decision 4 and `TestScenarioDevAddonWorker`.

### Minor 2 — four rules that no test reaches · attribution: model

Each mutant below was run with `go test -overlay` on the whole package, and each one passed:

- **M3e: the Lake hook marks no endpoint key** (`internal/function/workerenv.go:287`). Decision 5 names `lakeHook`. A
  probe shows that the rule works: a VM worker bound to a Lake gets `FUNCD_LAKE_LAKE_URL=http://funcd.host:54321`.
- **M6a: `retire` keeps the listener** (`internal/function/function.go:2165`). Decision 6 says that the listener goes
  at `Remove`. Without this release, a retired worker's port stays open with a valid token until its Function goes.
- **M13: a start keeps the earlier run's port file** (`internal/provider/docker/workers.go:369`). A restarted worker
  on the host's network would read as listening on the old port, and `refresh` keeps that port for the whole run.
- **M9: a failed pull returns its raw error** (`internal/provider/docker/workers.go:245`). This undoes the
  `RuntimeUnavailable` that the builder chose.

The fix is one test row for each: a Lake binding in `TestDevAddonWorkerSpec`; a retired revision in
`TestDevAddonWorkerListenerGoes`; a second `Start` on `onHost` that must not read as listening before the new port file
is written; and an engine whose `Pull` fails.

### Routed (not this work's)

- **`ListenerFor(ref local.Ref, id runtime.InstanceID)`**, against the Contracts' `ListenerFor(id)` · adr · home:
  elsewhere, the decider.
  - The code follows a decision; this is no gap. ADR-0351 Decision 2 (`docs/adr/0351-…md:83-85`) was accepted after
    ADR-0346 on 2026-11-23, and it says that "ADR-0346's per-worker listener take[s] the whole ref". A run's instance
    ID names the run and not its Job, so `id` alone cannot fix the caller that Decision 6 requires.
  - What is open is tracking. ADR-0351 lists ADR-0346 only under "Relates to", so ADR-0346 has no back-link for this
    change. The decider can add `- **Superseded in part by**: ADR-0351 (2026-11-23) — the Contracts' ListenerFor takes
    the caller's ref` to ADR-0346. Commit b245f966 calls this an ADR gap and does not cite ADR-0351.
- **A host path in a container worker's env** · uncovered · home: the decider, or the shim-protocol follow-up under
  F139.
  - Under dev, the Lake hook's `DUCKDB_EXTENSION_DIRECTORY`, dev's host directory of extensions (`devLakeOptions` in
    `cmd/funcdctl/dev.go`), reaches a VM worker's env unchanged (the same probe).
  - It does not exist inside the container, and on containerd the key is never set. ADR-0346 says nothing about it.
    No fix is proposed until the decider rules.

### Choices of the implementation, for the decider to confirm

- **When a worker listens.** On the host's network, a worker listens once its port file holds a port.
  The shim writes that file in its listen callback (funcd-typescript v0.36.0 `shim/shim.mjs:4844`), which is the same
  signal that the process driver uses (ADR-0160). On an engine in a VM, which writes no port file, the driver probes
  `/health/readiness` and takes any status as an answer, as Decision 4 says. A bare TCP connect is not enough there,
  because the engine's port proxy accepts a connection before the shim listens.
- **The run volume's mode.** `/run/funcd` is made 0777 at each start, where Decision 2 says that the volume becomes
  the image user's. The `Engine` interface cannot inspect an image, so the driver cannot learn that user. Only this
  worker's container and its helpers mount the volume.
- **A failed pull.** Every failed Docker pull reads as `RuntimeUnavailable`. containerd keeps a failure other than a
  missing image as a retried error (`internal/runtime/containerd/containerd_linux.go:1336-1341`). Under dev, ADR-0149
  Decision 5's supervision pass recovers it. nerdctl pulls when it runs a container, so a missing image fails at
  `Start` there instead. The stop grace of 10 s is the engines' `stopGrace`, and `EnforcesLimits` is true because the
  engine applies `--cpus` and `--memory`.

### ✅ Verified correct (keep it)

- **The local API.**
  - Each worker has its own listener on 127.0.0.1 with a 32-byte token. A request must carry the token exactly once;
    the comparison is constant-time, and the header is stripped before any handler.
  - The listener goes when `Create` fails, when the ref is removed and at `Close`.
  - The 403 is answered through `wrap`. ADR-0330 Decision 3 exempts only the pool's member refusal from the
    interceptor, and the token refusal has no known cause, as that refusal has none.
- **`Pair`.**
  - It routes by `Image` and records which driver holds each worker. On a miss it asks each driver's `Status`, and it
    merges `List`.
  - It forwards output to both drivers, structured logs to the process driver alone, the longer stop grace, and
    limits only when both drivers enforce them.
  - No process-mode spec sets `Image`, the pool's included. Engine workers under dev keep runtimes of their own.
- **The worker driver.**
  - The bundle is copied through the helper's tar into a read-only volume, never a bind of a host folder.
  - On an engine in a VM, the port is published on 127.0.0.1 only and `funcd.host` resolves to `host-gateway`.
  - On Linux, the boot ID and the network namespace detect the host's network and refuse a rootless engine.
  - The driver records the exit code, and a signal above 128, and sets `FUNCD_LOG_STDOUT`.
  - The reap clears only its own role. Under `--persist`, the add-on runtime keeps only the engines' volumes.
- **The engine client's three additions** are each needed:
  - `RunSpec.Cmd` carries the port's `WorkerSpec.Command`, which the runtime contract runs.
  - `Container.Pid` carries the port's `Instance.PID`, which the contract requires to be positive
    (`internal/runtime/runtimecontract/contract.go:57`). Outside the drivers, only the Linux bench reads it.
  - `Helper.HostNetwork` is the host-network helper that Decision 5 names.
- **Mutants.** 32 mutants were written and run with `go test -overlay` outside the tree; 27 were caught.
  - **The token check:** any token passes, the header is kept, or two headers pass. All three were caught.
  - **The Pair routing:** inverted, no fallback on a miss, logs sent to the image side, limits taken from either
    driver, or the shorter grace. All five were caught.
  - **The endpoint rewrite:** none, every key, the raw `FUNCD_INVOKE_URL`, S3 unmarked, or add-on unmarked. All five
    were caught.
  - **Port publishing:** no port in a VM, Docker or nerdctl publishing on every interface, or a port on the host's
    network. All four were caught.
  - **The reap:** every role, `--persist` keeping the workers' volumes, or keeping none. All three were caught.
  - **The rest:** the gate, rootless, signals, the port file, readiness, and the listener's release when `Create`
    fails or the Function goes. All seven were caught.
  - **The five that passed:** M5d, a `Close` with no reap, is equivalent, because `StopAll` reaps every container of
    the key afterwards. The other four are Minor 2.
  - **On the real engine:** M3c and M1a also fail `TestScenarioDevAddonWorkerBindings` on colima.
- **The checks**, run through `scripts/agent/d`:
  - `go build ./...` exits 0.
  - `go vet` exits 0: `./...`, `-tags dev`, `-tags e2e`, and for Linux.
  - `golangci-lint` reports 0 issues on the touched packages and their dependents, also with `--build-tags dev` and
    for Linux.
  - gofmt, `go mod verify`, `go mod tidy -diff`, `check-hygiene`, `check-behavior` and `check-generated.sh` are clean.
  - `go test -race` passes on `internal/runtime`, `internal/workernode/local`, `internal/provider/...` and
    `internal/function`. `TestWorkersOnDocker` ran on colima's Docker in 11.1 s.
  - The dependents pass: `internal/artifact`, `kindref`, `dataplane`, `health`, `services/addon`, `services/sql`,
    `testkit/realshim`, `runtime/...`, `cmd/funcdctl`, `cmd/funcd` and `pkg/funcd`.
  - The dev scenarios on colima's Docker: five pass, in 0.6 to 1.4 s each, and the Linux one skips. The rest of the
    `-tags dev` suite passes in 217 s.
  - `go test -tags e2e ./pkg/funcd/...` passes in 413 s.
- **Tracking.** The ADR is `Reviewing`, and only its status line changed. F139 reads "dev workers: reviewing". The four
  back-links of the ADR's partial supersessions are present.

### Definition of Done

17 of 20 items hold:
- the 5 Review-checklist items;
- 3 of the 5 plan items: one test per scenario, `just ci-full` through the checks above (the gate runs it in full),
  and a Job on Docker on macOS;
- 9 of the 10 generic items.

Misses: "on Linux in CI" and "every scenario passes" (Major 1, model), and the behavior row (Minor 1, model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0346 (implementation) → changes-requested, 0/1/2, 3 model-attributed, DoD 17/20.
See `docs/reviews/model-scorecard.md`.

### Recommendation

The builder makes the real-engine scenarios run on CI's Docker, updates row `:51`, and adds the four test rows. A
second round then stamps ADR-0346 `Implemented` and F139's dev-workers part `implemented`, once the PR's CI shows the
Linux scenario and the Job passing. The PR references #1017 and does not close it, because F139's shim protocol is
still an idea. Nothing goes to a superseding ADR. The decider rules on the ADR-0351 back-link and on the extension
directory.
