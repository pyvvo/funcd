# ADR-0122 implementation review — claude-opus-4-8

**Verdict: pass** (reduced push/dev-config scope).

## Context

ADR-0122 was accepted this session as a manifest that compiles to the `Function` CRD, then **re-scoped by decider
override** (same session, pre-commit) down to the **client push/dev config**: `funcdctl.yaml` = `{runtime, handler,
bindings, contract}`, driving `funcdctl push` (bake the schema-only contract) and `funcdctl types`, with **no**
`Compile()`→CRD and **no** `apply -f funcdctl.yaml`. The implementation reviewed here is that reduced scope — a strict
reduction of the earlier, independently-reviewed full impl (the removed `Compile()`/apply paths + deploy-knob fields;
the retained core — `LoadManifest`/`ContractSides`/`GenerateTypes`/push-from-manifest/`types` — is unchanged from that
prior passing review).

## Verification (captured)

- `nix develop -c go build ./...` → exit 0.
- `nix develop -c go test ./pkg/sdk/... ./cmd/funcdctl/...` → `ok` (both packages). Full `go test ./...` +
  `golangci-lint run ./...` + `go mod verify` reported exit 0 by the implement step.
- Conformance spot-checks: `pkg/sdk/manifest.go` has **no** `Compile()` (removed); `funcdctl.yaml` top-level keys are
  exactly `runtime`/`handler`/`bindings`/`contract`; both example manifests are **block-style YAML** (no `{}`/`[]`
  flow); `funcdctl apply` reverted to the plain-resource decode (no `funcdctl.yaml`→Function path).
- Identity/path-leak grep over changed code files: clean.

## ✅ Verified correct — keep

- **`pkg/sdk` import discipline**: no `internal/*` import; the `contract.Check` profile gate stays in the
  `cmd/funcdctl` layer.
- **Reduced surface matches the reduced ADR**: manifest is authoring-only (`runtime`/`handler`/`bindings`/`contract`);
  deploy knobs (`name`/`namespace`/`scaling`/`pooling`/`image`) and the CRD-compile/apply are gone; `bindings` retained
  for `funcdctl types` (and the coming `funcdctl dev`).
- **On-artifact contract unchanged**: `funcdctl push` still gates each side (`contract.Check`) and bakes the ADR-0059
  `{dialect,input,output}` blob schema-only — the worker validates it via ADR-0123.
- **Scenarios covered**: push-from-manifest, types-python, types-node, out-of-profile-rejected — each a named passing
  test; the dropped `manifest-compiles-to-function`/apply tests were removed with the feature.

## Findings

None (Blocker/Major/Minor): the change is a scope reduction of already-reviewed, passing code, green on the sub-checks
and conformant to the overridden ADR. `funcdctl dev` and any binding-source unification are explicitly deferred
(ADR-0122 Open questions), not gaps in this scope.

## Attribution

All observations `model`-clean (0 model-attributed findings). The scope change is `adr`/decider-driven (the override),
not an implementation defect.
