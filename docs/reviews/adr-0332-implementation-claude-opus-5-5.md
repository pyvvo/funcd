## Verdict: changes requested — 1 blocker, 1 major (ADR-0332 implementation, model: claude-opus-5-5)

Reviewed on `feat/addons` at 7c08dcd7: the implementation (986f53b5), the add-on side of the edge sign-in (9ce67f82), the
lane fixtures (4220a5a1) and the guide pages (652cf96d). The code meets the ADR's Review checklist, and every Scenario
has a passing test named after it. The verdict is held back by one failing package run and by a guide page whose first
command and example manifest do not work.

### 🔴 Blocker 1 — pkg/funcd's full test run fails TestMain's answers check · attribution: model

ADR-0329 Decision 4: on a full run of `pkg/funcd`, `TestMain` fails when a described operation lists a status that no
test got. The Addon and AddonObject operations are described (`registerClusterCRUD` with their cases,
`addonObjectOperations` in `internal/controlplane/addon.go`), and no answers matrix gets their listed statuses, so
`go test ./pkg/funcd/`, and with it `just ci`, fails. Runs filtered with `-run` pass, as Decision 4 checks only observed
statuses then. The integrator is writing those matrices on `feat/addons`; this finding closes when they land.

### 🟡 Major 1 — The add-on guide's first command and its example IdentityProvider fail · attribution: model

`docs/guide/develop/services/addons/index.md` states a command and a manifest that funcdctl refuses:

- Line 17, `funcdctl apply -f https://github.com/pyvvo/funcd-addons/releases/download/v0.1.0/rauthy.yaml`: `apply -f`
  reads a local file or stdin (`cmd/funcdctl/cli.go:442-455`). When run, it exits 1:
  `funcdctl apply: read https://…/rauthy.yaml: open https://…/rauthy.yaml: no such file or directory`.
- Lines 28-43, the IdentityProvider: `spec.secrets` is not a field. `funcdctl apply -f` on the example as written exits 1:
  `decode IdentityProvider: … json: unknown field "secrets"`. The field is `spec.bindings.secret`
  (`api/types/v1alpha1/addon.go:212`). With it, the example still exits 1:
  `ObjectMeta.Validate: resourceGroup is required for kind "IdentityProvider"` (`api/types/v1alpha1/metadata.go:187-188`;
  only `funcdctl dev` fills in a resource group).

Fix (builder): fetch, then apply (`curl -fsSL <url> | funcdctl apply -f -`), and write the example as the lane fixture
`e2e/fixtures/addon-rauthy-login.yaml` and funcd-addons' `rauthy/examples/identityprovider.yaml` do, with
`metadata.resourceGroup` and `spec.bindings.secret`.

### Minor

- **Minor 1 — Two rules have no Go test of their own · model.** (a) Decision 5's `..` refusal: mutant M1 below removes
  it from `keptPath` (`internal/dataplane/dataplane.go`), and `TestAddonEntryKeepsItsPathAndRefusesPrivatePaths` and
  `TestScenarioAddonPrivatePath` still pass. Each `..` path they send cleans to a private path or to a path outside
  `/auth/v1`, which other checks refuse. A probe that sends `/auth/v1/x/../ping` gets 404 from the work and 418 (the
  engine) from the mutant. Only the lane step `/auth/v1/x/../ping` pins it. (b) No test reaches the reconciler's
  `EnvInvalid` hold (`internal/services/addon/reconcile.go:58-61`); `TestRenderEnv` covers only the function. Fix: add the
  `/auth/v1/x/../ping` case to the scenario test, and one reconcile test whose env fails to evaluate.
- **Minor 2 — The API and the reconciler match an object's Addon differently · model.** The API finds an object's Addon
  by plural (`addonFor`, `ofPlural` in `internal/controlplane/addon.go`), which is the kind without case, as Decision 1
  compares kinds. The reconciler compares the kind exactly (`addonOf` in `internal/services/addon/reconcile.go:108-119`,
  `MapAddon` in `addon.go:123-129`). A probe deleted the Addon `rauthy` and registered the kind again as
  `Identityprovider`. The API then served `login` under the new Addon, and the reconciler kept it `Pending`,
  `AddonNotFound`, until the object was written again. Decision 2 gives an object to "the Addon that registers its
  `spec.kind`", and the two components disagree on which Addon that is. Fix: match by plural in `addonOf` and `MapAddon`.
