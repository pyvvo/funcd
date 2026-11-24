## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (ADR-0344 implementation, model: claude-opus-5-5)

This report reviews `feat/addon-runtimes` from `f7977e96` (the Jobs work, PR #1555, not reviewed here) to `1e098771`:
the feature `006450b8`, the guide and behavior rows `508bed1a`, the dev scenario's snapshot Addon `7ad79554`, the
containerd lane case `8a7435fa`, the funcd-addons v0.3.0 pin `9b6e2171` and the status bump `1e098771`. The reviewer
did not write the code. Every check below was run at `1e098771`, not read.

The design is implemented as decided: resolution, the gate's three causes, the image recorded once, the Addon watch for
Functions and Jobs, the admission's names and kinds, and the engine path all hold, and seven of nine mutants are
caught. One finding sends the work back: the guide's first command applies a manifest that the new API refuses.

### 🔴 Blockers

None.

### 🟡 Major 1 — The add-on guide's first command applies a manifest that the new API refuses  ·  attribution: model

- `docs/guide/develop/services/addons/index.md:19` pipes
  `https://github.com/pyvvo/funcd-addons/releases/download/v0.2.0/rauthy.yaml` to `funcdctl apply -f -`.
- funcd-addons' release attaches each `<addon>/app/addon.yaml` as `<addon>.yaml` (its
  `.github/workflows/release.yml:74`). At v0.2.0, `rauthy/app/addon.yaml` has the flat shape that this change removes:
  `spec.kind`, `spec.image`, `spec.port`, and `spec.runtimes` holding the string `containerd`.
- A funcdctl built at `1e098771` refuses that file before it sends any request, exit 1:
  `sdk.DecodeManifest: decode Addon: … json: unknown field "basePath"`. v0.3.0's `rauthy/app/addon.yaml` decodes and
  reaches the request. The API's schema refuses the old shape too: `AddonSpec` is closed and requires `runtimes`, a
  list of `ShippedRuntime` objects.
