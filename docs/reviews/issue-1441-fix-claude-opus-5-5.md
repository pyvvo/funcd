## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1441 batch 1, round 3, model: claude-opus-5-5)

Round 3 reviews commit 31aebe90 `docs(api): every field of Function, App and Workflow has a description`, on branch
`docs/describe-every-field-and-key`. The chain was rebuilt on `origin/main` 3f329326, which merged #1448. Since round
2's 087f39cf, one change answers round 2's Minor: `asCode` is gone, and kindref now writes a description's `<`, `>`, `{`
and `}` as entities. Every other file of the batch is byte-identical to 087f39cf's.

- **The one change:** `escaped()` in `internal/controlplane/kindref/kindref.go`, with `TestDescriptionMarkupIsEscaped`.
- **Findings:** all 8 of round 1 and the 1 of round 2 are fixed.
- **Review conditions:** this review ran in an agent separate from the session that wrote the work. Every check below
  was run, not read. Nothing was written into the worktree: 31aebe90 was extracted with `git archive` into a scratch
  folder, and every check ran there.

### Verification run (round 3)

| Check | Command | Result |
|---|---|---|
| source diff | `git diff 087f39cf 31aebe90 -- api/types internal/app internal/function internal/workflow internal/testkit/doctag` | empty |
| extra file | `git diff 3f329326 31aebe90 -- internal/testkit/funcdenv/`; `git cat-file -e 6542f356:internal/testkit/funcdenv/funcdenv.go` | equals main's; the file is new on main (#1448), not the batch's |
| per file | each of the 29 files that 31aebe90 changes, compared with 087f39cf | 21 identical; 8 differ: `kindref.go`, `kindref_test.go` and 6 Spec pages. The OpenAPI document, all tags, the pin tests and the list are identical |
| generation | `go -C <scratch>/tree run ./internal/controlplane/cmd/specgen/ -out <scratch>/x.yaml -guide <scratch>/guide`, diffed against `git show 31aebe90:<path>` | the OpenAPI document is byte-identical, and so are all 16 pages. The other 2 Spec pages at 31aebe90 (`running-funcd`, `tools/funcdctl`) are #1448's: specgen does not write them, and their marker differs from kindref's, so it never deletes them |
| page diff from round 2 | a script that compares each changed row of the 7 pages between 087f39cf and 31aebe90 | 50 description cells changed; each differs only in the escaping: the text with code spans removed equals the text with entities decoded. No other cell changed |
| page scan | a script over the 16 specgen pages: 1043 rows, every line with its code spans removed | no `<` before a letter, `{` or `}` on any line, the type column included. No backtick and no other `&` in a description. 94 `&lt;` and 94 `&gt;`, 12 `&#123;` and 12 `&#125;` |
| tests | `go -C <scratch>/tree test -count=1`: `kindref`, `api/types/v1alpha1`, and the `TestDocNamesTheConstant` and `TestSpecGeneratedFromGo` tests of `internal/app`, `internal/function`, `internal/workflow` and `internal/controlplane` | all `ok`; `TestDescriptionMarkupIsEscaped`, `TestEveryFieldIsDescribed`, `TestScenarioSpecTabGenerated` and `TestScenarioDocNamesTheConstant` PASS |
| vet and lint | `go vet`, then `golangci-lint run` on `kindref`, in the extracted tree | exit 0, `0 issues.` |
| tree | `git status --porcelain` in the worktree | clean: nothing written |

Mutants of `escaped`, run as `go test -overlay` files on the extracted tree: 3 of 3 killed.

1. `escaped` returns its input unchanged. `TestDescriptionMarkupIsEscaped` fails on the Function page.
2. `escaped` replaces only `<` and `>`. The test fails on the Workflow page's `${{ }}`.
3. `escaped` replaces only `{` and `}`. The test fails on the Function page.

Round 2's mutants still hold: the tests they ran are unchanged, and so is the code they changed, except the removed
`asCode`.

### 🔴 Blockers

None.

### 🟡 Majors

None. The four Majors of round 1 were fixed in round 2. Their tags and the pin test are byte-identical in 31aebe90:

- **`FunctionBlob.alias` is fixed.** It reads "code that reaches the prefix through ctx.blob passes it"
  (`api/types/v1alpha1/function.go:149`), which is true on the Function, App and both Lake headings.
- **`FunctionStatus.drainingRevision` is fixed.** It now says how the field drains, idle "no sooner than
  runtime.handOutSettle after the switch", and that "A worker that an open workflow run pins is not counted and stays
  until the run ends" (`function.go:207`). This matches `internal/function/function.go:2088-2103` and
  `docs/behavior/functions.md:63`.
