# ADR-0079: Resource rename — `Config` → `ConfigMap`

- **Status**: Implemented (2026-06-23)
- **Date**: 2026-06-23 (judged 2026-06-23 — right decision, ADR-0045 kind-rename model applied; folded 1 Blocker
  (the blanket `s/\bConfig\b/ConfigMap/g` would corrupt ~139 unrelated `Config` tokens across 7 other types +
  `internal/config`'s own `type Config` → replaced with a **qualified-symbol** rename: scoped bare-`Config` only
  in `api/types/v1alpha1/*.go`, `v1.Config` elsewhere, explicit control-plane handler names, + a byte-identical
  `internal/config` guard) + 2 Majors (add `docs/PROJECT-SUMMARY.md` to scope; make the token-precise gate the
  authoritative file list — ~16 Go, not "14") + minors. Verification gate confirmed precise (`\bConfig\b` does
  not match `FuncdConfig`); refines (not supersedes) ADR-0003; OpenAPI regen via `just generate`.)
- **Deciders**: green-0-rabbit
- **Tags**: resource, rename, api, configmap, openapi
- **Realizes**: [FEAT-0000/F03](../feat/0000-feat-v1.md) (the resource-model kind list it renames a kind in)
- **Relates to**: [ADR-0003](0003-resource-model-and-api-typing.md) (defined the `Config` kind — this **refines
  that name**; the resource model + typing stand), [ADR-0045](0045-rename-sandbox-to-worker.md) (the kind-rename
  + OpenAPI-regen + grep-gate pattern this follows), [ADR-0005](0005-api-surface-code-first-huma.md) (huma
  reflects the OpenAPI from the Go types — `just generate`), [ADR-0024](0024-funcdcli-and-sdk.md) (the SDK
  kind→plural map)

## Context & Need

The resource kind **`Config`** is a namespaced, **pure-data** named map (`spec.data map[string]string`; no
status, no reconciler — a 21-line type). Its name is **overloaded**: funcd *also* has **`FuncdConfig`** — the
daemon config file (`funcdconfig.yaml`, `internal/config`), a different thing that is **not** a metastore
resource. Renaming the resource to **`ConfigMap`** (a) **disambiguates** it from `FuncdConfig` and (b) aligns
with the kubectl-style model users expect (k8s `ConfigMap`).

It is **behavior-preserving** — same resource, same `spec.data`, same metastore persistence/RV/watch — but,
unlike the CLI rename (ADR-0078), it touches the **API surface**: the REST plural `configs` → `configmaps`, the
**OpenAPI** spec, and the SDK kind map. ADR-0003 named the `Config` kind (frozen); per ADR-0045, a rename is a
recorded decision: the frozen ADRs keep `Config` as history, this ADR is their forward pointer, and
newest-Accepted-wins carries the new name into the living surfaces.

## Scenarios

- **scenario: kind-registered-as-configmap** — Given the api package, When kinds are registered, Then
  `KindConfigMap = "ConfigMap"` is present and there is **no** `Config` kind / `KindConfig` constant.
- **scenario: api-surface-renamed** — Given the regenerated OpenAPI + SDK, When a client lists the resource,
  Then the REST plural is **`configmaps`** (path `…/namespaces/{ns}/configmaps`), not `configs`.
- **scenario: round-trip-unchanged** — Given a `ConfigMap` with `spec.data`, When it is created/read/watched
  through the metastore, Then resourceVersion, generation, and watch behave **identically** to the former
  `Config` (behavior-preserving).
- **scenario: cli-uses-configmap** — Given `funcdctl`, When a user runs `funcdctl apply` / `funcdctl get
  configmap`, Then it operates on the renamed resource (the former `funcdctl get config`).
- **scenario: no-live-config-kind** — Given the implemented rename, When the **token-precise** gate runs
  (`\bKindConfig\b`, `\bv1\.Config\b`, `^kind: Config$`), Then it returns **zero** live matches — and
  **`FuncdConfig` is untouched** (still present, unchanged).
- **scenario: metastore-lane-green** — Given the rename, When `just lima-example-metastore` applies a
  `ConfigMap`, restarts the daemon, and reads it back, Then it recovers and ends `final status: PASS`.

## Scope

**In** — rename the **resource** kind/type `Config` → `ConfigMap` across the live (tracked) surfaces:
- **api/types/v1alpha1** — `config.go` → `configmap.go` (`Config` → `ConfigMap`, `ConfigSpec` → `ConfigMapSpec`,
  the `GVK`/`Validate` methods); `metadata.go` (`KindConfig Kind = "Config"` → `KindConfigMap Kind = "ConfigMap"`;
  the kind lists, the GVK `switch`, `pureKinds` membership).
