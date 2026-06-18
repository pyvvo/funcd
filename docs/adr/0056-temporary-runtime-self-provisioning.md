# ADR-0056: Temporary container-runtime self-provisioning — `funcd install` downloads crun/containerd/CNI; funcd writes its own conflist + ip_forward

- **Status**: Implemented
- **Date**: 2026-06-19 (**Accepted 2026-06-19** · **Implemented 2026-06-19** — judge: no Blockers; the security model (SHA-256 verify-before-chmod/exec,
  mismatch aborts writing nothing) confirmed airtight and the refines-not-supersedes boundary correct. Folded 2 Majors + 3
  Minors: **M1** pin the EXACT conflist `ensureCNIConf` emits (the ADR-0011/0032 bridge `funcd0` + firewall form) and frame it
  as *reproducing* that conflist rather than newly guaranteeing lateral-deny (ADR-0011's deferred concern); **M2** a single
  shared `binDir = <funcdRoot>/bin` source of truth consumed by both `install`/`LayDown` and `ctrmanager`/`resolveBin`;
  plus `Asset.Install` member/dest seam, `Plan` iterating the shared `assets` table, and `Plan`'s host-arch.)
- **Deciders**: green-0-rabbit
- **Tags**: packaging, install, runtime, containerd, temporary
- **Realizes**: [FEAT-0000/F19](../feat/0000-feat-v1.md) (packaging: single binary + `funcd install`) — also advances the
  self-contained runtime of [FEAT-0000/F12](../feat/0000-feat-v1.md).
- **Refines**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) — **implements its deferred
  `funcd install` lay-down** (ADR-0054 *decided* "install lays down crun + containerd" but the implementation **stubbed the
  binary fetch** as "a release concern") and adds two small self-provisioning steps (the CNI conflist + ip_forward). It does
  **not supersede** ADR-0054: every ADR-0054 decision stands; this fills the deferred part with an explicit **temporary**
  mechanism that has an exit.
- **Relates to**: [ADR-0011](0011-runtime-sandbox-port.md)/[ADR-0032](0032-curated-runtime-images-container-execution.md) (the
  CNI bridge+firewall netns model + the crun OCI runtime — **unchanged**; this only *writes the conflist* the driver already
  loads and *lays down the binaries* the driver already execs), [ADR-0026](0026-packaging-and-release.md) (packaging/install),
  [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md)/[ADR-0055](0055-funcd-bench-subcommand.md) (`funcd bench
  --containerd`, the lane that benefits).

## Context & Need

ADR-0054 promises "one binary + `sudo funcd install`": funcd embeds its curated **images** and manages its own
**containerd**. But the binary lay-down was stubbed — funcd still needs **crun**, the **containerd** binary, the **CNI
plugins**, a **CNI conflist**, and **ip_forward** supplied from outside. Standing up `funcd bench --containerd` on a bare box
(or the Lima harness, `ccd5e0b`) still means: install crun ≥1.20 (Debian's apt crun is too old for containerd v2's OCI 1.3
spec), drop the CNI plugins, hand-write the bridge+firewall conflist, and `sysctl net.ipv4.ip_forward=1`. That is exactly the
out-of-band setup ADR-0054 set out to delete.

This ADR closes that gap **temporarily**, without the per-arch release build ADR-0054's bundling needs: `funcd install`
**downloads** the pinned, checksum-verified binaries (the interim stand-in for embedding), and funcd **writes its own
conflist + enables ip_forward** on the containerd path (it already knows the bridge name + subnet and runs as root). A bare
Linux box becomes `sudo funcd install && funcd bench --containerd` — nothing else.

## Scenarios

- **scenario: install-lays-down-runtime** — *Given* a bare Linux box with **only the funcd binary** (no crun/containerd/CNI),
  *when* `sudo funcd install`, *then* crun + containerd (+ `containerd-shim-runc-v2` + `ctr`) land in funcd's bin dir, the CNI
  plugins land in `/opt/cni/bin`, funcd's conflist is written, and `ip_forward=1` — and `funcd bench --containerd` then runs a
  real container with **nothing else installed**.
- **scenario: checksum-mismatch-aborts** — *Given* a downloaded asset whose SHA-256 ≠ the pinned digest, *then* install fails
  with a clear `api/fault` error and the binary is **never** `chmod +x`'d or executed — no silent run of unverified bytes as root.
- **scenario: manager-finds-laid-down-binaries** — *Given* crun + containerd in funcd's bin dir but **not** on the system
  `PATH`, *when* the Manager brings up the private containerd, *then* it resolves both from funcd's bin dir (and runs the
  containerd child with that dir on its `PATH`, so it finds the shim + crun).