- **Minor 3 — Wording that does not match the code or the ADR · model.**
  - `docs/guide/develop/tools/funcdctl/plugins.md:24-25` shows `NAME  PATH` and `fns   /usr/local/bin/funcdctl-fns`; funcdctl
    prints `NAME   PATH` and `fns    <path>` (tabwriter padding 3, `cmd/funcdctl/plugin.go:173`).
  - `docs/guide/develop/services/addons/index.md:56` says that an OIDCProvider whose `addon` names the IdentityProvider "has
    funcd register" the client. `client.registration` does that (ADR-0336); `addon` names where funcd reaches the provider
    (`api/types/v1alpha1/oidc.go:27-32`).
  - `docs/guide/develop/services/addons/build.md:26` says, without a condition, that an engine reaches nothing outward and
    only funcd reaches its port. The page then tests under `funcdctl dev`, which applies no fence (ADR-0334 Decision 2).
  - `docs/behavior/addons.md`: the row "An Addon's kind" marks the 400 of `cannot change` `decided`, while Decision 1
    says 422. It is `differs`, as the row's neighbors mark their 400s. The row "An Addon's image" cites Decision 6 for the
    422, and the 422 is in the scenario `addon-image-pinned`.

### Routed (not this work's)

- **400 where the ADR says 422 · adr · home: elsewhere, the decider.** The scenarios `addon-image-pinned` (the registry
  refusal) and `addon-env-checked`, Decision 1 (`kind` cannot change), Decision 4 (the env check) and Decision 5 (the
  `authenticated` stance) say 422. So does ADR-0335 Decision 3 for a basePath under `/.auth`. Each of these refusals is a
  `fault.Invalid`, which is 400 by ADR-0148 Decision 1, and ADR-0332 does not supersede that Decision. The code follows
  ADR-0148, the tests assert 400 and say why, and the registry marks the rows `differs`. That matches the earlier rows
  for ADR-0291 (`docs/behavior/admission.md`) and ADR-0292 (`docs/behavior/limits.md`). The refusals that a schema checks
  answer 422, as the ADR says: a tag, a `process` runtime, an object's config. The work cannot meet both ADRs, so by the
  gate's rule the `Implemented` stamp waits for the decider's ruling, or for a short partial supersession of ADR-0332
  that says 400. Counted as one Minor (adr).
- **A failed converge shows `EngineNotReady` · uncovered · home: the decider (a question).**
  `internal/services/addon/reconcile.go:75-91` reports `EngineNotReady` with the cause in the message, and returns the
  error, so the controller retries. ADR-0332 exposes `EngineNotReady` and no reason for a failed converge. The Lake and
  the SQLDatabase report `EngineConvergeFailed` (`internal/services/lake/reconcile.go:165-169`,
  `internal/services/sql/reconcile.go:155-159`). A sound design could want either. No fix until the decider answers.
- **A lowercase token that ends in "s" is read as a plural · uncovered · home: the decider (a question).**
  `descriptorFor` (`pkg/sdk/kinds.go`) maps a kind name to its plural (Decision 2) and reads a lowercase token that ends
  in "s" as a plural. For a kind such as `Redis` (plural `rediss`), `funcdctl get redis` therefore asks for the plural
  `redis` and gets 404, while `Redis` and `rediss` work. Decision 2 says nothing of lowercase CLI tokens, and a client
  cannot read Addons (admin-only) to tell the two forms apart. No fix until the decider answers.
- **The engine's input chain admits funcd's bridge address · decided.** ADR-0332 Decision 3 names loopback and funcd's
  bridge address (ADR-0238) as the sources that the chain admits. Every funcd component that dials an engine from the
  host uses that address: the readiness probe, the edge, and the sign-in back channel. No finding.
- **The Addon uniqueness check runs on create only · decided.** Decision 1: "A change or a delete takes no lock and
  checks no object". The kind cannot change (`addonRules`), so a replace keeps its plural, and a replace never creates an
  object (`replaceObjIf` reads the stored object first). Two creates take the cluster-scoped admission lock (ADR-0147),
  since `addonUnique` reads Addons and is marked. No finding.
- **Guide statements of ADR-0333 and ADR-0334 · home: elsewhere, the integration step.** These do not exist at this
  commit and come with those ADRs' branches: a BackupSchedule scope `addons`, `snapshot.path`, `restore.path`,
  `restore.env` with `${{ restore.file }}`, dev's admin token `funcd-dev-admin-token`, and `funcdctl dev` running add-on
  engines. The published rauthy Addon also carries ADR-0333's `snapshot` and `restore`. At this commit funcdctl's decoder
  refuses it (`decode Addon: … json: unknown field "restore"`), so the `addon-rauthy` lane needs ADR-0333's AddonSpec
  fields as well as the module pin. Check the pages once they merge.
