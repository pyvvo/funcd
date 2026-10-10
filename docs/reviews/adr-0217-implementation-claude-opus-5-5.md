# ADR-0217 implementation review: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 2 minors (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `694d3452`, on top of `0cd67ef5` (the acceptance of ADR-0211 to ADR-0220). The
branch holds 6 commits: the resolver move and `Expr.Idents`, `internal/app/template`, the three `funcdctl app` verbs,
the e2e scenarios, a refactor of deploy and delete, and the status bump. The diff is 29 files, +3141/−108, of which 7
non-test Go files carry +1553/−79.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `EXIT=0`; `TestScenarioAppDeployWaits/current`, `TestScenarioAppDeployWaits/failed` and `TestScenarioAppDeleteReports` pass (36.1 s) |
| `go test -race -count=1` on `internal/app/template`, `internal/expr`, `internal/workflow` | all `ok`, uncached |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 13/13 `--- PASS`, among them `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses` |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0 |
| `git diff 0cd67ef5..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| Changed non-test files outside `cmd/funcdctl`, `internal/app/template`, `internal/expr`, `internal/workflow/condition.go`, `docs/`, `go.mod` | none |
| Seven mutants through `go test -overlay` | 6 killed, 1 equivalent (listed below) |
| Probe test through `go test -overlay` (not committed) | the findings below |

Mutants:

- `closeSchema` closes no subschema (`internal/app/template/values.go:177`): `TestValuesSchemaClosed` fails.
- `StrictTypes` without the leaf check (`internal/expr/schema.go:89`): `TestStrictTypesRefusesUntypedPaths` fails.
- Mixed roots no longer refused (`internal/app/template/render.go:290`): `TestRenderRefuses` fails.
- `imageDigest` no longer refused (`render.go:266`): `TestRenderRefuses` fails.
- `app.lock` no longer refused (`internal/app/template/load.go:69`): `TestLoadRefuses` fails.
- `no change` printed whenever the stored spec equals the rendered one (`cmd/funcdctl/app.go`, `deploy`):
  `TestCLIAppDeployWaitsForStamp` fails.
- Equivalent: `treeKinds` without the Secret exclusion passes every test. `gc.Pairs()` makes Secret a child of
  Identity only (`internal/gc/gc.go`), and Identity is not reachable from App, so the exclusion changes nothing today.
  This is not a finding.

### 🔴 Blockers

None.

### 🟡 Major 1: a fragment key in another letter case bypasses the image, digest, `version` and `paused` refusals · attribution: model

Render routes a scalar by a field path that it builds from the YAML key text (`render.go:230-232`) and matches against
`imagePath` and `digestPath` (`render.go:46-47`, `:255-268`); `fragment` refuses `version` and `paused` by exact key
text (`render.go:207-213`). `sdk.DecodeManifest` then decodes the fragment with `yaml.UnmarshalStrict`
(`pkg/sdk/sdk.go:430`), which goes through `encoding/json` and matches a key to a field without regard to letter case.
So a key that differs only in case escapes the check and still sets the typed field. A probe test (overlay, the
template package's `baseApp`) rendered each of these without error:

- `Image: registry.example/todo-api:1.0.0` in a function → `image="registry.example/todo-api:1.0.0"`;
- `Functions:` as the section key with a literal `image:` → the same literal image;
- `ImageDigest: sha256:000…0` beside `image: ${{ images.api }}` → `imageDigest` set;
- `Version: 9.9.9` → no refusal; the value is dropped silently, because `appendSections` copies only list fields.

This breaks Decision 6 ("render refuses any other value there", "Any `functions[].imageDigest` … is refused") and the
fifth Review-checklist item, and so the F120 exit criterion "an image outside that table is refused". ADR-0218 relies
on the same rule ("a digest comes only through `images`"). Fix (builder): check what was decoded, not only the key
text. For example, refuse a fragment key that does not equal a JSON field name exactly, or compare the decoded image
and digest fields with the values that routing wrote. Add tests with a case-variant key for each of the three image
fields, `imageDigest`, `version` and `paused`.

### Minor

- **Render can write an expression that comes from a value** · attribution: model. Decision 5 says "Render never
  writes an expression; a field that needs one holds it literally". `router.scalar` substitutes the result of an
  evaluation without checking it (`render.go:297-305`). A values file with `label: "${{ event.data.x }}"` and a fragment
  with `handler: ${{ values.label }}` render `handler: "${{ event.data.x }}"`. In a Workflow `when` or a Sensor `input`,
  the server would then evaluate that text. The same happens with a `registry` value: the image becomes
  `${{ event.data.x }}/shop-api:1.0.0`. Fix (builder): refuse a string result, or a registry, whose trimmed text starts
  with `${{` or that contains `${{` before `values`, `app` or `images`, naming `<file>:<line>` and the path.
- **YAML aliases and merge keys are refused, but the ADR does not list that refusal** · attribution: adr.
  `walk` refuses an alias or a `<<` key (`render.go:227-229`, `:244-245`). The choice is sound: routing replaces the
  anchored node in place and drops its anchor, and a merge key changes the field path. However, the ADR's refusal
  table is silent on it. Record it when an ADR next touches the template format (ADR-0218 is the natural place).
  The model is not at fault.

### ✅ Verified correct (keep it)

- **The resolver move** (`internal/expr/schema.go`): `whenSchemaResolver` calls `expr.NewSchemaResolver` and keeps its
  behavior. The `internal/workflow` tests pass unchanged with `-race`. `StrictTypes` refuses enum, const, type-array,
  oneOf, `$ref`, property-less objects and undeclared segments, and never returns `string`. `Expr.Idents` is built on
  `identsOf`, deduplicates in source order and excludes `undefined`.
- **Load** refuses an extra file or directory, a nested directory in `resources/`, another extension, `app.lock`
  (naming ADR-0218), an unknown key or `apiVersion`, an invalid name, a non-strict version, a range, a digest or a host
  in `images`, a `registry` that reads `app` or `images` or interpolates, and a `when` that names no file or reads
  `images`. A non-directory source fails with "deploying from a registry ref is ADR-0218".
- **Values**: one mapping per file, YAML 1.2 (`on: yes` stays a string), maps merge key by key, and a list, a scalar or
  `null` replaces. The schema compiles as draft 2020-12 only and refuses every non-local `$ref`. `closeSchema` adds
  `unevaluatedProperties: false` exactly where Decision 3 says, including the `$defs` walk. A key declared under `then`
  or beside a `$ref` is accepted. Each refusal names the instance location and the keyword.
- **Routing and typing** on the normal path: typed substitution (an integer, a boolean, the string `"true"`, an
  object); pass-through byte for byte for `event`, `step` and `${{ true }}`; refusal of mixed roots, an unparsable
  `${{` and interpolation, with `<file>:<line>: <path>`. A contract property named `image` and a Sensor input `image`
  are treated as data.
- **Render**: each fragment is decoded alone through `sdk.DecodeManifest`; `App.Validate` errors are rewritten to
  `<file>: <section>[j]`, and a name that two files declare names both files. Render returns nothing on error. The
  golden render of the to-do fixture matches, and `-o json` prints the same App.
- **Deploy** follows Decision 8 closely: `n0` comes from `latestRevision`; the stored group is used, and another
  `--resource-group` is refused naming both; there is no apply when `sameAppSpec` holds; `no change` is printed only when
  revision `n0` holds the spec; the highest revision in `[n0, latest]` that holds the spec is followed; part and `Ready`
  changes are printed; exit 1 on `Failed` (`ChildNotReady` naming the part), on a changed spec, or on a deleted App;
  conflicts are retried; there is no client timeout. The CLI tests cover every case that the Implementation plan lists
  except the post-hook cases, which wait for ADR-0214.
- **Delete** derives the tree kinds from `gc.Pairs()` at run time, follows controller UIDs transitively (a grandchild
  Revision, and one created after the delete), prints `deleted`, prints `waiting` once, and prints `kept` only for
  non-`ref` stores that are still present. A refused delete is surfaced.
- **Scope and conventions**: no server package changed except the resolver move; `internal/app/template` is not in
  `cmd/funcd`'s dependencies; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct; there is no `panic`, no
  `fmt.Print*` and no new logging; errors use `api/fault`. The changed test helpers are a move of `lockedBuffer` into
  the untagged `cli_test.go` and a viewer token for the delete-refusal test. No test was weakened or deleted.

### Definition of Done

16 / 18 items hold. The items are the 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one
passing test per scenario name, no server package changed except the resolver move) and 7 applicable generic items
(real behavior, contracts honored, tree, conventions, deps, scope, tracking). Misses: Review-checklist item 5 and
"contracts honored", both because of Major 1 (model). `just ci-full` was not run by this gate, because the repo-wide
e2e suite runs once in the PR gate. The two e2e scenarios of this ADR were run directly and pass. That item is not
counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → changes-requested, 0/1/2, 2 model-attributed, DoD 16/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Close the case-variant key bypass (Major 1) with tests, and refuse a substituted value that is an expression
(Minor 1). Then run this review again. ADR-0217 stays `Reviewing` and F120 stays `reviewing`. The alias refusal
(Minor 2) needs no code change; it should be recorded in a later ADR.
