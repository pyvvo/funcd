# ADR-0055: `funcd bench` — fold the sustainability harness into the funcd binary

- **Status**: Implemented (2026-06-18)
- **Date**: 2026-06-18 (**Accepted 2026-06-18** — judge: no Blockers, no Majors; the contested calls (one binary, keep-libs,
  flag-driven single verb, the ADR-0054 build-dep) confirmed as deliberate, correctly-executed decisions, not defects. Folded
  2 Minors + 2 Nits: the `.golangci.yml` claim corrected from "no change" to "drop the now-dead `cmd/funcd-bench` exception,
  substance intact"; `--doctor`'s running-daemon probe pinned (control-plane dial, else the ADR-0054 private socket);
  "persistent flags" → "local flags". **Beyond the judge**, on acceptance: ADR-0051 moved from *relates-to* to a one-axis
  **supersession** — its `deps-confined-to-bench` scenario barred gopsutil/fortio from `cmd/funcd`, which the one-binary
  decision reverses; the `funcdcli` confinement + depguard rule are kept.)
- **Deciders**: green-0-rabbit
- **Tags**: bench, cli, packaging, observability
- **Realizes**: [FEAT-0000/F27](../feat/0000-feat-v1.md) (benchmark & sustainability harness).
- **Supersedes** (each on one axis only; both back-linked on acceptance):
  1. [ADR-0040](0040-benchmark-sustainability-harness.md) **on packaging only** — the harness ships as a **`funcd bench`
     subcommand**, not the standalone `cmd/funcd-bench` binary. ADR-0040's *harness* (`internal/bench`, the embed-and-measure
     design) is **kept**; only the *entrypoint* moves.
  2. [ADR-0051](0051-bench-adopt-gopsutil-fortio.md) **on the `cmd/funcd` confinement axis only** — its `deps-confined-to-bench`
     scenario + DoD asserted `go list -deps ./cmd/funcd/...` **excludes** gopsutil/fortio; with the bench now folded into
     `funcd` (the one-binary decision), they **do** ride in the shipped `funcd` binary via `internal/bench`. ADR-0051's
     **`funcdcli` confinement** and its **depguard deny rule** are **kept** (funcdcli never imports `internal/bench`).
