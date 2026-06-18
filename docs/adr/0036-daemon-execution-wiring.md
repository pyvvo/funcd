# ADR-0036: Wire function execution into the standalone `funcd` daemon

- **Status**: Implemented
- **Date**: 2026-06-16 (**Implemented 2026-06-16** — review pass: the standalone binary executes functions (embedded shim extracted + WithRuntimeShim, node-gated TestDaemonExecutesFunction green), containerd mode fails fast off Linux, node-absent degrades; Major (containerd.Config env) + Minors folded; no new dependency. **Accepted 2026-06-16** after judge pass — no Blockers. Folded the **Major** (container
  mode under-specified `containerd.Config` → added `FUNCD_CONTAINERD_SOCKET`/`FUNCD_SNAPSHOTTER`/`FUNCD_CNI_*`/
  `FUNCD_SUBNET_CIDR` env + defaults) + Minors (the image prefix is an operator convention matching ADR-0032;
  `os.MkdirAll` the data dir before extracting the shim; the artifact store is wired in both modes). The judge
  confirmed the process-mode wiring is already proven by `docs/demo/server` and that `WithRuntimeShim +
  WithArtifactStore` selects the oras materializer + ADR-0035 resolver. No new dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: daemon, cmd-funcd, execution, shim, embed, packaging, F12
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — making the **standalone binary**
  execute functions, not just the embedded path; co-realized with ADR-0030/0032)
- **Relates to / refines**: [ADR-0028](0028-platform-control-plane-wiring.md) (extends `cmd/funcd`'s wiring with
  execution) · [ADR-0030](0030-function-execution-runtime-shim-node.md) (the shim it launches) ·
  [ADR-0031](0031-oci-artifact-distribution-oras.md)/[ADR-0035](0035-artifact-digest-resolution-at-revision.md)
  (the artifact store it configures) · [ADR-0032](0032-curated-runtime-images-container-execution.md) (container
  mode) · [ADR-0026](0026-packaging-and-release.md) (the embedded shim ships in the single binary). **Answers
  ADR-0034's open question.**

## Context & Need

The platform can execute functions (ADR-0030/0032), but **`cmd/funcd` never wires it** — it builds
`funcd.New(Production(), WithRuntime(process.New()), …)` with no `WithRuntimeShim` / `WithContainerExecution`
/ `WithArtifactStore`. So the **standalone `funcd` binary runs the legacy placeholder** and cannot run a
function; only the *embedded* platform (the e2e harness, ADR-0034) executes. This ADR wires execution into the
daemon so the single binary actually works, and resolves ADR-0034's "where does the shim live for a shipped
binary?" — by **embedding the Node shim into the binary**.

## Scenarios

- **scenario: daemon-executes-process** — *given* the standalone `funcd` binary with `node` on PATH, *when* a
  user deploys a function and POSTs its data-plane route, *then* the handler runs and returns its response
  (the embedded-vs-daemon gap is closed).
- **scenario: shim-is-self-contained** — *given* the shipped binary (no repo checkout), *then* it executes
  functions with **no external `shim.mjs` file** — the shim is embedded (`go:embed`) and extracted to the data
  dir on boot.
- **scenario: container-mode-selected** — *given* `FUNCD_RUNTIME=containerd` on Linux, *then* the daemon wires
  the containerd driver + `WithContainerExecution` (curated images, ADR-0032) instead of the process shim.
- **scenario: node-absent-degrades** — *given* no `node` and process mode, *then* the daemon still boots and
  serves the control plane, logging a clear warning that functions won't execute (no crash).

## Scope

- **In**: wire execution in `cmd/funcd`, mode-selected by env — **process mode** (default, cross-platform:
  embedded Node shim + `WithRuntimeShim` + `WithArtifactStore`) and **containerd mode** (`FUNCD_RUNTIME=
  containerd`, Linux: the containerd driver + `WithContainerExecution` + `WithArtifactStore`); **embed the Node
  shim** into the binary (`go:embed`) + extract on boot; a default artifact-store dir under `FUNCD_DATA_DIR`;
  an image-prefix → curated-image mapping for container mode.