- **scenario: funcd-writes-conflist-and-ipforward** — *Given* the managed-containerd execution path with an empty
  `CNIConfDir`, *when* the driver starts, *then* it writes `10-funcd.conflist` (the exact ADR-0011/0032 bridge `funcd0` +
  `firewall` form, IPAM over `SubnetCIDR`) and sets `ip_forward=1`, **before** loading CNI — idempotent; and *given* a
  `CNIConfDir` that already holds a conflist, the existing file is **left byte-for-byte untouched**.
- **scenario: install-print-no-touch** — *Given* `funcd install --print`, *then* it prints the version-stamped unit + the
  lay-down plan (what it *would* download to where) and **downloads/touches nothing**.

## Scope

**In:** `funcd install` downloading + SHA-256-verifying + laying down pinned **crun**, **containerd** (the static release
tarball: containerd + `containerd-shim-runc-v2` + `ctr`), and the **CNI plugins** (bridge, firewall, host-local, loopback,
portmap), arch-detected, idempotent; the **Manager** resolving crun/containerd from funcd's bin dir then `PATH` (+ that dir on
the containerd child's `PATH`); the **driver** writing funcd's conflist if absent + enabling ip_forward before `cni.Load`;
`--print` staying a no-touch dry-run that also prints the plan.

**Out:** the **real bundled release** (go:embed / release tarball — ADR-0054's open question; this ADR's **exit**); the
`--containerd <socket>` external-daemon path (the operator's containerd — unchanged, no lay-down); **air-gapped** install (a
consequence of the temporary *download* choice — called out, the exit fixes it); arbitrary user images; native macOS/Windows.

## Constraints & Decision drivers

- **Finish the ADR-0054 UX without its release build.** "One binary + `sudo funcd install`" must actually work now; the
  per-arch embed/tarball is heavier and deferred. Download is the cheapest mechanism that delivers the UX.
- **Root supply-chain safety.** Install runs as root and the binaries run as root, so a download **must** be checksum-pinned
  and verified **before** `chmod +x`/exec. A bad/MITM'd asset must abort, never run.
- **Don't change the execution model (ADR-0011/0032).** crun, the runc-v2 shim, the bridge+firewall netns, fixed-port
  addressing — all unchanged. This ADR only *provides* the binaries the driver already execs and *writes* the conflist it
  already loads.
- **Idempotent + non-destructive.** Re-running install is safe (skip present+version-matched binaries); funcd never clobbers
  an operator's existing conflist, and ip_forward is a harmless host-wide enable.
- **Apache-2.0/MIT-compatible only.** No new Go dependency — `net/http` + `crypto/sha256` + `archive/tar` (stdlib) do the
  download/verify/extract. crun stays a separately-exec'd GPL binary (the ADR-0011 boundary, unchanged).

## Alternatives considered