- **Pending the pin and the final gate, not findings.** `TestScenarioLaneStagesResolve` fails with
  `go mod download github.com/pyvvo/funcd-addons: … not a known dependency`, and `TestScenarioExamplesBuildWithTheTools`
  waits on the same pin. The `addon-rauthy` lane case, which is the packet-level proof of the input chain on
  containerd, was not run.

### ✅ Verified correct (keep it)

- **Checks**: `go build ./...` exit 0. `go vet` on the touched packages exits 0 on darwin, and on linux for
  `internal/network`, `internal/runtime/containerd` and `cmd/funcd`. `go tool golangci-lint run` reports 0 issues on the
  touched packages, on darwin and on linux. `gofmt -l` lists nothing, and `go mod verify` passes. `go test -race` passes
  on `api/types/v1alpha1`, `internal/auth/cedar`, `internal/controlplane/...`, `internal/dataplane`,
  `internal/edge/router`, `internal/edge/static`, `internal/network/...`, `internal/platform/config`,
  `internal/platform/pathx`, `internal/provider`, `internal/runtime`, `internal/runtime/containerd`,
  `internal/services/addon`, `pkg/sdk`, `scripts/behavior`, `cmd/funcd`, and `cmd/funcdctl` (also with `-tags dev`).
  `go test -race -run Addon ./pkg/funcd/`: 20 tests pass, each in 0.4 to 1.0 s, with `t.Parallel()` and short pacing.
  `just check-behavior` exits 0, `just check-specs` reports 21 specs with 0 failing, and `just check-hygiene` reports
  clean. The Linux-only tests of the work (`TestFenceAdmitsOnlyLoopbackAndTheBridgeAddress`,
  `TestRegistryCredsReadTheDockerStore`) did not run on this host. They vet clean under `GOOS=linux`, and they run in the
  gate's `just test-linux`.
- **Scenarios**: all 13 have a test named after them, and each passes: `TestScenarioAddonRegisterAdminOnly`,
  `…ImagePinned`, `…EnvChecked`, `…ObjectRuns`, `…KindUnregistered`, `…ConfigSchema`, `…ConfigInvalid`, `…Removed`,
  `…PrivatePath`, `…RuntimeUnavailable`, `…SecretKeys`, `…SecretMissing` and `…SecondKind`. They run over a fake
  `provider.Runtime` and an httptest engine, as the Implementation plan says.
- **Decision 1**: the kind form, a kind of funcd's in any case (`funcdKind`, authorization-only kinds included), and a
  plural that funcd serves. `ReservedPlurals` takes the plurals from the OpenAPI paths, so it covers `pkg/sdk/kinds.go`
  and also non-kind routes such as `volumes` and `deadletters`. Another Addon's kind or plural is refused 409, and a kind
  that changes is refused. The image pattern is `<repository>@sha256:<64 hex>` (a tag beside the digest is refused, a
  registry port is accepted). `runtimes` takes `containerd` only. Each private path must lie under `basePath`.
  `contract.Check` runs the profile gate and also compiles (`internal/contract/contract.go:45`).
- **Decision 2**: one name is one object per namespace across add-on kinds (409). With no Addon of the kind, create and
  replace are 404, and list, get and delete still serve what is stored. A config off the schema is 422, at
  `body.spec.config`, and the detail names the property. The cap is 400 through a marked admission (`ReadsNamespace`
  while the cap is on); 0 means 20, and a negative value means no cap. Dry runs are tested
  (`TestAddonObjectDryRun`), and `If-Match` is wired as for every other kind (`withReplaceVersion`, `deleteGuarded`).
- **Decisions 3 and 4**: the reconciler reads the Addon on each pass and watches Addons, Secrets and OIDCProviders. It
  holds an object with no engine and no entry for `AddonNotFound`, `RuntimeUnavailable` (naming both modes),
  `ConfigInvalid`, `SecretNotFound` and `BindingResolveFailed`. The engine gets one replica, `EgressTo("")` (fenced, with
  no endpoint) and `HostOnly`. `HostOnly` is copied to the WorkerSpec and is part of the spec fingerprint
  (`TestConvergeCopiesTheFenceToTheWorker`). `writeNetns` refuses a host-only worker that has no egress fence (read). Only the
  `secretKeys` reach the engine: mutant M2 is killed. Secrets are read by Cedar with the entity type `AddonObject`, as
  for a Lake, and a namespace Policy can forbid one (`TestAddonSecretForbidden`,
  `TestSecretCapability_AnAddonObjectReadsAsItself`). The ADR writes `funcd::AddonObject`; funcd's Cedar schema has no
  namespace, so `AddonObject` is right.
