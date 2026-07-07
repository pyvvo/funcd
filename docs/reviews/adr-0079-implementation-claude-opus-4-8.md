# ADR-0079 implementation review — resource rename `Config` → `ConfigMap` (`claude-opus-4-8`)

- **ADR**: [0079](../adr/0079-rename-config-to-configmap.md) — resource-kind rename, behavior-preserving, API-surface
- **Phase**: implementation · **Model**: claude-opus-4-8
- **Verdict**: **pass** (DoD 5/5, no Blockers/Majors) · 2026-06-23

## Verification (evidence — run, not eyeballed)

| check | result |
|---|---|
| type + registry | `api/types/v1alpha1/configmap.go` exists (`config.go` gone); `ConfigMap`/`ConfigMapSpec`/`KindConfigMap = "ConfigMap"`; the kind lists / GVK switch / `pureKinds` updated |
| **token-precise gate** | **0** — `git ls-files \| grep -vE '^docs/(adr\|reviews)/' \| xargs grep -nE '\bKindConfig\b\|\bv1\.Config\b\|^kind: Config$' \| sed -E 's#adr/…config…##gI' \| grep …` → 0 |
| **guard** | `git diff internal/config` **clean** (the daemon `type Config struct` untouched); `FuncdConfig` count unchanged; `tls.Config` (blueprint) preserved |
| `go build ./...` | OK |
| `go test ./...` | green (api/types, controlplane, store, sdk, pkg/funcd all `ok`); `internal/bench` flake is **pre-existing** (its tree has **zero** rename diff — `git diff internal/bench` clean) |
| `go tool golangci-lint run ./...` · `go mod verify` | **0 issues** · all modules verified |
| **OpenAPI** | `just generate` regenerated `api/openapi/funcd.v1alpha1.yaml`: **`/configmaps` paths** (×2), `ConfigMap` schema, **no stale `/configs`**; regen is **deterministic** (generate twice → identical) so `just ci` staleness passes once committed |
| **all 3 Lima lanes** (whole-platform retest) | **kv · fn-to-fn · metastore** each `final status: PASS` on real containerd (metastore applies a `ConfigMap`, restarts the daemon, recovers it) |
| **OpenAPI operationIds** | a retest caught the first sweep had left `getConfig`/`deleteConfig`/`replaceConfig` stale (the func-name sweep didn't hit the lowercase operationId strings); completed → **all `*ConfigMap`** + regenerated |
| `go test ./...` (full, no exclusions) | **all `ok`** — incl. `internal/bench` (the earlier failure was a load-flake; clean run passes); `tests/{chaos,e2e,lint-fixtures}` green |

## Scenarios → checks (all pass)

- **kind-registered-as-configmap** — `KindConfigMap = "ConfigMap"`; no `Config`/`KindConfig`. ✅
- **api-surface-renamed** — OpenAPI `/configmaps` + `ConfigMap` schema, no `/configs`. ✅
- **round-trip-unchanged** — the store/controller/contract tests pass with the renamed kind (unchanged assertions
  modulo the token) — behavior-preserving. ✅
- **cli-uses-configmap** / **metastore-lane-green** — the lane's `funcdctl get configmap` + apply round-trips a
  `ConfigMap` across a daemon restart. ✅
- **no-live-config-kind** — the token-precise gate → 0 and `FuncdConfig` is untouched. ✅

## DoD (ADR Review checklist) — 5/5

1. ✅ `configmap.go` exists; `ConfigMap`/`ConfigMapSpec`/`KindConfigMap`; kind lists/GVK switch/`pureKinds` updated.
2. ✅ Token-precise gate → 0; `FuncdConfig` unchanged.
3. ✅ `pkg/sdk/kinds.go` → `KindConfigMap: {"configmaps", true}`; OpenAPI regenerated (`/configmaps` + `ConfigMap`)
   and committed; regen deterministic → `just ci` staleness green.
4. ✅ build/test/lint/mod green; the metastore Lima lane PASS.
5. ✅ `docs/adr/*` + `docs/reviews/*` untouched; `internal/config`/`FuncdConfig` untouched; F03 links ADR-0079;
   no new dep; no identity/path leak.

## ✅ Verified correct (keep)

- **Qualified-symbol rename, not a blanket `\bConfig\b` sweep** — the judge's Blocker (a bare sweep would have
  corrupted ~139 `Config` tokens across 7 unrelated types + `internal/config`'s own `type Config struct`) was
  honoured: bare `Config`/`ConfigSpec` swept **only** in `api/types/v1alpha1/*.go`; `v1.Config`/`KindConfig`
  qualified everywhere else; the control-plane handler funcs/DTOs/`Tags`/REST-path renamed by **explicit name**
  (leaving `huma.Config` intact). Guard confirmed `internal/config` byte-clean.
- **API surface consistent** — the REST path (`routes_rest.go` `/configs`→`/configmaps`), the OpenAPI operationIds
  (`*Config`→`*ConfigMap`), the `Tags`, and the SDK plural all moved together; the regenerated spec has no
  `/configs` remnant.
- **Docs prose handled by hand** (the gate doesn't catch plain prose) — blueprint `#### ConfigMap` + the kind
  list, `PROJECT-SUMMARY` callout + kind list, F03 — while `tls.Config` and `FuncdConfig` were left untouched.

## Findings
None (Blocker/Major/Minor). The `internal/bench/TestBenchSmoke` flake is **env** (load-sensitive; `internal/bench`
has zero rename diff), not model-attributed.

## Recommendation
**pass** — `Accepted → Implemented`. The resource is now `ConfigMap` (k8s-style, disambiguated from the daemon
`FuncdConfig`); behavior, the metastore, and every other kind are unchanged; the OpenAPI/SDK expose `configmaps`;
frozen ADRs keep `Config` as history with this ADR as the pointer.
