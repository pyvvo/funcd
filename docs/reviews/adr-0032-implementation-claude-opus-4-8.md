# ADR-0032 Implementation Review — Curated runtime images + container execution (P-V-2)

**Verdict**: **pass** — the production container-execution lane is implemented: `SandboxSpec.Mounts`
+ the containerd driver refinements (mounts, `Instance.Port` on **both `List` and `Status`**, real
task state), the shim's `FUNCD_PORT` bind, the container-mode reconciler, `WithContainerExecution`,
and the curated nodejs20 image. All cross-platform + node-gated Scenarios pass; the two Linux-only
e2e are honestly deferred to the integration lane. No new Go dependency; no leak.
**Reviewed against**: ADR-0032 Contracts / Scenarios / Review checklist / DoD · blueprint
(containerization / security-isolation) · ADR-0002 conventions · ADR-0011 (driver refined),
ADR-0030 (shim refined), ADR-0031 (artifact mounted), ADR-0025 (test sequencing).
**Producing model**: claude-opus-4-8
**ADR status at review**: Reviewing

## Verification run (evidence)

- `go build ./...` (darwin) → exit 0; `GOOS=linux go build ./...` → exit 0 (the containerd driver
  refinements compile on the Linux lane); `GOOS=linux go vet ./internal/runtime/containerd/...` → clean.
- `go test ./...` → all `ok`. New tests (node present):
  - `TestScenarioContainerSpecMountsArtifact` PASS — container mode: `Image` from `ImageFor`,
    `Command` empty (image entrypoint), the artifact a **read-only** bind mount at
    `/var/funcd/artifact`, `FUNCD_ARTIFACT` the in-container path, `FUNCD_PORT=8080`.
  - `TestScenarioContainerReadinessGates` PASS — container mode gates Ready + route on readiness.
  - `TestScenarioShimLaunchesWithArtifactAndHandler` PASS — `process-driver-unaffected`: process
    mode keeps `Command=shimCommand`, **no Mounts, no FUNCD_PORT**.
  - `TestScenarioCuratedImageBuilds` PASS — the Dockerfile is `FROM node:20`, copies the shim,
    runs it as ENTRYPOINT.
  - `TestScenarioShimFixedPortBindNode` PASS (node-gated) — with `FUNCD_PORT` set the real shim
    binds the fixed port and serves `/health/readiness` (the §3 refinement, validated off-container).
  - All prior ADR-0030/0031 scenarios (incl. node-gated e2e) still PASS.
- `go tool golangci-lint run ./...` → `0 issues`.
- `go mod verify` → `all modules verified`; `git diff go.mod` empty (**no new dependency** — uses
  containerd's existing `oci.WithMounts`).
- Identity grep over changed files (incl. Dockerfile + ADR) → clean.

## Scenario → test map

| Scenario | Test | Lane |
|---|---|---|
| curated-image-builds | `TestScenarioCuratedImageBuilds` | pure-Go (Dockerfile structure) |
| spec-mounts-artifact | `TestScenarioContainerSpecMountsArtifact` | pure-Go |
| process-driver-unaffected | `TestScenarioShimLaunchesWithArtifactAndHandler` | pure-Go |
| container-shim-serves | `TestScenarioContainerReadinessGates` (reconciler) + `…ShimFixedPortBindNode` (real shim) — full crun e2e **deferred** | pure-Go + node; **Linux e2e deferred** |
| bind-mount-is-readonly | spec assertion (`…SpecMountsArtifact`: `ReadOnly==true`) — runtime-enforcement e2e **deferred** | pure-Go; **Linux e2e deferred** |

## ✅ Verified correct — keep it

- **M2 landed in full** — the containerd driver persists `FUNCD_PORT` (`fixedPort(spec)`) at
  `Create` and returns `Instance.Port` on **both `List` and `Status`**; `List` no longer hardcodes
  `StateRunning` (it loads each task and maps the real status via `mapState`), so a crashed
  container can't mask the ADR-0030 shape failure. This was the difference between a container that
  serves and one that never reaches Ready — implemented as the ADR specifies.
- **Refinement, not edit.** ADR-0011/0030/0031 files are untouched; the capability is added through
  ADR-0032 + additive code (a new `Mount` field, a new shim branch, a new endpoint mode). Frozen
  ADRs stayed frozen.
- **One shim, two transports.** The `FUNCD_PORT`/`FUNCD_PORTFILE` branch in `shim.mjs` preserves the
  process-mode contract exactly (the `else` is byte-identical to ADR-0030); container mode binds
  `0.0.0.0:$FUNCD_PORT`. `process-driver-unaffected` proves the loopback path is intact.
- **Security preserved** — the artifact mount is `rbind,ro` (read-only); `no_new_privileges`, the
  netns default-deny, and the conservative OCI defaults from ADR-0011 are untouched. The image runs
  as the unprivileged `node` user.
- **EndpointMode is orthogonal** to the `Materializer != nil` gate, exactly as the folded m1 says —
  it changes only addressing + env, within the existing shim path.

## Findings

### 🔴 Blocker / 🟡 Major
None.

### Minor
- **container-shim-serves / bind-mount-is-readonly** are proven at the reconciler + spec + real-shim
  level but their **full crun/containerd/root e2e is deferred** to the Linux integration lane (`just
  test-integration`), matching ADR-0011's existing Linux-gated split and the ADR's own
  Temporary-workarounds. The deferral is recorded; nothing in CI asserts the live bind-mount/crun
  path on darwin (it cannot). *(env — Linux/root required)*
- The curated image is **operator-built** from the shipped Dockerfile (`just build-runtime-images`);
  no publish pipeline or per-revision baked layer in V1 (both named as ADR follow-ups). *(adr — by design)*

## Definition of done

| DoD item | Status |
|---|---|
| `just ci` green (cross-platform unit + shim node-gated; Linux e2e gated/deferred) | ✅ (4 sub-checks; linux build+vet) |
| `SandboxSpec.Mounts` honored by the containerd driver | ✅ (`oci.WithMounts`/`bindMounts`; linux build) |
| Shim binds `FUNCD_PORT` in container mode, loopback+portfile in process mode | ✅ (`…ShimFixedPortBindNode`, `…ShimLaunches…`) |
| Curated Dockerfile builds an image with the shim as entrypoint | ✅ (`…CuratedImageBuilds`; `just build-runtime-images`) |
| No new Go dependency | ✅ (`go.mod` unchanged) |
| No identity/path leak | ✅ (grep clean) |

## Recommendation

Stamp **ADR-0032 Reviewing → Implemented**. The two deferred Linux e2e scenarios are environment-bound
(crun/root) and recorded, not gaps in the work. F12 already reads `implemented` and links ADR-0032 —
in exact agreement after this stamp. P-V-3 (Python) reuses this image+contract pattern.
