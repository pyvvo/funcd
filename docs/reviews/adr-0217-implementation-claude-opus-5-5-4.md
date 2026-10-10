# ADR-0217 implementation review, round 4: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 2 majors, 1 minor (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `70552d76`, rebased onto `0ba028df`. This round reviews the rework commits
`a413d29e` ("app deploy keeps the stored spec.paused") and `70552d76` ("run the ADR-0217 template scenarios in
parallel"), which answer the round-3 review (`docs/reviews/adr-0217-implementation-claude-opus-5-5-3.md`), and checks
the whole branch again.

The round-3 finding is fixed: the branch now sits on a base that has ADR-0212, and deploy copies the stored
`spec.paused`. On the branch, everything is green. Two findings hold the pass. One is the model's: `StrictTypes`
descends a values segment that has no explicit `type`, which Decision 4 forbids, and a mistyped value then renders
silently. The other is sequencing: `main` has moved again (ADR-0210, ADR-0213), and the branch merged with it no longer
builds, and its two e2e scenarios fail.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` (tidy, specgen, hygiene, fmt, lint `0 issues.`, tests, `go build`, `go mod verify`) |
| `go test -race -count=1 ./internal/app/template ./internal/expr ./internal/workflow` | all `ok`, uncached, `EXIT=0` |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 17/17 `--- PASS`, among them `TestScenarioAppRenderMatches`, `TestScenarioAppRenderRefuses` and `TestCLIAppDeployKeepsPaused`, `EXIT=0` |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `TestScenarioAppDeployWaits/current` (7.7 s), `/failed` (24.0 s) and `TestScenarioAppDeleteReports` (4.2 s) pass, `EXIT=0` |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct |
| `git diff 0ba028df..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| `panic(`, `fmt.Print*`, a logging import, `not implemented`, `TODO` or `t.Skip` added in Go | none |
| Server packages changed | only the resolver move (`internal/expr`, `internal/workflow/condition.go`) |
| Probe test through `go test -overlay` (not committed) | an untyped parent segment renders a mistyped value silently (Major 1) |
| Trial merge with `origin/main` (`1d563ae5`) in a scratch worktree, since removed | build fails; after a scratch rename, one unit test and both e2e scenarios fail (Major 2) |

### Round-3 findings

- **Major 1 (deploy must keep `spec.paused` once `main` has ADR-0212), fixed.** The branch is rebased onto
  `0ba028df`, which has ADR-0212. `applyRendered` copies the stored `spec.paused` into the rendered App before the
  compare and the apply (`cmd/funcdctl/app.go:377`), and `sameAppSpec` compares `WithoutPause()`.
  `TestCLIAppDeployKeepsPaused` shows that a deploy that changes a paused App's spec keeps it paused, that the same
  spec is not applied again, and that a resume during the wait is no spec change. Both verb sets are in `appCmd` and
  `Short`, and `TestCLIAppGroupVerbs` lists the seven verbs.
- **Minor 1 (alias, merge-key and `!!binary` refusals not in the ADR's table), carried forward** as Minor 1 below.

### 🔴 Blockers

None.

### 🟡 Major 1: `StrictTypes` descends a segment without an explicit `type`, so a mistyped parent value renders silently · attribution: model

Decision 4: "each segment an expression reads must be declared under properties with one explicit type". In strict
mode, `schemaResolver.Resolve` checks the type only of the last segment (`internal/expr/schema.go:89`). An
intermediate segment is descended whenever it has `properties` (`schema.go:74-87`), with or without a `type`. The test
asserts this reading: `internal/expr/schema_test.go:45` ("a parent needs properties, not a type, to be descended").

Evidence (a probe test through `go test -overlay`, not committed): `valuesSchema`
`{"type":"object","required":["a"],"properties":{"a":{"required":["n"],"properties":{"n":{"type":"integer"}}}}}`
and the fragment `kv: [{name: shop-kv, maxValueBytes: ${{ values.a.n }}}]`.

- values `{"a":{"n":7}}` renders `maxValueBytes: 7`, as expected;
- values `{"a":"str"}` passes validation, because `properties` and `required` apply only to objects. Render then
  succeeds with `maxValueBytes` absent (0), no error and exit 0, although `values.a.n` was checked as a required
  integer.

So a values mistake that the ADR says fails on the client, naming the key, deploys a wrong App instead. Hand-written
schemas often leave `type: object` off a node that has `properties`, so this is not a corner case.

Fix (builder): in strict mode, refuse to descend through a segment whose `type` is not exactly `object`, naming the
path, as for the leaf. Change the `schema_test.go:45` assertion to expect `fault.NotFound`, and add a render test where
a parent value has the wrong type. The root of `values` can stay exempt: `ReadValues` and `mergeValues` make it a
mapping.

### 🟡 Major 2: the branch does not build or pass its scenarios once merged with the current `main` · attribution: env (sequencing)

Evidence: a scratch worktree at the branch head, `git merge --no-commit origin/main` (`main` at `1d563ae5`).

1. Textual conflicts: `cmd/funcdctl/cli_test.go` (imports: `sync` on the branch, `sync/atomic` on `main`; both are
   needed), `docs/reviews/model-ledger.json` and `docs/reviews/model-scorecard.md`.
2. After those are resolved, `go build ./...` fails with `cmd/funcdctl/app.go:319:70: undefined: appApplyAttempts`.
   ADR-0210's #878 replaced `appApplyAttempts` and `devApplyAttempts` with one `applyAttempts`
   (`cmd/funcdctl/workflow.go` on `main`). The rework `a413d29e` started using `appApplyAttempts` two minutes after
   #878 merged.
3. With that name changed in the scratch tree, build and vet pass, and the `cmd/funcdctl` app tests pass (both render
   scenarios included). Two failures remain:
   - `TestRenderRefuses/unknown_section` (`internal/app/template/template_test.go:351`) uses `configMaps` as its unknown
     section. ADR-0213's #886 made `configMaps` (and `secrets`) sections of `v1.AppSpec`, so render now accepts it.
     The code is right; the test needs another unknown key.
   - `TestScenarioAppDeployWaits/current`, `/failed` and `TestScenarioAppDeleteReports` fail at the first deploy with
     `admission.app-parts: spec.functions[0].secrets[0]: Function/todo-api names Secret "todo-stripe-key", which
     spec.secrets does not declare` (ADR-0213 Decision 7).

The fixture's own rule settles the fix. The ADR's fixture keeps "only the sections and fields `v1.AppSpec` holds when
this ADR is built", and on `main` `v1.AppSpec` holds `secrets` and `configMaps`. So the parts of the design note's
to-do template that the fixture dropped for ADR-0213 come back. These are `resources/secrets.yaml`, which declares
`todo-stripe-key` (design note, "secrets: declarations only (names, keys), no values"), and the `configMaps` section.
The test still creates Secret `todo-stripe-key` before it deploys, because an App never creates a Secret (ADR-0213
Scope). The golden App of `app-render-matches` changes with the fixture.

Attribution: not the model's error. The branch is based on `0ba028df`. #878 merged at 15:16 UTC and #886 at
15:54 UTC, each two minutes before `a413d29e` and `70552d76` respectively were committed. This finding is not scored against the model.

Why it holds the pass: a pass freezes ADR-0217 as `Implemented`, and the fixture and golden changes above are part of
this ADR's verified scenarios. They should be reviewed, not made during a merge after the ADR is frozen.

Fix (builder, in the same rework as Major 1):

1. Rebase onto the latest `origin/main`. Keep both imports in `cli_test.go`, and use `applyAttempts` in `deploy`.
   Take `main`'s ledger and scorecard, and let the review gate append its rows again with `scorecard.py`.
2. Give `TestRenderRefuses/unknown_section` a key that is no App section.
3. Restore the fixture's `secrets` and `configMaps` parts as above, regenerate `render.golden.yaml`, and run the two
   e2e scenarios.
4. Right before the next review, check that `git merge-tree --write-tree HEAD origin/main` is clean, so that `main`
   does not move under the branch again.

### Minor

- **Minor 1: the alias, merge-key and `!!binary` key refusals are not in the ADR's refusal table** ·
  attribution: adr. `walk` refuses an alias or a `<<` key (`internal/app/template/render.go:232-233`) and a `!!binary`
  key (`render.go:235-236`). The refusals are sound, because each one would hide a field from routing. Record them
  when an ADR next touches the template format (ADR-0218 is the natural place). No code change.

### ✅ Verified correct (keep it)

- **The paused copy** (`a413d29e`) is one line in the right place: the copy happens after render and before both the
  `sameAppSpec` compare and the apply. The deploy retry now reuses the existing apply bound instead of a second
  constant, and the test fails without the copy.
- **The e2e scenarios run in the parallel phase** (`70552d76`). Each starts its own platform, so the package's serial
  time stays under `go test`'s default timeout.
- **The resolver move** (`internal/expr/schema.go`): `whenSchemaResolver` calls `expr.NewSchemaResolver` and keeps its
  behavior; the `internal/workflow` tests pass unchanged with `-race`. `StrictTypes` never returns `string` for an
  undeclared path. `Expr.Idents` is built on `identsOf`.
- **Load and values**: every Decision 1-2 refusal, YAML 1.2 values, the merge rules, draft 2020-12 only, no
  non-local `$ref`, and `unevaluatedProperties: false` added where Decision 3 says.
- **Routing and images**: typed substitution, pass-through byte for byte, refusal of mixed roots, an unparsable
  `${{`, interpolation, an expression written from a value, and an image or digest set by a written scalar, a
  case-variant key or a substituted value. A key `image` in a `json.RawMessage` field stays data. Sections are
  appended by reflection over `v1.AppSpec`, so ADR-0213's `configMaps` and `secrets` render without a code change.
- **Deploy**: `n0` from `latestRevision`, the stored group, no apply when `sameAppSpec` holds, `no change` only when
  revision `n0` holds the spec, the highest revision in `[n0, latest]` that the App's UID controls and that holds the
  spec, exit 1 on `Failed`, on a changed spec or on a deleted App, conflicts retried, no client timeout.
- **Delete**: the tree kinds derived from `gc.Pairs()` at run time, controller UIDs followed transitively, `deleted`,
  `waiting` once, and `kept` only for non-`ref` stores still present.
- **Scope and conventions**: `internal/app/template` is not in `cmd/funcd`'s dependencies; errors use `api/fault`;
  `go.mod` changes as Decision 10 says; no test was weakened, skipped or deleted.

### Definition of Done

16 / 18 items hold. The items are the 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one
passing test per scenario name, no server package changed except the resolver move) and 7 applicable generic items
(real behavior, contracts honored, tree, conventions, deps, scope, tracking). Misses: "contracts honored" (Major 1,
model: Decision 4's explicit type per segment) and "one passing test per scenario name" (Major 2, env: the two e2e
scenarios fail on the merge with the current `main`). `just ci-full` was not run by this gate, because the repo-wide
e2e suite runs once in the PR gate; the two e2e scenarios of this ADR were run directly. That item is not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → changes-requested, 0/2/1, 1 model-attributed, DoD 16/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Make the strict resolver require `type: object` on every descended segment, with a test (Major 1). Then rebase onto
the latest `main` and bring the fixture, the golden and the one unit test in line with ADR-0213 (Major 2). Run this
review again once the branch merges cleanly with `main`. ADR-0217 stays `Reviewing` and F120 stays `reviewing`.
Minor 1 needs no code change.