- `508bed1a` rewrote the paragraphs around this command, and `9b6e2171` moved the pin to v0.3.0, but the command still
  names v0.2.0. FEAT-0019 (#1516) changed this link when it pinned v0.2.0. ADR-0332's first review raised the same
  command as its Major 1.
- Design check: it happens at `1e098771` (above). Nothing decided it: ADR-0344's Scope lists the guide, Implementation
  plan item 5 lists the guide's add-on pages, and the Review checklist asks that the guide match. No issue matches
  (searched for `rauthy.yaml` and `releases/download funcd-addons`), and no board card holds it.

Fix (builder): name v0.3.0 in the command.

### Minor

- **Minor 1 — Doc text that the rewrite left stale or broken** · model. Three doc strings still name the Addon's flat
  fields, which now sit in the kind entry's `spec.runtimes[<i>].attributes`: `api/types/v1alpha1/addon.go:603`
  (`AddonObjectSpec.Config`, "its Addon's spec.config"), `:619` (`AddonObjectBindings.Secret`, "the Addon's
  spec.secretKeys") and `internal/controlplane/addon.go:508` (the 422 case of an add-on object's write, "the schema of
  the Addon's spec.config"). They reach the API reference (`api/openapi/funcd.v1alpha1.yaml:180`, `:213`, `:892`,
  `:25674`, `:26049`) and the Spec pages (`docs/guide/develop/services/addons/spec.md:139`, `:150`,
  `docs/guide/develop/workloads/app/spec.md:45`, `:58`). Separately, `internal/services/addon/reconcile.go:28` breaks
  the `Reconcile` doc comment's sentence after "object's". Fix: name the kind entry's `config` and `secretKeys`,
  rejoin the comment, then run `just generate`.
- **Minor 2 — Two rules have no test that reaches them** · model.
  - (a) Decision 8 records the image "while no worker of the Revision runs". Mutant M2b removes that guard
    (`internal/function/addonruntime.go:118`) and survives `TestScenarioOperatorRuntimeUnchanged`. That test's
    assertion "a running worker of the Revision keeps it unrecorded" holds because the Function is steady, and a steady
    pass skips the gate and the fill. When `steadyState` is forced to return false, the real code passes the test and
    M2b fails it (`Should be empty, but was registry.example.com/deno@sha256:…`). The code is right, but nothing pins
    the guard. Fix: make a pass run while a worker of the Revision runs (two replicas, one of which exits), then assert
    that the Revision stays unrecorded and that the replacement runs the operator's image.
  - (b) Decision 4's override keys on a server. `cmd/funcd/main.go:418` hands the keys of
    `runtime.containerd.imageOverride` to `WithImageOverrides`. Mutant M7 passes no keys and survives all of
    `cmd/funcd`'s tests. `TestScenarioAddonRuntimeNameRefused` sets the option itself. Fix: add a `cmd/funcd` test whose
    config maps an override key, and in which an Addon that ships that name gets 400.

### Routed (not this work's)

- **pyvvo/funcd-addons' README** (line 18 at v0.3.0) still pipes v0.1.0's `rauthy.yaml`, which also has the old shape.
  Home: elsewhere, that repository's next PR.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go build ./...`; `go vet` on the 15 touched packages, also with
  `-tags dev` on `cmd/funcdctl` and `-tags e2e` on `pkg/funcd`; `go tool golangci-lint run` on the same packages, also
  with `--build-tags dev`, 0 issues; gofmt clean; `go test` on the touched packages, all ok (`pkg/funcd` 93 s,
  `cmd/funcdctl -tags dev` 234 s); the new tests under `-race`; the ten Docker-backed dev scenarios on colima's Docker,
  all run and passed with none skipped, among them `TestScenarioDevAddonBackup` with `7ad79554`'s nested snapshot
  Addon; `./scripts` and `internal/testkit/langmod`, which guard `lanes.yaml` and the Venom suites; `go mod tidy -diff`
  empty; `go run ./scripts/behavior -check`; `just check-hygiene`; the spec generator left `api/openapi` and the Spec
  pages unchanged.
- **The scenarios**, each a named, unskipped, passing test:

  | Scenario | Test |
  |---|---|
  | addon-runtime-function | `TestScenarioAddonRuntimeFunction`; `TestContainerdModePullsAnAddonsRuntimeImage` for the pull; the lane case |
  | addon-runtime-wrong-workload | `TestScenarioAddonRuntimeWrongWorkload` (Job and Engine entries) |
  | addon-runtime-process-driver | `TestScenarioAddonRuntimeProcessDriver` |
  | addon-runtime-job | `TestScenarioAddonRuntimeJob` |
  | addon-runtime-appears | `TestScenarioAddonRuntimeAppears` (before the Addon, then while it served only Job) |
  | addon-runtime-pinned | `TestScenarioAddonRuntimePinned` |
  | addon-runtime-gone | `TestScenarioAddonRuntimeGone` |
  | operator-runtime-unchanged | `TestScenarioOperatorRuntimeUnchanged`; `TestADR0149_MissingImageContainerd` unchanged |
  | addon-runtime-name-refused | `TestScenarioAddonRuntimeNameRefused`, `TestAddonShapeRefusals` |
  | addon-shape-refused | `TestScenarioAddonShapeRefused`, `TestAddonShapeRefusals` |
  | addon-engine-unchanged | `TestScenarioAddonEngineUnchanged`; the three rauthy lanes |
  | addon-engine-process-server | `TestScenarioAddonEngineProcessServer`, on the production provider runtime |
  | addon-runtimes-only | `TestScenarioAddonRuntimesOnly` |
  | addon-template-needs-kind | `TestScenarioAddonTemplateNeedsKind`, `TestLoadRefusesABadAddon` |

- **The mutants**, each run with `go test -overlay`:

  | Mutant | Result |
  |---|---|
  | M1, never record a Revision's image | caught: `TestScenarioAddonRuntimeAppears`, `TestScenarioOperatorRuntimeUnchanged` |
  | M2b, record it while a worker runs | survived (Minor 2a) |
  | M3, workers ignore the recorded image | caught: `…Function`, `…Pinned`, `…Job` |
  | M4, the steady pass skips the gate | caught: `TestScenarioAddonRuntimeGone` |
  | M5, no 409 for another Addon's runtime | caught: `TestScenarioAddonRuntimeNameRefused` |
  | M6, the template loader takes an Addon with no kind | caught: `TestLoadRefusesABadAddon`, `TestScenarioAddonTemplateNeedsKind` |
  | M7, no override keys reach the admission | survived (Minor 2b) |
  | M8, an Addon's change requeues no Job | caught: `TestScenarioAddonRuntimeJob` |
  | M9, funcd's own runtime names allowed | caught: `TestScenarioAddonRuntimeNameRefused` (`duckdb`, `rqlite`) |

- **The design**: `ownRuntime`, then `shippedRuntime`, then ADR-0149's rule (Decision 6). `runtimeGate`'s three causes
  read as Decision 6 words them. A Revision records the entry's image at its creation (`stamp`), or once in
  `fillRuntimeImage`, and `workerImage` runs it. `MapAddon` requeues the Functions and the Jobs on a runtime that is
  none of funcd's own (Decisions 7 and 11). A steady pass rechecks the gate, so a deleted Addon fails its Functions at
  their next pass while their serving workers keep serving. A JobRun starts only on a Ready Job, and the Job's pass
  records the image before it sets Ready, which is the condition in the ADR's Design model. An http Function cannot
  name an Addon's runtime, since no Addon ships a node name (`TestAddonRuntimeFunctionIsCloudEvent`).
- **The admission**: funcd's own names come from `embedimg.Runtimes()`, the node and python prefixes from `Validate`,
  and the override keys from the config. A runtime that another Addon ships gets 409 in `addon-unique`, which takes the
  cluster-scoped lock. An Addon gains its kind once and keeps it.
- **The contracts** match the ADR's block. Because a Function entry shares the attributes struct, `port` and
  `readiness` are optional in the schema, and `Validate` requires them in an Engine entry with 400, as Decision 3 and
  the behavior row say. `embedimg.Runtimes()` adds no image to funcdctl: `go tool nm` on a funcdctl built at
  `1e098771` lists embedimg's init and `tarFile`, and no embedded file system.
- **The lanes**: the four lane logs under the main checkout's `.cache/lanes/` end `final status: PASS`. `addon-runtime`
  ran all five cases and eight steps. Its times fit `9b6e2171`, and those of `addon-rauthy-signin` and
  `addon-rauthy-restore` fit `1e098771`. From `9b6e2171` to `1e098771`, only the ADR's status line and the F139 row
  change. pyvvo/funcd-addons#8 is merged, v0.3.0 is released and pinned, and its `rauthy/app/addon.yaml` is one Engine
  entry whose attributes hold the kind.
- **Tracking**: the ADR's diff is its status line alone. The F139 row reads `runtimes: reviewing · dev workers: accepted
  · shim protocol: idea`. The blueprint was synced at acceptance (`blueprint.md:44`, `:288`). The behavior rows in
  `addons.md`, `workers.md`, `functions.md`, `funcdctl.md` and `funcdctl-dev.md` are the ones that the ADR lists.

### Definition of Done

16 of 17 items hold: 7 of the 8 Review-checklist items and 9 generic items (the touched packages' checks, with the
gate's `just ci-full` still to run; every scenario; real behavior; the contracts; the tree; the conventions; the deps;
no scope creep; tracking). The miss is the checklist's "The guide, the API reference … match" (Major 1 and Minor 1,
model).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0344 (implementation) → changes-requested, 0 blockers, 1 major and 2 minors, all 3
model-attributed, DoD 16/17. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to the builder: Major 1, a one-line fix, and the two Minors. Nothing is stamped: ADR-0344 stays `Reviewing`, and
F139 stays `runtimes: reviewing`. The PR that ships it references #1017 and does not close it, since the last of
F139's ADRs closes it.
