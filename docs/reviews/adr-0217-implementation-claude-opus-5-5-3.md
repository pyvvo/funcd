# ADR-0217 implementation review, round 3: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 1 minor (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `4141c9ad`, on top of `0cd67ef5`. This round reviews the rework commit `4141c9ad`
("refuse an image or digest set by a substituted value"), which answers the round-2 review
(`docs/reviews/adr-0217-implementation-claude-opus-5-5-2.md`), and checks the whole branch again. The rework changes
only `internal/app/template/render.go` (+23) and its test file (+62).

The round-2 model finding is fixed, and no model finding remains. The verdict is still changes requested, because
the branch cannot land as it is: `main` now holds ADR-0212 (Implemented), so Decision 8 requires deploy to keep a
stored `spec.paused`, and the branch has no such code. This is a sequencing gap, not the model's error, but the
rework must close it before this gate can stamp the ADR `Implemented`.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` |
| `go test -race -count=1 ./internal/app/template ./internal/expr ./internal/workflow` | all `ok`, uncached, `EXIT=0` |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 13/13 `--- PASS`, among them `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses`, `EXIT=0` |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `TestScenarioAppDeployWaits/current`, `/failed` and `TestScenarioAppDeleteReports` pass (35.2 s), `EXIT=0` |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct |
| `git diff 0cd67ef5..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| `panic(`, `fmt.Print*`, a logging import, `not implemented` or `TODO` in non-test Go of the branch | none |
| `t.Skip` added in a test | none |
| Server packages changed | only the resolver move (`internal/expr`, `internal/workflow/condition.go`) |
| Mutant: the `imageField` check of the rework removed (`render.go:316-319`), through `go test -overlay` | killed by `TestRenderRefusesSubstitutedImage` |
| Probe tests through `go test -overlay` (not committed) | five more bypass forms, all refused (listed below) |
| `git merge-tree --write-tree HEAD origin/main` | conflicts in `cmd/funcdctl/app.go`, `cmd/funcdctl/app_test.go`, `docs/reviews/model-ledger.json`, `docs/reviews/model-scorecard.md` |

Probe forms, each refused with `<file>:<line>: <path>` and the image or digest path inside the value:

- `workflows: ${{ values.list }}`, a list whose step sets `function.image`;
- `workflows[0].steps: ${{ values.list }}`, with the key written `IMAGE`;
- `sites: ${{ values.list }}`, with `image`;
- `functions: - ${{ values.obj }}`, with the key `imageDigeſt` (U+017F, which the decode folds to `imageDigest`);
- `functions: ${{ values.list }}`, whose image equals the rendered `reg.example/shop-api:1.0.0`: refused too, because
  Decision 6 wants `${{ images.<name> }}` written in the fragment, not an equal string.

### Round-2 findings

- **Major 1 (a substituted object or list sets an image or a digest), fixed.** `router.scalar` now walks each
  substituted node with its path (`imageField`, `render.go:324-341`) and refuses an image or digest field inside it,
  with the same case-insensitive paths as a written scalar. `TestRenderRefusesSubstitutedImage` covers the five forms
  of round 2 and a case variant, and shows that a key `image` in a Sensor `input` (a `json.RawMessage`) stays data.