- **Embed the binaries now (go:embed, per-arch).** The real ADR-0054 endgame: offline, no network. *Rejected as the interim:*
  it **is** the deferred release concern (per-arch builds, +~80–120 MB, GPL crun now *inside* the binary's bytes — a heavier
  licence question than exec'ing it). Kept as the **exit criterion**.
- **Ship a release tarball (funcd + sibling binaries).** Offline-capable, lighter than embedding. *Rejected for now:* still
  needs a per-arch release build + shipping the binaries; more than a stopgap. Also a valid exit.
- **Keep provisioning external (the Lima harness / an `install.sh`).** *Rejected:* that is the status quo this ADR removes —
  ADR-0054's whole point is that funcd, not the operator, owns its runtime. The Lima `provision` shrinks to `sudo funcd install`.
- **Use the OS package manager (apt) for crun/containerd.** *Rejected:* Debian's crun is too old (1.8 < the 1.20 OCI-1.3
  floor), versions vary by distro, and it reintroduces the out-of-band install ADR-0054 deletes. Pinned static binaries are
  reproducible across distros.

## Decision

**One shared runtime root.** funcd's runtime root is **`/var/lib/funcd`** (the value already used by `cmd/funcd` install +
the Manager). The single source of truth for the laid-down binaries is **`binDir = <root>/bin`** (`/var/lib/funcd/bin`),
exported once and consumed by **both** `funcd install`/`LayDown` (which writes there) **and** `ctrmanager`/`resolveBin` (which
reads there) — so install can never lay binaries where the Manager won't look. It is a **sibling** of the Manager's existing
containerd data-root (`<root>/containerd`), not the same dir. The CNI plugins go to the CNI standard `/opt/cni/bin`.

1. **`funcd install` downloads the runtime (the temporary lay-down).** For the detected arch (`amd64`|`arm64`), it fetches
   pinned, **SHA-256-verified** static assets — **crun** (≥1.20), **containerd** (the static release tarball →
   `containerd`, `containerd-shim-runc-v2`, `ctr`), and the **CNI plugins** (bridge, firewall, host-local, loopback,
   portmap) — laying crun/containerd into **`binDir`** and the CNI plugins into `/opt/cni/bin`. Each asset's pinned
   **version + SHA-256** is a constant in funcd; the checksum is verified **before** `chmod +x`/exec. Idempotent: skip an
   asset already present at the pinned version. (Temporary — see *Temporary workarounds*.)
2. **The Manager resolves binaries from `binDir` first, then `PATH`.** `ctrmanager` looks up crun + containerd in `binDir`
   before `exec.LookPath`, and starts the private containerd child with `binDir` **prepended to its `PATH`** so containerd
   finds `containerd-shim-runc-v2` + crun. (A system install — `--containerd <socket>` — is unaffected.)
3. **The driver self-provisions its CNI.** The containerd driver (which holds `CNIConfDir` + `SubnetCIDR`) **reproduces the
   ADR-0011/0032 funcd conflist** — `cniVersion 1.0.0`, name `funcd`, the `bridge` plugin (`bridge: funcd0`, `isGateway`,
   `ipMasq`, host-local IPAM over `SubnetCIDR`, default route) then the `firewall` plugin — into `CNIConfDir` **only if
   absent**, and enables `net.ipv4.ip_forward=1`, **before** `cni.Load`. It writes the **exact same** conflist the driver has
   always loaded (the validated Lima/homebox form) — it does **not** add or alter the netns/lateral-deny model (that is
   ADR-0011's concern + its deferred `sandbox-lateral-deny` scenario, untouched here). An existing conflist is never clobbered.
4. **`--print` is unchanged + describes the plan.** `funcd install --print` prints the version-stamped unit **and** the
   lay-down plan (each asset → version, source, destination), and **downloads/touches nothing**.

## Temporary workarounds

- **Download-on-install is a temporary stand-in for ADR-0054's binary bundling.** It is **network-dependent** (the install
  host must reach the pinned GitHub release assets) and therefore **not air-gapped**, and it **trusts pinned upstream
  releases** — mitigated by SHA-256 pinning + verify-before-exec, but it is not the same as shipping bytes funcd built.
  **Exit criterion:** the real **bundled per-arch release** (go:embed or a release tarball — ADR-0054's open question), which
  replaces every download with shipped, verified bytes; when it lands, Decision 1's download path is deleted (the resolution
  in Decision 2 stays). Until then this is tracked debt, owned by F19's packaging follow-up.