- **Decision 5**: the entry is an upstream entry at `basePath` with the path kept, ranked after the Lake's
  (`TestAggregator_AddonObjectRankedAfterTheLake`). Its stance is the object's own `ingress.auth`, and a namespace's
  `authenticated` default does not reach it. `ingress.path` and `authenticated` are refused. A private path spelled with
  `//` or `/./` is 404, and funcd reaches it at `status.address`. The oidc stance, `ProviderNotFound` with its 503 and
  its end on a new provider, the default namespace's host rule, and the `/.auth` refusal follow ADR-0335 Decisions 3 and
  10. They also match how a Function shows `ProviderNotFound` (`internal/function/ingress.go`).
- **Decision 6**: `AddonImageAllowed` normalizes with `distribution/reference`, as `image_ref.go` does. It compares the
  host without case and refuses a host that only starts with a listed one. The reconciler reads no registry list
  (`TestAddonRegistryRemovedStopsNoEngine`). The containerd driver pulls with the Docker credential store
  (`credentialResolver`, read; its Linux-only test runs in the gate).
- **Checklist "no add-on kind"**: no non-test Go file, no part of the OpenAPI golden and nothing in the Cedar schema
  names `IdentityProvider` or `EchoService`.
- **Contracts**: the types, `AddonPlural`, `ProviderSpec.HostOnly` and `FenceNetns(netnsPath, allow, admit)` match.
  The fields marked ADR-0333 (`snapshot`, `restore`, `deletion`) are left to ADR-0333's branch.
- **Tree and conventions**: every file of the Implementation plan is present. The extras are explained: `pathx`
  shares `ContainsDotDot` with the static backend, `internal/runtime` carries `WorkerSpec.HostOnly`, and `kindref.Omitted`
  lists the two kinds. The commits add no dependency (the `go.mod` change comes from ADR-0335's commit). There is no stub,
  no `panic` and no TODO. The ADR changed only from `Accepted` to `Reviewing`, the feat row F258 reads `reviewing`, and
  the blueprint names the Addon.
- **Guide pages, against the code**: `plugins.md` matches `cmd/funcdctl/plugin.go`: the variables and their sources,
  `dev` as the version of an unversioned build, the plain-word rule, built-ins and the first plugin on PATH win, exec
  replaces the process, an exec failure exits 1, and relative PATH entries are skipped. A built `funcdctl` ran the page's
  `funcdctl-fns` with `--server` handed on. In `build.md`, these match the code and pyvvo/funcd-addons: the engine table
  (except its last row, rauthy's stop behavior, which is the image's and was not checked), `kind`, `image`,
  `privatePaths`, `config`, `env` (the ternary and `+` are in `internal/expr`'s subset), `secretKeys`, the plugin's
  `keys`, `urls` and `$FUNCDCTL apply -f -`, and funcd-addons' CI, e2e, release assets and manifest checks.

### Mutants (`go test -overlay`, nothing written to the tree)

- **M1**: `keptPath` without the `..` refusal. It survives `TestAddonEntryKeepsItsPathAndRefusesPrivatePaths` and
  `TestScenarioAddonPrivatePath` (Minor 1).
- **M2**: `secretEnv` without the `secretKeys` filter. It is killed: `TestScenarioAddonSecretKeys` fails, since
  `BOOTSTRAP_ADMIN_PASSWORD` reaches the engine.

### Definition of Done

17 of 20 items hold: the 8 Review-checklist items, `just check-behavior`, and 8 generic items (real behavior, contracts,
the provider runtime's tests, tree, conventions, dependencies, scope, tracking). Misses:
- The item "`just ci` is green" fails (Blocker 1, model).
- The item "the guide pages state only real fields and commands" fails (Major 1, model).
- The item "the `addon-rauthy` lane case passes" is pending the module pin, ADR-0333's AddonSpec fields and the final
  gate.

The 400 and 422 difference is routed to the decider: the scenario tests pass and assert the convention's 400.

### Model scorecard

To record: claude-opus-5-5 on ADR-0332 (implementation) → changes-requested. 1 blocker, 1 major and 4 minors; 5 are
model-attributed, and the adr-attributed one is the 400 and 422 Minor. DoD 17/20.

### Recommendation

Back to the builder: the answers matrices (in hand with the integrator), the guide's command and example, and the
Minors. The decider rules on the 400 and 422 difference before the `Implemented` stamp. After the module pin and
ADR-0333's merge, the final gate runs the `addon-rauthy` lanes and the two pending tests.
