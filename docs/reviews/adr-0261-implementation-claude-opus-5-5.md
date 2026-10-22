## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (ADR-0261 implementation, model: claude-opus-5-5)

Reviewed: funcd `feat/adr-0261-lake-impl` at 4204dcc2 (two commits on main 3f7a4d3d: the rename, and the `go.mod`
pins), with funcd-typescript#90 (v0.21.0), funcd-python#98 (v0.15.0) and funcd-functions#26 (v0.21.0), all merged.

The rename itself is complete and green. One finding blocks sign-off: in three places the rename overreached and
renamed ADR-0082's *provider catalog*, which Decision 2 keeps, to "lake". The gates cannot catch it, because they
look only for a "catalog" that is left, not for one that was renamed when it should have stayed.

### 🟡 Major 1 — ADR-0082's provider catalog renamed to "lake" in three places  ·  attribution: model

Decision 2 keeps "catalog" for ADR-0082's provider catalog and says a kept meaning uses the phrase "provider
catalog"; it names "the ADR-0082 catalog" as wording to reword to that phrase, not to rename. The review checklist
requires `provider.Catalog` to be unchanged. The identifier `provider.Catalog` is unchanged, but the text that
describes it now says "lake", so each sentence below is false:

- `internal/provider/spec.go:4` — "The lake is the blueprint provider model in code." and `:8` — "the lake only
  classifies and replaces no facade, port or driver". This is the package doc of `internal/provider`, whose line 1
  says the package "holds the platform provider catalog (ADR-0082)". Before the rename, both lines said "The
  catalog" (`git show 3f7a4d3d:internal/provider/spec.go`).
- `docs/feat/0001-feat-v1.1.md:128` (row F56) — "an immutable `Lake`" (was `` `Catalog` ``, the type
  `provider.Catalog`), "a **static lake assembled at the composition root**", and "`observability-serving` was later
  dropped from the lake". The same row's heading was changed correctly to "a built-in/add-on provider catalog".
- `docs/DOCUMENTATION-PLAN.md:168` — "(it is in the ADR-0082 lake)".

Fix (builder): in these lines, write "provider catalog" (and `` `Catalog` `` for the type) instead of "lake". A
word-diff of the funcd rename that looks for `catalog` → `lake` near "provider" or "ADR-0082" found no other site.

### Minor

- **Stale access-key comment** · model · `internal/blob/s3gateway/iam.go:64` says `"C" for a Lake`, while
  `kindCodeLake = "L"` (`:46`) and the names table row "Access-key code (ADR-0175) `C` → `L`" hold in the code. The
  comment describes a key format that no longer exists. Fix: `"L" for a Lake`.
- **The status line lost part of its record** · model · `docs/adr/0261-rename-catalogservice-to-lake.md:3`: the
  rewritten status line drops "judged in three rounds, the last verdict accept". The skill allows only the
  `Accepted → Reviewing` edit. Nothing else in the ADR changed (`git diff 3f7a4d3d..HEAD` on the file touches only
  line 3), so the decision is intact. Fix: keep the acceptance clause as it was and add the Reviewing status in front
  of it.
- **Gates 1–2 "print nothing" conflicts with the test plan** · adr · ruled below.

#### Ruling on gates 1–2 (attribution: adr)

The test plan asks for tests that name the old words to prove that they are refused: `Kind("CatalogService")` is
`Invalid`, a Function with `bindings.catalog` gets 422, and a config file with `catalog:` fails. Gates 1–2 grep
every live file, tests included, and require empty output. The two requirements cannot both hold, so the ADR has the
defect, not the model. On this tree, gate 1 prints only `api/types/v1alpha1/lake_test.go:258,271-275`,
`internal/controlplane/lake_scenario_test.go:11,19` and `pkg/sdk/kinds_test.go:99-100`. Gate 2 prints those lines
and also `lake_scenario_test.go:22,25` and `internal/platform/config/config_test.go:432-436`. Every line is a
refusal assertion or its doc comment. Gate 3 prints nothing. Experiment 0005 had all three gates at 0 lines before
these tests existed (`results.json`). The model was right to name the old words in plain text: building them by
concatenation to get past the grep would hide what the tests prove. **Ruling: gates 1–2 are satisfied.** A later
rename ADR should exclude refusal tests from its gates by name. This ADR does not need to be superseded.

### ✅ Verified correct (keep it)

- **Gates (run here)**: gate 3 prints nothing. In list 4, every line is a DuckLake catalog-file term
  (`images/runtime/duckdb/*`, `_ducklake/catalog.db`, `catalogFile`, `FUNCD_DUCKLAKE_CATALOG`), the provider catalog
  (`internal/provider/provider*.go`, `pkg/funcd/providers.go`), Unity Catalog (`docs/feat/0008`) or the Pingora
  "catalogue" (`docs/feat/0006`). The `images/runtime/duckdb` diff changes only two comments that name the resource
  (`asLocation`, "the Lake's spec.bindings.secret").
