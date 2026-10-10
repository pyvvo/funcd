# ADR-0217 implementation review, round 5: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 1 minor (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `70528ada`, still based on `0ba028df`. This round reviews the rework commit
`70528ada` ("strict typing reads a value only through type object"), which answers the model-attributed finding of the
round-4 review (`docs/reviews/adr-0217-implementation-claude-opus-5-5-4.md`), and checks the whole branch again.

The model's round-4 finding is fixed, with a test that fails without the fix. On the branch, everything is green, and
no finding is the model's. One finding still holds the pass: the round-4 sequencing finding. The branch was not
rebased, and merged with the current `main` it still does not build, and its unit test and its two e2e scenarios still
fail. The rework task told the builder to fix the model-attributed finding and nothing else, so this is not counted
against the model.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` (tidy, specgen, hygiene, fmt, lint `0 issues.` twice, tests, `go build`, `all modules verified`) |
| `go test -race -count=1 ./internal/app/template ./internal/expr ./internal/workflow` | all `ok`, uncached |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 17/17 `--- PASS`, among them `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses` |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `TestScenarioAppDeployWaits/current` (7.9 s), `/failed` (24.3 s) and `TestScenarioAppDeleteReports` (3.5 s) pass, `EXIT=0` |
| Revert check: `TestRenderRefuses` with the pre-fix `internal/expr/schema.go` through `go test -overlay` | `--- FAIL: TestRenderRefuses/untyped_parent`, so the new test covers the fix |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct |
| `git diff 0ba028df..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| `panic(`, `fmt.Print*`, a logging import, `not implemented`, `TODO` or `t.Skip` added in Go | none |
| `git merge-tree --write-tree HEAD origin/main` (`main` at `390a6981`) | conflicts in `cmd/funcdctl/cli_test.go`, `docs/reviews/model-ledger.json`, `docs/reviews/model-scorecard.md` |
| Trial merge in a scratch worktree, since removed | build fails; after a scratch rename, one unit test and all three e2e runs fail (Major 1) |

### Round-4 findings

- **Major 1 (model): `StrictTypes` descended a segment without an explicit `type`, fixed.** In strict mode,
  `schemaResolver.Resolve` now refuses to read through a segment whose `type` is not `object`
  (`internal/expr/schema.go:76-78`), so the check reports the reference as unknown and names it. The root stays
  exempt, as the round-4 review allowed, because `ReadValues` and the merge make `values` a mapping. The non-strict
  path used by Workflow `when` is unchanged (the check is guarded by `s.strict`), and the `internal/workflow` tests
  pass with `-race`. `schema_test.go` now expects `fault.NotFound` for `loose.k` instead of asserting the old reading,
  and `TestRenderRefuses/untyped_parent` (`internal/app/template/template_test.go:359`) renders
  `${{ values.loose.n }}` with `loose: str` and expects `resources/a.yaml:3: kv[0].maxValueBytes` and `loose`. The
  round-4 probe case (a parent with `properties` and no `type`, given a string) now fails on the client, naming the
  file, line and path, instead of rendering a wrong App.
- **Major 2 (env, sequencing): the branch does not build or pass its scenarios once merged with `main`, not
  addressed.** Carried forward as Major 1 below.
- **Minor 1 (adr): alias, merge-key and `!!binary` refusals not in the refusal table, carried forward** as Minor 1.

### 🔴 Blockers

None.

### 🟡 Major 1: the branch does not build or pass its scenarios once merged with the current `main` · attribution: env (sequencing)

Evidence: a scratch worktree at `70528ada`, `git merge --no-commit origin/main` (`main` at `390a6981`, one commit,
ADR-0203's #888, past the round-4 check). The result matches round 4:

1. Textual conflicts: `cmd/funcdctl/cli_test.go` (imports: `sync` on the branch, `sync/atomic` on `main`; both are
   needed), `docs/reviews/model-ledger.json` and `docs/reviews/model-scorecard.md`.
2. With those resolved, `go build ./...` fails: `cmd/funcdctl/app.go:319:70: undefined: appApplyAttempts`. ADR-0210's
   #878 replaced `appApplyAttempts` and `devApplyAttempts` with one `applyAttempts` (`cmd/funcdctl/workflow.go` on
   `main`).
3. With that name changed in the scratch tree, build passes and the `cmd/funcdctl` app tests pass (both render
   scenarios included). Two failures remain:
   - `TestRenderRefuses/unknown_section` (`internal/app/template/template_test.go:356`) still uses `configMaps` as its
     unknown section, which ADR-0213's #886 made a section of `v1.AppSpec`.
   - `TestScenarioAppDeployWaits/current`, `/failed` and `TestScenarioAppDeleteReports` fail at the first deploy with
     `admission.app-parts: spec.functions[0].secrets[0]: Function/todo-api names Secret "todo-stripe-key", which
     spec.secrets does not declare` (ADR-0213 Decision 7).

