# ADR-0011: Function runtime port (`runtime.Runtime`: process + containerd/crun drivers)

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted 2026-06-14 after judge pass — made the **F12/P-M curated-runtime
  scope seam** explicit [Major]; added `containerd-sandbox-lifecycle` + `sandbox-exec` scenarios and
  the flat-driver-layout note [Minor] folded in. **Amended same day, pre-implementation, on decider
  direction: the OCI runtime binary is `crun`, not `runc`** — C-based, lower per-sandbox RSS,
  drop-in OCI-compatible via the `io.containerd.runc.v2` shim's `BinaryName`. Blueprint synced.
  **Implemented 2026-06-14** — review pass, see docs/reviews/adr-0011-implementation-claude-opus-4-8.md;
  port + process driver (contract suite passing) + containerd/crun Linux driver (cross-compiled) +
  non-Linux stub; `just ci` green, `GOOS=linux go build` ok; containerd integration deferred to Linux lane)
- **Superseded in part by**: [ADR-0045](0045-rename-sandbox-to-worker.md) (2026-06-16) — **naming only**: `SandboxSpec` →
  `WorkerSpec` and the "sandbox" execution-unit term → "worker" (the design here is unchanged; read this ADR for the port,
  ADR-0045 for the names; "sandbox" in this frozen text ≡ "worker").
- **Deciders**: green-0-rabbit
- **Tags**: runtime, sandbox, containerd, crun, runc, process, netns, port, data-plane
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers, one-file
  driver, `api/fault`, ctx-first, no-`any`, no globals), [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `Function`/`Revision`/`RuntimeClass` resources the controller maps into a `SandboxSpec`),
  [ADR-0006](0006-store-database-layer-port.md)/[0008](0008-bus-messaging-port.md) (sibling
  substrate-port pattern: one port, ≥2 drivers, contract-suite parity, in-memory/dev driver from the
  library or hand-written where none exists), [blueprint.md — Function runtime / Worker / Network
  manager / Security & isolation](../../blueprint.md). **cgo-free** (containerd client + go-cni are
  pure-Go); the **containerd driver is Linux-only** (`//go:build linux`) with a non-Linux stub — the
  process driver is cross-platform and is the dev/e2e/CI driver.

## Context & Need

A function ultimately **runs in a sandbox**: a process with an isolated filesystem/network that the
worker creates, starts, watches, stops, and harvests logs from. The production mechanism is
**containerd + crun** (crun is the OCI runtime binary, invoked via containerd's `io.containerd.runc.v2`
shim — a drop-in OCI-compatible replacement for the Go-based runc, chosen for **lower per-sandbox
memory**) with conservative OCI defaults (no added caps, `no_new_privileges`,
default seccomp), each sandbox on its own **network namespace** wired directly by the runtime driver
(veth/bridge, no Kubernetes CNI machinery) with **default-deny lateral traffic** — and a **plain
process** driver for dev/e2e (the `InMemory()`/laptop path, no root, no containerd). Nothing in the
data plane can place or run a function until this port exists: the function-lifecycle ADR (P-M, which
adds the in-sandbox **shim**, CloudEvents contract, and artifact loading), the activator/scale-to-zero
(P-H2), and eventing (P-Q) are all built *on* it. funcd only fully *runs* on Linux as root (containerd
+ netns); pure-Go development and the e2e harness use the process driver anywhere.

**Purpose**: define and implement the **`runtime.Runtime` port** — create / start / stop / status /
logs / exec / list a **sandbox instance** (one replica of a function) over a typed `SandboxSpec` — with
**two drivers**: a cross-platform **process** driver (runs a command as a supervised OS child; the
dev/e2e/CI driver) and a Linux-only **containerd/crun** driver (pulls a curated runtime image, runs it
as a crun OCI container with conservative defaults and a default-deny netns). Callers: the worker /
controller (P-J/P-M) that places functions; the activator (P-H2) that wakes them. The port is
**internal-plane** (functions never call it). Conformance is mechanical: the **same `runtimecontract`
suite passes against the process driver** (and the containerd driver on the Linux integration lane);
a created sandbox starts, reports a PID and `Running`, captures its logs, stops gracefully
(SIGTERM→SIGKILL) and idempotently, and an unknown instance is a typed `NotFound`.

