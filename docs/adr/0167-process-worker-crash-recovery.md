# ADR-0167: Process worker crash recovery — a saved worker registry, reaped at boot

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, process-driver, containerd, crash-only, config
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (the function runtime behind the `runtime.Runtime` port)
- **Supersedes**: [ADR-0011](0011-runtime-sandbox-port.md) Decision 2, the clause "`Status`/`List` read an in-memory,
  mutex-guarded registry" only (the registry is now also saved; the rest of Decision 2 stands)
- **Relates to**: draft ADR-0152 (adds the `funcd/owner-kind` label, which the boot sweep ignores; the sweep calls
  `discard`, whose behaviour ADR-0152 does not change) · draft ADR-0160 (edits the same `process.go` `Start`/`wait`/`Stop`; its `discard` removes a
  worker's boot dir, so this reap also clears boot dirs left by a hard kill) · draft ADR-0163 (the stop-grace key stays
  here) · draft ADR-0168 (pipes are not re-attached after the reap). No landing order: the later PR rebases.

## Context & Need

Issue [#44](https://github.com/pyvvo/funcd/issues/44), reproduced on main 1193be6 at driver and daemon level (a
skeptic re-ran it and refuted nothing):

- `kill -9` of the daemon left 2 Node workers (about 60 MB each, ppid 1) that still answered 200; the restart added 2
  more, so 4 ran. Nothing in the process driver survives the daemon: `New()` starts an empty table
  (`internal/runtime/process/process.go:42-51`), `cmd/funcd/main.go:712` passes no saved state, and only a running
  daemon reaps (`wait()` `process.go:172-175`, `Close()` `:307-329`).
- The reconciler re-creates replicas on the empty runtime by design (ADR-0143 4.3,
  `internal/function/function.go:888-905`), so each crash adds one full set of workers.
- Each crash also leaks the temp files `funcd-worker-*.log` and `*.log.port` (`process.go:77-85`; only `Close`
  removes them, `:323-327`).
- A Function deleted while funcd is down keeps its orphans forever, which breaks ADR-0020's `delete-reclaims`.
- The same leak hits `funcdctl dev` (`cmd/funcdctl/dev.go:643`, which runs on `funcd.InMemory()` →
  `pkg/funcd/presets.go:50` `process.New()`) and the dev catalog engine (`internal/catalog/devengine/devengine.go:223`,
  a `duckdb` child with no process group).
- On containerd, `reclaim()` (`internal/runtime/containerd/containerd_linux.go:343-366`) only frees a name that is
  re-created; a leftover whose name never returns stays, because `Sweep` (`:493`) is called only from the bench.

ADR-0014 C4 (crash-only: rebuild the world from actual state on boot) and `blueprint.md:437` ("No state lives only
in memory") are not met for process mode. The stop grace is the constant `stopGrace = 3 * time.Second`
(`process.go:25`).

## Scenarios

- **scenario: crash-restart-leaves-desired-workers** — Given a daemon in process mode with a Function at 2 replicas,
  When the daemon is killed with SIGKILL and restarted on the same data dir, Then exactly 2 workers of that Function
  run within one supervision period, and none has ppid 1 from the first run.
- **scenario: reused-pid-never-killed** — Given a registry entry whose pid now belongs to a process with a different
  start time or an argv without the entry's instance ID, When the driver opens, Then that process is not signalled
  and the entry is cleared.
- **scenario: temp-files-removed** — Given a crashed run left the driver's temp files named in the registry (today
  `funcd-worker-*.log` and `*.log.port`; `funcd-worker-*.port` once ADR-0168 lands), When the driver opens, Then
  every one of them is gone.
- **scenario: deleted-while-down-reaped** — Given a Function deleted from the store while funcd is down, When funcd
  restarts, Then no worker of that Function runs.
- **scenario: dev-persist-restart-reaps** — Given `funcdctl dev --persist` killed with SIGKILL, When it starts again
  on the same `--persist-to` dir, Then the first run's workers and dev catalog engines are gone and the new run's
  replicas serve.
- **scenario: stop-grace-from-config** — Given `runtime.process.stopGrace: 500ms`, When a worker ignores SIGTERM,
  Then `Stop` returns after SIGKILL in under 1 s; given `0s`, `-1s` or `soon`, Then funcd exits before serving and
  names the key.
