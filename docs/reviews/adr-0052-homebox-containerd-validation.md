# ADR-0052 homebox validation — the containerd cgroup-footprint lane on real hardware

- **Date**: 2026-06-17
- **What**: first end-to-end run of the [ADR-0052](../adr/0052-bench-containerd-cgroup-footprint-lane.md) `funcd-bench
  --containerd` lane over the **real** ADR-0032 production path (gateway → activator → containerd worker in a netns → crun
  container → shim), on the deployment-target box.
- **Environment**: homebox — 8-core / 15 GiB **Ubuntu 24.04** (noble), **cgroup v2**, containerd **2.2.1** (apt), crun
  **1.28** (GitHub release; apt's 1.14 is too old — see Finding 4), CNI reference plugins 1.1.1, node 18, a local
  `registry:2` serving the curated `funcd/runtime-nodejs22` image.
- **Reproduce**: [`scripts/bench-containerd-homebox.sh`](../../scripts/bench-containerd-homebox.sh) (provision → build →
  upload → run → fetch → clean up). On a remote box: `just bench-containerd` (default `homebox`, amd64). **Locally on
  macOS**: `just bench-containerd-lima` spins up a same-arch Linux VM with Lima (from the Nix dev shell), runs the lane,
  and tears the VM down — the driver auto-detects the target arch. A local arm64 Lima run reproduced the finding
  (**~22 MB/fn marginal cgroup, 0.33× the RSS, ~736-fn ceiling**) — different absolute numbers than amd64 homebox (slower
  per-core, virtualised), same shared-page conclusion.

## Verdict

**Sustainable, and the lane did its job.** The 100-agent target fits with **~10× headroom** measured over the production
path. More importantly, being the *first* exercise of the ADR-0032 containerd path on hardware (its integration test was
deferred, ADR-0011), the lane surfaced **three real funcd bugs + one environment requirement** — all fixed (commit
`b6f12a9`) — and **corrected a standing misconception**: per-process RSS *over*-counts for density, it is not "optimistic."

## Measurement

Real crun containers, density sweep, `--density 4` (reproduced twice: 15.9 MB/1028 fns and 15.7 MB/1043 fns):

| metric | containerd cgroup (homebox) | process RSS lane (for contrast) |
|---|---|---|
| **marginal memory / function** | **~15.7 MB** (cgroup `memory.current`) | ~60 MB (RSS) |
| cgroup ÷ RSS | **0.26×** | — |
| platform baseline | ~45 MB | ~45 MB |
| throughput / fn (warm) | ~3.1k req/s, p99 ~5 ms | ~5.9k req/s, p99 ~4 ms |
| **max density (16 GB budget)** | **~1043 fns** | ~250 fns |
| fits ~100 agents? | ✅ **yes** (~10× headroom) | ✅ yes |

**The finding: RSS over-counts for density (cgroup is 0.26× RSS, *lower*).** Every function runs the *same* curated image,
so the runtime's read-only pages (the node binary, shared libs, the shim) are faulted **once** — charged to the first
container's cgroup — and shared by every other container via the page cache. Per-process RSS, by contrast, counts those
shared pages in **every** process. So the **marginal** cost of one more function is its private heap (~16 MB), not a whole
node runtime (~60 MB), and the real density ceiling (~1043) is **higher** than the process-RSS lane's ~250 suggested. The
*absolute* first container is still larger than one RSS (page cache + kernel slab), but that one-time cost amortises across
the fleet. This is the nuance the lane existed to find — for **density**, the process-RSS verdict was pessimistic, not
"optimistic."

## Findings — bugs surfaced by running the real path (all fixed, `b6f12a9`)

### 🔴 1. Non-absolute OCI `process.cwd` — crun rejects (`model`/driver)
- **Evidence**: `create task for "z-r0": "." must be absolute` (crun), on every container.
- **Root cause**: containerd's `oci.WithImageConfig` copies the image's `WorkingDir` over the spec's `/` default; for a
  **WORKDIR-less** image that is `""`. runc tolerates an empty cwd (→ `/`); **crun does not**.
- **Fix**: [`internal/runtime/containerd/containerd_linux.go`](../../internal/runtime/containerd/containerd_linux.go) —
  `withAbsoluteCwd` SpecOpt defaults a non-absolute cwd to `/` (+ unit test `cwd_linux_test.go`).

### 🔴 2. Empty container log path (`model`/driver)
- **Evidence**: same `"." must be absolute` after Fix 1 — the spec dump showed `cwd=/` was clean, isolating the log URI.
- **Root cause**: container mode never set `WorkerSpec.LogPath`, and the driver passed `""` straight to `cio.LogFile`; the
  containerd shim needs an **absolute** log path. The `WorkerSpec` contract documents `"" → driver picks a temp file`, but
  the containerd driver didn't honour it.
- **Fix**: the driver resolves an empty `LogPath` to an absolute temp file before `cio.LogFile`.

### 🔴 3. Artifact unreadable by the container user (`model`/materializer)
- **Evidence**: shim log `funcd-shim: shape error: Cannot find module '/var/funcd/artifact/handler.mjs'`. A
  world-readable control mount loaded fine → confirmed perms, not naming.
- **Root cause**: the materializer wrote the artifact `0600` under `0750` dirs (root-owned), but the curated image runs as
  `USER node` (uid ≠ 0), which can neither traverse the dir nor read the file.
- **Fix**: [`internal/artifact/artifact.go`](../../internal/artifact/artifact.go) — artifacts are container-readable
  `0644`/`0755` (non-secret read-only code; secrets are injected separately; process mode is unaffected) (+ regression
  assertion in `artifact_test.go`).

### 🟡 4. crun too old for containerd v2's OCI spec (`env`)
- **Evidence**: `OCI runtime create failed: unknown version specified`.
- **Root cause**: containerd v2 stamps `ociVersion: 1.3.0` (vendored runtime-spec v1.3.0); Ubuntu 24.04's crun **1.14.1**
  only knows spec 1.0.0 and rejects it.
- **Resolution**: install crun **≥ ~1.20** (homebox: 1.28 from the crun GitHub release, over `/usr/bin/crun`). Not a funcd
  defect — a target-box prerequisite, now in the runbook + the provisioning script.

## Operational notes (for the next run)

- **Image distribution**: the box has no docker, so the curated image is built with **buildah** and served from a local
  `registry:2`, because the driver's `client.Pull` *resolves* the ref. containerd's default client resolver uses
  **plain-HTTP for `localhost`** (`MatchLocalhost`), so `--image-prefix localhost:5000/funcd/runtime-` needs no TLS.
- **Registry**: run it `--null-io` — `ctr run -d` otherwise leaves stdout on a FIFO nobody drains, and `registry:2`'s
  per-request logging fills the pipe and **hangs** under the reconcile-loop pull pressure.
- **CNI**: funcd does **not** write its own conflist; supply a `bridge`+`firewall` conflist (default-deny lateral) at the
  configured `CNIConfDir` (homebox: `/var/lib/funcd/cni/conf`), and point `--cni-bin-dir` at the plugins (Ubuntu installs
  them under `/usr/lib/cni`).
- **Flakiness at higher density**: `--density > 4` is unreliable on a hand-provisioned box — each function triggers a
  registry pull, and **force-killed runs leak containerd shims + netns/IP allocations** that starve later containers. A
  `systemctl restart containerd` + a CNI-state purge between sweeps clears it; the script does this automatically.
  `--density 4` reproduces cleanly.

## What this validates

- The ADR-0032 containerd/crun execution path now **works end to end on hardware** (cwd + logpath + artifact-perms fixed).
- ADR-0052's lane delivers the **honest production footprint** and its skip-vs-run contract behaves (it ran where the path
  was present; it skips cleanly where it is not).
- The sustainability verdict holds on the real target — and is *better* than the process-RSS proxy implied, once shared
  image pages are accounted for.
