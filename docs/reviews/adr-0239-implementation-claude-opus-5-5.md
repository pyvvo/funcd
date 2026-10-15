## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0239 implementation, model: claude-opus-5-5)

Reviewed `git diff 34435008..e3d3b06a` on branch `docs/adr-site-runtime-config`: the implementation commit on top of the
accepted ADR. The lane evidence is the builder's app-todo run on real containerd (cases C1-C5), read from its log and
not rerun here.

Two Definition-of-done items are outside this review and must land with the PR:

- **The funcd-functions release and its `go get`.** The lane stages `apps/site-config` from funcd-functions. That
  directory exists only in an unreleased commit, which a gitignored local `go.work` points at; `go.mod` still pins
  v0.14.0. With the `go.work` disabled, `TestScenarioLaneStagesResolve` (`internal/testkit/langmod`) fails because
  v0.14.0 has no `apps/site-config`. The PR needs the release and the bump.
- **The repo-wide gate** (`scripts/agent/gate.sh`) runs once on the PR.

The F152 `kind/feature` issue is not filed yet. It is filed with the PR, whose description says `Fixes #N`. The
accepted ADR stays frozen without the citation.

### Minor

- **m1 · `model` · the e2e refusal test hardcodes the cap.** Decision 7 makes the 32 KiB cap "a fixed constant shared
  by admission, the reconciler and the tests". `pkg/funcd/site_config_e2e_test.go:465` builds the oversized value with
  `33<<10` and expects the literal `32768`, instead of deriving both from `v1.SiteRuntimeConfigMaxBytes`. The unit tests
  derive the input from the constant but match the literal in the message (`api/types/v1alpha1/site_runtimeconfig_test.go:47`,
  `internal/site/runtimeconfig_test.go:201`). A changed cap fails these tests loudly, so the cost is maintenance only.
- **m2 · `model` · `site-config-configmap-missing` does not check the bundle.** The scenario says that the bundle and
  the last `config.json` keep serving while the Site is `ConfigMapNotFound`. `TestScenarioE2ESiteConfigConfigMap`
  (`pkg/funcd/site_config_e2e_test.go:310-314`, and after the delete at 333-335) checks only `config.json`; no request
  to `/` runs in that window. The reconciler half checks the bundle rule in its fresh-Site subtest
  (`internal/site/runtimeconfig_test.go:150`) but not in the serving one (159-169). `compileRoute` always emits the
  bundle rule, so the behavior holds; the assertion is missing.
- **m3 · `model` · C5 waits inside its script.** C5 polls `config.json` in a `for … sleep 1` loop for up to 10 s
  (`e2e/app-todo.venom.yml:2553`), and the step has no `retry`. Rule 3 of the venom-e2e skill makes `retry`/`delay`
  the wait. The loop exists to keep the ETag and digest from before the deploy in shell variables. A mutation step
  that records them in the VM, followed by an assertion step with `retry`/`delay`, would follow the rule. The only
  other loop in the suite waits for a service to stop, not for a reconcile.

### ✅ Verified correct (keep it)

- **Checks**, each run once from the worktree:
  - `scripts/agent/d go build ./...` → exit 0.
  - `scripts/agent/d go vet ./api/types/v1alpha1/ ./internal/site/ ./internal/gc/ ./internal/app/ ./pkg/funcd/` → exit 0.
  - `scripts/agent/d go tool golangci-lint run` on the same packages → exit 0, 0 issues; with `--build-tags e2e ./pkg/funcd/`
    → exit 0, 0 issues.
  - `scripts/agent/d go test -count=1 ./api/types/v1alpha1/ ./internal/site/ ./internal/gc/ ./internal/app/ ./internal/controlplane/... ./pkg/funcd/`
    → exit 0. This includes the OpenAPI golden test in `internal/controlplane`, so the regenerated
    `api/openapi/funcd.v1alpha1.yaml` matches the types.
  - `scripts/agent/d go test -count=1 -tags e2e -run 'TestScenarioE2ESiteConfig' -v ./pkg/funcd/` → exit 0.
  - `scripts/agent/d go test -count=1 -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` → exit 0 (with the local
    `go.work`).
  - `scripts/agent/d go mod verify` → exit 0. `git status --porcelain` is empty.
- **Scenarios.** All 13 scenarios have a passing e2e test. Each test calls `t.Parallel()` and names its scenarios in
  its doc comment. The scenarios that say "within 5 s" wait at most 5 s (`configWithin`). The template test's time is
  four `funcdctl` calls of about 1 s each, not a wait. The reconciler halves have unit tests in
  `internal/site/runtimeconfig_test.go`.