- **scenario: containerd-leftover-removed-at-boot** — Given a funcd container left in a `funcd-<ns>` containerd
  namespace whose Function no longer exists, When funcd starts in containerd mode, Then the container, its snapshot
  and its CNI attachment are gone before the first reconcile.

## Scope

**In:** the saved registry of the process driver (daemon process mode and `funcdctl dev`, with and without
`--persist`); the dev catalog engine's `duckdb` processes; the identity check per OS; the reap at driver open; the
config key for the stop grace; a containerd boot sweep.

**Out:** adopting orphans (they are reaped, never re-attached); the lifeline (a worker exits by itself when funcd
dies) — deferred to a backlog card on the board, no shim change here; the containerd stop grace (stays the 10 s
constant, `containerd_linux.go:41`); orphans that serve until the next boot when funcd is never restarted.

## Constraints & Decision drivers

- Fix macOS and Linux alike, with no shim release (ADR-0141: a shim change is a release in each language repo).
- Never signal a process funcd did not start (pid reuse).
- Crash-only boot (ADR-0014 C4); delete-reclaims (ADR-0020); replicas re-created by the reconciler (ADR-0143 4.3).
- One stop grace for the normal stop and the reap; today's value as the default.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **C. Saved registry, reaped at boot** | Both OSes; no shim change; fixes temp files and delete-while-down | Identity code per OS; a crash between spawn and the registry write leaks one worker | **chosen** (decided) |
| Adopt orphans instead of reaping | No cold start after a restart | A non-child cannot be waited on: liveness polling and its own interval | rejected (decided) |
| A. Parent-death signal (`Pdeathsig`) | One line on Linux | No macOS equivalent without a shim change; fires on thread exit; no temp-file fix | rejected |
| B. Lifeline fd watched by the shim | Instant; no pid reuse | Shim releases in both language repos; still leaks temp files | deferred (backlog card) |
| A separate reap grace key | Tune reap apart from stop | Two keys for one policy | rejected (decided) |

## Decision

1. **The registry.** The process driver saves one entry per running worker (instance ID, pid, pgid, start time,
   identity token, temp-file paths) in `workers.json` in its state dir. The state dir is the `process` dir that
   `process.Open` receives (Decision 2), never the data dir itself. It writes the entry after `cmd.Start`
   returns and removes it in the same `d.mu` hold in which `wait()` records the exit; `Close` empties it. Every write replaces the whole file
   atomically (write `workers.json.tmp`, `fsync`, `rename`, `fsync` the dir) while `d.mu` is held, so concurrent
   `Create`/`Start`/`Stop` serialize on the lock that already guards the in-memory table.
2. **Locations.** Daemon: `<storage.dataDir>/process/workers.json`. `funcdctl dev --persist`:
   `<persist-to>/process/workers.json`. `funcdctl dev` without `--persist` has no data dir, so it uses
   `<os.UserCacheDir()>/funcd/dev/<first 16 hex of sha256(absolute project dir)>/process/workers.json`. A driver
   with no state dir (`process.New()`, used by tests and `realshim`) keeps today's in-memory behavior.
3. **One owner per registry.** Each registry holds an exclusive `flock` on `<state dir>/<name>.lock` for its lifetime
   (`workers.lock` for the driver, `engines.lock` for devengine), so the two registries of one `funcdctl dev` process
   never contend. Open fails with `fault.Conflict` when it is held, so one registry has exactly one owner at a
   time.
4. **Identity.** One matching rule: an entry's token must be a substring of one argv element. The driver appends
   `--funcd-instance=<instance ID>` as the last argv element of every worker it starts (the identity token), and
   records the OS start time read back right after `cmd.Start`. At open, an entry is ours only if the live pid's start
   time equals the saved one **and** its argv contains the token: Linux reads `/proc/<pid>/stat` field 22 and
   `/proc/<pid>/cmdline`; macOS reads `kern.proc.pid.<pid>` (`p_starttime`) and `kern.procargs2.<pid>` via
   `golang.org/x/sys/unix` (already in `go.mod`). Any read error, a missing pid or a mismatch means "not ours":
   nothing is signalled. A substring match stays safe because the start time must also match: a pid with its saved
   start time names exactly one process, the one the driver started with that token. The argv check only confirms
   this against a reused pid, since an instance ID is reused when a replica is re-created (ADR-0142, ADR-0143) and
   `r1` is a prefix of `r10`. The reap also never signals a pgid other than the saved one.
