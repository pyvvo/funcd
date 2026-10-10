# ADR-0217 implementation review, round 6: claude-opus-5-5

## Verdict: pass, 0 blockers, 0 majors, 3 minors (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `26b53101`, now based on `390a6981`. This round reviews the rebase and the two
commits that answer the round-5 sequencing finding (`docs/reviews/adr-0217-implementation-claude-opus-5-5-5.md`):
`69f419b1` ("build ADR-0217 on main after ADR-0210 and ADR-0213") and `26b53101` ("the to-do template declares its
Secret and defines its ConfigMap"). It also checks the whole branch again.

The round-5 Major is fixed. The branch builds and passes on its new base, and merged with the current `main`
(`b785e9a8`, three commits later) it still builds and its four scenarios pass. The only merge conflicts are in the
append-only review ledger and scorecard. No finding is the model's. Every Review-checklist item holds, so ADR-0217
moves to `Implemented` and F120 to `implemented`.

### Verification run

All commands ran through `scripts/agent/d`, in the review worktree unless the row says "trial merge".

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` (tidy, specgen, hygiene, fmt, lint `0 issues.` twice, tests, the `dev`-tag tests, `go build`, `all modules verified`) |
| `go test -race -count=1 ./internal/app/template ./internal/expr ./internal/workflow` | all `ok`, uncached |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 17/17 `--- PASS`, among them `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses` (7 subtests) |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `/current` (7.9 s), `/failed` (24.3 s) and `TestScenarioAppDeleteReports` (3.5 s) pass, `EXIT=0` |
| `git merge-tree --write-tree HEAD origin/main` (`main` at `b785e9a8`) | conflicts only in `docs/reviews/model-ledger.json` and `docs/reviews/model-scorecard.md` (Minor 3) |
| Trial merge with `origin/main` in a scratch worktree, ledger and scorecard taken from `main`, since removed | `go build ./...` exit 0; `go vet` of the touched packages exit 0; `internal/app/template`, `internal/expr` and `cmd/funcdctl` tests `ok`; the two e2e scenarios pass (8.1 s, 23.8 s, 4.3 s), `EXIT=0` |
| `git range-diff 0ba028df..70528ada 390a6981..HEAD` | the 15 earlier commits carried over unchanged, except the conflict resolution in `cli_test.go` (both `sync` and `sync/atomic` kept) and the review docs |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct |
| `git diff 390a6981..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| `panic(`, `fmt.Print*`, a logging import, `not implemented`, `TODO`, `FIXME` or `t.Skip` added in Go | none |

### Round-5 findings

- **Major 1 (env, sequencing): the branch did not build or pass its scenarios on `main`, fixed.** All four steps of the
  round-5 fix are done:
  1. The branch is rebased onto `390a6981`. `deploy` retries a conflict with ADR-0210's shared `applyAttempts`
     (`cmd/funcdctl/app.go:319`), and the comment on `applyAttempts` (`cmd/funcdctl/workflow.go:253`) now names
     `app deploy` too.
  2. `TestRenderRefuses/unknown_section` uses `volumes`, which is not an App section
     (`internal/app/template/template_test.go:356`).
  3. The fixture declares Secret `todo-stripe-key` with both keys (`resources/secrets.yaml`) and defines ConfigMap
     `todo-settings` (`resources/config.yaml`), as the design note's App does (`docs/reports/app-design.md:1257-1266`).
     `render.golden.yaml` gains both sections, and the golden test still compares the whole output exactly. The e2e
     test creates only the Secret, with both keys, because an App never creates one (ADR-0213). `app-delete-reports`
     also expects `deleted ConfigMap/todo-settings-<hash>` (`pkg/funcd/app_template_e2e_test.go:181`).
  4. Against the current `main`, only the ledger and the scorecard conflict (Minor 3).
- **Minor 1 (adr): alias, merge-key and `!!binary` refusals not in the refusal table, carried forward** as Minor 1.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Minor 1: the alias, merge-key and `!!binary` key refusals are not in the ADR's refusal table** · attribution:
  adr. `walk` refuses an alias or a `<<` key (`internal/app/template/render.go:232`) and a `!!binary` key
  (`render.go:235-236`). The refusals are sound, because each one would hide a field from routing. Record them when an
  ADR next touches the template format (ADR-0218 is the natural place). No code change.
- **Minor 2: the fixture text of the Scenarios assumes ADR-0213 is not built** · attribution: adr. The Scenarios
  preamble says the fixture drops `resources/secrets.yaml` and the `configMaps` section, and that the e2e test creates
  ConfigMap `todo-settings` because "they exist outside the App before and after ADR-0213". Implementation plan step 4
  says the same. ADR-0213 was implemented first. On the base, `AppSpec` holds `configMaps` and `secrets`
  (`api/types/v1alpha1/app.go:51-53`), and ADR-0213 Decision 7 refuses a Function that names an undeclared Secret.
  The preamble's general rule, "keeping only the sections and fields `v1.AppSpec` holds when this ADR is built", makes
  the fixture keep both sections, so the builder followed the rule that governs. Record the change when ADR-0218, which
  extends this fixture, is next edited. No code change.
- **Minor 3: the review ledger and scorecard conflict with `main`** · attribution: env (integration). `main` gained
  rows after the rebase (#892), and both sides append to the same files. When this branch is integrated, keep the rows
  of both sides in `model-ledger.json` and regenerate `model-scorecard.md` with `scorecard.py`. No code change.

### ✅ Verified correct (keep it)

- **The rebase**: no code change apart from the `applyAttempts` rename and the import resolution, and every earlier
  commit carried over as it was reviewed.
- **The fixture update**: the rendered App now includes the design note's `configMaps` and `secrets` parts. The
  ConfigMap name is routed through `${{ app.name + "-settings" }}`, and the server repoints it (ADR-0213 Decision 3).
  Delete reports the hashed ConfigMap as part of the App's tree.
- **The strict-typing fix** (`9054e1af`): reading through a segment whose `type` is not `object` is refused in strict
  mode only, and `TestRenderRefuses/untyped_parent` covers it.
- **Load and values**: every Decision 1-2 refusal, YAML 1.2 values, the merge rules, draft 2020-12 only, no non-local
  `$ref`, and `unevaluatedProperties: false` added where Decision 3 says (`closeSchema`,
  `internal/app/template/values.go:154-195`).
- **Routing and images**: typed substitution, pass-through byte for byte, and the refusals of mixed roots, an
  unparsable `${{`, interpolation, an expression written from a value, and an image or digest set by a written scalar,
  a key in another letter case or a substituted value. A key `image` inside a `json.RawMessage` field stays data.
- **Render assembly**: every `AppSpec` field except `version` and `paused` is a list, and `appendSections` appends all
  lists in file order. `relocate` names `<file>: <section>[j]` for an `App.Validate` refusal. Nothing is returned on
  error.
- **Deploy**: `n0` comes from `latestRevision`, and deploy uses the stored App's group. It does not apply when
  `sameAppSpec` holds and prints `no change` only when revision `n0` holds the spec. It follows the highest controlled
  revision in `[n0, latest]` that holds the spec. It exits 1 when that revision is `Failed`, when the spec changes or
  when the App is deleted. It keeps the stored `spec.paused`, retries a conflict and has no client timeout.
  `deployPoll` is the only interval.
- **Delete**: the tree kinds come from `gc.Pairs()` at run time (`TestAppTreeKinds` derives them, with no count).
  Controller UIDs are followed transitively. Delete prints `deleted` and prints `waiting` once, and it prints `kept`
  only for non-`ref` stores that still exist.
- **Scope and conventions**: `cmd/funcd` does not depend on `internal/app/template`. Errors use `api/fault`. `go.mod`
  changes as Decision 10 says. No server package changed except the resolver move. No test was weakened, skipped or
  deleted.

### Definition of Done

18 / 18 items hold: 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one passing test per
scenario name, no server package changed except the resolver move) and 7 applicable generic items (real behavior,
contracts honored, tree, conventions, deps, scope, tracking). This gate did not run `just ci-full`, because the
repo-wide e2e suite runs once in the PR gate. It ran this ADR's two e2e scenarios on the branch and on the trial merge,
as in the earlier rounds, so that item is not counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → pass, 0/0/3, 0 model-attributed, DoD 18/18. See
`docs/reviews/model-scorecard.md`.

### Status

ADR-0217 moved `Reviewing` → `Implemented` (2026-10-10), and the FEAT-0010 F120 row moved `reviewing` → `implemented`.
This run makes no GitHub writes, so the orchestrator owns the board card's move to `Done`.

### Recommendation

Integrate the branch, resolving Minor 3 as described. Minors 1 and 2 are ADR text for ADR-0218 to record. They need no
code change.