- **Out**: building/publishing the curated images (operator path, ADR-0032); the slatedb store wiring
  (ADR-0026 build tag); the Python shim (P-V-3); a *new* e2e that drives the real binary (a later extension —
  ADR-0034's embedded journey still holds).

## Constraints & Decision drivers

- **Single self-contained binary** (ADR-0026) — the shim must ship *inside* the binary, not as a sidecar file;
  `go:embed` + extract-on-boot.
- **Cross-platform default** — process mode works on any OS with `node`; containerd mode is the Linux opt-in.
- **Compose, don't reinvent** — it only wires the existing `pkg/funcd` options; `cmd/funcd` stays a thin shell
  (ADR-0014), no business logic.
- **Degrade, don't crash** — missing `node` (process) → warn + control-plane-only; non-Linux containerd → fail
  fast with a clear message.
- **No new dependency** — `go:embed` is stdlib; the runtime drivers already exist.

## Decision

`cmd/funcd` selects an execution mode from `FUNCD_RUNTIME` (default `process`) and wires the matching options:

1. **Embed the shim.** A new `shim/nodejs` Go package embeds `shim.mjs` (`//go:embed`). `cmd/funcd` writes it to
   `$FUNCD_DATA_DIR/shim.mjs` on boot — the binary needs no external shim file.
2. **Process mode (default).** Resolve `node` (`FUNCD_NODE` or PATH). If found:
   `WithRuntimeShim(node, <extracted shim>)` + `WithArtifactStore($FUNCD_DATA_DIR/artifacts)` (→ the oras
   materializer + resolver, ADR-0031/0035). If `node` is absent: log a warning and wire neither (control plane
   only — functions reconcile to the legacy placeholder, as today).
3. **Containerd mode (`FUNCD_RUNTIME=containerd`).** Construct `containerd.New(Config{...})` from env (defaults
   below); on a non-Linux build it errors → exit with a clear message. Wire `WithRuntime(containerd)` +
   `WithContainerExecution(imageFor)` + `WithArtifactStore(...)` (the artifact store is needed in *both* modes —
   a container sandbox still pulls the artifact). `imageFor(rt) = <prefix><rt>:latest` (`FUNCD_IMAGE_PREFIX`,
   default `funcd/runtime-`) — e.g. `nodejs20 → funcd/runtime-nodejs20:latest`. The prefix is a **convention the
   operator's `just build-runtime-images` must tag to match** (ADR-0032); V1 ships only `nodejs20`.
4. Control-plane + data-plane listeners are already wired by `Production()` (ADR-0028/0033) — unchanged.

## Contracts

`cmd/funcd` is the composition shell (ADR-0014/0028) — its "contract" is the wiring + env, not a Go API:

```go
// shim/nodejs/embed.go  (new)
package nodejs
import _ "embed"
//go:embed shim.mjs
var Shim []byte   // the runtime shim, embedded into every funcd binary

// cmd/funcd: extract Shim → $FUNCD_DATA_DIR/shim.mjs, then by FUNCD_RUNTIME:
//   process    → funcd.WithRuntimeShim(node, shimPath) + funcd.WithArtifactStore(dir)   [default]
//   containerd → funcd.WithRuntime(cd) + funcd.WithContainerExecution(imageFor) + funcd.WithArtifactStore(dir)
```

### Config (env)
| Var | Meaning | Default |
|---|---|---|
| `FUNCD_RUNTIME` | `process` \| `containerd` | `process` |
| `FUNCD_DATA_DIR` | data dir (shim + artifacts + blob/nats) | `/var/lib/funcd` |
| `FUNCD_NODE` | node binary path (process mode) | PATH lookup |
| `FUNCD_IMAGE_PREFIX` | curated-image prefix (containerd mode) | `funcd/runtime-` |
| `FUNCD_CONTAINERD_SOCKET` | containerd socket (`containerd.Config.Socket`) | `/run/containerd/containerd.sock` |
| `FUNCD_SNAPSHOTTER` | snapshotter | `overlayfs` |
| `FUNCD_CNI_BIN_DIR` · `FUNCD_CNI_CONF_DIR` | go-cni bin / conf dirs | `/opt/cni/bin` · `$FUNCD_DATA_DIR/cni` |
| `FUNCD_SUBNET_CIDR` | lateral bridge subnet | `10.63.0.0/16` |

### Dependencies & I/O
| | Item |
|---|---|
| Adds (lib) | none (`go:embed` is stdlib) |
| Consumes | `pkg/funcd` options, the `shim/nodejs` embed, `internal/runtime/{process,containerd}` |
| External | `node` on PATH (process mode) / containerd + curated images (containerd mode) |

## Implementation plan

1. **`shim/nodejs/embed.go`** — `//go:embed shim.mjs` → `var Shim []byte`.
2. **`cmd/funcd/main.go`** — `os.MkdirAll($FUNCD_DATA_DIR, 0o700)`, extract the shim to
   `$FUNCD_DATA_DIR/shim.mjs`; switch on `FUNCD_RUNTIME`: process (node lookup → `WithRuntimeShim` +
   `WithArtifactStore`, else warn) / containerd (`containerd.New(Config from env)` → `WithRuntime` +
   `WithContainerExecution` + `WithArtifactStore`, error off-Linux). Keep it a thin shell.
3. **Tests** — `cmd/funcd` unit: the shim embed is non-empty and parses as the contract; an `imageFor` mapping
   helper test (`nodejs20 → funcd/runtime-nodejs20:latest`); a node-gated test that the assembled
   process-mode `Platform` runs a function end to end (reuse the embedded-platform pattern, but built from the
   `cmd/funcd` wiring helper so the daemon path is covered).
4. **DoD** — `just ci` green; the standalone binary (process mode, node present) deploys + invokes a function;
   the shim is embedded (no external file); containerd mode is selected by env (Linux); no new dependency; no leak.

## Review checklist

- [ ] **Daemon executes** (`daemon-executes-process`): process-mode wiring runs a real function (node-gated).
- [ ] **Self-contained** (`shim-is-self-contained`): the shim is `go:embed`-ed + extracted; no external file.
- [ ] **Container mode** (`container-mode-selected`): `FUNCD_RUNTIME=containerd` wires the containerd driver +
      `WithContainerExecution`; off-Linux it fails fast.
- [ ] **Degrades** (`node-absent-degrades`): no node → control plane serves + a clear warning; no crash.
      `cmd/funcd` stays a thin shell; no new dependency; no leak.

## Consequences

- (+) **The single binary works** — `funcd` + `funcdcli` is a complete, self-contained deploy-and-invoke
      experience; ADR-0034's journey can now (optionally) target the real binary.
- (+) **Self-contained shim** — `go:embed` means no sidecar file to ship/lose; the binary is the unit (ADR-0026).
- (−) **process mode needs `node`** on the host — acceptable for a dev/single-box runtime; containerd mode
      (curated images carry the shim) is the no-host-node prod path.
- (note) **slatedb store + curated-image build/publish stay separate** (ADR-0026/0032) — this only wires
      execution, not persistence or image production.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| process mode requires `node` on PATH | the V1 reference shim is Node (ADR-0030) | containerd mode (curated image carries node+shim); a future self-contained WASM runtime |

## Alternatives considered

- **Ship `shim.mjs` as a sidecar file next to the binary** — rejected: breaks the single-binary promise
  (ADR-0026); `go:embed` makes the binary self-contained.
- **Leave the daemon placeholder-only; embed-only execution** — rejected: that is the gap; a standalone FaaS
  binary that can't run functions isn't the product.
- **Always wire containerd in Production()** — rejected: containerd is Linux + root + curated images; process
  mode is the portable default, containerd the explicit opt-in.

## Open questions

| Question | Answered in |
|---|---|
| A self-contained runtime needing no host `node` (embed a JS engine / WASM)? | V2/V3 (runtime classes) |
| Auto-build/pull the curated images for containerd mode at first run? | an ADR-0032 successor (image publish pipeline) |

## References

- [ADR-0034](0034-end-user-journey-acceptance-e2e.md) — whose open question this answers.
- [ADR-0030](0030-function-execution-runtime-shim-node.md)/[ADR-0032](0032-curated-runtime-images-container-execution.md)
  (execution) · [ADR-0031](0031-oci-artifact-distribution-oras.md) (artifacts) · [ADR-0026](0026-packaging-and-release.md)
  (single binary) · [ADR-0028](0028-platform-control-plane-wiring.md) (the wiring this extends).