- **Names table, row by row**: `KindLake Kind = "Lake"` (`api/types/v1alpha1/metadata.go:38`) and
  `v1.KindLake: {"lakes", true}` (`pkg/sdk/kinds.go:65`). `LakeLocation` uses `json:"location"`, `asLocation`,
  `lakes,omitempty` (4 sites) and `lake,omitempty` (2 sites). `ScopeKindLake ScopeKind = "Lake"`
  (`rolesassignment.go:38`). `FUNCD_LAKE_<ALIAS>_URL/_TOKEN`; `lake::query`/`lake::write`, `lakeBindings`,
  `lake-query-binding` in `builtin_lake.cedar` and `LakeCapability`; the roles "Lake Query Reader" and "Lake
  Contributor"; `kindCodeLake = "L"`; `lakeToken`/`lake-token`; the four `Lake*` status reasons; `lake-store-owner`.
  The daemon config is `Lake` with `json:"lake"` and `FUNCD_LAKE_ENGINE_POLL_INTERVAL`/`_PROBE_TIMEOUT`
  (`internal/platform/config/config.go:313-317`). Dev mode has `WithLakeDir`, `WithLakeExtensionDir`,
  `WithLakeProviderRuntime` and `funcd-dev-lake`. The packages `internal/lake/{gateway,devengine,embedengine}` and
  `internal/services/lake` exist, and the build has `build-lake-engine`, `lake-engine-pin`/`unpin` and
  `scripts/fetch-lake-engine.sh`. The default path `/lake/<name>` comes from the kind name
  (`internal/provider/storage.go:35` gives `<storageRoot>/lake` the same way).
- **Language repos**: none of the three PRs adds a line with "catalog" outside the kept phrases.
  `ctx.lake`/`context.lake` reads `FUNCD_LAKE_<ALIAS>_` per call, and both shims fail with
  `context.lake.get: lake "<alias>" is not bound`, as *Contracts → Shims* requires. Experiment 0005's run of each
  repo's CI and gates passed (`results.json`).
- **Scenario → test, all passing (run here, `-count=1`)**: lake-kind-registered → `TestScenarioLakeKindRegistered`,
  `TestScenario_lake_kind_registered` (lists `/lakes`; `/catalogservices` 404; `bindings.catalog` 422) and
  `TestKindFromToken`. lake-default-path → `TestScenario_exposure_lake_default_path`. lake-binding-env-and-ctx →
  `TestResolveLakeEnvInjectsBoundListener` and the `internal/function` Lake tests, plus the shims' ctx tests in
  their repos' CI. lake-query-authorized → `internal/lake/gateway` and `internal/auth/cedar`, both `ok`.
  old-config-key-refused → `TestScenarioOldConfigKeyRefused`. lake-quack-round-trip → the `duckdb` lane (final status
  PASS in 142 s; its cases include "lake-reader … round-trips SQL over the spec-bindings-lake binding" and
  "lake-recreate-keeps-url"). app-declares-lake and lake-backup-restore → the `app-todo` lane, case D18 (PASS in
  649 s; `funcdctl get lake todo-lake`, `e2e/app-todo.venom.yml:2024`). Package runs were `ok` for
  `api/types/v1alpha1`, `pkg/sdk`, `internal/platform/config`, `internal/lake/gateway`, `internal/auth/cedar` and
  `internal/provider`, and for `-run Lake` on `internal/controlplane`, `internal/services/lake` and
  `internal/function`.
- **Repo-wide evidence (not rerun)**: `scripts/agent/gate.sh` on 4204dcc2 returned GATE PASS. Its steps were audit,
  ci-full (627 s, e2e included), linux, linux-tests, stress (227 changed tests, 0 failed) and tree-unchanged.
- **Pins**: `go.mod` pins funcd-functions v0.21.0, funcd-python v0.15.0 and funcd-typescript v0.21.0. There is no
  `go.work` and no pseudo-version.
- **Tracking**: F180 is at `reviewing` and the ADR is at `Reviewing`. The history directories (`docs/adr` apart from
  the status line, `docs/reviews`, `docs/reports`) are untouched.
- **New tests are short and do not narrate**. Each one has a single doc comment that names its scenario.

### Definition of Done

4 / 5 items hold (the ADR's review checklist has 4 items, plus its Definition of done). Miss: checklist item 3,
"`provider.Catalog` and the DuckLake terms are unchanged". The identifier is unchanged, but its documented meaning
was renamed (Major 1, model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0261 (implementation) → changes-requested, 0/1/3, 3 model-attributed, DoD 4/5.
See docs/reviews/model-scorecard.md.

### Recommendation

The builder rewords the three provider-catalog sites and the `iam.go:64` comment, and restores the acceptance clause
on the ADR's status line. These are text-only changes, so a rerun of the gates is enough; the lanes do not need to
run again. Then this gate stamps `Implemented`.
The ADR's References cite no feature issue, so the PR has no `Fixes #N` to carry.