Attribution: not the model's error. The base predates #878 and #886, and the round-5 rework task scoped the builder to
the model-attributed finding only ("and nothing else"), so the builder was right not to rebase in that rework. Not
scored against the model.

Why it still holds the pass: the reason round 4 gave is unchanged. A pass freezes ADR-0217 as `Implemented`, and the
fixture and golden changes this merge needs are part of this ADR's verified scenarios: the fixture keeps "only the
sections and fields `v1.AppSpec` holds when this ADR is built", and on `main` that includes `secrets` and
`configMaps`. Those changes should be reviewed before the stamp, not made during an integration after it.

Fix (builder, or the integration step, whichever the run routes it to; the rework loop sends only model-attributed
findings to the builder, so this finding must be routed explicitly or the next round repeats it):

1. Rebase onto the latest `origin/main`. Keep both imports in `cli_test.go`, and use `applyAttempts` in `deploy`.
   Take `main`'s ledger and scorecard, and let the review gate append its rows again with `scorecard.py`.
2. Give `TestRenderRefuses/unknown_section` a key that is no App section.
3. Restore the fixture's `resources/secrets.yaml` (declaring `todo-stripe-key`) and the `configMaps` part from the
   design note, regenerate `render.golden.yaml`, and run the two e2e scenarios. The test still creates Secret
   `todo-stripe-key`, because an App never creates a Secret (ADR-0213 Scope).
4. Right before the next review, check that `git merge-tree --write-tree HEAD origin/main` is clean.

### Minor

- **Minor 1: the alias, merge-key and `!!binary` key refusals are not in the ADR's refusal table** ·
  attribution: adr. `walk` refuses an alias or a `<<` key (`internal/app/template/render.go:232`) and a `!!binary`
  key (`render.go:235-236`). The refusals are sound, because each one would hide a field from routing. Record them
  when an ADR next touches the template format (ADR-0218 is the natural place). No code change.

### ✅ Verified correct (keep it)

- **The strict-typing fix** (`70528ada`) is three lines in the resolver, guarded by strict mode, with the old test
  assertion replaced rather than deleted and a render-level test that fails without the fix. The doc comment of
  `StrictTypes` states the new rule.
- **The paused copy** (`a413d29e`): the stored `spec.paused` is copied after render and before both the `sameAppSpec`
  compare and the apply; `TestCLIAppDeployKeepsPaused` passes.
- **The resolver move** (`internal/expr/schema.go`): `whenSchemaResolver` calls `expr.NewSchemaResolver` and keeps its
  behavior; `StrictTypes` never returns `string` for an undeclared path; `Expr.Idents` is built on `identsOf`.
- **Load and values**: every Decision 1-2 refusal, YAML 1.2 values, the merge rules, draft 2020-12 only, no non-local
  `$ref`, and `unevaluatedProperties: false` added where Decision 3 says.
- **Routing and images**: typed substitution, pass-through byte for byte, refusal of mixed roots, an unparsable `${{`,
  interpolation, an expression written from a value, and an image or digest set by a written scalar, a case-variant
  key or a substituted value. A key `image` in a `json.RawMessage` field stays data.
- **Deploy**: `n0` from `latestRevision`, the stored group, no apply when `sameAppSpec` holds, `no change` only when
  revision `n0` holds the spec, the highest controlled revision in `[n0, latest]` that holds the spec, exit 1 on
  `Failed`, on a changed spec or on a deleted App, conflicts retried, no client timeout.
- **Delete**: the tree kinds derived from `gc.Pairs()` at run time, controller UIDs followed transitively, `deleted`,
  `waiting` once, and `kept` only for non-`ref` stores still present.
- **Scope and conventions**: `internal/app/template` is not in `cmd/funcd`'s dependencies; errors use `api/fault`;
  `go.mod` changes as Decision 10 says; no test was weakened, skipped or deleted (the `lockedBuffer` helper moved from
  `dev_phase3_test.go` to `cli_test.go`).

### Definition of Done

17 / 18 items hold. The items are the 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one
passing test per scenario name, no server package changed except the resolver move) and 7 applicable generic items
(real behavior, contracts honored, tree, conventions, deps, scope, tracking). Miss: "one passing test per scenario
name" (Major 1, env: the two e2e scenarios fail on the merge with the current `main`). `just ci-full` was not run by
this gate, because the repo-wide e2e suite runs once in the PR gate; the two e2e scenarios of this ADR were run
directly. That item is not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → changes-requested, 0/1/1, 0 model-attributed, DoD 17/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

No model finding is left. Route Major 1 to the builder or the integration step explicitly: rebase onto the latest
`main`, fix the one unit test, restore the fixture's `secrets` and `configMaps` parts with a new golden, and run the
two e2e scenarios. Run this review again once `git merge-tree` against `main` is clean. ADR-0217 stays `Reviewing` and
F120 stays `reviewing`. Minor 1 needs no code change.