- **Minor 1 (alias, merge-key and `!!binary` refusals not in the ADR's table), carried forward** as Minor 1 below.
- **Minor 2 (the branch conflicts with `main`), not addressed**, and now a Major (Major 1 below). The rework was
  asked to fix the model findings only, so it was right to leave it. It is still open.

### 🔴 Blockers

None.

### 🟡 Major 1: on `main`, Decision 8 requires deploy to keep `spec.paused`, and the branch cannot land without it · attribution: env (sequencing)

Evidence:

- `git merge-tree --write-tree HEAD origin/main` reports content conflicts in `cmd/funcdctl/app.go` (the `appCmd`
  verb list and the block after it, where `main` added `app pause` and `app resume`), `cmd/funcdctl/app_test.go`,
  `docs/reviews/model-ledger.json` and `docs/reviews/model-scorecard.md`.
- On `main`, ADR-0212 is `Implemented (2026-10-10)`. `AppSpec` has `Paused` and `WithoutPause()`
  (`api/types/v1alpha1/app.go:34-50` on `main`), and `sameAppSpec` compares `WithoutPause()`.
- ADR-0212 Decision 9 on `main` says that a plain `funcdctl apply` without `paused: true` resumes an App, and that
  "`app deploy` copies the stored `spec.paused` into the applied spec and never resumes one (ADR-0217 Decision 8)".
- On the branch, `Render` never sets `spec.paused`: it refuses a fragment key `paused` (`render.go:216-217`), and
  deploy applies the rendered App as it is (`cmd/funcdctl/app.go:371-380`). After a merge, a deploy that changes the
  spec of a paused App would apply `paused: false` and resume it. That breaks ADR-0217 Decision 8 ("deploy copies the
  stored `spec.paused` into the applied spec, so a deploy never resumes an App") and ADR-0212 Decision 9.

Attribution: not the model's error. The branch was built from `0cd67ef5` between 15:32 and 16:05 (+0200), and ADR-0212
reached `main` at 16:18 (+0200) through #876. Decision 8 makes the copy conditional ("Once `AppSpec` has `paused`"),
and on the branch's base `AppSpec` has no `paused`. This finding is not scored against the model.

Why it blocks the pass: a pass stamps ADR-0217 `Implemented`, which freezes it, and the next step only rebases and
gates the branch. A mechanical conflict resolution keeps both verb lists but adds no `paused` copy, and no test would
catch it. The gap must be closed while the ADR is still `Reviewing`.

Fix (the builder, in the next rework, even though the finding is not model-attributed):

1. Rebase `impl/adr-0217` onto the current `origin/main`. In `cmd/funcdctl/app.go`, keep both verb sets
   (`render`, `deploy`, `delete`, `history`, `rollback`, `pause`, `resume`) in `appCmd` and in `Short`, and keep both
   blocks after it; update `TestCLIAppGroupVerbs` to the seven verbs. Take `main`'s ledger and scorecard, then append
   this branch's review rows again with `scorecard.py`.
2. In deploy, copy the stored App's `spec.paused` into the rendered App before the compare and the apply, and compare
   a revision's spec with `WithoutPause()` (`sameAppSpec` on `main` already does).
3. Add a CLI test: a deploy that changes the spec of a paused App applies it with `paused: true` and the App stays
   paused. Check that the case-folded fragment `paused` refusal still holds after the rebase.

### Minor

- **Minor 1: the alias, merge-key and `!!binary` key refusals are not in the ADR's refusal table** ·
  attribution: adr. `walk` refuses an alias or a `<<` key (`render.go:232-233`) and a `!!binary` key
  (`render.go:235-236`). The refusals are sound, because each one would hide a field from routing. Record them when
  an ADR next touches the template format (ADR-0218 is the natural place). No code change.

### ✅ Verified correct (keep it)

- **The round-3 rework is minimal and tested.** One source change of 23 lines. `imageField` reuses the same
  `imagePath` and `digestPath` expressions as a written scalar, so the key-case rule of round 2 holds inside a value
  too. The test fails when the check is removed. The error names the expression's file, line and path, and the image
  or digest path inside the value.
- **Decision 6 now holds for every route into an image field**: a written scalar, a case-variant key, an escaped or
  tagged key, and a substituted object or list at any depth. A key `image` in a `json.RawMessage` field stays data.
- **The resolver move** (`internal/expr/schema.go`): `whenSchemaResolver` calls `expr.NewSchemaResolver` and keeps
  its behavior; the `internal/workflow` tests pass unchanged with `-race`. `StrictTypes` never returns `string` for an
  undeclared path. `Expr.Idents` is built on `identsOf`.
- **Load and values**: every Decision 1-2 refusal, YAML 1.2 values, the merge rules, draft 2020-12 only, no
  non-local `$ref`, and `unevaluatedProperties: false` added where Decision 3 says.
- **Routing**: typed substitution, pass-through byte for byte, refusal of mixed roots, an unparsable `${{`,
  interpolation, and an expression written from a value or a registry.
- **Deploy** (`cmd/funcdctl/app.go:312-496`): `n0` from `latestRevision`, the stored group, no apply when
  `sameAppSpec` holds, `no change` only when revision `n0` holds the spec, the highest revision in `[n0, latest]`
  that the App's UID controls and that holds the spec, exit 1 on `Failed`, on a changed spec or on a deleted App,
  conflicts retried, no client timeout.
- **Delete**: the tree kinds derived from `gc.Pairs()` at run time, controller UIDs followed transitively, `deleted`,
  `waiting` once, and `kept` only for non-`ref` stores still present.
- **Scope and conventions**: `internal/app/template` is not in `cmd/funcd`'s dependencies; errors use `api/fault`;
  `go.mod` changes as Decision 10 says; no test was weakened, skipped or deleted (`lockedBuffer` moved from
  `dev_phase3_test.go` to `cli_test.go` unchanged).

### Definition of Done

17 / 18 items hold. The items are the 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one
passing test per scenario name, no server package changed except the resolver move) and 7 applicable generic items
(real behavior, contracts honored, tree, conventions, deps, scope, tracking). Miss: "contracts honored", because of
Major 1 (env, sequencing): Decision 8's `paused` clause applies on `main` and is not implemented. `just ci-full` was
not run by this gate, because the repo-wide e2e suite runs once in the PR gate; the two e2e scenarios of this ADR were
run directly and pass. That item is not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → changes-requested, 0/1/1, 0 model-attributed, DoD 17/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Rebase onto `main`, add Decision 8's `paused` copy with a test, and run this review again. The rest of the work
needs no change. ADR-0217 stays `Reviewing` and F120 stays `reviewing`. Minor 1 needs no code change; record it in a
later ADR.
