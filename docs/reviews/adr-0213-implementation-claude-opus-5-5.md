# ADR-0213 implementation review: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 1 minor (ADR-0213 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0213`, head `a1ce1fe2`, on `origin/main` at `390497d8` (the merge of ADR-0212). Seven
commits: the types and server, the type tests, the server tests, e2e, the status bump, a comment reflow and an e2e fix. The diff is
15 files, +1441/−34; the non-test Go code is `api/types/v1alpha1/{app,configmap}.go`,
`internal/app/{reconcile,status,admission}.go`, `internal/gc/gc.go` and `pkg/funcd/funcd.go`, plus the generated
OpenAPI. `go.mod` and `go.sum` are unchanged.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` (tidy, generate, check-hygiene, fmt, golangci-lint plain and `dev`, `go test ./...` plain and `dev`, build, `go mod verify`) | `EXIT=0`; lint `0 issues.` twice; `hygiene: clean`; `git status` clean afterwards, so the regenerated OpenAPI matches the committed one |
| `go test -tags e2e -race -count=1 -run 'TestScenarioApp(SecretDeclared\|SecretUndeclaredRefused\|ConfigChangeRolls\|PreF116App)$' -v ./pkg/funcd` | `EXIT=0`, 4/4 `--- PASS` (both subtests of `app-secret-declared`), 0 skipped, no data race (34 s) |
| `go test -tags e2e -count=1 -run` over every `TestScenarioApp…` and the GC, Workflow and resource-group delete scenarios in `./pkg/funcd` | `EXIT=0`, `ok` (190 s): 50 tests, the 35 App Scenarios of ADR-0199, ADR-0200, ADR-0212 and this ADR and 15 GC, Workflow-delete and resource-group Scenarios, so the new pair and the Secret watch regress none of them |
| `go test -race -count=1` of the 18 new or extended tests in `./api/types/v1alpha1`, `./internal/app` and `./internal/gc` | all `--- PASS`, all packages `ok`, uncached |
| 20 mutants through `go test -overlay` (listed below) | 20 killed |
| `git diff 390497d8..HEAD -- docs/` | `docs/adr/0213-app-config-and-secret-declarations.md`: only the status line, `Accepted` → `Reviewing`; `docs/feat/0010-feat-apps.md`: only the F116 status cell, `accepted` → `reviewing` |

Mutants (each fails the named tests, so the guard is pinned):

- No Secret check (`internal/app/reconcile.go:358`): `TestAppSecretCheckStopsBeforeAnyWrite`, `…Order`,
  `…DegradesAfterReady` and `…FailsAtDeadline` fail.
- No key test (`reconcile.go:369`): `TestAppSecretCheckStopsBeforeAnyWrite` and `TestAppSecretCheckOrder` fail.
- The Secret check moved before the ownership check (`reconcile.go:315-323`): `TestAppSecretCheckOrder` fails.
- `MapSecret` without the name filter (`reconcile.go:616`): `TestMapSecret` fails.
- `configMaps` dropped from, or moved last in, the reconciler's `entries()` (`reconcile.go:122-128`):
  `TestAppConfigMapsWrittenFirst` (and `TestAppConfigChangeRolls` for the drop) fail.
- No repointing of a `config` name, or of an image step (`api/types/v1alpha1/app.go:554`, `:570`):
  `TestAppConfigRepointing` fails.
- A hash of 4 bytes (`app.go:152`): `TestAppConfigMapName` fails. The cap at 53 (`app.go:23`), no stored-name clash
  (`app.go:383`), no `validateSecrets` (`app.go:398`): `TestAppConfigValidate` fails.
- The undeclared rule without Workflows or without CatalogServices, or with a `ref` step not exempt
  (`internal/app/admission.go:126-136`): `TestAdmissionRefusesUndeclaredSecret` fails.
- `gc.InUse` without ConfigMap users (`internal/gc/gc.go:539-540`): `TestInUse`,
  `TestAppConfigMapInUseWaitsForALaterSweep` and `TestAppConfigChangeRolls` fail; without the CatalogService user
  (`gc.go:576-577`): `TestInUse` fails. `usedElsewhere` without ConfigMap (`gc.go:427`):
  `TestAppConfigMapInUseWaitsForALaterSweep` fails.
- No `(App, ConfigMap)` pair (`gc.go:42`): `TestPairsOrderAppConfigMapAfterBucket`, `TestPairsCoverEveryControllerRef`,
  `TestAppConfigMapInUseWaitsForALaterSweep` and `TestAppConfigChangeRolls` fail.
- No Secret watch (`pkg/funcd/funcd.go:1039`): both subtests of `TestScenarioAppSecretDeclared` fail on "the parts are
  written within 5 s", since the supervision period is 10 s.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The OpenAPI schema allows a `configMaps[].name` of 63 characters, while `Validate` refuses more than 52** ·
  attribution: `model`. `AppConfigMap.Schema` (`api/types/v1alpha1/app.go:156-161`) keeps the entry schema's
  `maxLength: 63` for `name` and states the cap only in the description; `api/openapi/funcd.v1alpha1.yaml` shows
  `maxLength: 63` under the description "at most 52 characters". A 53-character name passes a schema-driven client
  (an agent or a generated SDK) and is then refused by `App.Validate` with 422. The behavior is correct, since the
  server refuses it, but the published schema states a different limit than the server. Fix: set
  `MaxLength` to `maxAppConfigMapNameLength` on the `name` property in `AppConfigMap.Schema`.

### ✅ Verified correct (keep it)

- **Contracts match the ADR.** `AppSpec` gains `ConfigMaps []AppConfigMap` and `Secrets []AppSecret` after
  `Catalogs`, both `omitempty`; `AppConfigMap{Name, Ref, ConfigMapSpec inline}` and `AppSecret{Name, Description,
  Keys}` carry the contract's JSON names; `maxAppConfigMapNameLength = 52`; `AppConfigMap.Schema` is
  `entrySchema(r, ConfigMapSpec, false)` (no `deletion`); `AppConfigMap.MarshalJSON` uses `marshalEntry`, so a `ref`
  entry marshals as `{"ref":"shared"}`; `AppSecret.Schema` is closed, with `name` and `keys` required, `keys`
  `minItems: 1` and each key under the `envName` pattern; `validateEnvKey(op, field, key)` has the contract's message
  and `validateEnvKeys` calls it; `reasonSecretNotFound`, `reasonSecretKeyMissing` and
  `MapSecret(ctx, obj) []controller.Request` match; `pkg/funcd` registers
  `ctrl.Watches(v1.KindSecret.GVK(), appReconciler.MapSecret)`.
- **Decisions 1 and 2.** A spec without the sections marshals without them, so the stamp is unchanged
  (`TestAppConfigJSON`). `AppConfigMapName` is `<name>-` plus the hex of the first 5 bytes of SHA-256 over
  `json.Marshal(spec)`; the test recomputes it from `{"data":{"A":"1","B":"2"}}` and checks that key order does not
  change it and other data does. A 52-character name passes, 53 fails, two entries with one stored name and a data key
  that is not an env-var name are refused with their field paths.
- **Decision 3.** `entries()` repoints on copies: a Function's and a CatalogService's `spec.config` and an image
  step's `function.config` name the stored name, while a `ref` entry's name, an undefined name, a `ref` part and a
  `ref` step are left as written; `json.Marshal(a.Spec)` is byte-equal before and after `Parts()`. The AppRevision
  keeps the declared names (`TestAppConfigMapsWrittenFirst`).
- **Decision 4.** `configMaps` entries come first in both `entries()` functions; the ConfigMap's
  `resourceVersion` is lower than every other part's, it carries the App's controller reference, and its child line
  is `Ready` once it exists, through the existing no-status rule of `judge`.
- **Decision 5.** A data change creates a new ConfigMap and repoints `todo-api`; the old one stays `Pruning`
  `NotCurrent` until the switch, is deleted in the switch pass, and stays `Pruning` `InUse: Function/audit` while a
  Function the App does not control names it. A rollback creates it again. `gc.Pairs()` has `(App, ConfigMap)` right
  after `(App, Bucket)` and right before `(App, AppRevision)`, and no `(App, Secret)`; the GC keeps an App ConfigMap
  that a foreign Function or a Function held by an open run names, and collects it once both are gone.
- **Decisions 6 and 7.** `validateSecrets` refuses a name that is not a DNS label, a repeated name, empty `keys`, a
  repeated key and a key that is not an env-var name, each with its field path; strict decoding refuses `data` in a
  `secrets` entry, and the API answers 422. The undeclared rule runs in `app-parts` before the part admissions, on
  create and on update, for a Function, an image step and a CatalogService, with the ADR's exact message; a `ref`
  step is exempt; it is not in `App.Validate` (a part naming an undeclared Secret passes `Validate`).
- **Decision 8.** `checkSecrets` runs after the `ChildNotOwned` loop and before the first write, in declaration
  order, names the first missing key, and tests membership only. A stopped pass writes no part, prunes nothing,
  requeues after `controller.SupervisionPeriod`, sets `Ready=False` and the latest AppRevision's `Applied=False`
  with the reason, gives `Degraded` after Ready, and at the deadline turns the revision `Failed` with
  `ChildNotReady` naming the Secret. A Secret has no child line, and the App never owns it. The sentinel test
  collects every log message and attribute, every returned error and the App and both AppRevisions as JSON, and
  finds no Secret value; a rotated value changes no `resourceVersion`. `MapSecret` reads only the Secret's metadata
  and requeues only the Apps of its namespace that declare its name. Nothing in `internal/app` creates, updates or
  deletes a Secret.
- **Decision 9.** A stored App whose part names an undeclared Secret keeps reconciling to a new current revision
  (`TestAppOlderAppWritesStatus`); in e2e its next apply and `app rollback` to its first AppRevision are refused and
  stamp nothing, and the same spec with the declaration added is accepted.
- **Scenarios.** Each of the 4 Scenarios has a `TestScenarioApp…` e2e test in `pkg/funcd/app_config_e2e_test.go`
  whose assertions follow the ADR text: the parts written within 5 s of the key, the worker's `STRIPE_API_KEY`, a
  value change that moves no part's `resourceVersion` and reaches a worker started afterwards; the deadline path with
  `failedPacing()`; the 422 refusals with their field paths and nothing stored; the ConfigMap created before
  `todo-api` is written (watch events compared by `resourceVersion`), `TZ=UTC` after the switch, the old ConfigMap
  deleted, rollback back to `Europe/Paris`, and a hand-changed undefined ConfigMap that moves no part's generation.
  Every item of plan step 3 has a named test.
- **Conventions.** No `any` in an exported or port API, no `panic`, `fmt.Print*`, `t.Skip` or production
  `time.Sleep`, `log/slog` only, ctx-first, `api/fault` kinds throughout; the last commit replaces a trailing
  `require.Never` whose goroutine could outlive the test with a check in the test goroutine.
- **Scope.** Nothing outside Decisions 1 to 9 and the Implementation plan was added: no hold check, no dry run, no
  pre-hook, no Secret write, no migration. The one-line change in `internal/app/revision_test.go` adds ConfigMap to
  `versionsAll`, which the new tests need.

### Definition of Done

15/15 hold: the 6 Review-checklist items, 3 items of plan step 4 (`just ci` green, a passing test per Scenario, no
`go.mod` change), and 6 generic items (real behavior with no stubs, Contracts honoured, tree versus the plan,
conventions, scope, tracking). For `just ci-full`, this gate ran `just ci` (exit 0), the 4 Scenarios with `-race`
and the App and GC e2e Scenarios. The full `test-e2e` lane runs once at the PR gate (`scripts/agent/gate.sh`) and was
not run here.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0213 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 15/15. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0213 moves `Reviewing → Implemented` and FEAT-0010 F116 `reviewing → implemented` in this commit. The
`model` Minor (the schema's `maxLength` for a defined ConfigMap name) is a small follow-up for the builder. The main
session moves the board card to Done. The PR gate still owes the full `just ci-full` run.
