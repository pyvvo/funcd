# ADR-0217 implementation review, round 2: claude-opus-5-5

## Verdict: changes requested, 0 blockers, 1 major, 2 minors (ADR-0217 implementation, model: claude-opus-5-5)

Work: branch `impl/adr-0217`, head `d4de3635`, on top of `0cd67ef5`. This round reviews the rework commit `d4de3635`
("route fragment keys in any case and never write an expression from a value"), which answers the round-1 review
(`docs/reviews/adr-0217-implementation-claude-opus-5-5.md`), and checks again the whole branch. The rework changes
only `internal/app/template/render.go` (+26/−7) and its test file.

Both round-1 findings against the model are fixed. A new probe shows that the image rule of Decision 6 can still be
bypassed: a value that is an object or a list, substituted into a fragment, sets an image field or `imageDigest`
without any check.

### Verification run

All commands ran in the review worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `just ci` | `EXIT=0` |
| `go test -race -count=1 ./internal/app/template ./internal/expr ./internal/workflow` | all `ok`, uncached |
| `go test -race -count=1 -run 'TestScenarioAppRender\|TestCLIApp\|TestApp' -v ./cmd/funcdctl` | 13/13 `--- PASS`, among them `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses` |
| `go test -tags e2e -count=1 -run 'TestScenarioAppDeployWaits\|TestScenarioAppDeleteReports' -v ./pkg/funcd` | `EXIT=0`; `TestScenarioAppDeployWaits/current`, `/failed` and `TestScenarioAppDeleteReports` pass (34.8 s) |
| `go list -deps ./cmd/funcd \| grep -c internal/app/template` | `0` |
| `go mod tidy -diff` | no output, exit 0; `go.mod` makes only `jsonschema/v6` and `semver/v3` direct |
| `git diff 0cd67ef5..HEAD -- docs/adr/` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10)` |
| `docs/feat/0010-feat-apps.md` | the F120 row moved `accepted` → `reviewing`, nothing else changed |
| `panic(`, `fmt.Print*` or a logging import added in non-test Go | none |
| Five mutants of the rework through `go test -overlay` | 5 killed (listed below) |
| Probe tests through `go test -overlay` (not committed) | the findings below |

Mutants of the rework, each killed by `TestRenderRefuses`:

- `imagePath` and `digestPath` without `(?i)` (`render.go:48-49`).
- The `holdsExpression` check on a substituted result disabled (`render.go:313`).
- `version` compared by exact text instead of `strings.EqualFold` (`render.go:214`).
- The `!!binary` key refusal disabled (`render.go:235`).
- The expression check on an evaluated `registry` disabled (`render.go:133`).

### Round-1 findings

- **Major 1 (case-variant keys), fixed.** The image and digest paths now match without regard to case, and `version`
  and `paused` are compared with `strings.EqualFold`. Tests cover `Image`, `Functions`, `Steps`/`Function`/`IMAGE`,
  a site `Image`, `ſites` (U+017F), `ImageDigest`, `Version` and `PAUSED`, and `TestRenderRoutesKeyInAnyCase` shows
  that a case-variant `Image: ${{ images.api }}` still renders. Probes confirm more forms: `worKflows` with the Kelvin
  sign (U+212A), a double-quoted key with an escape (`"im\x61ge"`), a key with a local tag (`!foo image`) and
  `!!str IMAGE` are all refused with `<file>:<line>: <path>`. The rework also refuses a `!!binary` key, which the
  decode would read as its decoded text.
- **Minor 1 (an expression written from a value), fixed.** `holdsExpression` refuses a substituted result that holds,
  at any depth, a scalar that starts with `${{` or that interpolates `values`, `app` or `images`. An evaluated
  `registry` gets the same check. Tests cover a string, an interpolating string, a nested object member and the
  registry.
- **Minor 2 (alias refusal not in the ADR), carried forward** as Minor 1 below.

### 🔴 Blockers

None.

### 🟡 Major 1: a substituted object or list value sets an image field or `imageDigest` without the image rule · attribution: model

Decision 5 lets a value of type object or array replace a fragment scalar ("an object or list a node"), and
`StrictTypes` accepts an object that declares `properties`. `router.scalar` checks the image rule only on the path of
the scalar it routes (`render.go:263-275`): the path of `${{ values.fn }}` is `functions[0]`, not
`functions[0].image`. The substituted node is not walked again (`render.go:316`), so the image and digest fields
inside it are never checked. A probe test (overlay, a values schema that declares the object's properties) rendered
each of these without error:

- `functions:\n  - ${{ values.fn }}` with `fn.image: evil.example/x:1.0.0` → `image="evil.example/x:1.0.0"`;
- the same with `fn.imageDigest: sha256:000…0` → `imageDigest` set;
- `functions: ${{ values.fns }}` with a list of such objects → the literal image;
- `workflows[0].steps[0].function: ${{ values.fnimg }}` with `fnimg.image` → the step's image is the literal;
- `sites:\n  - ${{ values.site }}` with `site.image` → the site's image is the literal.

This breaks Decision 6 ("render refuses any other value there", "Any `functions[].imageDigest` … is refused"),
Review-checklist item 5, and the F120 exit criteria "an image outside that table is refused" and "an install can change
only the registry the images come from". The round-1 Major was the same rule bypassed by key case. The round-1 review
proposed to check the decoded fields, which would have closed both. The rework matched key case only.

Fix (builder): apply the image and digest rule to every node that substitution writes. For example, walk the
substituted node with its path and refuse any image or digest path inside it. Alternatively, check the decoded
`AppSpec` (each image equals a value of `images`, every `imageDigest` empty) and name the file. Add tests for the five
forms above.

### Minor

- **Minor 1: the alias, merge-key and `!!binary` key refusals are not in the ADR's refusal table** ·
  attribution: adr. `walk` refuses an alias or a `<<` key (`render.go:232-233`), and since this rework a `!!binary`
  key (`render.go:235-236`). Both refusals are sound: an alias or a merge key would hide a field from routing, and a
  `!!binary` key decodes to text that routing does not see. The ADR's refusal table does not list them. Record them
  when an ADR next touches the template format (ADR-0218 is the natural place). The model is not at fault.
- **Minor 2: `main` has moved, and the branch now conflicts with it** · attribution: env (sequencing). After
  `0cd67ef5`, `main` received ADR-0212 (#876): `AppSpec.Paused`, `AppSpec.WithoutPause()`, and a `sameAppSpec` that
  compares `WithoutPause()`. `git merge-tree` reports conflicts in `cmd/funcdctl/app.go`, `cmd/funcdctl/app_test.go`,
  `docs/reviews/model-ledger.json` and `docs/reviews/model-scorecard.md`. After the rebase, Decision 8 applies in
  full: "deploy copies the stored `spec.paused` into the applied spec, so a deploy never resumes an App". The branch
  could not do this on its base, so this is not the model's fault. The rework should rebase onto `main`, add that copy
  with a test (a deploy of a paused App keeps it paused), and check that the case-folded `paused` refusal still holds.

### ✅ Verified correct (keep it)

- **The rework is minimal and tested.** It changes one source file. Each of its five behaviors has a test that fails
  when the behavior is removed. The error messages keep `<file>:<line>: <path>` and show the key as written.
- **The resolver move** (`internal/expr/schema.go`): `whenSchemaResolver` calls `expr.NewSchemaResolver` and keeps its
  behavior; the `internal/workflow` tests pass unchanged with `-race`. `StrictTypes` never returns `string` for an
  undeclared path. `Expr.Idents` is built on `identsOf`.
- **Load and values**, unchanged since round 1 and passing again: every Decision 1-2 refusal, YAML 1.2 values, the
  merge rules, draft 2020-12 only, no non-local `$ref`, and `unevaluatedProperties: false` added where Decision 3
  says.
- **Routing on a written scalar**: typed substitution, pass-through byte for byte, refusal of mixed roots, an
  unparsable `${{` and interpolation. A contract property `image` and a Sensor input `image` are treated as data.
- **Deploy** (`cmd/funcdctl/app.go:312-496`): `n0` from `latestRevision`, the stored group, no apply when `sameAppSpec`
  holds, `no change` only when revision `n0` holds the spec, the highest revision in `[n0, latest]` that the App's UID
  controls (`appHistory`) and that holds the spec, exit 1 on `Failed`, on a changed spec or on a deleted App,
  conflicts retried, no client timeout.
- **Delete**: the tree kinds derived from `gc.Pairs()` at run time, controller UIDs followed transitively, `deleted`,
  `waiting` once, and `kept` only for non-`ref` stores still present.
- **Scope and conventions**: no server package changed except the resolver move; `internal/app/template` is not in
  `cmd/funcd`'s dependencies; errors use `api/fault`; no test was weakened or deleted.

### Definition of Done

16 / 18 items hold. The items are the 8 Review-checklist items, 3 Implementation-plan "Done" items (`just ci`, one
passing test per scenario name, no server package changed except the resolver move) and 7 applicable generic items
(real behavior, contracts honored, tree, conventions, deps, scope, tracking). Misses: Review-checklist item 5 and
"contracts honored", both because of Major 1 (model). `just ci-full` was not run by this gate, because the repo-wide
e2e suite runs once in the PR gate. The two e2e scenarios of this ADR were run directly and pass. That item is not
counted.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0217 (implementation) → changes-requested, 0/1/2, 1 model-attributed, DoD 16/18. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Apply the image and digest rule to substituted nodes, with tests for the five forms of Major 1. Rebase onto `main` and
add Decision 8's `paused` copy (Minor 2). Then run this review again. ADR-0217 stays `Reviewing` and F120 stays
`reviewing`. Minor 1 needs no code change; it should be recorded in a later ADR.
