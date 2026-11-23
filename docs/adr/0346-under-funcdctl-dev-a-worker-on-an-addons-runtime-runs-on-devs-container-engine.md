# ADR-0346: Under funcdctl dev, a worker on an Addon's runtime runs on dev's container engine

- **Status**: Proposed (2026-11-23), judged three times, nothing open: round 1's Blocker (with the decider's option
  A), round 2's Blocker and Major, and every Minor folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: dev, runtime, addons, functions, jobs, local-api
- **Realizes**: [FEAT-0012/F139](../feat/0012-feat-infrastack.md) (runtimes from add-ons: its workers under
  `funcdctl dev`)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) Decision 13's interim and
  `addon-runtime-process-driver`'s dev clause (Decisions 1 to 8); [ADR-0064](0064-fn-to-fn-rpc-links.md) Decision 3,
  as ADR-0158 and ADR-0243 left it, for its transport only (a dev container worker reaches the local API on a listener
  of its own, not a bound socket: Decision 6); [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 6 (not every
  container and volume of the key is an engine's) and Decision 7 (`--persist` keeps the engines' volumes only:
  Decision 7); [ADR-0306](0306-workload-hooks-check-a-functions-bindings-and-give-its-workers-their-env.md) Decision 1
  (a hook also names the keys of its env that hold funcd's loopback address: Decision 5)
- **Relates to**: [ADR-0011](0011-runtime-sandbox-port.md) (the worker port) ·
  [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) (a token in a worker's env) ·
  [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (output) ·
  [ADR-0306](0306-workload-hooks-check-a-functions-bindings-and-give-its-workers-their-env.md) (the bindings' env) ·
  F137 (Jobs)

## Context & Need

The decider ruled on 2026-11-23 that under `funcdctl dev` a Function or a Job on an Addon's runtime must run, not fail
`RuntimeUnavailable` (ADR-0344 Decision 13): the InfraStack add-on's OpenTofu Jobs, for one, must run on a developer's
machine. Dev runs add-on engines on Docker or nerdctl (ADR-0334), but every worker on its process driver
(`cmd/funcdctl/dev.go:1080`, `:1092`), whose gate refuses a runtime with no shim
(`internal/function/function.go:3131-3144`), and whose specs set `Image` to the runtime's name (`function.go:3283`,
`internal/function/pool.go:840`). A worker in a container cannot reach what a process worker reaches: its local API is
a Unix socket (ADR-0064), colima shares only `$HOME` with its VM, and the bindings' endpoints are the host's
`127.0.0.1` (ADR-0306). Callers: a developer's Function or Job on an Addon's runtime, dev, and the shims. A server is
unchanged: there such a worker runs on containerd.

## Scenarios

- `scenario: dev-addon-worker` — *Given* `funcdctl dev` on Docker or nerdctl and an Addon `deno` whose `deno2` lists
  `Function` and `Job`, *When* a Function and a Job name `deno2`, *Then* their workers run `deno2`'s image on the
  engine, the Function turns Ready and the run succeeds, and a Function on `nodejs22` beside them runs on the process
  driver.
- `scenario: dev-addon-worker-linux` — *Given* dev on a Linux host, *Then* such a worker shares the host's network and
  listens on the host's loopback only; *given* a rootless engine, *then* it fails `RuntimeUnavailable` naming it.
- `scenario: dev-addon-worker-bindings` — *Given* such a worker that binds a KVStore, owns a Secret and links to
  `pricing`, *When* its handler calls `ctx.kv`, `ctx.secret.put` and `ctx.invoke`, *Then* all reach funcd, and `pricing`
  never sees the token; *when* a request on its listener carries another token or none, *Then* 403.
- `scenario: dev-addon-worker-no-engine` — *Given* dev with neither engine, *Then* such a worker fails
  `RuntimeUnavailable` naming what dev tried, and a Function on `nodejs22` runs.
- `scenario: dev-addon-worker-output` — *When* such a worker prints a line and its run exits 3, *Then* `funcdctl logs`
  shows the line and the run records exit code 3.
- `scenario: dev-addon-worker-reap` — *Given* dev killed while such a worker runs, *When* dev starts again, *Then* the
  session's worker containers and volumes go, and `--persist`'s engine volumes stay.

## Scope

**In**: Decisions 1 to 8. **Out**: Function volumes under dev, still refused outside containerd (ADR-0262); pull
credentials, as ADR-0334 has them; funcd's own runtimes on the engine; a server, unchanged. **Measured**: colima's
host gateway (below). Linux host networking is the engine's documented behavior, which CI's real-engine test checks on
Docker; colima's Docker runs the lane case; nerdctl runs the tests wherever it answers; no test here checks Docker
Desktop's gateway.

## Constraints & Decision drivers

- Reuse ADR-0334's engine, session key, helper and `127.0.0.1` publishing, and the containerd worker's shape
  (`function.go:3305-3316`); the process driver, the worker port (ADR-0011) and a server do not change.
- funcd's listeners stay on `127.0.0.1` everywhere: the decider chose option A on 2026-11-23, over binding them on the
  bridge's gateway (B) or running such workers on macOS only (C).
- Measured on 2026-11-23 on colima: a bind mount from outside `$HOME` came up empty in the container; `--add-host
  funcd.host:host-gateway` resolved to `192.168.5.2`, and a fetch from the container reached host listeners bound to
  `127.0.0.1` and to `0.0.0.0`.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| B: bind funcd's listeners on the bridge's gateway on Linux | one network mode | every container on that bridge reaches them, guarded only by tokens and keys | Rejected (decider) |
| C: run such workers on macOS engines only | no Linux path | Linux developers and CI could not run them | Rejected (decider) |
| Bind-mount the local API's socket, as containerd does | no new transport | colima shares only `$HOME`, and a Unix socket does not cross into its VM | Rejected |
| A bearer token on one shared listener | one listener | the token alone would name the caller, against ADR-0064's connection-scoped identity | Rejected |
| No local API under dev for such workers | nothing new | a Job's bindings and Secret writes (F137, F144) are what it runs for | Rejected |

## Decision

1. **The gate.** A worker runtime may implement `runtime.ImageRunner`, whose `RunsImages` says whether it runs a worker
   from an image now and, when not, why. The gate passes an Addon's runtime (ADR-0344 Decision 6) when the server's
   runtime runs images, and otherwise fails it as on a process server. When dev finds no engine, its image side is
   `runtime.NoImages(why)`, whose reason is the engine search's (ADR-0334 Decision 1).
2. **The worker's shape.** A Function's or Job's worker on an Addon's runtime is built in the container shape
   (`function.go:3305-3316`): no `Command`, the artifact under `/var/funcd/artifact` in the bundle volume, mounted
   read-only, and `Image` the image its Revision recorded (ADR-0344 Decision 8). On an engine that shares the host's
   network (Decision 5) it takes no `FUNCD_PORT`, so the shim binds the loopback, and writes its port to
   `FUNCD_PORTFILE` in a second, writable volume at `/run/funcd`, which the helper makes the image user's; on an
   engine in a VM it takes `FUNCD_PORT` 8080 and no port file. No other Function or Job worker in process mode sets
   `Image`. Engine workers keep theirs (`internal/provider/runtime.go:380-391`) and never run over `Pair`: dev gives
   them runtimes of their own.
3. **The pair.** `runtime.Pair(process, images)` creates a Function's or Job's worker whose spec has an `Image` on
   `images` and any other on `process`, and records which driver holds each instance. Both drivers reap at start, so
   the record starts empty, and on a miss `Pair` asks each driver's `Status`, since instance IDs are deterministic.
   `List` merges both. It implements `ImageRunner` from `images`, and forwards output capture (ADR-0168) to the driver
   that holds a worker, structured logs (ADR-0081) to the process driver, the longer stop grace, and limits only when
   both enforce them. Dev passes it in both of its option lists (`dev.go:1080`, `:1092`).
4. **The worker driver.** `docker.NewWorkers` runs workers on the engine dev found for its add-on engines (ADR-0334
   Decision 1, nerdctl through the same `Engine`). It copies the bundle into the worker's volume through ADR-0334's
   helper and mounts it read-only, never a bind mount; takes the worker as listening once its `/health/readiness`
   answers with any status, checked within `Status` with no timer of its own; reads stdout and stderr through the
   engine's log follow, as the containerd driver reads a task's FIFOs (ADR-0168); records the exit code, and the
   signal when the code is above 128; and sets `resources` as the engine's CPU and memory limits. A container worker
   has no structured-log channel, so the driver sets `FUNCD_LOG_STDOUT`, which no other worker gets, and the shims
   then print each record to stdout (plan step 5), and its records reach funcd as lines. It pulls as ADR-0334 does.
5. **The network.** funcd's listeners stay on the host's `127.0.0.1`, and `ImageRunner.EndpointHost` names the host a
   container worker reaches them by.
   - An engine whose host-network helper reads the host's own boot ID (`/proc/sys/kernel/random/boot_id`) shares the
     host's network: the worker runs with it, its endpoints stay `127.0.0.1`, and `RunSpec.Port` is unused. A rootless
     engine has no host network, so `RunsImages` is false, naming it.
   - An engine in a VM, always on macOS and also Docker Desktop, colima or Lima on Linux: the worker binds 8080 in its
     own network, published on `127.0.0.1` at a free port, and resolves `funcd.host` to the engine's host gateway. The
     reconciler writes `funcd.host` for `127.0.0.1` in exactly the env keys its workload hooks mark as funcd's
     endpoints, in their shared or own part (`s3Hook`, `lakeHook`, `addonHook`; ADR-0306 Decision 1), and in
     `FUNCD_INVOKE_URL`, so the driver's spec stays as the worker port has it. Dev sets no other endpoint: egress is off
     under dev (ADR-0117).
6. **The local API.** Each such worker gets a listener of its own on the host's `127.0.0.1`, named in
   `FUNCD_INVOKE_URL`, which fixes its caller as a socket does, so ADR-0064's connection-scoped identity holds. A
   request proves it comes from that worker with the token funcd issued it at `Create`, in `FUNCD_INVOKE_TOKEN`, sent
   as `X-Funcd-Invoke-Token`, compared in constant time, and stripped before any pass-through (`ctx.fetch`,
   `ctx.invoke`). A request with another token or none gets 403. The listener and its token go at `Remove`, when
   `Create` fails, and with the Function's own `Remove` (`function.go:89-90`). The shims use this transport when
   `FUNCD_INVOKE_URL` is set.
7. **Names and the reap.** A worker's container and volume are named `funcd-dev-worker-<key>-<hash>`, the hash of its
   namespace, name, revision and replica, and labelled with the session key and the role `worker`, so ADR-0334
   Decision 7's reap of a crashed session clears them with the engines'. The worker driver lists, stops and removes
   only its own role, and `--persist` keeps the engines' volumes only.
8. **Jobs and images.** A Job's run on an Addon's runtime runs as such a worker. Its bindings and Secret writes reach
   funcd only if its image's shim speaks Decision 6's transport, which funcd's shims do; how F141 builds the OpenTofu
   runtime is F141's, and another image's transport belongs to the shim-protocol follow-up under F139.

## Design model

None. The driver implements ADR-0011's port, as ADR-0334's implements the provider port, and adds no lock, timer or
stop path of its own: its stop is the engine's, with the forwarded grace, and its readiness probe runs within
`Status`. `Pair` routes by a record it fills as workers are created, and the token check is per request.

## Behavior rows

- **funcdctl-dev.md**: "A worker on an Addon's runtime" (ADR-0344's interim row) becomes Decisions 1 to 6's rule; "The
  add-on runtime under dev" (`:51`) also names the workers it runs; new rows "A dev container worker's local API" (the
  token, 403) and "Leftover dev workers" (the reap).
- **functions.md**: the hard-gate reasons keep `RuntimeUnavailable`, with the engine search's cause or the rootless one
  under dev.

## Temporary workarounds

None.

## Contracts

```go
// ImageRunner is a worker runtime that may run a worker from an image (internal/runtime).
type ImageRunner interface {
	RunsImages() (ok bool, why string)
	EndpointHost() string // "127.0.0.1" on the host's network, "funcd.host" on an engine in a VM
}

// NoImages runs no image, and says why; dev uses it when it finds no engine (internal/runtime).
func NoImages(why string) Runtime

// Pair runs a Function's or Job's worker whose spec has an Image on images, any other on process (internal/runtime).
func Pair(process, images Runtime) Runtime

// NewWorkers runs workers on dev's container engine, nerdctl's through the same Engine (internal/provider/docker).
func NewWorkers(ctx context.Context, eng Engine, key string, logger *slog.Logger) (runtime.Runtime, error)

// The engine client grows (internal/provider/docker).
type Container struct {
	// … today's fields
	ExitCode int
}

type Mount struct {
	Volume, Target string
	ReadOnly       bool // new
}

type RunSpec struct {
	// … today's fields but Volume and Target, which Mounts replaces (an engine passes one); Port is unused with HostNetwork
	Mounts      []Mount  // a worker's bundle, read-only, and /run/funcd
	HostNetwork bool
	ExtraHosts  []string // "funcd.host:host-gateway"
	NanoCPUs    int64
	MemoryBytes int64
}

// A workload hook (ADR-0306) also returns the keys of its env that hold funcd's loopback address (internal/function).
check(ctx context.Context, fn *v1.Function) (shared map[string]string, endpoints []string, gate *gateFailure, err error)

// InvokeSocketProvider (internal/function), which local.Manager implements, gains a dev container worker's listener.
ListenerFor(id runtime.InstanceID) (url, token string, err error)
ReleaseListener(id runtime.InstanceID)
```

| Consumes | Exposes |
|---|---|
| ADR-0334's engine and key; a Revision's `runtimeImage`; the hooks' env and its endpoint keys (ADR-0306) | `ImageRunner`, `NoImages`, `Pair`, `NewWorkers`; `FUNCD_INVOKE_URL`, `FUNCD_INVOKE_TOKEN`, `X-Funcd-Invoke-Token` |

## Implementation plan

1. `internal/runtime`: `ImageRunner`, `NoImages`, `Pair`; `runtimecontract` runs on `Pair` over two fakes.
2. `internal/function`: the gate reads `ImageRunner`; `workerSpec` and `pool.go` set `Image` only in the container shape
   and build an Addon-runtime worker in it with Decision 6's env; the providers' workload hooks mark their endpoint
   keys (ADR-0306).
3. `internal/workernode/local` (`Manager`): `ListenerFor` and `ReleaseListener`, the token check, and the header
   stripped before a pass-through; `internal/function`'s `InvokeSocketProvider` gains both.
4. `internal/provider/docker`: `NewWorkers`, `Container.ExitCode`, `RunSpec`'s new fields; `internal/provider/nerdctl`
   the same; `runtimecontract` runs on `NewWorkers` over a fake `Engine`.
5. The shims: `FUNCD_INVOKE_URL`, `FUNCD_INVOKE_TOKEN` and the header, and a record printed to stdout when
   `FUNCD_LOG_STDOUT` is set, in pyvvo/funcd-typescript and pyvvo/funcd-python, released, their pins bumped here
   (ADR-0141).
6. `cmd/funcdctl/dev.go`: `Pair(process, workers)`, or `Pair(process, NoImages(why))`, in both option lists.
7. Docs: the guide's dev page and the behavior rows; tests: one `TestScenario…` per scenario, the real-engine ones
   skipped with no engine, as ADR-0334's are.
8. Done when `just ci-full` passes and a Job runs on an Addon's runtime under dev on Docker, on macOS in the lane case
   and on Linux in CI; built after ADR-0344 (F139) and the Jobs ADRs (F137).

## Review checklist

- [ ] Under dev, a worker on an Addon's runtime runs on the engine and one on funcd's runtimes on the process driver;
      with no engine or a rootless one, the cause says so.
- [ ] No process-mode Function or Job spec sets `Image`; `Pair` routes by it and forwards each capability as Decision 3.
- [ ] Nothing a worker needs is bind-mounted; on Linux it uses the host's network, on macOS `funcd.host`.
- [ ] Each worker's listener checks its token in constant time, strips it before any pass-through, and goes at
      `Remove`; another token or none gets 403.
- [ ] Worker containers carry the key and the role `worker`; the reap clears a crashed session's, and `--persist`
      keeps the engines' volumes.

## Consequences

- **(+)** A Function or a Job on an Addon's runtime runs under dev as on a server, OpenTofu's Jobs included.
- **(−)** Such a worker needs Docker or nerdctl and starts slower than a process worker: a container and a copied
  bundle each.
- **(−)** Its local API port is guarded by its token alone: any local user, and on colima every container in the VM,
  add-on engines included, can reach the port. In process mode any process of the developer's user can already open a
  Function's socket.
- **(−)** On Linux such a worker shares the host's network, as process workers do.
- **(−)** A container worker has no structured-log channel: its records arrive as stdout lines, without their fields.
- **(−)** The shims carry a second transport, and an image whose shim lacks it reaches no local API under dev.

## Open questions

None.

## References

- FEAT-0012 F139 and its issue [#1017](https://github.com/pyvvo/funcd/issues/1017); ADR-0344 Decision 13; the decider's
  option A of 2026-11-23
- ADR-0011, ADR-0064, ADR-0081, ADR-0141, ADR-0158, ADR-0168, ADR-0243, ADR-0262, ADR-0306, ADR-0334, ADR-0338,
  ADR-0344, ADR-0345
- `internal/function/function.go` (`workerSpec`, `runtimeUnavailable`), `internal/function/pool.go`,
  `internal/provider/docker/docker.go`, `cmd/funcdctl/dev.go`; the shims' `shim.py:393-407` and `shim.ts:191-220`
