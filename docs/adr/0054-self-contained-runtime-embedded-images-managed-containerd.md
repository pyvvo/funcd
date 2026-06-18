# ADR-0054: Self-contained runtime — embed curated images + privately manage containerd/crun (the k3s model)

- **Status**: Implemented
- **Date**: 2026-06-18 (**Accepted 2026-06-18** · **Implemented 2026-06-18** — judge: no Blockers (the heavy trade-offs — ~150 MB binary, funcd
  supervising containerd, superseding ADR-0032, custom-Python maintenance — confirmed as the decider's deliberate,
  blueprint-backed choice, not defects). Folded 1 Major + 3 Minors: **M1** the node-base change supersedes **ADR-0039** (its
  decided base) — header now declares supersession of ADR-0032 (distribution) *and* ADR-0039 (node base, version kept); the
  install altitude justified, `Tar`'s not-found case clarified, the root-containerd privilege surfaced in Constraints, the
  `Temporary workarounds` heading + the ~150 MB *ceiling* note added. Judge confirmed sound: the supersede-one-axis-keep-the-
  other boundary, the crun GPL separate-binary boundary (matches ADR-0011), and the Python-3.14 custom-base reasoning.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, packaging, distribution, single-binary
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind `runtime.Runtime`) — and refines the
  packaging story of [FEAT-0000/F19](../feat/0000-feat-v1.md).
- **Supersedes** (each on one axis only; both back-linked on acceptance):
  1. [ADR-0032](0032-curated-runtime-images-container-execution.md) **on image *distribution*** — registry-pull + per-deploy
     bind-mount of the *curated base* → an **embedded** image imported into a funcd-managed containerd. ADR-0032's
     **execution** model (read-only bind-mount of the *artifact*, fixed-port netns addressing) is **kept unchanged**.
  2. [ADR-0039](0039-pin-node-runtime-to-node22.md) **on the node curated-image *base*** — `node:22-bookworm-slim` →
     `distroless/nodejs22`. ADR-0039's node-**22 version** pin is **kept** (distroless/nodejs22 is still node 22); only the
     *base image* changes.
- **Relates to**: [ADR-0011](0011-runtime-sandbox-port.md) (the containerd/crun driver + netns lateral-deny — unchanged),
  [ADR-0026](0026-packaging-and-release.md) (single binary + version stamp + systemd unit + install — this refines it),
  [ADR-0049](0049-python-runtime-shim.md)/[ADR-0050](0050-python-worker-pooling-subinterpreters.md)
  (the Python shim + its **≥3.14** subinterpreter pool — drives the custom Python base), [ADR-0031](0031-oci-artifact-distribution-oras.md)
  (the *user artifact* still arrives via OCI push/pull — only the *runtime base* changes).

## Context & Need

The blueprint promises funcd "ships as a single binary like **k3s or faasd**" and is "self-contained." But ADR-0032's
runtime distribution is the opposite: the operator must install a **system containerd + crun + CNI**, **build + publish**
the curated runtime image, and have funcd **pull it from a registry**. Standing this up on real hardware (ADR-0052's
homebox/VM validation) exposed the friction first-hand — installing containerd/crun, writing a CNI conflist, building the
image with buildah, running a local registry just to serve it. That is a lot of out-of-band setup for a "drop one binary
and go" platform.

This ADR closes the gap by adopting the **k3s/dockerd model**: funcd **embeds** its curated runtime images and **privately
manages its own containerd + crun**. Deploying funcd becomes *one binary + `sudo funcd install`* — no registry, no separate
runtime install, no CNI hand-wiring. (k3s bundles + manages its own containerd under a private data-root; dockerd runs
containerd as a managed child on a private socket — this is the established, battle-tested pattern, not a novelty.)

## Scenarios

- **scenario: runs-out-of-the-box** — *Given* a fresh Linux box with **only the funcd binary**, *when*
  `sudo funcd install && systemctl start funcd`, *then* funcd starts its private containerd, imports its embedded curated
  images, and can run a function in a real crun container — **no** registry, **no** `apt install containerd/crun/cni`.
- **scenario: embedded-image-no-registry** — *Given* a deployed function, *when* it is scheduled, *then* its curated
  runtime image is loaded from funcd's **embedded** copy (imported into the managed containerd), **never pulled** from a
  registry.
- **scenario: system-containerd-override** — *Given* `funcd --containerd /run/containerd/containerd.sock`, *when* funcd
  starts, *then* it uses the **existing system containerd** instead of starting its own (the Docker-style escape hatch).
- **scenario: lighter-curated-bases** — *Given* the curated images, *then* node is built on **`distroless/nodejs22`** and
  python on a **custom distroless 3.14** base, each small enough to embed (~50–60 MB compressed).