## Contracts

```go
// internal/runtime/provision — the temporary download lay-down (stdlib only: net/http, crypto/sha256, archive/tar).
package provision

// Asset is a pinned, checksum-verified downloadable runtime binary/tarball. Install is a PER-ASSET closure that encodes
// the asset's shape — a lone binary (chmod 0755 into binDir), or an untar selecting named members to their dests
// (containerd → {containerd, containerd-shim-runc-v2, ctr} in binDir; cni-plugins → {bridge, firewall, host-local,
// loopback, portmap} in cniDir). It runs ONLY after the SHA-256 of `data` matched the pin.
type Asset struct {
	Name    string                   // "crun" | "containerd" | "cni-plugins"
	Version string                   // pinned, e.g. "1.28"
	URLFor  func(arch string) string // arch ∈ {"amd64","arm64"}
	SHA256  map[string]string        // arch → hex digest (verified before Install)
	Install func(data []byte, binDir, cniDir string) error
}

// assets is the single pinned table both LayDown and Plan iterate (so --print can never drift from the real download set).
// LayDown downloads each asset for the host arch, verifies its SHA-256 BEFORE any chmod+exec, then calls Install;
// idempotent (skips an asset already present at its pinned version). A checksum mismatch returns fault.Invalid and
// installs NOTHING (no partial state, no chmod, no exec). binDir/cniDir are the destinations.
func LayDown(ctx context.Context, binDir, cniDir string) error
// Plan returns the human-readable lay-down plan (asset → version, source URL, dest) for `funcd install --print`, for the
// HOST arch, by iterating the same `assets` table — pure, no network, no disk.
func Plan(binDir, cniDir string) []string

// cmd/funcd + internal/runtime/ctrmanager share ONE runtime root + binDir (M-of-the-judge: a single source of truth):
//   funcdRoot = "/var/lib/funcd"               // already the install dataRoot + the Manager's root
//   binDir    = filepath.Join(funcdRoot, "bin") // where LayDown writes AND resolveBin reads (sibling of <root>/containerd)
// ctrmanager.resolveBin("containerd") → binDir/containerd if present, else exec.LookPath; startContainerd runs the child
// with binDir prepended to PATH so it finds containerd-shim-runc-v2 + crun.

// internal/runtime/containerd — the driver self-provisions CNI before cni.Load (New):
//   ensureCNIConf(confDir, subnetCIDR string) error  // writes 10-funcd.conflist IF ABSENT — the EXACT ADR-0011/0032 form:
//       {cniVersion:"1.0.0", name:"funcd", plugins:[ {bridge funcd0, isGateway, ipMasq, host-local ipam over subnetCIDR,
//        default route}, {firewall} ]} — never clobbers an existing file.
//   enableIPForward() error                          // sysctl net.ipv4.ip_forward=1 (idempotent), behind a writeFile seam
```

**Dependencies & I/O:** **no new Go module** (stdlib download/verify/untar). Writes to `binDir` (`/var/lib/funcd/bin`),
`/opt/cni/bin`, `CNIConfDir`, and `/proc/sys/net/ipv4/ip_forward`. Network egress to the pinned release hosts at **install**
time only. crun stays a separately-exec'd binary (GPL boundary, ADR-0011).

## Implementation plan

- `internal/runtime/provision/`: `provision.go` — the `Asset` table (pinned crun **1.28**, containerd **≈2.2.x**, cni-plugins
  **v1.9.1** to match the validated Lima/homebox set, with their published SHA-256 digests), `LayDown` (per-arch download →
  `sha256.Sum256` compare → install: a lone binary `chmod 0755`, or untar the containerd/cni archives selecting the named
  members), `Plan`. Stdlib only.
- `internal/runtime/ctrmanager/manager_linux.go`: `resolveBin` (binDir-first); `startContainerd` runs the child with binDir on
  `PATH`; `layDownCrun` resolves via `resolveBin`.