- **`WorkflowPooling.mode` is fixed.** It now states the process-runtime limit (`api/types/v1alpha1/workflow.go:62`).
- **`ResourceSpec.memory` is fixed.** It says "1 to 15 digits", and `TestScenarioDocNamesTheConstant` pins it against
  `ByteSizeGrammar` (`api/types/v1alpha1/doc_test.go:26`). Round 2's mutant showed that a changed grammar fails it.

### Minors

None.

- **Round 2's Minor is fixed.** It said that `asCode` missed an expression with a brace, and that its test checked only
  fixed strings. `asCode` is gone. `escaped` writes `<`, `>`, `{` and `}` as entities in every description cell
  (`internal/controlplane/kindref/kindref.go:341-345`, applied at `kindref.go:205`), so no expression or placeholder
  can reach a page as markup, whatever it holds. `TestDescriptionMarkupIsEscaped` (`kindref_test.go:38-56`) now checks
  each description cell of every page that `Render` returns for a raw `<`, `{` or `}`, as round 2 proposed. The
  replacements are the same four that #1448's `internal/reference/page.go:44-47` `cell` makes for the funcd and
  funcdctl Spec pages, so the guide escapes one way.
- **Round 1's four Minors stay fixed.** Their tags are byte-identical to 087f39cf's, as round 2 checked:
  - `volumes.capacityBytes` is named in `FunctionSpec.volumes` and `VolumeEntry.size`.
  - `VolumeEntry.ref` says a ref "states the retained volume's own size, since apply refuses another size".
  - `Pooling.worker` and `FunctionStatus.pool` name the resource group.
  - No tag says "stamp".

### Routed (not this work's)

- **The raw placeholders on the Spec pages: done.** Round 2 took them into the commit, and round 3 replaces the code
  spans with entities. Each changed cell keeps the same text, the OpenAPI document keeps the plain descriptions, and
  the type column stays as it was. Its patterns and enum values are in code spans, which no Markdown or MDX parser
  reads as markup. The test leaves the type column out, by design. Today no line holds raw markup outside a code span
  (the page scan above). A later `patternDescription` with `<` or `{` would be the one way back in. That belongs to
  kindref's owner, ADR-0323, if it ever happens.
- **What a `pass` step reads differs from ADR-0096: unchanged, still routed to the decider.** ADR-0096 says a pass
  sees the step's flowing input with params merged in (`docs/adr/0096-engine-native-builtin-steps.md:140-142` and
  `160-162`). The engine passes the run input and no params (`internal/workflow/engine.go:1131`,
  `internal/workflow/condition.go:156-157`), and `TestPassTransformsInEngine` pins that
  (`internal/workflow/builtin_test.go:137-164`). The tags still follow the code. This needs a `differs` row in
  `docs/behavior/eventing.md` or the decider's call. It was found by reading the code, not by a run.

### ✅ Verified correct (keep it)

- **Generation is clean, and the list only shrank.** Specgen run on 31aebe90's own source reproduces the committed
  OpenAPI document and all 16 pages. `internal/controlplane/kindref/testdata/undescribed.txt` loses 238 lines and gains
  none.
- **The entities read as the text they stand for.** CommonMark decodes `&lt;`, `&gt;`, `&#123;` and `&#125;` in a table
  cell. No description holds a backtick, so no entity sits in a code span, where it would show as written. No
  description holds another `&`, so nothing else is decoded by accident.
- **The pins catch a changed constant.** That is 4 mutants over rounds 1 and 2: `defaultPoolLimit`, `MinCPU`,
  `stepIdleTimeout` and `ByteSizeGrammar`. Every named reason and condition that a tag copies is pinned.
- **The tags hold no ADR number, internal name, path or backtick.** Each config key and funcdctl command a tag names
  exists under that name.
- **The security claims hold:** no Secret value reaches a worker's environment, a binding grants reads while only owners
  and RolesAssignment writers write, and the edge's authenticated stance answers 401, then 403 (round 1's evidence).
- **The Function, App and Workflow facts match the code and the behavior rows.** Round 1 checked them field by field,
  and round 2 checked the 8 changed tags. All of them are unchanged in 31aebe90.
- **The shared types read true in every place they show.** This covers Ingress, RouteAuth, ResourceSpec,
  SecretDeclaration and the FunctionBlob fields, and the FunctionSpec and WorkflowSpec fields under `App.spec.functions[]`
  and `App.spec.workflows[]`.
- **`internal/testkit/doctag` is one small helper used by four tests.** The api package imports it only from a test
  file, which `.golangci.yml`'s `api-boundary` rule allows. The `.claude/rules/api-types.md` addition matches ADR-0323
  Decision 4.