- **Relates to**: [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (**build-depends on it** —
  the containerd lane reuses funcd's embedded image + managed containerd), [ADR-0052](0052-bench-containerd-cgroup-footprint-lane.md)
  (the containerd footprint lane, now a `funcd bench` lane), [ADR-0053](0053-funcdcli-bench-subcommand.md) (the *client*
  `funcdcli bench` — unchanged, the separate sibling), [ADR-0042](0042-cobra-cli-framework.md) (the funcd cobra root it adds
  a verb to), [ADR-0026](0026-packaging-and-release.md) (one binary).

## Context & Need

The sustainability harness (`funcd-bench`, ADR-0040) **embeds the platform** (`funcd.New`) and samples its memory — but
**`funcd` *is* the platform**, so a separate binary was always slightly redundant. ADR-0054 makes that redundancy
acute *and* removable: the `funcd` binary now also carries the **runtime** (embedded curated images + a privately-managed
containerd + crun). So the bench can live **inside `funcd`** and reuse all of it — the containerd footprint lane needs
**zero** extra setup (no separate binary, no buildah, no local registry, no image build), it just benches funcd's own
runtime.

The decider's call (explicit): **one binary.** Production-hardening (stripping the bench from a shipped daemon) is deferred
until funcd actually ships to users and we see how they react — a deliberate design-phase simplification.

## Scenarios

- **scenario: bench-is-one-flag-driven-verb** — *Given* the `funcd` binary, *when* `funcd bench --help`, *then* it is a
  **single verb** whose mode is chosen by **mutually-exclusive flags** (`--containerd`, `--doctor`) defaulting to the
  in-process lane — **not** a `process`/`containerd`/`doctor` subcommand tree; the standalone `cmd/funcd-bench` no longer exists.
- **scenario: in-process-default** — *Given* any box, *when* `funcd bench` **with no mode flag**, *then* it runs the
  in-process embed bench over the **memory + file** substrates (RSS, the ADR-0040 lane) — the default, no container infra needed.
- **scenario: containerd-lane-reuses-embedded-runtime** — *Given* a Linux box, *when* `funcd bench --containerd`, *then* it
  measures the cgroup footprint (ADR-0052) using funcd's **own embedded image + managed containerd** (ADR-0054) — **no**
  buildah, **no** registry, **no** image build or `--image-prefix` dance.
- **scenario: doctor-checks-not-installs** — *Given* `funcd bench --doctor`, *then* it **reports** whether the containerd lane
  can run (containerd/crun/cgroup-v2 present) and *suggests* a Linux box/VM if not — it **never** installs anything, spins a
  VM, or ships Lima (text guidance only).
- **scenario: mode-flags-mutually-exclusive** — *Given* `funcd bench --containerd --doctor` (two modes at once), *then* it
  fails fast with a clean `api/fault` usage error — exactly one mode (or the bare default) runs, never two.
- **scenario: bench-libs-in-funcd-not-funcdcli** — *Given* the move, *when* the import graph is checked, *then* gopsutil +
  fortio appear in `go list -deps ./cmd/funcd/...` (via `internal/bench`) but **stay absent** from `./cmd/funcdcli/...`; the
  `bench-libs` depguard rule keeps its substance — it still denies *direct* bench-lib imports in shipped code (`funcd`'s bench
  file imports `internal/bench`, not the libs), the only edit being dropping the now-dead `cmd/funcd-bench` exception.
- **scenario: dedicated-box** — *Given* `funcd bench --containerd`, *then* its containers run in an **isolated containerd
  namespace** so they don't mix with a live daemon's functions; and `--doctor` **warns** if a `funcd` daemon is already running
  — detected by a dial to the default control-plane address (ADR-0024), else the ADR-0054 private containerd socket — (a
  density+load bench contends for the box — run it on a dedicated/idle host, the kubemark rule).

## Scope

**In:** a **single** `funcd bench` cobra verb on the funcd root (ADR-0042) whose mode is chosen by **mutually-exclusive
flags** — bare = the in-process lane (default), `--containerd` = the cgroup-footprint lane, `--doctor` = the component check
— **not** a subcommand tree; reusing `internal/bench` **unchanged**; the `--containerd` lane wired to ADR-0054's **embedded
image + managed containerd** (its own namespace); **removing** `cmd/funcd-bench`; re-pointing `just bench` → `funcd bench`;
refining ADR-0051's confinement (bench libs now legitimately in `funcd`, still barred from `funcdcli`); the dedicated-box
guard + `--doctor`'s running-daemon warning.

**Out:** changing the **measurement** (`internal/bench`'s lanes/metrics are reused as-is, ADR-0040/0051/0052); the
**`funcdcli bench`** client tool (ADR-0053 — stays separate, it answers a different question); going **stdlib** (dropping
fortio/gopsutil — the cleaner consolidation, but a rewrite, explicitly deferred per "keep it simple"); installing/spinning
any VM or shipping **Lima** (doctor suggests, never acts); stripping the bench from a hardened production build (deferred —
"one binary now, see how people react").

## Constraints & Decision drivers

- **One binary (decider's explicit call).** The bench rides in `funcd`; no build tag, no separate artifact. The caveat —
  dev/bench code + the bench libs in the production daemon — is **knowingly accepted** for the design phase.
- **Reuse, don't rebuild.** `internal/bench` (ADR-0040/0051/0052) is reused verbatim; this ADR is a **re-wiring** (a cobra
  entrypoint + the ADR-0054 runtime), not new measurement logic.
- **ADR-0054 makes it cheap.** The containerd lane's whole provisioning (buildah, local registry, image build, the
  `bench-containerd-homebox.sh` dance) **collapses** — it uses funcd's embedded image + managed containerd directly.
- **Confinement, refined not broken.** fortio/gopsutil now reach `funcd`'s dep graph (transitively via `internal/bench`) —
  this **refines ADR-0051's "never in `funcd`"** to "in `funcd` via the bench; **still out of `funcdcli`**." The `bench-libs`
  depguard rule keeps its substance (direct-import-only; `funcdcli` never imports `internal/bench`) — the only edit is
  dropping the now-dead `cmd/funcd-bench` exception (that dir is removed; the `internal/bench` exception stays).
- **A bench is a dedicated-box tool (physics, not packaging).** `funcd bench` stands up a *throwaway* platform instance;
  load+density on a *serving* node is exactly what you must not do — so it isolates its containerd namespace and `doctor`
  warns on a live daemon. (Same constraint as kubemark.)

## Alternatives considered

- **Keep `funcd-bench` a standalone binary** (or a separately-installed tool). *Rejected by the decider:* funcd *is* the
  platform the bench embeds, and ADR-0054 already puts the runtime in `funcd` — a second binary duplicates both. "One binary."
- **Put `funcd bench` behind a `//go:build bench` tag** (lean prod daemon; bench only in a bench build). *Rejected by the
  decider for now:* it splits the binary; "one binary, see how people react when we ship." (Revisitable later — the clean
  hardening path.)
- **Go stdlib (drop fortio/gopsutil; reuse `internal/loadgen` + `/proc`).** Consolidates to one load engine + keeps `funcd`
  lean. *Deferred, not chosen now:* it's a real rewrite of `internal/bench` (supersedes ADR-0051) and regresses the macOS
  process-lane (gopsutil gave cross-platform RSS). "Keep it simple" → keep the libs; revisit if `funcd`'s supply chain matters.
- **Fold into `funcdcli` instead of `funcd`.** *Rejected:* the sustainability bench *embeds the platform* — that's the
  daemon's domain, not the client CLI's; `funcdcli bench` (ADR-0053) is the *client* load tool and stays separate.

## Decision

1. **A single `funcd bench` cobra verb** on the funcd root (ADR-0042), mode selected by **mutually-exclusive flags** (not a
   subcommand tree): **bare `funcd bench`** = the in-process lane (memory+file substrate, RSS — ADR-0040, **the default**);
   **`--containerd`** = the cgroup-footprint lane (ADR-0052); **`--doctor`** = check + guide (never install/VM/Lima). Passing
   more than one mode flag is a clean `api/fault` usage error. It calls `internal/bench` **unchanged**.
2. **The `--containerd` lane reuses ADR-0054.** It runs over funcd's **embedded image + privately-managed containerd**, in its
   **own containerd namespace** — no buildah, no registry, no image build. (Build-depends on ADR-0054's implementation.)
3. **Keep the bench libs (option a).** fortio + gopsutil stay (no `internal/bench` rewrite). They now ride in `funcd`'s dep
   graph via `internal/bench`; **ADR-0051's confinement is refined** — bench libs are allowed in `funcd`, **kept out of
   `funcdcli`** (which never imports `internal/bench`). The `bench-libs` depguard rule keeps its substance (still bars *direct*
   gopsutil/fortio imports in shipped code); its **only** edit is removing the now-dead `cmd/funcd-bench` exception.
4. **Remove `cmd/funcd-bench`.** Its `main` becomes the `funcd bench` verb; `internal/bench` (the harness) stays. `just bench`
   → `funcd bench`. (Supersedes ADR-0040's standalone-binary *packaging* only.)
5. **Dedicated-box guard.** The `--containerd` lane uses an isolated containerd namespace (never the live daemon's);
   `--doctor` warns when a `funcd` daemon is already running; the docs state plainly: run `funcd bench` on a **dedicated/idle** box.
6. **`funcdcli bench` (ADR-0053) is untouched** — the client load tool, separate role.

## Contracts

```go
// cmd/funcd — ONE bench verb, mode chosen by mutually-exclusive flags (thin: cobra → internal/bench, reused unchanged).
func (a *app) benchCmd() *cobra.Command   // `funcd bench`, registered in newRootCmd's AddCommand
//   local flags: --containerd (bool) | --doctor (bool)  — at most one set; both set → fault.Invalid.
//   bare (neither flag) → bench.Run (memory+file, RSS); --containerd → bench.RunContainerd; --doctor → component check.
//   --doctor's "is a funcd daemon already running?" warning probes a concrete signal: a dial to the default
//   control-plane address (ADR-0024), falling back to presence of the ADR-0054 private containerd socket.
//   The --containerd lane gets its containerd socket + curated image from ADR-0054's runtime Manager
//   (the same Manager cmd/funcd uses to serve), in a dedicated bench namespace — NOT a fresh buildah/registry.
```

**Dependencies & I/O:** **no new module.** `cmd/funcd` now imports `internal/bench` (→ gopsutil + fortio enter `funcd`'s
graph; `funcdcli`'s graph is unchanged). Consumes the same knobs ADR-0040/0052 defined (substrate, concurrency, duration,
density, out-dir, the containerd knobs) as cobra flags on the one verb, alongside the `--containerd`/`--doctor` mode flags.
Reuses ADR-0054's `Manager` for the containerd socket + embedded image.

## Implementation plan

- `cmd/funcd/bench.go`: one `benchCmd()` with `--containerd`/`--doctor` bool flags (mutually exclusive — both set →
  `fault.Invalid`); register in `newRootCmd`. Map the old `cmd/funcd-bench` flags onto the single verb. The bare default
  calls `bench.Run`; `--containerd` calls `bench.RunContainerd`; `--doctor` calls a check that reports component presence
  (reusing the `RunContainerd` skip-reason logic) + warns on a running daemon. **`--containerd` passes ADR-0054's
  `Manager`-provided socket + a dedicated namespace** (replacing the homebox script's buildah/registry/`--image-prefix`).
- Delete `cmd/funcd-bench/`; move any shell-only logic (none of substance) into the verb.
- `justfile`: `bench` → `go run ./cmd/funcd bench …`; drop/retarget `bench-containerd*` recipes at `funcd bench --containerd`
  (the homebox script's provisioning is no longer the bench's path — it stays only as a dev helper for a bare box, if kept at all).
- `.golangci.yml`: **one edit** — drop the now-dead `!**/cmd/funcd-bench/**` exception from the `bench-libs` rule (that dir is
  removed). The substance is unchanged: the `internal/bench/**` exception stays, and `cmd/funcd/bench.go` imports
  `internal/bench` (not the libs directly), so it needs no exception; `funcdcli` stays clean.
- **Test plan.** Non-gated (`just ci`): `funcd bench --help` shows the one verb + the `--containerd`/`--doctor` flags (cobra
  wiring); `funcd bench --containerd --doctor` → `fault.Invalid` (mutual exclusion); `funcd bench --doctor` on a box without
  containerd reports "not available" + a non-empty reason, exit 0 (the skip-path), and warns generically if a daemon is
  detected; `go list -deps ./cmd/funcdcli/...` still excludes gopsutil/fortio (the confinement that's *kept*). Deferred to the
  Linux integration lane (`FUNCD_IT=1`, root, **+ ADR-0054 implemented**): `containerd-lane-reuses-embedded-runtime`
  end-to-end (real managed containerd + embedded image + a function). **DoD:** `go build` · `golangci-lint` (rule substance
  intact, dead `cmd/funcd-bench` exception dropped) · `go test` green · `cmd/funcd-bench` gone · `go list -deps` shows libs in
  `funcd`, absent from `funcdcli` · no identity/path leak.

## Review checklist

- [ ] `funcd bench` is a **single** cobra verb; mode is `--containerd`/`--doctor` flags (bare = in-process default), **not** a
      subcommand tree; both flags set → `fault.Invalid`; `cmd/funcd-bench` removed; `just bench` retargeted to `funcd bench`.
- [ ] bare + `--containerd` reuse `internal/bench` unchanged; `--containerd` uses ADR-0054's managed containerd + embedded
      image (dedicated namespace), **not** buildah/registry.
- [ ] `--doctor` checks + guides only — no install, no VM, no Lima; warns on a running daemon.
- [ ] `go list -deps ./cmd/funcd/...` includes gopsutil/fortio (via the bench); `./cmd/funcdcli/...` **excludes** them; the
      `bench-libs` depguard rule keeps its substance (dead `cmd/funcd-bench` exception dropped, `internal/bench` exception kept).
- [ ] Dedicated-box caveat documented; the `--containerd` lane isolates its namespace.
- [ ] Non-gated flag/`--doctor`/import-graph tests pass in `just ci`; the real-container e2e deferred (needs ADR-0054); no leak.

## Consequences

- **One binary**: `funcd` serves *and* benches itself; no `cmd/funcd-bench`. The containerd footprint is now `funcd bench
  --containerd` on any funcd box — **zero provisioning** (it reuses the embedded runtime), so the buildah/registry/Lima
  homebox dance disappears from the bench path.
- **The production daemon carries dev/bench code + fortio/gopsutil** — knowingly accepted for now; the `//go:build bench`
  tag or the stdlib consolidation are the documented hardening paths for "when we ship to everyone."
- **ADR-0051's invariant is narrowed** (libs out of `funcd` → out of `funcdcli` only); the depguard rule still enforces the
  `funcdcli` half + bars direct bench-lib imports elsewhere.
- **`funcd bench` is a dedicated-box tool** (it stands up a throwaway platform) — not for live serving nodes; isolated
  namespace + a `doctor` warning make that safe-by-default, the docs make it explicit.

## Open questions

- **Implementation order:** the bare in-process lane + `--doctor` + the cobra wiring can land **before** ADR-0054; the
  `--containerd` lane's "reuse the embedded runtime" part **build-depends on ADR-0054's `Manager`**. Sequenced at impl
  (default lane + `--doctor` first, `--containerd` lane after ADR-0054).
- **Fate of `scripts/bench-containerd-homebox.sh`:** once the containerd lane uses the embedded runtime, the script's
  provisioning is redundant for the bench; keep it only as a bare-box dev helper, or retire it. Decided at impl.

## References

- ADR-0040 (the harness, packaging superseded), ADR-0051 (confinement refined), ADR-0052 (the containerd footprint lane),
  ADR-0053 (the *client* `funcdcli bench`), ADR-0054 (the embedded runtime + managed containerd this reuses), ADR-0042
  (cobra), ADR-0026 (one binary). kubemark — the precedent for an embedded "platform benches itself" rig run on a dedicated box.