- **The Go files the token-precise gate flags** (~16, the gate is authoritative): the api type + its tests,
  `internal/controller` tests, `internal/controlplane/{handlers,routes_rest,controlplane,stubs}.go` (the CRUD
  dispatch + the `*Config` handler funcs/DTOs/`Tags`), `internal/store` tests + `storecontract/contract.go`,
  `pkg/funcd` tests, `pkg/sdk/kinds.go`.
- **SDK + OpenAPI** — `pkg/sdk/kinds.go` (`KindConfig: {"configs", true}` → `KindConfigMap: {"configmaps", true}`);
  **regenerate** `api/openapi/funcd.v1alpha1.yaml` via `just generate` (huma reflection, ADR-0005) and commit it
  (the `just ci` generate-staleness gate must pass).
- **Lanes / fixtures / docs** — `e2e/fixtures/metastore-config.yaml` → `metastore-configmap.yaml`
  (`kind: ConfigMap`) + the `lima-example-metastore` recipe's `cp`; `e2e/metastore.venom.yml`
  (`funcdctl … apply -f …/metastore-configmap.yaml`, `funcdctl get configmap persisted`, + the testcase/`info:`
  prose `…a Config`→`…a ConfigMap`); `blueprint.md` (the Resources §"Config" kind), `docs/PROJECT-SUMMARY.md` (the
  resource callout + the kind list — **not** the ADR-row rows that quote a frozen ADR's title), `docs/feat/0000`
  (the F03 kind list) + any example/doc referencing the `Config` resource; append ADR-0079 to F03.

**Out**: the **daemon config `FuncdConfig`** (`funcdconfig.yaml`, `internal/config`) and the daemon config
**keys** (`config.*`) — a separate concern this rename **disambiguates from**, never touched; the resource
**semantics** (a pure-data map — unchanged); the **frozen ADRs' text** (`docs/adr/*`) + `docs/reviews/*`.

## Constraints & Decision drivers

- **Behavior-preserving** — same resource semantics; `go test ./...` stays green with unchanged assertions
  (only the kind-name token differs in fixtures), per ADR-0045.
- **API-surface change → OpenAPI regen** — `just generate` must run and the regenerated
  `api/openapi/funcd.v1alpha1.yaml` committed (the `just ci` staleness check fails otherwise).
- **Token-precise sweep — must NOT touch `FuncdConfig`** — `\bConfig\b` does **not** match inside `FuncdConfig`
  (`d`+`C` is no word boundary), and `\bConfigSpec\b` is swept separately; a naïve `s/Config/ConfigMap/g` would
  corrupt `FuncdConfig` → `FuncdConfigMap` and is **forbidden**.
- **Sweep tracked files only** (`git ls-files`) path-excluding `docs/adr` + `docs/reviews`; frozen ADRs immutable.
- **Zero new deps.**

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| The kind name | **`ConfigMap`** (singular) | **`Config`** (status quo) — overloaded with `FuncdConfig`; non-k8s. **`ConfigMaps`** — breaks the singular-PascalCase kind convention every funcd kind follows (`Function`, `Secret`, `KVStore`). **`Configuration`** — verbose, not the k8s term. |
| `ConfigSpec` | **Rename → `ConfigMapSpec`** | Keep `ConfigSpec` — inconsistent with the renamed type (`ConfigMap` + `ConfigSpec` reads wrong). |
| Recording | **A rename ADR refining ADR-0003** (ADR-0045 pattern) | Silent rewrite — forbidden (frozen ADRs); the divergence must be recorded with this ADR as the pointer. |

## Decision

Rename the resource kind/type `Config` → `ConfigMap`, behavior-preserving, across the live surfaces; regenerate
the OpenAPI.

1. **Type** — `api/types/v1alpha1/config.go` → `configmap.go`: `Config` → `ConfigMap`, `ConfigSpec` →
   `ConfigMapSpec`; `GroupVersionKind` → `KindConfigMap.GVK()`; `Validate` → `validateMeta(…, KindConfigMap)`.
2. **Registry** — `metadata.go`: `KindConfigMap Kind = "ConfigMap"`; update the kind lists, the GVK `switch`
   case, and `pureKinds`.
3. **Rename the qualified resource symbols** (`KindConfig` → `KindConfigMap`, `v1.Config` → `v1.ConfigMap`,
   `ConfigSpec` → `ConfigMapSpec`) across the tracked files the token-precise gate flags — **the gate is the
   authoritative file list** (currently ~16 Go files + `pkg/sdk/kinds.go` + the fixture + the venom suite; the
   per-category list in Scope is illustrative). Rename the control-plane `*Config(...)` handler funcs/DTOs by
   explicit name. **Never** a bare-`Config` sweep (it would corrupt 7 unrelated `*.Config` types — see the plan).
4. **SDK + path** — `pkg/sdk/kinds.go`: `KindConfigMap: {"configmaps", true}` (was `{"configs", true}`); the REST
   path becomes `/configmaps`.
5. **OpenAPI** — `just generate` → regenerate + commit `api/openapi/funcd.v1alpha1.yaml` (the `ConfigMap` schema
   + `configmaps` paths).
6. **Lanes/fixtures** — rename `metastore-config.yaml` → `metastore-configmap.yaml` (`kind: ConfigMap`) + the
   recipe `cp`; update `e2e/metastore.venom.yml` (`funcdctl get configmap`).
7. **Frozen ADRs untouched** — keep `Config` as history; **refines** ADR-0003; F03 appends ADR-0079.
8. **`FuncdConfig` + behavior unchanged.**

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/configmap.go  (was config.go)
type ConfigMap struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ConfigMapSpec `json:"spec"`
}
type ConfigMapSpec struct {
	Data map[string]string `json:"data,omitempty"`
}
func (c *ConfigMap) GroupVersionKind() GroupVersionKind { return KindConfigMap.GVK() }
func (c *ConfigMap) Validate() error { return validateMeta(c.TypeMeta, &c.ObjectMeta, KindConfigMap) }