- **scenario: one-service-install** — *Given* `sudo funcd install`, *then* it lays down crun + the private containerd and
  writes + enables a **single** `funcd.service` systemd unit (funcd is the service; containerd is its child — **no**
  separate containerd unit); it is idempotent and `sudo funcd uninstall` reverses it.
- **scenario: version-locked-shim** — *Given* a funcd binary, *then* the embedded shim image and funcd are **version-locked**
  (the shim can never skew from the platform that runs it).

## Scope

**In:** lighter curated bases (distroless node + a **custom distroless Python 3.14**); **embedding** the per-arch curated
OCI images into the funcd binary (`go:embed`) and importing them into containerd at startup; a **k3s-style private-managed
containerd** (funcd supervises a containerd child on a private socket + data-root) with a **`--containerd <socket>`**
override; **bundling crun**; a **`funcd install`/`uninstall`** subcommand (one systemd unit, funcd-as-the-service), refining
ADR-0026; **per-arch** release builds (the embedded image matches the binary's arch). *(All one altitude — "how funcd's
runtime is distributed, run, and laid down": `install` is the **delivery mechanism for the embedded assets**, so it belongs
with them rather than as a drift-prone separate script. The ADR-0026 precedent — binary + version stamp + systemd + docs as
one packaging topic — is the same shape. If it ever needs splitting, the `install`/`uninstall` subcommand is the clean seam.)*

**Out:** the container **execution mechanics** — per-worker **netns + default-deny lateral**, the **read-only bind-mount of
the user artifact**, and **fixed-port addressing** stay **exactly** as ADR-0011/0032 (this ADR changes *distribution +
runtime management + packaging*, not how a container runs); arbitrary **user OCI images** / a per-revision baked image (still
deferred, ADR-0032/blueprint); **CRI** (funcd uses the containerd **client API**, not CRI — so the plain `containerd` build
suffices, the `cri-containerd-*` archives are irrelevant); native macOS/Windows container execution (the path is Linux; on
macOS you run it in a VM via the dev script — *not* shipped in funcd); the **funcd-bench** tool's packaging (a separate ADR).

## Constraints & Decision drivers

- **Blueprint: single-binary, embed-first, "like k3s/faasd," self-contained.** This ADR is the runtime layer finally
  honoring that; registry-pull + a separately-installed daemon contradict it.
- **Established pattern, not invention.** k3s (single binary bundles + manages containerd under `/var/lib/rancher/k3s/...`)
  and dockerd (containerd as a managed child on its own socket, `--containerd` to use an external one) are the templates.
- **Embedding insulates us from registries.** The base is pulled **once at build time** and baked in; runtime never touches
  a registry (notably: `gcr.io/distroless` is being retired — a build-time-only concern when embedded).
- **Python needs ≥3.14.** ADR-0050's subinterpreter pool requires Python 3.14, but off-the-shelf `distroless/python3` is
  **3.11**. So node uses stock distroless; python uses a **custom** distroless built from `python:3.14-slim` → `distroless/cc`
  (the shim is **stdlib-only**, so the venv-portability problem that blocks the general case does not apply to us).
- **Apache-2.0/MIT-compatible only.** containerd (Apache-2.0), crun (GPL-2.0 **binary**, invoked as a separate executable —
  not linked), distroless (Apache-2.0). crun is shipped as a standalone binary funcd *execs*, never linked, so its GPL does
  not reach funcd's Go code — the **same boundary ADR-0011 already records** for crun. *(Verify at impl.)*
- **Privilege: no new boundary.** The private-managed containerd runs **as root** — the same privilege funcd already holds
  for netns/container setup (ADR-0011); it is not a new privilege surface. The `--containerd` path defers to the operator's
  existing daemon. The **execution isolation of ADR-0011/0032 (netns + default-deny lateral) is unchanged** — this ADR
  touches distribution + which daemon runs the container, never how the container is confined.

## Alternatives considered

- **Keep ADR-0032 registry-pull (incl. publishing to GHCR).** Standard, small binary. *Rejected as the default:* it
  requires a reachable registry + a separately-installed containerd — the opposite of the blueprint's self-contained
  single-binary goal. Kept as the **`--image <ref>` override** for custom runtimes.
- **Require a system containerd (operator `apt install`s it).** Simplest for funcd. *Rejected:* defeats "drop one binary and
  go"; the homebox/VM validation showed how much setup that is. Kept as the **`--containerd <socket>`** opt-in.
- **Ship a shell installer (`install.sh`).** *Rejected:* a second artifact that drifts from the binary; a `funcd install`
  subcommand using the embedded assets keeps the single-binary ethos (and `--print` gives the same transparency).