**F12 is realized in two parts — this ADR owns the *mechanism*, not the *workload*.** P-G/F12 delivers
the **runtime port + process/containerd drivers that *run* a runtime image** + the **netns lateral-deny**.
The **curated nodejs/python runtime *images* themselves — with the embedded platform shim, the CloudEvents
contract, artifact loading, and the function shape** — are co-realized in **P-M/F13** (they are one
cohesive thing with the shim, so they build there). To this port a curated runtime is just a
`SandboxSpec.Image` it pulls and runs. The feat **F12 row therefore tracks the port/driver mechanism on
this ADR; its "curated runtimes + shim" sub-clause completes with F13** — F12 is *not* read as fully done
on ADR-0011 alone.

## Scenarios

- `scenario: sandbox-create-start-running` — **Given** a `SandboxSpec` whose command runs briefly,
  **when** `Create` then `Start` are called, **then** `Status` reports `Running` with a non-zero PID
  (and after the command exits, `Status` reports `Stopped`).
- `scenario: sandbox-logs-captured` — **Given** a sandbox whose command writes to stdout, **when** it
  has run, **then** `Logs` returns a reader over that output.
- `scenario: sandbox-stop-idempotent` — **Given** a started long-running sandbox, **when** `Stop` is
  called, **then** the process is terminated promptly (SIGTERM, then SIGKILL) and `Status` reports
  `Stopped`; **and** a second `Stop` returns no error (idempotent).
- `scenario: instance-not-found` — **Given** an unknown `InstanceID`, **when** `Status` or `Stop` is
  called, **then** it returns a typed `fault.NotFound`.
