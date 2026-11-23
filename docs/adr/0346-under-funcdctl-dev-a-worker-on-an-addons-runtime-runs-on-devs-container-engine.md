# ADR-0346: Under funcdctl dev, a worker on an Addon's runtime runs on dev's container engine

- **Status**: Proposed (2026-11-23)
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: dev, runtime, addons, functions, jobs, local-api
- **Realizes**: [FEAT-0012/F139](../feat/0012-feat-infrastack.md) (runtimes from add-ons: its workers under
  `funcdctl dev`)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) Decision 13's interim and
  `addon-runtime-process-driver`'s dev clause (such a worker is no longer `RuntimeUnavailable` under dev: Decisions 1
  to 7); [ADR-0064](0064-fn-to-fn-rpc-links.md) Decision 3 as ADR-0158 left it (a dev container worker's local API
  caller is named by its token, not its socket: Decision 5); [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md)
  Decision 7 (the reap clears a session's worker containers too: Decision 6)
- **Relates to**: [ADR-0011](0011-runtime-sandbox-port.md) (the worker port) ·
  [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) (a token in a worker's env) ·
  [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (output) · [ADR-0262](0262-function-owned-volumes.md) (volumes) · F137 (Jobs)

## Context & Need

The decider ruled on 2026-11-23 that under `funcdctl dev` a Function or a Job on an Addon's runtime must run, not fail
`RuntimeUnavailable` (ADR-0344 Decision 13): the InfraStack add-on's OpenTofu runtime, for one, must run its Jobs on a
developer's laptop. Dev already runs add-on engines on the developer's Docker or nerdctl (ADR-0334), but every worker on
its process driver (`cmd/funcdctl/dev.go:1080`, `:1092`), whose gate refuses a runtime with no shim
(`internal/function/function.go:3131-3144`), and whose spec puts the runtime's name in `Image` for every worker
(`function.go:3283`, `internal/function/pool.go:840`). A worker in a container also cannot reach what a process worker
reaches: its local API is a Unix socket (ADR-0064), colima shares only `$HOME` with its VM, and the bindings' endpoints
are the host's `127.0.0.1` (`internal/function/workerenv.go`). Callers: a developer's Function or Job on an Addon's
runtime, dev, and the shims.

## Scenarios

- `scenario: dev-addon-worker` — *Given* `funcdctl dev` on Docker or nerdctl and an Addon `deno` whose `deno2` lists
  `Function` and `Job`, *When* a Function and a Job name `deno2`, *Then* their workers run `deno2`'s image on the
  engine, the Function turns Ready and the run succeeds, and a Function on `nodejs22` beside them runs on the process
  driver.
- `scenario: dev-addon-worker-bindings` — *Given* such a worker that binds a KVStore and owns a Secret, *When* its
  handler calls `ctx.kv` and `ctx.secret.put`, *Then* both reach funcd's local API with its token; *when* a request
  carries another worker's token or none, *Then* 403.
- `scenario: dev-addon-worker-no-engine` — *Given* dev with neither engine, *Then* such a worker fails
  `RuntimeUnavailable` naming what dev tried, and a Function on `nodejs22` runs.
- `scenario: dev-addon-worker-output` — *When* such a worker prints a line and its run exits 3, *Then* `funcdctl logs`
  shows the line, and the run records exit code 3.
- `scenario: dev-addon-worker-reap` — *Given* dev killed while such a worker runs, *When* dev starts again, *Then* the
  session's leftover worker containers and volumes go, and `--persist`'s engine volumes stay.

## Scope

**In**: Decisions 1 to 7. **Out**: Function volumes under dev, which stay refused outside containerd (ADR-0262); pull
credentials, which stay as ADR-0334 has them; a worker of funcd's own runtimes on the engine; Docker Desktop's and
nerdctl's host gateway, which the lane case measures (only colima's is measured here).

## Constraints & Decision drivers

- Reuse ADR-0334's engine, its session key, its helper and its `127.0.0.1` publishing, and the containerd worker's
  shape (`function.go:3305-3316`); the process driver and the worker port (ADR-0011) do not change.
- colima shares only `$HOME` with its VM: measured on 2026-11-23, a bind mount from outside it came up empty in the
  container, so nothing a worker needs is bind-mounted.
- A container on colima reaches the host's `127.0.0.1` through the engine's host gateway: measured on 2026-11-23,
  `--add-host funcd.host:host-gateway` resolved to `192.168.5.2`, and a fetch from the container reached listeners bound
  to `127.0.0.1` and to `0.0.0.0` on the host.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Bind-mount the local API's socket, as containerd does | no new transport | colima shares only `$HOME`, and a Unix socket does not cross into its VM | Rejected |
| A relay per worker inside the engine's VM | keeps the socket | a helper container and a relay protocol per worker | Rejected |
| No local API under dev for such workers | nothing new | a Job's bindings and Secret writes (F137, F144) are what it runs for | Rejected |
| Route by a new spec field instead of `Image` | explicit | a second field saying what `Image` already says, once process specs stop setting it | Rejected |
| Run such workers in containerd inside a Lima VM | the production driver | a second VM per session beside the engine dev already uses | Rejected |

## Decision

1. **The gate.** A worker runtime may implement `runtime.ImageRunner`: `RunsImages` says whether it runs a worker from
   an image now and, when it does not, why. The gate passes an Addon's runtime (ADR-0344 Decision 6) when the server's
   runtime runs images, and fails it, as on a process server, when it does not; under dev with no engine, the cause is
   the engine search's own, naming what dev tried (ADR-0334 Decision 1).
2. **The worker's shape.** A worker on an Addon's runtime is built in the container shape (`function.go:3305-3316`): no
   `Command`, the artifact under `/var/funcd/artifact`, `FUNCD_PORT` 8080, and its `Image` the image its Revision
   recorded (ADR-0344 Decision 8), or its Job's definition. No other worker in process mode sets `Image`: the process
   driver never reads it, and a pool host carries none (`pool.go:840`).
3. **The pair.** `runtime.Pair(process, images)` creates a worker whose spec has an `Image` on `images` and any other on
   `process`, records which driver holds each instance, and routes `Start`, `Stop`, `Status`, `Logs`, `Exec` and
   `Remove` by that record, rebuilt from both drivers' `List` at start; `List` merges both. It implements
   `ImageRunner` from `images`, and forwards the optional capabilities to the driver that holds a worker: output capture
   (ADR-0168), the stop grace (the longer of the two) and limits (enforced only when both enforce them). Dev passes it
   in both of its option lists (`dev.go:1080`, `:1092`).
4. **The worker driver.** `docker.NewWorkers` runs workers on the engine dev found for its add-on engines (ADR-0334
   Decision 1, nerdctl through the same `Engine`): it copies the bundle into the worker's volume through ADR-0334's
   helper, mounted read-only, and never bind-mounts it; publishes the shim's port on `127.0.0.1` at a free port; takes a
   worker as listening once the shim's `/health/readiness` answers on it; reads stdout and stderr through the engine's
   log follow, as the containerd driver reads a task's FIFOs (ADR-0168); records the exit code and signal from the
   engine's inspect; and sets `resources` as the engine's limits. It pulls as ADR-0334 does, with no credentials.
5. **The way back to funcd.** Each worker container resolves `funcd.host` to the engine's host gateway, and every host
   endpoint in its env names `funcd.host` in place of `127.0.0.1`. Its local API, a Unix socket in process and containerd
   modes, is instead a listener of its own on the host's `127.0.0.1`, given in `FUNCD_INVOKE_URL`, and a request names its
   worker by the token funcd issued that worker in `FUNCD_INVOKE_TOKEN`, as the add-on proxy's token names a Function
   (ADR-0338). A request with another worker's token, or none, gets 403. The token lives as long as the worker. The
   shims speak this transport when `FUNCD_INVOKE_URL` is set.
6. **Names and the reap.** A worker's container and volume are named `funcd-dev-worker-<key>-<hash>`, the hash of its
   namespace, name, revision and replica, and labelled with the session key and the role `worker`, so ADR-0334
   Decision 7's reap of a crashed session clears them with the engines'. The worker driver lists, stops and removes only
   its own role, and keeps no worker across sessions: `--persist` keeps engines' volumes only.
7. **Jobs.** A Job's run on an Addon's runtime runs as such a worker, so its bindings and Secret writes reach funcd as a
   Function's do, and ADR-0345's destroy Job runs under dev.

## Design model

None. The driver implements ADR-0011's port, as ADR-0334's implements the provider port, and adds no lock, timer or
stop path of its own: its stop is the engine's stop with the forwarded grace. `Pair` routes by a record it rebuilds from
its drivers' lists, and the token check is per request.

## Behavior rows

- **funcdctl-dev.md**: "A worker on an Addon's runtime" (ADR-0344's interim row) becomes Decision 1 to 5's rule;
  "The add-on runtime under dev" (`:51`) also names the workers it runs; new rows "A dev container worker's local API"
  (the token, 403) and "Leftover dev workers" (the reap).
- **functions.md**: the hard-gate reasons keep `RuntimeUnavailable`, with the engine search's cause under dev.

## Temporary workarounds

None.

## Contracts

```go
// ImageRunner is a worker runtime that may run a worker from an image (internal/runtime).
type ImageRunner interface {
	RunsImages() (ok bool, why string)
}

// Pair runs a worker whose spec has an Image on images and any other on process (internal/runtime).
func Pair(process, images Runtime) Runtime

// NewWorkers runs workers on dev's container engine, nerdctl's through the same Engine (internal/provider/docker).
func NewWorkers(ctx context.Context, eng Engine, key string, logger *slog.Logger) (runtime.Runtime, error)

// Container gains the exit of a stopped container, and RunSpec the hosts a container resolves and a read-only volume.
type Container struct {
	// … today's fields
	ExitCode int
	Signal   string
}

type RunSpec struct {
	// … today's fields
	ExtraHosts     []string // "funcd.host:host-gateway"
	VolumeReadOnly bool
}
```

| Consumes | Exposes |
|---|---|
| ADR-0334's engine and key; a Revision's `runtimeImage`; the worker env (ADR-0306) | `ImageRunner`, `Pair`, `NewWorkers`; `FUNCD_INVOKE_URL` and `FUNCD_INVOKE_TOKEN` |

## Implementation plan

1. `internal/runtime`: `ImageRunner`, `Pair`; `runtimecontract` runs on `Pair` over two fakes.
2. `internal/function`: the gate reads `ImageRunner` (Decision 1); `workerSpec` and `pool.go` set `Image` only in the
   container shape, and build an Addon-runtime worker in it with `funcd.host` endpoints and Decision 5's env.
3. `internal/workernode/local`: a listener per dev container worker on `127.0.0.1`, its requests named by their token.
4. `internal/provider/docker`: `NewWorkers`, `Container`'s exit, `RunSpec`'s extra hosts and read-only volume;
   `internal/provider/nerdctl` the same; `runtimecontract` runs on `NewWorkers` over a fake `Engine`.
5. The shims: `FUNCD_INVOKE_URL` with `FUNCD_INVOKE_TOKEN` in pyvvo/funcd-typescript and pyvvo/funcd-python, released,
   and their pins bumped here (ADR-0141).
6. `cmd/funcdctl/dev.go`: `Pair(process, workers)` in both option lists, `NewWorkers` on the engine dev already found.
7. Docs: the guide's dev page, the behavior rows; tests: one `TestScenario…` per scenario, a real-engine test that
   skips with no engine, as ADR-0334's do.
8. Done when `just ci-full` passes and the lane case runs a Job on an Addon's runtime under dev on Docker; built after
   ADR-0344 (F139) and the Jobs ADRs (F137).

## Review checklist

- [ ] Under dev, a worker on an Addon's runtime runs on the engine, and a worker on funcd's own runtimes on the process
      driver; with no engine, the cause names what dev tried.
- [ ] No process-mode spec sets `Image`; `Pair` routes by it and forwards output capture, the grace and limits.
- [ ] Nothing a worker needs is bind-mounted; its endpoints name `funcd.host`.
- [ ] A local API request with another worker's token, or none, gets 403.
- [ ] Worker containers carry the key and the role `worker`, and the reap clears a crashed session's.

## Consequences

- **(+)** A Function or a Job on an Addon's runtime runs under dev as on a server, OpenTofu's Jobs included.
- **(−)** Such a worker needs Docker or nerdctl, and starts slower than a process worker: a container and a copied
  bundle per worker.
- **(−)** Its local API is a port on the host's loopback guarded by a token, not a socket only it holds: a local process
  that reads the token from the worker's env can call as that worker, as with the add-on proxy's token (ADR-0338).
- **(−)** The shims carry a second transport for the local API.
- **(−)** Docker Desktop's and nerdctl's host gateway are measured only by the lane case.

## Open questions

None.

## References

- FEAT-0012 F139 and its issue [#1017](https://github.com/pyvvo/funcd/issues/1017); ADR-0344 Decision 13; the judge of
  ADR-0344's amendment on [#1526](https://github.com/pyvvo/funcd/pull/1526)
- ADR-0011, ADR-0064, ADR-0141, ADR-0158, ADR-0168, ADR-0262, ADR-0306, ADR-0334, ADR-0338, ADR-0344, ADR-0345
- `internal/function/function.go` (`workerSpec`, `runtimeUnavailable`), `internal/function/pool.go`,
  `internal/provider/docker/docker.go`, `cmd/funcdctl/dev.go`