5. **The reap.** At open, before it returns, the driver sends SIGTERM to each owned process group, waits up to the
   stop grace for all of them together, sends SIGKILL to the ones still alive, deletes every entry's temp files
   (owned or not), and writes an empty registry. The reconciler then re-creates replicas as today (ADR-0143 4.3); a
   Function deleted while down is never re-created, so its workers stay gone (ADR-0020).
6. **Stop grace.** The constant `stopGrace` becomes `runtime.process.stopGrace` (`FUNCD_PROCESS_STOP_GRACE`), a Go
   duration string, default `3s`, valid when `0 < d ≤ 10s` (the containerd constant), else funcd exits before
   serving naming the key; `process.Open` treats a grace ≤ 0 as the 3 s default, like `New()`. Normal `Stop`,
   `Close` and the reap use it. At shutdown the runtime `Close` starts inside the 5 s `closeTimeout` window
   (`pkg/funcd/funcd.go:93`; `Shutdown` calls `closeDriver(p.cfg.runtime)` with no ctx, `:1288`) and can run beyond
   it, before the log-Route drain and the telemetry flush, so a grace above about 2 s leaves the flush less time;
   `server.shutdownTimeout` + the grace + the 5 s close must fit the unit's `TimeoutStopSec`. This ADR owns the key;
   draft ADR-0163 keeps only the containerd 10 s grace out of its scope and names this key as ADR-0167's. `funcdctl dev` uses the default.
7. **Dev catalog engine.** `devengine` starts `duckdb` in its own process group, draws a random instance ID per
   engine start (new: today the dir is `os.MkdirTemp("", "funcd-dev-catalog-*")`, `devengine.go:185`), names its
   engine dir `funcd-engine-<instance ID>-*` and records the token `funcd-engine-<instance ID>`, which the `-init <initFile>`
   argv element contains (`devengine.go:223`). It uses the same registry format in `<state dir>/engines.json`; the
   same reap runs when `devengine.New` gets a state dir. Engines that run on the process driver are covered by
   Decision 1.
8. **Containerd boot sweep.** At driver construction in containerd mode, before `funcd.New` starts any controller,
   funcd lists the containerd namespaces named `funcd-*` and, for each, discards every container in it (the existing
   `Sweep`, which has no label filter, extended to all `funcd-*` namespaces). `Sweep` calls `discard`
   (`containerd_linux.go:516`), which already kills the task, tears down the CNI attachment from the container's
   labels and deletes the container with its snapshot. The driver has no instance at that point, so every such
   container is a leftover of a previous run; none is a live worker of this run. The sweep ignores
   `funcd/owner-kind` (ADR-0152) on purpose, since every kind's container there is a leftover. One funcd daemon owns
   the `funcd-*` namespaces of its containerd.
9. **Accepted window.** A crash between `cmd.Start` and the registry write leaks one worker; it is not reaped.

## Temporary workarounds

None. (On Linux, the `funcd install` unit's `KillMode=control-group`, `install.go:54`, already kills orphans when
systemd restarts funcd; this was read in code, not run.)

## Contracts

```go
// internal/runtime/procreg (new): the saved worker registry shared by the process driver and devengine.
type Entry struct {
	ID        string   `json:"id"`
	PID       int      `json:"pid"`
	PGID      int      `json:"pgid"`
	StartTime uint64   `json:"startTime"` // OS-native: clock ticks since boot (Linux), µs since epoch (macOS)
	Token     string   `json:"token"`     // substring of one argv element: "--funcd-instance=<id>" or "funcd-engine-<id>"
	Files     []string `json:"files"`     // driver-owned temp files deleted at reap
}

type Registry struct{ /* path, lock fd, entries */ }

func Open(dir, name string) (*Registry, error)        // <dir>/<name>.json; flock <dir>/<name>.lock; fault.Conflict if held
func (r *Registry) Put(e Entry) error                 // atomic rewrite; caller holds its own mutex
func (r *Registry) Delete(id string) error
func (r *Registry) Reap(ctx context.Context, grace time.Duration) (killed int, err error)
func (r *Registry) Close() error                       // empties the file, releases the lock

// per OS, build-tagged: identity_linux.go, identity_darwin.go
func startTime(pid int) (uint64, error)
func argvContains(pid int, token string) (bool, error) // true if some argv element contains token
func Owned(e Entry) bool { st, err := startTime(e.PID); if err != nil || st != e.StartTime { return false }; ok, err := argvContains(e.PID, e.Token); return err == nil && ok }

// internal/runtime/process
func Open(ctx context.Context, stateDir string, stopGrace time.Duration) (runtime.Runtime, error) // reaps, then serves
func New() runtime.Runtime                                                                         // unchanged, in-memory, default 3s grace
```