| Test | Scenarios | Time |
|---|---|---|
| `TestScenarioE2ESiteConfigTemplate` | two-environments, revalidates, ahead-of-spa, app-configmap | 4.98 s |
| `TestScenarioE2ESiteConfigConfigMap` | configmap-missing, configmap-change, configmap-deleted | 0.13 s |
| `TestScenarioE2ESiteConfigPathMounted` | path-mounted | 0.08 s |
| `TestScenarioE2ESiteConfigShadowsThenRemoved` | shadows-bundle-file, removed | 0.11 s |
| `TestScenarioE2ESiteConfigRouteConflict` | route-conflict, in both orders | 0.08 s |
| `TestScenarioE2ESiteConfigSharedPrefix` | shared-prefix | 0.11 s |
| `TestScenarioE2ESiteConfigRefused` | refused: key, rule with and without a trailing slash, size | 0.05 s |

- **Contracts match the ADR exactly.** `SiteSpec.RuntimeConfig` carries the `doc` tag, which is now the OpenAPI
  description. `SiteRuntimeConfig` holds values and one ConfigMap name, and no Secret reference. `SiteRuntimeConfigFile`,
  `SiteRuntimeConfigMaxBytes = 32 << 10`, `RuntimeConfigPath()` and `RenderSiteRuntimeConfig` (`json.Marshal` of a
  string map, in key order, `{}` for nil) are as specified. No edge code and no Route field changed.
- **The reconciler follows the ADR's order** (`internal/site/reconcile.go:162-200`). The config step runs after the
  bundle's index is asserted and before `ensureRoute`, and a failed bundle deploy returns before it. The object is
  written before the Route carries its rule, and the rule is compiled exactly when `runtimeConfig` is set and the
  object exists. The config reason is set after the Route step, and `RouteNotReady` wins over it. An unchanged body is
  not written again. Removal deletes the object first and is keyed on the Route's rule, so a failed delete keeps the
  rule for the next pass (`TestScenarioSiteConfigRemovedDeletesThenDrops`).
- **A failure rewrites nothing and never holds back the bundle rule.** `ConfigMapNotFound`, `RuntimeConfigTooLarge`
  over the cap, and `RuntimeConfigTooLarge` from the Bucket's `maxObjectBytes` each have a unit test. An `Invalid` Put
  maps to the existing `MaterializeFailed` reason, as `unpack` does.
- **The key is per Site**: `<prefix>/_runtime/<site>/config.json`. `servingDigest` ignores it, since the slug pattern
  rejects `_runtime/<site>`, and the shared-prefix tests prove that two Sites keep their own files.
- **App ConfigMaps.** `entries()` repoints `runtimeConfig.configMap` on a copy, as it repoints the config of a Function
  or a CatalogService, and the App's spec stays as declared (`TestAppSiteRuntimeConfigRepointing`). `gc.InUse` counts
  a Site as a user. Both the App's prune and the collector read it (`TestInUse`, the App prune subtest, and
  `InUse: Site/audit` in the e2e test). No API deletion protection uses `InUse`, so a named ConfigMap can still be
  deleted, as `site-config-configmap-deleted` requires.
- **The watch.** `MapConfigMap` maps a changed or deleted ConfigMap to the Sites of its namespace that name it, and
  `pkg/funcd/funcd.go` registers it.
- **Validation.** A values key must pass `validateEnvKey`, the rule that ConfigMap data keys also pass, so every key of
  the file is an identifier. The rule clash trims trailing slashes and follows `ingress.path`. The size check uses the
  shared renderer. `App.Validate` validates each part, so App templates meet the same rules.
- **The lane on real containerd** (app-todo, `final status: PASS`). C1 pushed one bundle and deployed the template into
  staging and prod. C2 served each environment's own `API_URL` with one bundle digest. C3 returned
  `Cache-Control: no-cache`, `Content-Type: application/json`, a weak ETag and `304` on a match. C4 served the SPA index
  on a deep link and the generated file at `/config.json`. C5 turned the flag on with a new ETag and the same digest,
  and prod kept its file. The lane ended 31 s before the implementation commit, which fits a run on the committed tree.
- **The example** (funcd-functions `apps/site-config`). `app.js` fetches `config.json` by a URL resolved against its own
  script, not against the page. The README has the Vite and `<base href>` snippets and says that the file is public.
- **Tracking.** The implementation commit changed only the ADR's status line (`Accepted → Reviewing`), and F152 was at
  `reviewing`. The acceptance commit holds the back-links in ADR-0139 and ADR-0213 and the blueprint clause. No
  dependency was added; `go.mod` and `go.sum` are unchanged.

### Definition of Done

11 / 13 hold: the Review checklist 6/6 and the ADR's "Done when" 5/5 (build, lint, tests with `-tags e2e`,
`go mod verify`, the lane on real containerd). Two items are pending, not failed (process): the funcd-functions release
with its `go get` bump, and the repo-wide gate on the PR.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0239 (implementation) → pass, 0/0/3, 3 model-attributed, DoD 11/13. See
docs/reviews/model-scorecard.md.

### Recommendation

Pass. The stamps are applied: ADR-0239 is `Implemented` and F152 is `implemented`. Before the PR merges, release
funcd-functions with `apps/site-config`, take it here with `go get`, and run the gate. The PR description says
`Fixes #N` for the F152 issue. Fix the three minors in the same PR if convenient.