// api/types/v1alpha1/metadata.go
KindConfigMap Kind = "ConfigMap"   // was KindConfig = "Config"
// pkg/sdk/kinds.go
v1.KindConfigMap: {"configmaps", true}   // was KindConfig: {"configs", true}
```

**Grep gate (token-precise, ADR-0045 pattern)** — must return **zero**, and must NOT flag `FuncdConfig`:

```bash
git ls-files | grep -vE '^docs/(adr|reviews)/' | xargs grep -nE '\bKindConfig\b|\bv1\.Config\b|^kind: Config$' \
  | sed -E 's#adr/[0-9]{4}[^ )]*config[^ )]*##gI' \
  | grep -E '\bKindConfig\b|\bv1\.Config\b|^kind: Config$'
# → 0.  Targets the RESOURCE kind only — \bKindConfig\b (not KindConfigMap/KindFuncdConfig), \bv1.Config\b (not
#   v1.ConfigMap), ^kind: Config$ (not kind: FuncdConfig). FuncdConfig + generic config plumbing are NOT matched.
#   docs/adr + docs/reviews (frozen) excluded by path; ADR-filename tokens stripped.
```

| consumes | exposes |
|---|---|
| nothing new (a rename) | the `ConfigMap` kind + the `/configmaps` REST path + the regenerated OpenAPI/SDK |
| `KindConfigMap` (was `KindConfig`) · `just generate` (huma) | the renamed metastore-lane fixture + `funcdctl get configmap` |

## Implementation plan

**Files / steps**:
1. `git mv api/types/v1alpha1/config.go api/types/v1alpha1/configmap.go`;
   `git mv e2e/fixtures/metastore-config.yaml e2e/fixtures/metastore-configmap.yaml`.
2. **Rename by QUALIFIED symbol — NEVER a blanket `s/\bConfig\b/ConfigMap/g`.** A bare-`Config` sweep would
   corrupt ~139 unrelated `Config` tokens across **7 other types** (`bench.Config`, `config.Config`,
   `containerd.Config`, `ctrmanager.Config`, `gocni.Config`, `huma.Config`, `observability.Config`) **and**
   `internal/config`'s own `type Config struct` — the out-of-scope daemon-config package. So `\bConfig\b` is only
   safe **scoped to the resource's home package** `api/types/v1alpha1/*.go` (where bare `Config`/`ConfigSpec` ARE
   the resource). Concretely:
   - `api/types/v1alpha1/*.go` (scoped): `s/\bConfigSpec\b/ConfigMapSpec/g`, `s/\bKindConfig\b/KindConfigMap/g`,
     `s/\bConfig\b/ConfigMap/g`.
   - **All other tracked files** (excl. `docs/adr`/`docs/reviews`): `s/\bKindConfig\b/KindConfigMap/g`,
     `s/\bv1\.Config\b/v1.ConfigMap/g`, `s/\bv1\.ConfigSpec\b/v1.ConfigMapSpec/g` — qualified only, so the other
     `*.Config` types are untouched.
   - `internal/controlplane` (explicit names, NOT a sweep — `huma.Config` lives here): the resource's handler funcs
     `GetConfig/CreateConfig/ListConfigs/ReplaceConfig/DeleteConfig` → `…ConfigMap`, its DTOs
     `createConfigInput/configOutput/listConfigOutput` → `…ConfigMap…`, and `Tags: []string{"Config"}` → `{"ConfigMap"}`.
   - `pkg/sdk/kinds.go`: `"configs"` → `"configmaps"`. `^kind: Config$` → `kind: ConfigMap` in the fixture; update
     `e2e/metastore.venom.yml` (`funcdctl get config`→`get configmap`, the fixture path, and the testcase/`info:`
     prose `…a Config`→`…a ConfigMap`) + the `lima-example-metastore` recipe `cp`.
   - **GUARD**: after the sweep, `git diff internal/config` is **empty** and `grep -rc '\bFuncdConfig\b'` is
     unchanged. **Never** edit `docs/adr/*`, `docs/reviews/*`, `internal/config`, or any `FuncdConfig`/`*.Config`-other token.
3. `just generate` → regenerate `api/openapi/funcd.v1alpha1.yaml`; commit it.
4. Docs prose (the gate won't catch plain-prose `Config`, so by hand): `blueprint.md` (Resources §"Config"),
   `docs/PROJECT-SUMMARY.md` (the resource callout + kind list), `docs/feat/0000` F03 kind list `Config` →
   `ConfigMap`; append ADR-0079 to F03. Leave ADR-row text that quotes a frozen ADR's title as-is.

**go.mod / deps**: none.

**Test plan** — one check per scenario: **kind-registered-as-configmap** / **round-trip-unchanged** —
`go test ./...` green (the api/store/controller tests pass with the renamed kind; unchanged assertions modulo the
token). **api-surface-renamed** — `just generate` produces `configmaps` paths + a `ConfigMap` schema; `just ci`'s
generate-staleness check passes (spec committed). **no-live-config-kind** — the token-precise gate → 0, and
`grep -r FuncdConfig` is unchanged. **cli-uses-configmap** / **metastore-lane-green** —
`just lima-example-metastore` applies a `ConfigMap`, restarts, recovers → `final status: PASS`. Plus
`go build ./...`, `go tool golangci-lint run`, `go mod verify`.

**Definition of done**: `ConfigMap`/`KindConfigMap` registered (no `Config`/`KindConfig`); the token-precise gate
is 0 with `FuncdConfig` untouched; OpenAPI regenerated + committed (`configmaps` + `ConfigMap` schema), `just ci`
generate-check green; `go build`/`go test`/lint/`mod verify` green with unchanged assertions; the metastore Lima
lane PASS; frozen ADRs untouched; no new dep.

## Review checklist

- [ ] `api/types/v1alpha1/configmap.go` exists (`config.go` gone); `ConfigMap`/`ConfigMapSpec`/`KindConfigMap =
      "ConfigMap"`; the kind lists / GVK switch / `pureKinds` updated.
- [ ] `git ls-files | grep -vE '^docs/(adr|reviews)/' | xargs grep -nE '\bKindConfig\b|\bv1\.Config\b|^kind: Config$' | sed -E 's#adr/[0-9]{4}[^ )]*config[^ )]*##gI' | grep -E '\bKindConfig\b|\bv1\.Config\b|^kind: Config$'` → **0**; `FuncdConfig` count unchanged.
- [ ] `pkg/sdk/kinds.go` maps `KindConfigMap → {"configmaps", true}`; `api/openapi/funcd.v1alpha1.yaml`
      regenerated (`configmaps` paths + `ConfigMap` schema) and committed; `just ci` generate-staleness green.
- [ ] `go build` · `go test` (unchanged assertions) · `golangci-lint` · `go mod verify` green; the metastore
      Lima lane builds + applies a `ConfigMap` and ends `final status: PASS`.
- [ ] `docs/adr/*` + `docs/reviews/*` untouched; `internal/config`/`FuncdConfig` untouched; F03 links ADR-0079;
      no new dep; no identity/path leak.

## Consequences

**Positive**: the resource reads as the familiar k8s `ConfigMap` and is **disambiguated** from the daemon
`FuncdConfig`; the kubectl-style surface (`funcdctl get configmaps`, `/configmaps`) is idiomatic.
**Negative (accepted)**: an API-surface change (REST path + OpenAPI + SDK) — but no external consumers in the
design phase; and a rename that touches ~16 Go files (mostly one-line kind-registry/test-sample entries, not
bespoke logic — the `Config` type is 21 lines). **Neutral**: the resource semantics, the metastore, `FuncdConfig`,
and every other kind are unchanged; frozen ADRs keep `Config` as history with this ADR as the pointer.

## Open questions

- **Stored data with the old kind?** None to migrate — the design-phase metastore holds no persisted `Config`
  data across this change (the lanes re-apply from manifests); a future migration concern only if real data exists.

## References

- [ADR-0003](0003-resource-model-and-api-typing.md) (resource model — the `Config` kind, refined here) ·
  [ADR-0045](0045-rename-sandbox-to-worker.md) (kind-rename + OpenAPI-regen + grep-gate pattern) ·
  [ADR-0005](0005-api-surface-code-first-huma.md) (`just generate` / huma) · [ADR-0024](0024-funcdcli-and-sdk.md)
  (SDK kind map).
- [FEAT-0000](../feat/0000-feat-v1.md) — F03. k8s `ConfigMap` is the familiar named-config-data resource.