- **Alpine bases.** Smaller-ish. *Rejected for python* (musl + no 3.14 distroless/alpine parity with our pinned runtime);
  distroless is the cleaner, glibc-correct minimal base and node distroless is stock.
- **Run the runtime as a bare funcd child process (no container).** *Rejected:* loses the crun/netns isolation + the
  cgroup-measured footprint that the whole production model rests on.

## Decision

1. **Lighter curated bases.** **node** → `distroless/nodejs22` (stock; entrypoint `node`, no shell — funcd execs node
   directly). **python** → a **custom distroless 3.14**: multi-stage `FROM python:3.14-slim` copying the 3.14 runtime + its
   stdlib C-extension libs into `distroless/cc` (stdlib-only shim ⇒ no venv, no path-rewrite breakage). Each ~50–60 MB
   compressed. The funcd shim is `COPY`'d in as the entrypoint target; the **user artifact stays a runtime bind-mount**
   (ADR-0032 unchanged).
2. **Embed the images.** At build, each per-arch curated image is exported to an **OCI tar** and `go:embed`'d into funcd; at
   startup funcd **imports** it into the managed containerd's image store (`client.Import`). **No registry pull.** A
   `--image <runtime>=<ref>` override pulls from a registry (e.g. `ghcr.io/green-0-rabbit/…`) for a custom/updated runtime.
3. **Private-managed containerd (k3s model).** funcd lays down + **supervises its own containerd** as a child process on a
   **funcd-private socket + data-root** (e.g. `/var/lib/funcd/containerd`), with **crun bundled** alongside. **Default.**
   **`--containerd <socket>`** instead points funcd at an existing system containerd (Docker-style) and skips supervision.
4. **`funcd install` (subcommand, not a script).** Lays down crun + containerd, writes + enables a **single**
   `funcd.service` systemd unit — **funcd is the service; containerd is its managed child** (no separate containerd unit).
   Idempotent; `--print` dry-runs; `funcd uninstall` reverses it. Refines ADR-0026's "systemd unit + install docs."
5. **Execution unchanged.** Everything about how a container *runs* — netns, default-deny lateral, the read-only artifact
   bind-mount, fixed-port addressing, the `runtime.Runtime` port — is **exactly** ADR-0011/0032. This ADR changes only
   *where the image comes from*, *which containerd runs it*, and *how funcd is installed*.

## Temporary workarounds

None. (The impl-time pins in *Open questions* — data-root/socket paths, the custom-Python lib set, embed-binary vs
release-tarball — are bounded choices each with an exit, not un-exited stopgaps.)

## Contracts

```go
// internal/runtime/embedimg — the embedded curated images (per-arch, build-time baked).
//go:embed nodejs22.tar
//go:embed python314.tar
// Tar returns the embedded OCI image tar for a runtime ("nodejs22"/"python314"); the build wires the
// arch-matching tars into the binary. ok==false means "no embedded tar for this runtime" → the Manager
// falls through to ImageOverride[runtime]; a runtime in neither embed nor override is the Manager's
// fault.NotFound (never a silent miss).
func Tar(runtime string) (r io.Reader, ok bool)

// internal/runtime/ctrmanager — bring up the container runtime funcd will drive.
type Manager interface {
	// Ensure returns the containerd socket the driver should dial: it starts + supervises a private
	// containerd (default) and imports the embedded images, OR returns the externalSocket as-is when
	// --containerd is set. crun is laid down so the runc-v2 shim finds it.
	Ensure(ctx context.Context) (socket string, err error)
	Close() error
}
type Config struct {
	ExternalSocket string // --containerd <socket>; "" ⇒ private managed containerd
	DataRoot       string // private containerd state, e.g. /var/lib/funcd/containerd
	ImageOverride  map[string]string // --image runtime=ref ⇒ pull instead of embedded import
}

// cmd/funcd — install/uninstall the single systemd service (funcd manages containerd as its child).
func installCmd() *cobra.Command   // funcd install [--print]
func uninstallCmd() *cobra.Command // funcd uninstall
```

**Dependencies & I/O:** bundles the **containerd** + **crun** binaries (embedded or in the release tarball) + the per-arch
curated **OCI image tars**. Writes to a funcd data-root (`/var/lib/funcd/**`) + `/etc/systemd/system/funcd.service`. No new
Go module beyond the already-present containerd client. crun is execed as a separate binary (licence boundary).

## Implementation plan

- `images/runtime/nodejs22/Dockerfile` → rebase on `distroless/nodejs22`; **new** `images/runtime/python314/Dockerfile`
  (the multi-stage custom-distroless 3.14). A build step exports each to an OCI tar **per arch**.