- `internal/runtime/containerd/containerd_linux.go`: `ensureCNIConf` (render `10-funcd.conflist` from `SubnetCIDR`, write if
  absent) + `enableIPForward`, both called in `New` before `cni.Load`.
- `cmd/funcd/install.go`: `runInstall` calls `provision.LayDown` (after writing the unit); `--print` adds `provision.Plan`.
- **Test plan.** Non-gated (`just ci`): arch→asset URL/name construction; **SHA-256 verify** (matching bytes install,
  mismatching bytes → `fault.Invalid`, nothing written — table test with an httptest server or in-memory bytes); the conflist
  renderer + **write-only-if-absent** against a temp dir (existing file untouched); `ensureCNIConf`/`enableIPForward` behind a
  seam so the sysctl is unit-checkable; `install --print` prints the plan and touches nothing. **Deferred to the Linux
  integration lane** (`FUNCD_IT=1`, root, network): the real `funcd install` on a from-scratch box + a `funcd bench
  --containerd` run. **DoD:** `go build` · `golangci-lint` · `go test` green · no new module · no identity/path leak ·
  checksum-before-exec verified.

## Review checklist

- [ ] `funcd install` downloads + SHA-256-verifies (before chmod/exec) + lays down crun + containerd(+shim+ctr) + the CNI
      plugins for the host arch, idempotently; a checksum mismatch aborts with `fault.Invalid` and writes nothing.
- [ ] Pinned versions + digests are constants in funcd; no new Go module (stdlib download/verify/untar).
- [ ] The Manager resolves crun/containerd from `/var/lib/funcd/bin` first, then `PATH`; the containerd child has that dir on `PATH`.
- [ ] The driver writes the funcd conflist **only if absent** (existing untouched) + enables ip_forward **before** `cni.Load`.
- [ ] `funcd install --print` prints the unit + the lay-down plan and downloads/touches nothing.
- [ ] Execution model (crun, runc-v2 shim, netns bridge+firewall, fixed-port) unchanged vs ADR-0011/0032.
- [ ] Temporary workaround documented with the bundled-release exit; non-gated tests pass; real-box install deferred to the IT lane; no leak.

## Consequences

- **A bare Linux box is now `sudo funcd install && funcd bench --containerd`** — the ADR-0054 UX, delivered without the
  per-arch release build. `scripts/lima/funcd.yaml`'s four `provision` blocks collapse to a single `sudo funcd install`.
- **funcd reaches out to the network at install time** (the temporary cost) — bounded by SHA-256 pins + verify-before-exec;
  the bundled-release exit removes it.
- **funcd owns its CNI conflist + ip_forward**, so neither is operator setup; an operator who wants a custom conflist still
  wins (funcd only writes when absent).
- The crun **GPL boundary is unchanged** — crun is downloaded + exec'd as a separate binary, never linked.

## Open questions

- **Exact pinned containerd version + the digest source.** Match the validated set (≈2.2.x); digests are the published
  release checksums recorded as constants — pinned at impl.
- **`ctr` discoverability.** `binDir` is decided — `/var/lib/funcd/bin` (`<funcdRoot>/bin`, self-contained, removable with
  the data root, the one source of truth shared by install + the Manager). A future `funcd install` could also symlink `ctr`
  onto the operator's `PATH` if that ergonomics is wanted.
- **The exit.** Whether the bundled release is go:embed or a tarball is ADR-0054's open question, decided when that release
  build is built — at which point this ADR's download path retires.

## References

- ADR-0054 (the deferred install lay-down this implements; its embed-vs-tarball open question is the exit), ADR-0011/0032
  (crun + CNI execution, unchanged), ADR-0026 (packaging/install), ADR-0055/0052 (`funcd bench --containerd`).
- crun, containerd, containernetworking/plugins — upstream static release assets (Apache-2.0; crun GPL-2.0 binary, exec'd not linked).