```yaml
runtime:
  mode: process
  process:
    stopGrace: 3s # shutdownTimeout + stopGrace + 5 s close must fit TimeoutStopSec
```

| Consumes | Exposes |
|---|---|
| `storage.dataDir`, `--persist-to`, `os.UserCacheDir()`; `runtime.process.stopGrace` | `workers.json`, `workers.lock`, `engines.json`, `engines.lock` in the state dir (`<data dir>/process`); `--funcd-instance=<id>` in each worker's argv |

## Implementation plan

1. `internal/runtime/procreg` with the per-OS identity helpers; unit tests for atomic writes, the lock, and
   `Owned` against a live child, a dead pid and a reused pid (a child started without the token).
2. `process.Open`: the argv token, start-time read-back, `Put`/`Delete` under `d.mu`, the reap, `stopGrace` from
   the argument; a test proves the Node shim, the pool host and the Python shim start and serve with the
   trailing token. `main.go:712` calls `process.Open(ctx, filepath.Join(cfg.Storage.DataDir, "process"), grace)`.
3. Config: `Runtime.Process.StopGrace` in `internal/platform/config/config.go` with its `json`/`env` tags, default
   and validation; parsed with `parseDurationOr` (`cmd/funcd/main.go`, no zero allowed), then the 10 s upper bound.
4. `funcdctl dev`: `funcd.WithRuntime(process.Open(...))` after `funcd.InMemory()` with the Decision 2 dir;
   `devengine` gets the state dir, `Setpgid`, the token in the engine dir name, and the reap.
5. Containerd: a boot sweep over `funcd-*` namespaces in the runtime builder, before `funcd.New`; Linux-only test
   under the Lima lane.
6. One test per scenario, named after it; `crash-restart-leaves-desired-workers` and `dev-persist-restart-reaps`
   kill the child daemon with SIGKILL from the test. `just ci` green; the containerd lanes green.

## Review checklist

- [ ] No signal is ever sent unless start time and argv token both match; read errors count as "not ours".
- [ ] Every registry write happens under `d.mu` and is temp-write + fsync + rename.
- [ ] The lock stops a second driver on the same state dir; the driver and devengine registries use separate locks.
- [ ] Dev (both modes) and the dev catalog engine reap at start.
- [ ] The containerd sweep runs before any controller starts.
- [ ] `runtime.process.stopGrace` is the only grace key in the process driver; `New()` and the default use today's 3 s.
- [ ] One named, passing test per scenario; no shim change.

## Consequences

- Positive: a crash no longer multiplies workers; temp files and delete-while-down orphans are reclaimed on both
  OSes; ADR-0014 C4 and `blueprint.md:437` hold for process mode.
- Negative: a restart cold-starts every worker; each worker start and exit costs one fsync; worker argv gains one
  element; two `funcdctl dev` runs without `--persist` in the same project share one cache-dir registry, so the
  second now fails with `fault.Conflict` instead of starting.
- Risks accepted: the spawn-to-write window leaks one worker; orphans serve until funcd restarts (lifeline
  deferred); a worker that ignores SIGTERM holds the shutdown close for one full grace (Decision 6); the boot sweep assumes one funcd daemon per containerd instance (Decision 8).

## Open questions

None.

## References

- Issue [#44](https://github.com/pyvvo/funcd/issues/44); ADR-0011 (Decision 2), ADR-0014 C4, ADR-0020, ADR-0036,
  ADR-0054, ADR-0141, ADR-0142, ADR-0143 (4.3); drafts ADR-0163 (keys), ADR-0168 (pipes not re-attached after reap)
- Nomad `RecoverTask`; Linux `proc(5)` `stat` starttime; macOS `sysctl` `KERN_PROCARGS2`
