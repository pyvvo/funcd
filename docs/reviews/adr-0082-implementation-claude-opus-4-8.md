# ADR-0082 implementation review — the provider model: built-in/add-on catalog (`claude-opus-4-8`)

- **ADR**: [0082](../adr/0082-provider-model-catalog.md) — provider model, structural/refactor, behavior-preserving
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD 10/10, no Blockers/Majors) · 2026-06-29

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| `go build ./...` | **OK** (pinned `go1.26.4`) |
| `go test ./...` | **ALL PASS** (full suite; `internal/provider` + `pkg/funcd` `ok`) |
| `go tool golangci-lint run` (changed pkgs) | **0 issues** |
| `go mod verify` | all modules verified; **go.mod/go.sum untouched** (zero new deps) |
| **leaf invariant** | `go list -f '{{.Imports}}' ./internal/provider` → `api/fault sort` — imports **nothing** from `internal/`; cataloguing adds no import edge to the providers it names |
| **behavior-preserving (tree diff)** | only `internal/provider/*` (new) + `pkg/funcd/{funcd.go,providers.go}` (composition-root wiring) + `docs/*` touched; **no existing provider package edited** (`git diff` clean on `internal/{kvstore,blob,gateway,secrets,eventing}`) — and the full suite stays green |
| **catalog ≡ blueprint** | built-ins = kv, blob, secrets, eventing, invoke, ingress, egress, s3, log-ingest; add-ons = catalog-query (F48), observability-serving (F54); **`bus` excluded** (internal substrate, not function-bindable) — matches the blueprint provider model exactly |

## Conformance to the ADR Review checklist (10/10)

1. **Leaf** — `internal/provider` imports nothing from `internal/` (verified by `go list`). ✓
2. **Immutable `Catalog`, built by `New`; typed `Kind` enum; no package-level mutable state** (`provider.go`: unexported `descriptors`/`byName`, `Kind`/`Valid`). ✓
3. **`New` rejects a duplicate `Name` and an invalid descriptor with `fault.Invalid`** — `New` runs `Validate` per descriptor then dup-checks (`TestScenarioDuplicateNameRejected`, `TestNewValidatesDescriptors`). ✓
4. **`ByKind` partitions built-in vs add-on; `All`/`ByKind` stable-ordered** (sorted by Name; `TestScenarioCatalogEnumeratesBuiltins`/`…ClassifiesAddons` assert order). ✓
5. **Catalog covers every known provider with the right `Kind`; `bus` excluded** (`pkg/funcd/providers.go`). ✓
6. **Each descriptor `Port`/`Bindings` names the four-part shape; no facade/port renamed or moved** (`kvstore.KV`/`blob.Bucket`/`gateway.Gateway` are real types; tree diff shows zero edits to those packages). ✓
7. **Daemon logs the catalog at startup** (`logProviders` called in `Run` after "platform starting"; emits `builtin`/`addon` name lists). ✓
8. **Behavior-preserving** — no existing provider package edited; full suite passes unchanged. ✓
9. **Pure-Go, zero new deps** (`go mod verify`; go.mod untouched). ✓
10. **One passing acceptance test per Scenario** — `catalog-enumerates-builtins`, `catalog-classifies-addons`, `descriptor-names-the-shape`, `duplicate-name-rejected` in `internal/provider/provider_test.go`; the startup-log + behavior-preserving scenarios verified by the `pkg/funcd` suite + the tree diff. ✓

## ✅ Verified correct (what's strong — keep it)

- The **leaf-by-construction** design (descriptors declared at the composition root, `internal/provider` a pure types package) is exactly what makes "behavior-preserving, zero churn" *true* and not merely claimed — verified against the real import graph.
- `Port`-not-`Contract` naming avoids the real `internal/contract` clash and names actual types.
- The catalog membership matches the blueprint after the judge's M1 fix (bus excluded; eventing/invoke present) — the one place this artifact must mirror the blueprint, and it does.

## Findings

- **Blockers / Majors**: none.
- **Note (env / pre-existing, `env`-attributed — not ADR-0082):** the full suite initially showed one failure,
  `internal/platform/version/artifacts_test.go:45` asserting `build.sh` contains `"internal/version"`. This is a
  **stale assertion from the kernel-move commit 811dfb1** (which moved the package to `internal/platform/version`
  and updated `build.sh`'s `PKG`, but not this test). It is unrelated to ADR-0082 (which touches neither `version`
  nor `build.sh`); fixed in passing (`internal/version` → `internal/platform/version`) so the suite is green. Not a
  model defect of this implementation.

## Recommendation

**pass** — DoD 10/10, no Blockers or Majors, behavior-preserving verified against the real code. Stamp ADR-0082
`Reviewing → Implemented` and FEAT-0001/F56 → `implemented`.
