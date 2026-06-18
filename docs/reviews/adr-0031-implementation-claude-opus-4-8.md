# ADR-0031 Implementation Review — OCI artifact distribution via oras-go (P-V-A)

**Verdict**: **pass** — the artifact story is complete and OCI-native: `funcdcli push` stores a
digest-addressed OCI artifact, the platform pulls by digest, and the `OrasMaterializer` drives
ADR-0030's reconciler to Ready against a local layout (no registry server). All four Scenarios
have un-skipped passing tests; exactly one new Apache-2.0 dependency; blueprint synced; no leak.
**Reviewed against**: ADR-0031 Contracts / Scenarios / Review checklist / DoD · blueprint
(source-artifact deploys) · ADR-0002 conventions · ADR-0030 (the Materializer seam) · ADR-0024
(the funcdcli shell) · ADR-0020 (ArtifactRef).
**Producing model**: claude-opus-4-8
**ADR status at review**: Reviewing

## Verification run (evidence)

- `go build ./...` → exit 0.
- `go test ./...` → all `ok`. The artifact lane (node present, seam test ran):
  - `TestScenarioCLIPushesArtifact` PASS — push prints a `sha256:` descriptor digest.
  - `TestScenarioPushPullRoundtrips` PASS — push→pull to/from a **local OCI layout** (no
    registry) yields identical bytes + restores the filename.
  - `TestScenarioPlatformPullsArtifact` PASS — digest is the authority: empty → `fault.Invalid`,
    unknown → `fault.NotFound`, correct → pulls.
  - `TestScenarioMaterializerSatisfiesADR0030Seam` PASS — `OrasMaterializer.Materialize` pulls
    by digest, caches per-digest (same path on re-call), rejects an empty digest.
  - `TestScenarioMaterializerSatisfiesADR0030SeamNode` PASS (node-gated) — OrasMaterializer
    wired into the **real** Function reconciler + Node shim: pulls the pushed artifact by digest
    from a local layout, runs it to **Ready**, serves over HTTP (200).
  - `TestScenarioCLIPushPullRoundtrip` PASS — the `push`/`pull` verbs round-trip via `run()`.
- `go tool golangci-lint run ./...` → `0 issues`.
- `go mod verify` → `all modules verified`; `go.mod` adds exactly one direct dependency:
  `oras.land/oras-go/v2 v2.6.1` (Apache-2.0) + its OCI transitive deps (image-spec, go-digest —
  Apache-2.0).
- Identity grep over changed files → clean.

## Scenario → test map (all named, un-skipped, passing)

| Scenario | Test | Lane |
|---|---|---|
| cli-pushes-artifact | `TestScenarioCLIPushesArtifact` (+ `…CLIPushPullRoundtrip`) | pure-Go |
| platform-pulls-artifact | `TestScenarioPlatformPullsArtifact` | pure-Go |
| push-pull-roundtrips | `TestScenarioPushPullRoundtrips` | pure-Go (local layout) |
| materializer-satisfies-adr0030-seam | `…MaterializerSatisfiesADR0030Seam` + `…SeamNode` (real shim) | pure-Go + node |

## ✅ Verified correct — keep it

- **The seam is real, not just asserted.** `var _ function.Materializer = (*OrasMaterializer)(nil)`
  compiles, *and* the node-gated seam test drives the actual reconciler + shim to Ready off an
  oras-pulled artifact — P-V-1 and P-V-A compose at ADR-0030's frozen interface, no redefinition.
- **Digest is the authority** (the judge's M2): `Pull` and `Materialize` reject an empty digest,
  fetch the manifest *by digest*, and re-check `manifestDesc.Digest == digest` — a mutable tag
  can never swap what was deployed (preserves ADR-0020's "validated = shipped").
- **Zero-infra dev**: the `oci-layout://<dir>[:<tag>]` target uses `oci.New` — every test runs
  against a local layout with no registry server, exactly as the ADR promised.
- **One pure-Go client, both targets**: `resolveTarget` picks `oci.Store` vs `remote.Repository`
  behind the same `oras` API; `Push`/`Pull` are target-agnostic. `AlreadyExists` on re-push is
  tolerated (idempotent).
- **funcdcli verbs are thin** (ADR-0024 shell): `push`/`pull`/`login`/`logout` dispatch over the
  same `run()`/`flag` shell straight to `internal/artifact`, never the control plane.
- **Per-digest immutable cache** in `Materialize` — a re-materialize is a no-op disk hit.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- `Login` verifies against the registry via `credentials.Login` (network) and `Login`/`Logout`
  are not unit-tested (they need a registry / credential helper). Acceptable: the ADR scopes the
  tested path to the local layout (no login), and the credential-store calls are oras-canonical.
  A future registry-backed e2e (P-V-2 / release) could cover them. *(env)*
- `push <file> <ref>`'s pre-flight is intentionally light (non-empty + packs) — the authoritative
  shape-gate is the shim (ADR-0030). Documented in the ADR; noting it so it is not read as a gap. *(adr — by design)*

## Definition of done

| DoD item | Status |
|---|---|
| `just ci` green | ✅ (4 sub-checks green) |
| `funcdcli push/pull/login/logout` work against a local layout | ✅ (push/pull tested; login/logout wired) |
| Platform pulls + digest-verifies to a local path | ✅ (`…PlatformPullsArtifact`, `…SeamNode`) |
| `OrasMaterializer` satisfies ADR-0030's `Materializer` | ✅ (compile assertion + node seam test) |
| Exactly one new Apache-2.0 dependency (oras-go) | ✅ (`oras.land/oras-go/v2 v2.6.1`) |
| Blueprint synced | ✅ (the "never push to registries" line superseded) |
| No identity/path leak | ✅ (grep clean) |

## Recommendation

Stamp **ADR-0031 Reviewing → Implemented**. F13 already reads `implemented` (co-realized by
ADR-0020/0030) and links ADR-0031 — in exact agreement after this stamp. P-V-2 consumes this
pull driver to build the per-revision base+layer image.