- `scenario: sandbox-exec` — **Given** a running sandbox, **when** `Exec` runs a command, **then** a
  successful command returns nil and a failing one returns an error; `Exec` on an unknown instance
  returns `fault.NotFound`. (Best-effort in V1 — for the process driver the command runs as a child;
  the containerd driver execs it inside the container's namespaces.)
- `scenario: driver-conformance-parity` — **Given** the `runtimecontract` suite, **when** it runs
  against the **process** driver, **then** every assertion passes (and against the **containerd**
  driver on the Linux integration lane — `FUNCD_IT=1`, GOOS=linux, root).
- `scenario: containerd-requires-linux` — **Given** a non-Linux build, **when** `containerd.New` is
  called, **then** it returns a typed `fault.Unavailable` naming the Linux requirement (the stub),
  so the process driver is unambiguously the cross-platform path.
- `scenario: containerd-sandbox-lifecycle` *(Linux integration lane, deferred — see Test sequencing)* —
  **Given** the containerd driver on Linux, **when** a sandbox is pulled, created, started, observed,
  and removed, **then** it satisfies the same `runtimecontract` assertions as the process driver
  (create→running→logs→stop→gone) against real crun containers.
- `scenario: sandbox-lateral-deny` *(Linux integration lane, deferred — see Test sequencing)* —
  **Given** two containerd sandboxes on their own netns, **when** one tries to open a TCP connection
  to the other directly, **then** it is refused (default-deny lateral); function→function traffic is
  only via the gateway.

## Scope

**In**:
- The **`runtime.Runtime` port** in `internal/runtime` (`runtime.go`): `Create`/`Start`/`Stop`/
  `Status`/`Logs`/`Exec`/`List`/`Close` over a typed `SandboxSpec`/`Instance`; typed `InstanceID` and
  `State` enum; `api/fault`; ctx-first; driver-dep-free (imports no containerd).
- The **process driver** (`internal/runtime/process`): runs `SandboxSpec.Command` as a supervised OS
  child process, captures stdout+stderr to `LogPath`, tracks PID/state, stops gracefully. The
  cross-platform **dev/e2e/CI** driver (the "in-memory" equivalent for this port — there is no library
  to reuse, so it is hand-written, like the store's memory engine).
- The **containerd/crun driver** (`internal/runtime/containerd`, `//go:build linux` + a non-Linux
  stub): `New(Config)` over the containerd v2 client; `Create` pulls the runtime image and makes a
  **crun** OCI container (via the `runc.v2` shim's `BinaryName`) with **conservative OCI** (`WithImageConfig`, `WithEnv`, `WithNoNewPrivileges`,
  optional memory/CPU limits, **no added caps**); `Start`/`Stop` (SIGTERM→SIGKILL)/`Status`/`Logs`
  (`cio.LogFile`)/`List` (by `funcd/*` labels); each sandbox attached to its **own netns** via go-cni
  with **default-deny lateral** (the CNI bridge+firewall). One driver subpackage.
- The shared **`runtimecontract` suite** run against the process driver (and containerd on Linux).

**Out**:
- **The in-sandbox runtime shim, the CloudEvents handler contract, function *shape*, artifact loading,
  and health probes** — that is **P-M / F13** (the function-lifecycle ADR). This port runs a *command*
  / *image*; it does not know what CloudEvents is. The `SandboxSpec` is the handoff.
- **Scale-to-zero / activator** (buffer + wake) — **P-H2 / F11**; it *drives* this port.
- **The transparent egress gateway, nftables L7 redirect/TPROXY, SNI peek, DNS-aware policy, and
  `EgressPolicy` enforcement** — **V2 egress ADR**. V1 ships only **L3/L4 default-deny lateral** at the
  netns boundary (per the feat doc); full outbound egress control is explicitly deferred.
- **WASM and microVM (Kata) runtime classes** — V2/V3 behind this same port (the `external` driver and
  new driver subpackages); V1 is process + containerd/crun.
- **The worker API / supervisor / multi-node placement** — the worker *calls* this port; its
  control-plane/sandbox-local API and containerd-supervision are separate (P-J/P-L/worker work).
- **Daemon config loading** — the composition root (P-I) supplies a populated `containerd.Config`.

## Constraints & Decision drivers

- **C1 — blueprint mechanism**: containerd + crun (OCI runtime), conservative OCI, per-sandbox netns with
  default-deny lateral, done *directly* by the runtime driver (no k8s CNI controller); a plain-process
  driver for dev/e2e. Both pure-Go (containerd client, go-cni) — **no cgo**.
- **C2 — ADR-0002 port shape**: port in its own package (driver-dep-free), each driver in its own
  subpackage (containerd imports stay out of the port and off non-Linux builds), one-file driver,
  `api/fault`, ctx-first, no globals, no `any`, typed enums/IDs.
- **C3 — Linux-only production, cross-platform dev**: the containerd driver is `//go:build linux`
  with a non-Linux **stub** (`New` → `fault.Unavailable`), so `go build ./...` and the e2e harness work
  on macOS/CI while the real driver compiles for Linux. (The same split the legacy `internal/runtime`
  used; the store's slatedb cgo lane is the sibling precedent.)
- **C4 — contract-suite parity, in-memory/dev driver**: one `runtimecontract` proves both drivers obey
  the same observable contract; the process driver is the always-available dev/test driver (the store
  is the only port whose dev driver is a special engine — here it is a hand-written process supervisor,
  since no library *is* a sandbox runtime).
- **C5 — conservative isolation (V1–V2)**: no added capabilities, `no_new_privileges`, default seccomp;
  lateral default-deny. Strong isolation (Kata microVM) is V3; WASM is the untrusted-code path — both
  later, behind this port. **Default-deny, never default-allow** (blueprint security model).

## Alternatives considered

**Production sandbox mechanism** (driver: blueprint + embed-first + single-node):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **containerd + runc, netns wired directly** | the blueprint's pick; containerd client is pure-Go and embeddable; runc is the OCI default; conservative defaults are well-trodden; Kata/WASM slot in behind the same port later | containerd is a **supervised child process** (no embeddable form) + Linux/root only | **chosen** (blueprint-aligned; the one sanctioned supervised child besides OpenBAO) |
| Docker/Podman API | familiar | a heavier daemon; Docker not embeddable; duplicates what containerd already gives runc | rejected (heavier, not the blueprint mechanism) |
| Direct `runc`/`libcontainer` calls, no containerd | one fewer daemon | re-implements image pull, snapshots, task supervision that containerd already provides | rejected (reinvents containerd) |

**Dev/e2e driver** (driver: the no-root, cross-platform, library-embeddable test path):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Plain OS process supervisor** | no root, no containerd, runs on macOS/CI; exercises the full port contract; the `InMemory()`/e2e path | not a real isolation boundary (acceptable — it is dev/test only) | **chosen** |
| A second in-memory fake with no real process | trivial | proves nothing about start/stop/logs/signals; diverges from real behavior (no-mocks rule) | rejected (a real child process is the honest fake) |

**Linux-only handling** (driver: keep the tree building everywhere): **`//go:build linux` on the
containerd driver + a non-Linux stub returning `fault.Unavailable`** — chosen (matches the legacy
runtime split and the slatedb cgo-lane precedent); a single file with runtime `GOOS` checks is rejected
(containerd imports would still have to compile on macOS, which they will not).

**netns lateral-deny mechanism** (V1): **go-cni bridge + firewall plugin** for per-sandbox netns with
default-deny lateral — chosen (the blueprint's "veth/bridge directly, no k8s CNI controller"; go-cni is
pure-Go, the reference plugins do the L3/L4 rules). The L7 transparent egress gateway is **V2**.

**OCI runtime binary** (driver: per-sandbox memory footprint at single-node density):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **crun** (C) via the `io.containerd.runc.v2` shim | **lower RSS + faster start** than Go runc — matters when many sandboxes share one box (scale-to-zero density); fully OCI-compatible, a drop-in `BinaryName` swap; same conservative defaults | a C binary the host must provide (packaged with funcd) | **chosen** (decider direction — memory efficiency) |
| runc (Go) | the containerd/OCI default; ubiquitous | higher per-process memory than crun; no functional advantage here | rejected (heavier per sandbox for no gain) |

Either runtime is selectable later via the same `BinaryName` seam, so this is reversible at config level.

## Decision

### 1. The `runtime.Runtime` port — sandbox lifecycle over a typed spec (driver-independent)
`internal/runtime` exposes `Runtime`: `Create`/`Start`/`Stop`/`Status`/`Logs`/`Exec`/`List`/`Close`
over a typed `SandboxSpec` (namespace/name/replica, runtime `Image`, process `Command`, `Env`,
`Limits`, `LogPath`) returning a typed `Instance` (ID, PID, `State`, IP, timestamps). `InstanceID` and
`State` are typed; errors are `api/fault`; every blocking method is ctx-first. The port imports no
containerd/cni library — those live only in the `containerd` subpackage. Drivers are **flat
subpackages of the port** (`internal/runtime/process`, `internal/runtime/containerd`) — following the
established store/bus/blob precedent (ADR-0006/0008: `internal/store/memory`, `internal/bus/nats`),
which refines the blueprint's older `internal/runtime/providers/{…}` sketch; do not re-nest under
`providers/`. The controller (P-J/P-M) maps
`Function`+`Revision`+`RuntimeClass` (ADR-0003) into a `SandboxSpec`; this port does not read resources.

### 2. Process driver — supervised OS child (dev/e2e/CI, cross-platform)
`internal/runtime/process.New()` runs `SandboxSpec.Command` via `exec.Command`, wiring stdout+stderr to
`LogPath`, recording the PID, and tracking `State` (a wait goroutine flips `Running`→`Stopped`/`Failed`
on exit). `Stop` sends SIGTERM, waits a bounded grace, then SIGKILL — idempotent (a stopped/absent
instance is not an error). `Status`/`List` read an in-memory, mutex-guarded registry; an unknown id is
`fault.NotFound`. No root, no containerd — the driver the `InMemory()` harness and unit tests use.

### 3. containerd/crun driver — Linux-only, conservative OCI, default-deny netns
`internal/runtime/containerd` (`//go:build linux`) implements the port over the containerd v2 client.
The container is created with `WithRuntime("io.containerd.runc.v2", &runcoptions.Options{BinaryName:
"crun"})` — **crun is the OCI runtime** (C-based, lower RSS than Go runc; drop-in OCI-compatible).
`Create` pulls the image (`WithPullUnpack`, configured snapshotter), makes a container with **conservative
OCI** (`WithImageConfig`, `WithEnv` incl. `FUNCD_*`, `WithHostname`, `WithNoNewPrivileges`, optional
`WithMemoryLimit`/`WithCPUs`, **no added caps**) labelled `funcd/namespace|name|replica`, and a task with
`cio.LogFile(LogPath)`; attaches the task's netns via **go-cni** (bridge + firewall = default-deny
lateral). `Start`/`Stop` (task `Kill` SIGTERM→wait→SIGKILL→`Delete`, idempotent)/`Status`/`Logs`/`List`
(by labels). Every call is namespaced `funcd-<ns>`. A **non-Linux stub** (`//go:build !linux`) provides
the same `New(Config)` returning `fault.Unavailable`. The driver is integration-tested on the Linux lane.

### 4. Errors, isolation posture, lifecycle
containerd/cni errors map to `api/fault` (image/container not found → `NotFound`; daemon
unreachable/timeout → `Unavailable`; else `Internal`). Isolation is **conservative crun** (no caps,
`no_new_privileges`, default seccomp) with **L3/L4 default-deny lateral** — never default-allow. `Close`
releases the client/cni and leaks no goroutine. Strong isolation (Kata) and L7 egress are later, behind
this port / the egress ADR.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **containerd driver integration tests deferred** to the Linux lane (`FUNCD_IT=1`, linux, root) | containerd + netns need root + Linux + a running daemon — untestable in the pure-Go CI lane (cf. the slatedb cgo lane) | **P-S / F20** stands up the Linux-VM lane; the deferred `containerd-sandbox-lifecycle` + `sandbox-lateral-deny` scenarios run there |
| **V1 ships L3/L4 lateral default-deny only**, not the L7 egress gateway | full egress capture/`EgressPolicy` is a large, separable concern | a **V2 egress ADR** adds the transparent gateway (TPROXY/SNI/DNS) and `EgressPolicy` enforcement |
| **Process driver is not an isolation boundary** | it is the dev/e2e/CI path, not production | production uses the containerd driver on Linux; the process driver is never selected for untrusted workloads |
| **`Exec` is best-effort in V1** (debug/health use) | the primary lifecycle is Create/Start/Stop; Exec is a convenience | revisit if a consumer needs richer exec semantics (streaming, TTY) |

## Contracts

### The port (`internal/runtime/runtime.go`)
```go
package runtime

import (
	"context"
	"io"
	"time"

	"github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// InstanceID identifies one sandbox instance (a function replica).
type InstanceID string

// State is the lifecycle state of a sandbox instance.
type State string

const (
	StateCreated State = "created"
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateFailed  State = "failed"
)

// Limits bounds a sandbox's resources (0 = unlimited).
type Limits struct {
	MemoryBytes int64
	CPUs        float64
}

// SandboxSpec is the driver-independent request to run one function replica. The
// controller assembles it from Function/Revision/RuntimeClass (ADR-0003).
type SandboxSpec struct {
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Replica   int
	Image     string            // runtime image (containerd driver)
	Command   []string          // launch command (process driver)
	Env       map[string]string // typed-flat; no any
	Limits    Limits
	LogPath   string // file for captured stdout+stderr
}

// Instance is the observed state of one sandbox.
type Instance struct {
	ID        InstanceID
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Replica   int
	PID       int
	State     State
	IP        string // sandbox IP (containerd/netns); empty for the process driver
	CreatedAt time.Time
}

// Runtime is the sandbox port: create/start/stop/observe one function replica.
// Errors are api/fault; every method is ctx-first; the port imports no container lib.
type Runtime interface {
	Create(ctx context.Context, spec SandboxSpec) (Instance, error)
	Start(ctx context.Context, id InstanceID) error
	Stop(ctx context.Context, id InstanceID) error // SIGTERM→SIGKILL; idempotent
	Status(ctx context.Context, id InstanceID) (Instance, error) // fault.NotFound if unknown
	Logs(ctx context.Context, id InstanceID) (io.ReadCloser, error)
	Exec(ctx context.Context, id InstanceID, cmd []string) error // best-effort (V1)
	List(ctx context.Context, ns v1alpha1.NamespaceName) ([]Instance, error)
	Close() error
}

// NewInstanceID builds the canonical id "<ns>/<name>/r<replica>".
func NewInstanceID(ns v1alpha1.NamespaceName, name v1alpha1.ObjectName, replica int) InstanceID
```

### The process driver (`internal/runtime/process/process.go`)
```go
func New() runtime.Runtime // cross-platform; dev/e2e/CI driver
```

### The containerd driver (`internal/runtime/containerd/containerd_linux.go` + `_other.go` stub)
```go
// Config configures the containerd driver (composition root supplies it).
type Config struct {
	Socket      string // /run/containerd/containerd.sock
	Snapshotter string // overlayfs
	CNIBinDir   string // /opt/cni/bin
	CNIConfDir  string // funcd-written conflist dir
	SubnetCIDR  string // lateral bridge subnet
}

// New returns the containerd-backed Runtime on Linux; on other platforms it
// returns fault.Unavailable (the build-tagged stub).
func New(cfg Config) (runtime.Runtime, error)
```

### The contract suite (`internal/runtime/runtimecontract/contract.go`)
```go
func RunContract(t *testing.T, newRuntime func(t *testing.T) runtime.Runtime) // process (CI) + containerd (Linux)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault`, `api/types/v1alpha1` (ADR-0002/0003), stdlib `os/exec`/`os`/`io`/`syscall` | typed spec; fault errors |
| Adds (lib) | `github.com/containerd/containerd/v2` (client/oci/cio/namespaces), `github.com/containerd/go-cni` | **Apache-2.0**, pure-Go; **Linux-build-tagged only** — not compiled on macOS/CI |
| Exposes | `runtime.Runtime` + process driver + containerd driver + `runtimecontract` | consumed by P-M (function lifecycle), P-H2 (activator), the worker — **never functions** |

## Implementation plan

No business logic beyond supervising processes / adapting containerd to the port.

1. **`internal/runtime/runtime.go`** — `Runtime`, `SandboxSpec`, `Instance`, `InstanceID`(+`NewInstanceID`),
   `State`, `Limits` (driver-dep-free).
2. **`internal/runtime/process/process.go`** — `New()`: `exec.Command` supervision, stdout/stderr →
   `LogPath`, PID/state registry (mutex), wait goroutine, graceful idempotent `Stop`, `Status`/`Logs`/
   `List`/`Exec`/`Close`. Cross-platform.
3. **`internal/runtime/containerd/containerd_linux.go`** (`//go:build linux`) — the containerd v2 +
   go-cni implementation (§3). **`containerd_other.go`** (`//go:build !linux`) — `New` → `fault.Unavailable`.
4. **`internal/runtime/runtimecontract/contract.go`** — `RunContract` with the real lifecycle assertions.
5. **Deps** — `go get github.com/containerd/containerd/v2 github.com/containerd/go-cni`; `go mod tidy`.
   No cgo. Verify the Linux driver **cross-compiles**: `GOOS=linux go build ./...`.
6. **Test plan** (one named test per Scenario; CI vs Linux-lane split):
   - `internal/runtime/process/process_test.go` → `RunContract` (drives `sandbox-create-start-running`,
     `sandbox-logs-captured`, `sandbox-stop-idempotent`, `instance-not-found`, `sandbox-exec`,
     `driver-conformance-parity`) — **passing in `just ci`** on any OS.
   - `internal/runtime/containerd/containerd_other_test.go` → `containerd-requires-linux`
     (`New` → `fault.Unavailable` on the CI host) — **passing in `just ci`**.
   - `internal/runtime/containerd/containerd_linux_test.go` (`//go:build linux && integration`) →
     `containerd-sandbox-lifecycle` + `sandbox-lateral-deny`, **deferred** to the Linux lane
     (`FUNCD_IT=1`, root) — recorded, not run in `just ci` (Test sequencing).
7. **Definition of done** (= Scenarios executed):
   - `just ci` green on the CI host: build, lint (no `any`/globals/non-`slog`), test, mod verify.
   - `runtimecontract` passes against the **process** driver; `Stop` is graceful + idempotent; unknown
     id → `fault.NotFound`; logs captured; `containerd.New` → `fault.Unavailable` off Linux.
   - `GOOS=linux go build ./...` succeeds (the containerd driver compiles for Linux).
   - Only `containerd/v2` + `go-cni` added (Apache-2.0, Linux-tagged); `go.mod`/`go.sum` tidy; no cgo;
     the containerd integration scenarios are recorded as deferred (sequencing, not a gap).

## Review checklist

- [ ] `internal/runtime/runtime.go` defines `Runtime` (+ `SandboxSpec`/`Instance`/`InstanceID`/`State`/
      `Limits`) and imports **no** containerd/cni library.
- [ ] The **process driver** supervises a real child: PID + `Running`, stdout/stderr → `Logs`, graceful
      `Stop` (SIGTERM→SIGKILL) that is **idempotent**, unknown id → `fault.NotFound`; cross-platform.
- [ ] The **containerd driver** is `//go:build linux`, one driver subpackage, uses **conservative OCI**
      (no added caps, `no_new_privileges`, optional limits), `cio.LogFile` logs, `funcd/*` labels, and
      per-sandbox **netns with default-deny lateral** (go-cni); errors mapped to `api/fault`.
- [ ] A **non-Linux stub** provides `New` → `fault.Unavailable`; `GOOS=linux go build ./...` compiles
      the real driver; `just ci` builds the stub.
- [ ] `runtimecontract` passes against the process driver in `just ci`; the containerd integration test
      is build-tagged (`linux && integration`) and **deferred** (recorded, not silently dropped).
- [ ] Errors `api/fault`; ctx-first; no `any` in signatures; no globals; `slog` only; no cgo.
- [ ] Only `containerd/v2` + `go-cni` (Apache-2.0) added; `go.mod`/`go.sum` tidy; no identity/path leak;
      the port stays container-lib-free; the port is never exposed to functions.

## Consequences

- (+) The data plane gets its **sandbox runtime port**: a real, tested process driver for dev/e2e/CI on
  any OS, and a Linux containerd/crun driver with conservative isolation + default-deny lateral for
  production — the blueprint's runtime, behind one swappable port.
- (+) **P-M (function lifecycle), P-H2 (activator), P-Q (eventing) are unblocked** — they target the
  port; the `InMemory()` harness uses the process driver (no root/containerd).
- (+) Kata microVM (V3) and WASM (V2) slot in as new drivers behind the same port — the
  embed-now/isolate-harder-later seam.
- (−) The **containerd driver is Linux-root-only and integration-tested off the CI lane** — its
  scenarios are proven on the Linux VM lane (P-S), not in `just ci` (the slatedb-lane trade-off).
- (−) The process driver is **not an isolation boundary** — acceptable, it is dev/e2e only; production
  never selects it for untrusted code.
- (−) Adding the **containerd dep tree** is real weight (Linux-tagged only, pure-Go) — accepted: it is
  the blueprint's mandated runtime.
- (risk) Blind Linux code (untestable in CI) — mitigated by `GOOS=linux go build`, grounding the driver
  in the legacy `internal/runtime` reference, and the deferred Linux integration lane.

## Open questions

| Question | Where it gets answered |
|---|---|
| The in-sandbox **shim**, CloudEvents contract, function shape, artifact loading, health probes | **P-M / F13** (function lifecycle) — it builds on this port |
| Full **egress** (L7 transparent gateway, TPROXY/SNI/DNS, `EgressPolicy`) | a **V2 egress ADR**; V1 is lateral default-deny only |
| **WASM** and **Kata microVM** runtime classes | V2 (wasm) / V3 (kata) — new drivers behind this port |
| Worker API / supervisor (containerd child-process lifecycle) / multi-node placement | the worker/control-plane ADRs (P-J/P-L) |
| Richer `Exec` (streaming/TTY), pre-warmed pools / snapshot cold-start | follow-ups if a consumer needs them |

## References

- [containerd/containerd v2](https://github.com/containerd/containerd) (Apache-2.0) — pure-Go client
  (`client.New`, `namespaces`, `oci`, `cio`, image pull, task lifecycle), run as a supervised daemon.
- [containerd/go-cni](https://github.com/containerd/go-cni) (Apache-2.0) — per-sandbox netns via the CNI
  reference plugins (bridge + firewall = default-deny lateral).
- [crun](https://github.com/containers/crun) (GPL-2.0, a separate host binary invoked by containerd — not linked) / OCI runtime spec — conservative defaults (`no_new_privileges`, default seccomp, no added caps).
- [ADR-0006](0006-store-database-layer-port.md)/[0008](0008-bus-messaging-port.md) — sibling
  substrate-port pattern (one port, ≥2 drivers, contract-suite parity, special/Linux-gated driver).
- [blueprint.md](../../blueprint.md) — "Function runtime", "Worker", "Network manager", "Security &
  isolation", and the conservative-OCI / lateral-default-deny / curated-runtime decisions (crun per this ADR).