- `internal/runtime/embedimg`: `go:embed` the arch-matching tars + `Tar(runtime)`.
- `internal/runtime/ctrmanager`: the `Manager` — private-containerd supervision (start/health/stop on a private socket +
  data-root), `client.Import` of the embedded tars, crun lay-down; the `--containerd` external path.
- Wire `cmd/funcd` `executionOptions` to use the `Manager`-provided socket (replacing the fixed `FUNCD_CONTAINERD_SOCKET`).
- `cmd/funcd` `install`/`uninstall` cobra subcommands + the `funcd.service` template (version-stamped, ADR-0026).
- Per-arch release builds (`scripts/build.sh`/justfile): build the binary + embed the matching-arch image; bundle
  containerd+crun.
- **Test plan.** Unit (non-gated, `just ci`): `embedimg.Tar` returns a non-empty reader for each runtime; the install
  subcommand `--print` emits a valid unit without touching the system; the `Manager` selects the external socket when
  `ExternalSocket` is set. **Deferred to the Linux integration lane** (`FUNCD_IT=1`, root — the ADR-0011/0032 precedent):
  `runs-out-of-the-box`, `embedded-image-no-registry`, `one-service-install` (real private containerd + crun + a function in
  a container). **DoD:** `go build` per-arch · `golangci-lint` · `go test` green · the embedded tars present + arch-correct ·
  crun licence boundary verified · no identity/path leak.

## Review checklist

- [ ] node on `distroless/nodejs22`; python on the custom distroless **3.14** base; user artifact still a read-only bind-mount.
- [ ] Per-arch curated image `go:embed`'d + imported into containerd at startup; **no registry pull** by default;
      `--image runtime=ref` override works.
- [ ] Private managed containerd by default (private socket + data-root, supervised); `--containerd <socket>` uses a system one.
- [ ] crun bundled + laid down; execed as a separate binary (no GPL linkage into funcd).
- [ ] `funcd install` writes + enables **one** `funcd.service` (containerd is its child, not a 2nd unit); `--print` dry-runs;
      `uninstall` reverses; idempotent.
- [ ] Execution mechanics (netns, lateral-deny, fixed-port, bind-mount) byte-for-byte ADR-0011/0032 — unchanged.
- [ ] Non-gated unit tests pass in `just ci`; the real-container scenarios deferred to the integration lane; no identity/path leak.

## Consequences

- funcd becomes **genuinely self-contained** (the k3s/faasd promise): one binary + `sudo funcd install`, no registry, no
  separate runtime install. The homebox/VM provisioning collapses — **buildah + the local registry disappear** from the
  bench path too (it uses funcd's embedded image + managed containerd).
- The **shim is version-locked** to funcd (no skew) — a real correctness win.
- The funcd binary grows by the embedded images + bundled runtime — **~100–150 MB is the embed-everything *ceiling*** (the
  *Open questions* tarball option keeps funcd ~90 MB + sibling files; it's not a settled figure), and **release builds become
  per-arch** (each embeds its matching-arch image). Accepted: this is the cost of the single-binary, self-contained model
  (k3s is similarly sized).
- funcd now **supervises a containerd lifecycle** (start/stop/health) — more responsibility in the daemon; mitigated by the
  `--containerd` escape hatch for operators who already run one.
- ADR-0032's registry distribution is superseded; its execution decisions stand. GHCR is demoted from "the way images
  reach the box" to "an optional `--image` override."

## Open questions

- **Embed-in-binary vs ship-in-release-tarball for containerd+crun.** Both are "bundled"; embedding gives a lone binary
  (~150 MB), a tarball keeps funcd ~90 MB + a few sibling files. Decide at impl (the image is embedded either way; this is
  only about the two runtime *binaries*).
- **Exact data-root + socket paths** (`/var/lib/funcd/**`) and rootless feasibility — pinned at impl; V1 assumes root (the
  container path needs it).
- **The custom Python base's exact lib set** (`ldd` of 3.14 + stdlib `lib-dynload`) — enumerated at impl; stable once pinned.

## References

- blueprint.md ("ships as a single binary like **k3s or faasd**", self-contained; curated bases + bind-mount artifact).
- ADR-0032 (superseded on distribution), ADR-0011 (driver/execution kept), ADR-0026 (packaging refined), ADR-0039 (node 22),
  ADR-0049/0050 (Python shim + ≥3.14 pool).
- k3s — single binary bundles + manages containerd; dockerd — containerd as a managed child + `--containerd` override;
  GoogleContainerTools/distroless (#1543 — custom-python pattern; #1764 — gcr.io retirement, build-time-only when embedded).
