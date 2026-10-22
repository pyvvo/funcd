# ADR-0261: Resource rename — `CatalogService` → `Lake`, and every name derived from it

- **Status**: Accepted (2026-10-22, by the decider once experiment 0005 had run the rename through every check; judged in three rounds, the last verdict accept)
- **Date**: 2026-10-22
- **Deciders**: green-0-rabbit
- **Tags**: resource, rename, api, lake, cedar, shim, breaking-change
- **Realizes**: [FEAT-0003/F180](../feat/0003-feat-data-platform.md)
- **Relates to**: refines the names decided in [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (the
  kind) and in every ADR whose text uses a name of the table below, among them ADR-0091, 0130, 0131, 0137, 0138,
  0153, 0162, 0163, 0175, 0176, 0222, 0228, 0241, 0245, 0246, 0248, 0251, 0260; their decisions stand ·
  [ADR-0079](0079-rename-config-to-configmap.md) (the kind-rename pattern) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (how a language-repo change ships) ·
  [ADR-0082](0082-provider-model-catalog.md) (its *provider catalog* is another meaning and keeps its name)

## Context & Need

`CatalogService` (ADR-0086) is a namespaced resource whose reconciler runs a DuckDB engine over a DuckLake: Parquet
tables and DuckLake's catalog file under one bucket prefix (`spec.catalog`), and SQL over Quack behind a per-caller
PEP proxy (ADR-0137). Its name is wrong three ways:

- **It names one part.** In lakehouse tools a *catalog* (Iceberg REST, Unity, Polaris, Glue) is only the metadata
  service that engines call; this resource is the engine and its SQL endpoint as well.
- **It overloads its own field.** `spec.catalog` holds the tables and the catalog file, yet says "catalog".
- **"Service" collides** with the `Service` kind (a platform service binding). The peers name what they hold:
  `KVStore`, `SQLDatabase`, `Bucket`.

The decider chose **`Lake`** — generic, so the table format underneath (DuckLake today, Delta Lake or Iceberg later)
can change without another rename, and short in every derived name (`spec.lakes`, `ctx.lake`, `lake::query`) — and
**`spec.location`**, the word Iceberg, Delta Lake and Hive use for where a table's files live (checked, *References*).
They also chose a **full migration without compatibility** (no deployment uses a lake, 2026-10-22) and **one
vocabulary in the code too**. The word is in 220 live files of funcd, 8 of funcd-typescript, 40 of funcd-python and 11 of
funcd-functions (the gates' file set, at the commits in *References*). Purpose: whoever reads a manifest, a status,
a policy, a URL, a log line, a handler or the code sees one name for the resource.

## Scenarios

- **scenario: lake-kind-registered** — *Given* the API, *When* a client applies a `Lake`, *Then* `funcdctl get lake`
  lists it under `/namespaces/{ns}/lakes`; a `CatalogService`, or a Function with `bindings.catalog`, is refused.
- **scenario: lake-default-path** — *Given* a Lake whose ingress is enabled with no host and no path, *When* it
  reconciles, *Then* the edge serves it at `/lake/<name>`.
- **scenario: lake-binding-env-and-ctx** — *Given* a Function that binds the Ready Lake `sales` under the alias
  `gold` (`spec.bindings.lake`), *When* it runs, *Then* its worker has `FUNCD_LAKE_GOLD_URL` and
  `FUNCD_LAKE_GOLD_TOKEN`, and `ctx.lake.get('gold')` returns them in TypeScript and Python; `ctx.lake.get('other')`
  fails naming the alias.
- **scenario: lake-query-authorized** — *Given* that Function, *When* it queries through the proxy URL, *Then* Cedar
  `lake::query` permits it and a caller from another namespace is denied; an Identity whose RolesAssignment grants
  "Lake Query Reader" with a scope of kind `Lake` naming `sales` is permitted.
- **scenario: lake-quack-round-trip** — *Given* the `duckdb` lane's example (`examples/lake-quack`), *When* its
  reader runs SQL over Quack, *Then* rows come back, and a recreated Lake keeps its URL.
- **scenario: app-declares-lake** — *Given* an App that declares the Lake `todo-lake` in `spec.lakes`, with its
  `location` in the bucket `todo-files`, *When* it is deployed, *Then* `todo-lake` becomes Ready.
- **scenario: lake-backup-restore** — *Given* a BackupSchedule whose `scope.lakes` names `todo-lake`, *When* a Restore
  member sets `asLocation` to a prefix that holds no objects, *Then* the lake's files are copied there and a new Lake
  with that `spec.location` serves them.
- **scenario: old-config-key-refused** — *Given* a daemon config file with a `catalog:` section, *When* funcd
  starts, *Then* it fails on the unknown key; with `lake:` it starts.
- **scenario: no-catalog-name-left** — *Given* the four repos at their rename commits, *When* the gates of
  *Contracts* run, *Then* gates 1–3 print nothing and every line of list 4 is a kept meaning.

## Scope

**In** — every live surface of the four repos (the table in *Contracts*) and every identifier, file name, test,
comment, message, recipe, script, workflow, lane, fixture and example that uses the word; the living docs
(blueprint, PROJECT-SUMMARY, DOCUMENTATION-PLAN, feat docs, roadmap prose, READMEs, funcd-python's CLAUDE.md,
`examples/*.md`, the `area/services` label text).

**Out** — behavior; the meanings Decision 2 keeps; history (`docs/adr`, `docs/reviews`, `docs/reports`, every
`CHANGELOG.md`, `funcd-security-deep-dive.md`, and the experiment repo, pinned to a funcd commit). Switching the
table format or the engine is a later ADR's.

## Constraints & Decision drivers

- **Behavior-preserving**: every existing test keeps its assertions; only names change.
- **Qualified renames only**: a blanket `s/catalog/lake/` would rename ADR-0082's provider catalog and the DuckLake
  file (the ADR-0079 lesson).
- **ADR-0141**: a shim or example change is a release in its repo, then `go get` here; only `go.mod`/`go.sum` name a
  version. No new dependency.

## Alternatives considered

| Decision | Chosen | Rejected (why) |
|---|---|---|
| The kind | **`Lake`** | `CatalogService`: the three faults of *Context*. `Lakehouse`: precise, but long in every derived name (`spec.lakehouses`, `FUNCD_LAKEHOUSE_<ALIAS>_URL`). `DuckLake`: names the technology, so a format switch forces another rename. |
| `spec.catalog` | **`spec.location`** | Keep `catalog`: the word stays on the surface, and a Delta Lake has no catalog file. `storage`, `root`: not the lakehouse term (Iceberg's `location`, Delta's and Hive's `LOCATION`; DuckLake calls its `DATA_PATH` "the storage location"). |
| Old names | **Clean break** | Aliases for one release: two vocabularies in code and docs, for a single user. |
| Stored objects | **No converter** | A startup converter and a restore-time translation (or an Identity-key move): much code and test for no deployed lake. |
| Internal code | **Renamed too** | Public names only: "catalog" keeps two meanings in the code. |
| Test names that cite a frozen ADR's scenario | **Take the new word** | Keep the old word: a standing exception in the gates, while the rest of each name still finds its scenario. |
| Access-key code (ADR-0175) | **`L`** | Keep `C`: a letter that no longer stands for anything; no stored key depends on it. |

## Decision

1. **Rename** every surface of the four repos by the table in *Contracts*. Behavior, defaults and validation rules
   are unchanged; messages say "lake", and field paths in messages name the new fields (`spec.location.prefix`,
   `asLocation`).
2. **"catalog" stays only** for the DuckLake catalog file (`catalog.db`, `catalogFile`, "catalog file"), ADR-0082's
   provider catalog (`provider.Catalog`, `providerCatalog`, "provider catalog"), and other products' words (Unity
   Catalog, the Pingora "catalogue" of FEAT-0006). A kept meaning uses one of these phrases; any other wording ("the
   ADR-0082 catalog", "checkpoint its catalog") is reworded, and a database DuckDB attaches is a *database*.
3. **Names that cite history take the new word**: test names, lane testcase names, scenario slugs in comments. What
   stays is the file name of a frozen ADR and its row in PROJECT-SUMMARY's ADR index; elsewhere living text names an
   ADR by its number.
4. **No alias, no converter, no compatibility**: no deployed funcd uses a lake, only the examples do (the decider,
   2026-10-22). The API refuses the old kind and fields (its schemas set `additionalProperties: false`: 422); a
   daemon config file with `catalog:` fails at start (`yaml.UnmarshalStrict`); an old `FUNCD_CATALOG_ENGINE_*`
   variable is ignored (the defaults are 2 s). Nothing stored is converted, as experiment 0005 measured on the
   upgrade path: an older backup stays readable and its other members restore, but not its `CatalogService` member;
   an object stored with an old field loses it on read, so a BackupSchedule or App that named only `catalogs` then
   backs up no member and reports Succeeded; a stored `CatalogService` stays as an ignored orphan; and an Identity
   issued before the release gets its `lakeToken` at its next rotation (`spec.rotate`).
5. **Order (ADR-0141)**: the funcd branch is cut first (step 2 generates the Python examples' types with its
   funcdctl) and merges last; funcd-typescript, funcd-python and funcd-functions each merge a `feat!` PR and release
   in between.
6. **Records**: the frozen ADRs keep their text, with no back-link (refined, not superseded, as ADR-0079 did). At
   acceptance the blueprint takes the new names and F180 moves; the other living docs follow in the funcd PR.

## Temporary workarounds

None.

## Contracts

### Names (old → new)

| Surface | Old | New |
|---|---|---|
| Kind · REST · CLI | `CatalogService` · `/namespaces/{ns}/catalogservices` · `funcdctl get catalogservice` | `Lake` · `/namespaces/{ns}/lakes` · `funcdctl get lake` |
| Built from the kind at run time | default ingress path `/catalogservice/<name>` (`ingress.go:48`) · provider storage dir `<storageRoot>/catalogservice` (`storage.go:35`) | `/lake/<name>` · `<storageRoot>/lake` |
| Where the lake lives | `spec.catalog: {bucket, prefix}` (`CatalogRef`) · Restore member `asCatalog` | `spec.location` (`LakeLocation`) · `asLocation` |
| Root entries (`FunctionSpec`, `WorkflowSpec`, `AppSpec`) | `catalogs` (`CatalogEntry`) | `lakes` (`LakeEntry`) |
| Bindings (`FunctionBindings`, `StepBindings`) | `catalog: [{alias, catalog}]` (`FunctionCatalog`) | `lake: [{alias, lake}]` (`FunctionLake`) |
| Backup | `BackupScope.catalogs` · member kind `CatalogService` · layout `catalog/<name>/<attempt>/` · temp prefix `funcd-catalog-` | `lakes` · `Lake` · `lake/<name>/<attempt>/` · `funcd-lake-` |
| RolesAssignment scope kind | `Catalog` (`ScopeKindCatalog`) | `Lake` (`ScopeKindLake`) |
| Function environment | `FUNCD_CATALOG_<ALIAS>_URL`, `_TOKEN` | `FUNCD_LAKE_<ALIAS>_URL`, `_TOKEN` |
| Shims (TS, Python) | `ctx.catalog` · `CatalogClient` · `CatalogEndpoint` · `makeCatalog` · `catalog.ts`, `catalog.py` | `ctx.lake` · `LakeClient` · `LakeEndpoint` · `makeLake` · `lake.ts`, `lake.py` |
| Generated binding types (`pkg/sdk/types_gen.go`) | `catalogs` | `lakes` |
| Cedar | action `catalog::query` (reserved `catalog::write`) · entity type `CatalogService` · attribute `catalogBindings` · policy `catalog-query-binding` in `builtin_catalog.cedar` · `CatalogCapability` | `lake::query` (`lake::write`) · `Lake` · `lakeBindings` · `lake-query-binding` in `builtin_lake.cedar` · `LakeCapability` |
| Built-in roles | "Catalog Query Reader" · "Catalog Contributor" | "Lake Query Reader" · "Lake Contributor" |
| Provider descriptor (ADR-0082) | `catalog-query`, binding `spec.bindings.catalog` | `lake`, `spec.bindings.lake` |
| Access-key code (ADR-0175) | `C` | `L` |
| Lake token | Identity secret key `catalogToken` · escrow credential `catalog-token` | `lakeToken` · `lake-token` |
| Status reasons | `CatalogNotReady` · `CatalogResolveFailed` · `CatalogUnstable` · `CatalogEngineNotBundled` | `LakeNotReady` · `LakeResolveFailed` · `LakeUnstable` · `LakeEngineNotBundled` |
| Admission · edge-table source | `catalog-store-owner` · `catalog/<ns>/<name>` | `lake-store-owner` · `lake/<ns>/<name>` |
| Daemon config | `catalog.enginePollInterval`, `catalog.engineProbeTimeout` · `FUNCD_CATALOG_ENGINE_POLL_INTERVAL`, `_PROBE_TIMEOUT` | `lake.enginePollInterval`, `lake.engineProbeTimeout` · `FUNCD_LAKE_ENGINE_POLL_INTERVAL`, `_PROBE_TIMEOUT` |
| Dev mode | `WithCatalogDir` · `WithCatalogExtensionDir` · `WithCatalogProviderRuntime` · token `funcd-dev-catalog` | `WithLakeDir` · `WithLakeExtensionDir` · `WithLakeProviderRuntime` · `funcd-dev-lake` |
| Go packages · loggers | `internal/catalog/{gateway,devengine,embedengine}` · `internal/services/catalog` (aliases `cataloggw`, `catalogsvc`) · `catalog.gateway.*` | `internal/lake/…` · `internal/services/lake` (`lakegw`, `lakesvc`) · `lake.gateway.*` |
| Build | `just build-catalog-engine`, `catalog-engine-pin`, `catalog-engine-unpin` · `scripts/fetch-catalog-engine.sh` | `build-lake-engine`, `lake-engine-pin`, `lake-engine-unpin` · `scripts/fetch-lake-engine.sh` |
| Examples | funcd-python `examples/catalog-quack` (project, function `catalog-reader`, input field `catalog`), `releve-lakehouse/src/catalog-reader`, `catalogservice.yaml` files · funcd-functions `apps/todo/**/*catalog*.yaml` | `examples/lake-quack` (`lake-reader`, `lake`), `src/lake-reader`, `lake.yaml` · `*lake*.yaml` |

Every other identifier, file, test name, message and comment follows the same swap (`CatalogFiles` → `LakeFiles`,
`TestScenarioCatalogRecreateKeepsURL` → `TestScenarioLakeRecreateKeepsURL`, lane slug `dev-catalog-query` →
`dev-lake-query`).

### Go shapes

```go
// api/types/v1alpha1/metadata.go:38 and its kind lists; pkg/sdk/kinds.go: v1.KindLake: {"lakes", true}
const KindLake Kind = "Lake"

// api/types/v1alpha1/lake.go (was catalogservice.go): Lake, LakeSpec, LakeStatus, LakeBindings keep the fields of
// CatalogService, CatalogServiceSpec, CatalogServiceStatus, CatalogServiceBindings, except
// LakeSpec.Location LakeLocation `json:"location"` (was Catalog CatalogRef `json:"catalog"`).

// LakeLocation is the bound, owned (bucket, prefix) the lake keeps its files under; DuckLake's catalog file is
// <prefix>/_ducklake/catalog.db.
type LakeLocation struct {
	Bucket ObjectName `json:"bucket"`
	Prefix string     `json:"prefix" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
}

type LakeEntry struct{ Name, Ref ObjectName; LakeSpec } // was CatalogEntry; tags unchanged (name, ref, inline)
type FunctionLake struct{ Alias string; Lake ObjectName } // was FunctionCatalog; json alias, lake (was catalog)

// FunctionBindings.Lake, StepBindings.Lake []FunctionLake `json:"lake,omitempty"` (was Catalog, "catalog")
// FunctionSpec.Lakes, WorkflowSpec.Lakes, AppSpec.Lakes []LakeEntry `json:"lakes,omitempty"` (was "catalogs")
// BackupScope.Lakes []ObjectName `json:"lakes,omitempty"`; RestoreMember.AsLocation *LakeLocation `json:"asLocation,omitempty"`
// ScopeKindLake ScopeKind = "Lake"; workload.Member.Location v1.LakeLocation (was Catalog, ADR-0228)
```

### Shims

```ts
// funcd-typescript shim/src/lake.ts (was catalog.ts); FunctionContext.lake: LakeClient (was catalog)
export interface LakeEndpoint { url: string; token: string; }
export interface LakeClient { get(alias: string): LakeEndpoint; } // throws an Error naming an unbound alias
export function makeLake(env?: NodeJS.ProcessEnv): LakeClient;   // reads FUNCD_LAKE_<ALIAS>_URL and _TOKEN per call
```

funcd-python's `funcd_shim/lake.py` (was `catalog.py`) mirrors it: `context.lake`, `LakeClient.get(alias) ->
LakeEndpoint` (a `TypedDict` of `url`, `token`), a `KeyError` for an unbound alias. Both say `context.lake.get: lake
"<alias>" is not bound`.

### Gates

Run in each repo at its rename commit. `hits` drops PROJECT-SUMMARY's ADR-index rows and the targets of links to
frozen ADRs (Decision 3); its strips are case-sensitive, so `CatalogFiles` or `CATALOG` still shows.

```bash
live=(. ':!docs/adr/**' ':!docs/reviews/**' ':!docs/reports/**' ':!*CHANGELOG.md' ':!funcd-security-deep-dive.md')
keep='_ducklake/catalog\.db|catalog\.db|catalogFile|catalog files?|DUCKLAKE_CATALOG|DuckLake catalog|SQLite catalog|provider\.Catalog|providerCatalog|[Pp]rovider catalog|Unity[- ]Catalog|catalogue'
hits() { git grep -n -i catalog -- "${live[@]}" "$@" | grep -v -E '^docs/PROJECT-SUMMARY\.md:[0-9]+:\| \[ADR-' \
  | perl -pe 's/\]\([^)]*adr\/\d{4}-[^)]*\)/]/g'; }
# 1. No old name anywhere live (prints nothing).
hits | grep -E 'CatalogService|catalogservices?|KindCatalog|FUNCD_CATALOG_|catalog::|catalog-query|(ctx|context)\.catalog|bindings\.catalog|asCatalog|CatalogRef|CatalogEntry|FunctionCatalog|Catalog (Query Reader|Contributor)|catalogToken|catalog-token|builtin_catalog|catalogBindings|catalog-quack|catalog-reader'
# 2. No other "catalog" outside the kept meanings (prints nothing).
hits ':!images/runtime/duckdb/**' ':!internal/provider/provider.go' ':!internal/provider/provider_test.go' \
  | perl -pe "s{$keep}{}g" | grep -i catalog
# 3. No live path with the word (prints nothing).
git ls-files | grep -i catalog | grep -v -E '^docs/(adr|reviews|reports)/'
# 4. What the exclusions and strips kept, read once in review: every line must be a Decision 2 meaning.
git grep -n -i catalog -- images/runtime/duckdb internal/provider/provider.go internal/provider/provider_test.go; hits | grep -E "$keep"
```

| consumes | exposes |
|---|---|
| nothing new (a rename) · the three language-module releases | the `Lake` kind, `/lakes`, `/lake/<name>`, the renamed fields, environment, `ctx.lake` and Cedar names; the regenerated OpenAPI and SDK |

## Implementation plan

1. **funcd branch** `feat/adr-0261-lake`, cut first (step 2 builds its funcdctl), merged last (step 5).
2. **Language repos**, each a `feat!` PR, the gates, merge and release:
   - funcd-typescript (`feat(shim)!: ctx.lake replaces ctx.catalog (funcd ADR-0261)`): `shim/src/catalog.ts` →
     `lake.ts`; `types.ts`, `shim.ts`, `pool.ts`, `test/ctx.test.ts`, `README.md`; rebuild `shim/shim.mjs`,
     `shim/pool.mjs`.
   - funcd-python: `funcd_shim/catalog.py` → `lake.py`; `shim.py`, `types.py`, `_poolworker.py`, both shim tests,
     `shim/embed.go`; `examples/catalog-quack` → `examples/lake-quack` (files, project, function, bundle, input
     field; `uv lock`); `releve-lakehouse` (`lake.yaml`, `src/lake-reader`, Secret `releve-lake-token`); READMEs,
     `CLAUDE.md`, `justfile`; every `funcd_types.py` regenerated with step 1's funcdctl.
   - funcd-functions (`feat!: the todo App declares a Lake`): `apps/todo/app/app.yaml`, `app-1.1.0.yaml`,
     `app-1.2.0.yaml` (`lakes:`, `location:`); every `*catalog*.yaml` under `apps/todo` takes `lake` in its name and
     content (`dr/funcdconfig-lake.yaml` has `lake:`, `dr/restore-lake.yaml` has `asLocation`); READMEs.
3. **funcd renames**, one PR `feat(api)!: CatalogService is renamed Lake (ADR-0261)` whose `BREAKING CHANGE:` footer
   lists the public rows of the names table, the default path included, and Decision 4's consequences:
   - `git mv` the packages and files of the table (`catalogservice.go` → `lake.go`, `internal/catalog` →
     `internal/lake`, `internal/services/catalog` → `internal/services/lake`, every file named `*catalog*`,
     `builtin_catalog.cedar`, `scripts/fetch-catalog-engine.sh`, `e2e/fixtures/catalog-quack-recreate.yaml`); pin
     the moved `internal/lake/embedengine/engine.tar.gz` with `just lake-engine-pin`; rename identifiers with
     `gopls rename`.
4. **funcd wiring**: `go get` the three tags of step 2; `scripts/lanes.yaml` (the `duckdb` lane: `dir:
   examples/lake-quack`, `dist/lake-reader`, `lake.yaml`), `e2e/duckdb.venom.yml`, `e2e/app-todo.venom.yml` (step 2's
   paths, `funcdctl get lake`), the fixtures, `.github/workflows/{release,bundle-multiarch}.yml`, `justfile`;
   `just generate` (OpenAPI, SDK); regenerate `internal/auth/cedar/testdata/grants.golden.json` and
   `cmd/funcdctl/testdata/app-todo/render.golden.yaml`; the `area/services` text in the issue-management `driver.py`
   and `SKILL.md`, then sync the label; the living docs (PROJECT-SUMMARY through the project-summary skill,
   DOCUMENTATION-PLAN, feat docs, roadmap prose, `examples/*.md`, `examples/funcdconfig.yaml`,
   `internal/lake/embedengine/README.md`).
5. **Checks, once each**: the gates; vet, lint and build with `-tags dev` for `cmd/funcdctl`; `go test -run Lane
   ./cmd/funcd/ ./internal/testkit/langmod/`; `scripts/agent/gate.sh`; the `duckdb` and `app-todo` lanes. Then merge.

**Test plan** — every existing test is renamed with its subject and keeps its assertions. Scenario → test:
**lake-kind-registered** — a new `api/types/v1alpha1` test: `kind: Lake` validates and maps to `lakes`;
`Kind("CatalogService").Validate()` is `Invalid`; a new control-plane case: a Function with `bindings.catalog` gets 422. **lake-default-path** — the renamed
`TestScenario_exposure_catalog_default_path` asserts `/lake/<name>`. **lake-binding-env-and-ctx** — the renamed
`internal/function` environment test and the shims' `ctx` tests. **lake-query-authorized** — the renamed
`internal/lake/gateway` proxy and scenario tests and the `internal/auth/cedar` role and capability tests.
**lake-quack-round-trip** — the `duckdb` lane. **app-declares-lake**, **lake-backup-restore** — the `app-todo` lane.
**old-config-key-refused** — a new case beside the unknown-key test of `internal/platform/config`.
**no-catalog-name-left** — the gates' output, attached to each PR.

**Definition of done**: steps 1–5 done in order; gates 1–3 empty and list 4 read in every repo; `gate.sh` and the
two lanes green; history untouched.

## Review checklist

- [ ] In all four repos (funcd at its pinned tags), gates 1–3 print nothing and every line of list 4 is a Decision 2
      meaning.
- [ ] `KindLake` maps to `lakes`; `Kind("CatalogService")` is refused; the default path is `/lake/<name>`; every row
      of the names table holds (JSON tags, environment, Cedar names, `ctx.lake`, the daemon config key).
- [ ] `provider.Catalog` and the DuckLake terms are unchanged; the diff of `images/runtime/duckdb` touches only text
      that names the resource.
- [ ] Existing tests keep their assertions and the new tests exist; `go.mod` pins three releases (no `go.work`, no
      pseudo-version); OpenAPI and golden files regenerated; history untouched; no identity or path leak.

## Consequences

- **Positive**: one name on every surface, tied neither to one part (the catalog) nor to one format (DuckLake);
  "catalog" keeps one meaning in the code besides ADR-0082's provider catalog.
- **Negative (accepted)**: a breaking release of funcd and both shims; every manifest, Policy, RolesAssignment and
  handler that used the old names is rewritten (one user, v1alpha1). An exposed lake's default URL moves from
  `/catalogservice/<name>` to `/lake/<name>`, which external callers update, and `/lake/` becomes the prefix ADR-0176's
  claim rules protect. Between the shim releases and the funcd release, `@funcd-dev/shim` declares `ctx.lake` while
  funcd v0.21 injects `FUNCD_CATALOG_*`; funcd embeds its own pinned shim, so only a type check sees the gap.
- **Neutral**: the frozen ADRs keep "catalog" as history; a renamed test is found by the rest of its name. The
  engine and its protocol are still DuckDB's (Quack).

## Open questions

None. A switch of table format or engine gets its own ADR when one is wanted.

## References

- ADR-0079 and [ADR-0045](0045-rename-sandbox-to-worker.md) (kind renames), ADR-0141, ADR-0000 (process).
- Experiment 0005-lake-rename (pyvvo/experiment): the rename run through every check, two lanes and upgrade probes.
- The inventory: `git grep -i catalog` over the gates' file set of funcd `8b1ba127`, funcd-typescript `82aa819`,
  funcd-python `cb5757f` and funcd-functions `4c0317b` (2026-10-22), every distinct token classified as renamed, kept
  (Decision 2) or history.
- "Location", checked 2026-10-22: the [Apache Iceberg table spec](https://iceberg.apache.org/spec/) (table metadata
  `location`, the table's base location), [Delta Lake](https://docs.delta.io/latest/delta-batch.html) (`CREATE TABLE
  … USING DELTA LOCATION`), [Hive DDL](https://cwiki.apache.org/confluence/display/Hive/LanguageManual+DDL)
  (`LOCATION hdfs_path`), [DuckLake](https://ducklake.select/docs/stable/duckdb/usage/connecting) (`DATA_PATH`).
