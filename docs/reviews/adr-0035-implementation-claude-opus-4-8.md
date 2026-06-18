# ADR-0035 Implementation Review — Artifact tag→digest resolution at Revision stamp

**Verdict**: **pass** — the user can deploy an OCI function with **no digest**; the reconciler
resolves the tag → digest and pins it into the immutable Revision, the spec stays digest-free, a
moved tag never drifts a stamped Revision, explicit digests are honored, and an unresolvable ref
fails closed. No new dependency.
**Producing model**: claude-opus-4-8 · **ADR status at review**: Reviewing

## Evidence (four sub-checks)

- `go build ./...` exit 0; `go tool golangci-lint run ./...` → `0 issues`; `go mod verify` →
  verified, `go.mod`/`go.sum` unchanged (reused oras `Resolve`). Identity grep clean.
- `go test ./...` green (the one `TestScenarioCLIDelete` flake is ephemeral-port exhaustion under
  the full suite — passes `-count=1` in isolation; unrelated to this change).

## Scenario → test (all pass)

| Scenario | Test |
|---|---|
| deploy-without-digest | `internal/function` `TestScenarioDeployWithoutDigest` — resolved+pinned in Revision; **spec stays digest-free** |
| tag-move-does-not-drift | `…TestScenarioTagMoveDoesNotDrift` — existing Revision never re-resolved (resolver called once) |
| explicit-digest-honored | `…TestScenarioExplicitDigestHonored` — resolver not called |
| unresolvable-ref-fails | `…TestScenarioUnresolvableRefFails` — `Phase=Failed`, reason `ArtifactUnresolved`, never materialized |
| resolve-tag-to-digest | `internal/artifact` `TestScenarioResolveTagToDigest` — `Resolve` returns the pushed digest |
| e2e (no-digest) | `tests/e2e` `TestE2EUserJourney` — `funcdcli apply` with **no digest** → Ready → HTTP invoke |

## Judge findings verified folded

- **B1 (materialize mechanism)** — `converge` materializes a **copy** (`mfn := *fn;
  mfn.Spec.Artifact.Digest = pinnedDigest`); the original `fn` is never mutated, so `store.Update`
  persists only Status, not the resolved digest (proven by `…DeployWithoutDigest` asserting the
  stored spec digest stays empty). No `Materializer` signature change.
- **M1 (no-drift ordering)** — resolve+pin happens only on `ensureRevision`'s create path; the
  early-return path returns the existing Revision's digest (proven by `…TagMoveDoesNotDrift`).
- **M2 (fail-closed)** — a resolver error/empty → `errArtifactUnresolved` → `Failed`; `Pull`'s
  empty-digest rejection stays as the backstop.

## ✅ Verified correct — keep it

- The `ArtifactResolver` seam is auto-wired by a type-assert (`materializer.(ArtifactResolver)`),
  so OCI mode resolves and file:// mode (no resolver) is unchanged — the existing function/artifact
  tests still pass (no regression).
- Blueprint synced (digest auto-pinned at Revision); demo updated to the no-digest flow
  (`function.yaml` drops the digest, `demo.yaml` carries `artifactRef`).

## Findings

🔴/🟡 None. **Minor (env):** the e2e + the OCI seam tests are node-/layout-gated as before.

**Recommendation**: stamp **Implemented**.
