## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0331 implementation, model: claude-opus-5-5)

The work spans two repositories. In funcd, it is ADR-0331's commits on `feat/api-answers-batch-2`: `a8659fd5` (the
layout in `kindref`, the Spec tabs' linked tables, specgen, `check-generated.sh`, CI's filter and the doc tags of
`api/fault`), `c842340a` (the Overview), `f7aaf36c` (the layout regenerated after the rollout items) and the status
commit `e3d156b8`. In funcd-frontend, it is `f7df933` and `26c6186` on `feat/api-reference` at `26c61861`, on its main
`93e5ae4`: fumadocs-openapi 12.6.4 renders the reference, and `check-api-reference.ts` checks the site's five scenarios.
Every check below ran on those heads, and neither tree changed. The batch's ADR-0329 and ADR-0330 rollouts have their
own reviews.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

1. **`search-finds-a-case` reads the index instead of searching it** · attribution: model
   - The check passes when a search entry of an answering page holds the case's text (funcd-frontend
     `packages/docs/scripts/check-api-reference.ts:227-245`). The scenario asks that a search for the text return such
     a page, and the ADR's plan names the check "(any page that answers the case)".
   - The site's own search code, run on the built index as the browser runs it (`zbsearch`'s `create` and `load`, then
     fumadocs-core 16.18.4's `searchAdvanced` with its defaults: full text, 60 results, 8 per page), returns an
     answering page for 342 of the 343 case texts. "The name in the path is not a DNS-1123 label.", the 422 of
     `getNamespace`, `replaceNamespace` and `deleteNamespace`, returns eight pages of App, AppRevision and Backup, whose
     case reads "The namespace or the name in the path is not a DNS-1123 label.", and no Namespace page. The search
     dialog in a headless Chromium shows the same first results, Read an AppRevision and Read an App.
   - The checklist item, "A page's search entries hold each of its cases' when texts", holds.
   - Fix: make the check query the index with the site's search code, then resolve the one miss.
2. **The site's checks take the expected answers from the site's own output** · attribution: model
   - `check-api-reference.ts` reads its spec from `build/client/openapi.json` (line 42), which `bundledSpec()` writes.
     The pages build their Answers rows from the same bundled document, so a fault in that pipeline changes the
     expected rows along with the pages.
   - A mutant of `app/lib/openapi.ts` that drops `x-funcd-cases` while it loads the spec, built against the batch,
     removes every case from the tables, the copies and the search index: Replace a KVStore shows 9 rows, one per
     status, instead of 18, and no search entry holds a case. `reference-shows-an-operation`,
     `function-call-in-reference`, `search-finds-a-case` (on 0 case texts) and `reference-has-its-own-sidebar` pass.
     Only `agent-reads-an-operation` fails, with 169 problems, and only because the description of a status with
     several cases spans lines: the copy's `cell` joins them, and the check's `cell` (line 248) does not.
   - Fix: take the expected cases from the mirrored spec, `content/funcd/api/openapi/funcd.v1alpha1.yaml`, or check
     that the served document holds the same cases.
3. **`TestReferencePaths` has no oracle of its own for the verb order or a kind folder's title** · attribution: model
   - `checkLayout` ranks a kind's pages with the package's own `verbs`
     (`internal/controlplane/kindref/apireference_test.go:74`), and the per-verb check reads it too (line 146). A
     mutant that puts `get` before `create` in `verbs` passes every test of `kindref` and specgen. Only list before
     delete is spelled out, in the Backup expectation.
   - The title is compared in lower case (line 100), so a mutant that lowercases it passes too. The site's trail check
     reads the title from the same `meta.json`.
   - Decision 3.2 fixes both: "a kind's folder is titled with the kind and lists its operations list, create, get,
     replace, delete, then by name".
   - Fix: spell the order in the test, and compare the title with the tag of the folder's operations.

### Routed (not this work's)

- **A reference page's summary shows twice in the search results** · home: elsewhere · destination: the decider ·
  attribution: adr.
  - Decision 3.3 says that the page's structured data holds its summary, and `withCases` adds it (funcd-frontend
    `packages/docs/app/lib/source.ts:116`). fumadocs already indexes the page's title, which is the summary, as the
    page's own entry. A search that matches the summary lists the page and, under it, the same text again.
  - Ruling: keep it, or let the page's entry stand for the summary, which the Decision's wording does not allow today.
- **Each Markdown copy writes its body's schema in full** · home: elsewhere · destination: the decider · attribution:
  none, a choice inside Decision 3.5.
  - The body's schema is JSON with every reference filled in (`source.ts:279`). The 143 copies hold 1,677,090 bytes;
    App's create and replace copies hold 269,110 and 269,977, and `llms-full.txt` holds 2,745,630. The implementer
    measured 1.0 MiB for `llms-full.txt` before the change; this review did not rebuild the earlier site.
  - A lighter reading: a body that is a kind names the kind and links its Spec tab's copy, which lists every field with
    its limits, as `kindref`'s `referenceFields` already assumes when it leaves a kind's fields to its Spec page. Only
    the other bodies are then written in full.
  - Ruling: keep the full schemas, or link the kinds' Spec tabs.

### The implementer's choices, judged

funcd:

| Choice | Verdict |
| --- | --- |
| The sections in the order of `Features`, pinned to `develop/meta.json` by a test | Sound. `TestWriteReferenceRemovesOnlyStaleMetaFiles` compares the order with the guide's, and a mutant that sorts the sections by name fails `TestReferencePaths`. |
| An unplaced tag fails `ReferenceFiles`, so `just generate`, and `ReferencePaths` leaves it out | Sound. `ReferencePaths` has no error in the Contracts, so the generate step is where it must fail. The test's `Lease` case shows both. |
| A shared page name fails `ReferenceFiles` | Sound. It keeps the Contracts' "Paths are unique"; the test's two `get-logs` operations show it. |
| The plural rule: `y` to `ies`, else `es` or `s`, else the bare tag | Sound for every kind and tag today. The test checks it against an independent oracle, the last segment of a list's path, and a recount from the spec finds no name that keeps its kind's plural. |
| "Fields that no Spec page shows": the bodies, the answers and `FaultError` | Sound. `TestUndescribedCoversTheReferenceSchemas` covers them and fails on an emptied `FaultError.detail`. |
| `check-generated.sh` covers `docs/guide/api/**/meta.json`, not `index.md` | Sound. Generate never writes the Overview, as it never writes the Develop chapter's hand-written pages; `api/openapi` already covers `reference-paths.json`. |
| specgen derives the repository root from `-guide` | Sound. It needs no second flag and refuses a guide that is not a `docs/guide`, which the test shows. |

The Overview's corrections to the spike's draft, each checked against the code:

| Statement | Code |
| --- | --- |
| The call of a Function needs a credential only under an authenticated stance: the Function's `spec.ingress.auth.mode`, else its namespace's `spec.edgeDefaults.auth.mode` | `serveByName` takes the Function's mode (`internal/dataplane/dataplane.go:197`), `authStance` falls back to the namespace's and then to open (line 400), and `authnPlugin` enforces only `authenticated` (`internal/dataplane/plugins.go:54`). |
| `/openapi.json`, `/openapi.yaml` and `/docs` need no token, and `/schemas` serves nothing | `isPublic` (`internal/controlplane/middleware/authn.go:39`). `NewAPI` sets `DocsPath` and `OpenAPIPath` but no `SchemasPath` (`internal/controlplane/controlplane.go:256`), and huma v2.40.0 serves the schemas only when it is set. huma also serves `/openapi-3.0.json` and `/openapi-3.0.yaml`, which do need a token; the Overview names neither, so it holds. |
| An error body is problem+json unless its operation names another | In the spec, only `invokeFunction`'s 422 and 500 name another body, `ContractViolation` as `application/json`. |
| The gateway serves neither the spec nor `/docs` | The data plane registers no such route; the control plane's huma API is the only one. |

The rest of the Overview holds too. The table of problem types equals the set that the spec's cases list, no operation
lists `urn:funcd:problem:internal`, every control plane path is under `/apis/funcd.io/v1alpha1`, the keys
`server.listenAddr` and `server.dataPlaneAddr` exist (`cmd/funcd/credentials.go:112-116`), a replace or delete is
conditional on `If-Match` or, for a replace, on `metadata.resourceVersion` (`precondition.go`, `withReplaceVersion`,
the stale 409 case), and every create and replace of a writable kind takes `dryRun` (`dryrun.go`, ADR-0220).

funcd-frontend:

| Choice | Verdict |
| --- | --- |
| A record of sources passed to `loader()` instead of `multiple` | Sound. fumadocs-core 16.18.4 marks `multiple` deprecated in favor of that record. |
| `$id: 'documentation'` on the wrapped root | Sound and needed. fumadocs-ui finds the open root by `$id` once a move brings a new tree. In a headless Chromium, after a move between two reference pages, the switcher leads to `/funcd/docs`, and from there it offers `/funcd/docs/api`. |
| The root's description "Guides and concepts" | Harmless wording that the ADR leaves open. |
| The Markdown copy also names an operation's own server | Beyond Decision 3.5's list, but kept: only `invokeFunction` has one, and without it the call's copy would read as a control plane path. |
| Each body's schema in full | A sound reading of "the body's schema" at a size cost; routed to the decider above. |
| The search shows a page's summary twice | A cost of Decision 3.3's wording; routed to the decider above. |
| A reference page gets the "Markdown" link, through `PageTabs` | Sound. `PageTabs` is the guide pages' existing markup, extracted unchanged, so every page keeps one shape. |

The two open questions, which the ADR assigns to the funcd-frontend pull request:

- **The samples keep fumadocs' `https://example.com`.** Acceptable for now. The spec names no server for the control
  plane, `example.com` is a reserved placeholder, and the Overview says that `server.listenAddr` sets the address. The
  call's samples use its `{gateway}` default, `127.0.0.1:8081`. The real fix is the funcd follow-up that the ADR
  names, a server in `SpecAPI`; this review found no issue or board card for it yet.
- **The playground is off** (`playground: { enabled: false }`). Sound: the public site has no funcd to call. The page
  shows the operation's method and path in its place.

### ✅ Verified correct (keep it)

- **The layout.** A recount from the spec gives 143 operations that are described and not of an `Omitted` kind; the 25
  operations of `Omitted` kinds stay out, and no other operation is undescribed. They are exactly the keys of
  `reference-paths.json`, every path is unique, and no name keeps its kind's plural. The root `meta.json` is the API
  reference root with the Overview and five sections. `ReferencePaths`, `SectionMeta` and `ReferenceFiles` match the
  Contracts.
- **The Spec tabs.** Each HTTP API section is the table of Decision 3.6, with tag-placed operations on the disaster
  recovery and events tabs. On the built site, the 15 Spec pages with a section hold 143 links to the reference, and
  each opens a built page; the 17 Spec copies hold 143 links to reference copies, and each copy exists. In a headless
  Chromium, the KV Spec tab's "Replace a KVStore" opens `/funcd/docs/api/services/kvstore/replace`.
- **The pages.** The built site holds `/docs/api` and one page for each of the 143 entries, and no other. Each page's
  static HTML holds its Answers table, one row per case and per status with no case: 18 rows for Replace a KVStore and
  20 for Call a Function, with JavaScript off.
- **The spec is loaded once.** No page's loader data holds `bundled`: the payload holds `proxyUrl` only. No page or
  script holds `x-funcd-cases`, and Replace a KVStore is 219,784 bytes of HTML against a 935,138-byte spec. The check's
  `bundled` test matches fumadocs-openapi's own key (`dist/server/index.js:48`). `/openapi.json` holds the whole spec,
  77 paths, 183 schemas and funcd's operations, with fumadocs' loader raising its version field to 3.2.0. In a headless
  Chromium, the page fetches it once and not again after a move to Read a KVStore, with no console error.
- **One function for the rows.** `answersOf` in `app/lib/openapi.ts` builds the rows for the page's loader and for the
  copy, and the check computes its own with `rowsOf`.
- **The copies.** Each holds the summary, the method and path, the authentication of the operation's own security, the
  parameters, the body and the Answers rows, and `check-md-links` passes on 209 copies.
- **The sidebar.** Each reference page's tree holds the Documentation and API reference roots. The static sidebar of a
  reference page lists the Overview and the five sections, and a guide page's lists the guide only.
- **ADR-0329 Decision 2 keeps its test.** `TestFunctionCallInTheSpec` checks the `{gateway}` server and its
  `server.dataPlaneAddr` description, the `X-Funcd-Namespace` header, the optional security, and that the control
  plane neither lists nor routes the call. `pkg/funcd`'s `TestFunctionAnswersMatrix` keeps a row for each of the
  call's 20 answers, and the site's page shows each of them as a row.
- **The site writes no placement or naming rule.** `apiSource` takes each page's folder and name from its entry in
  `reference-paths.json`, and the folders' titles and order come from funcd's `meta.json` files.
- **Dependencies.** fumadocs-openapi is pinned at 12.6.4 with `json-schema-typed`, as the plan says, and every package
  the site adds to `yarn.lock` is MIT, ISC or BSD-3-Clause.
- **Tracking.** Since acceptance, the ADR's only edit is its status line, and the F02 row read "API reference:
  reviewing".

### Checks run

funcd, at `e3d156b8`:

| Check | Result |
| --- | --- |
| `go test -race -count=1` on `./internal/controlplane/kindref/...`, `./internal/controlplane/cmd/specgen/...`, `./scripts/...` and `./api/fault/...` | exit 0, 5 packages ok |
| `go vet` and `golangci-lint run` on `kindref`, specgen and `api/fault` | exit 0; 0 issues |
| `just generate`, twice | exit 0 each; the tree stayed clean |
| `scripts/check-generated.sh` | exit 0 |
| `just ci`, the e2e suite and the gate | not rerun: the builder's gate passed on `f7aaf36c`, and `e3d156b8` changes only the status line and the F02 row |

funcd-frontend, at `26c61861`:

| Check | Result |
| --- | --- |
| `yarn install`; `yarn lib build && yarn ui build` | exit 0 |
| `yarn build` in `packages/docs`, with `FUNCD_DIR` set to the batch: the sync, the build and every check | exit 0. The mirrored spec and `reference-paths.json` equal the batch's, byte for byte. The five checks report 143 pages, 20 rows for the call, 343 case texts, 143 copies and 144 pages with both roots. |
| `yarn react-router typegen`, then `yarn tsc -p tsconfig.json` in `packages/docs` | exit 0; exit 0 |
| `just ci` | exit 0 |
| `yarn vp lint` on the changed files of `packages/docs` | exit 0 |

Each funcd mutant ran in a scratch worktree of `e3d156b8`, and its file was restored after the run:

| Mutant | Result |
| --- | --- |
| `pageName` loses the `ies` form | killed: `TestReferencePaths`, "listEgressPolicies: its kind's plural is stripped" |
| The sections sorted by name | killed: `TestReferencePaths`, "the sections in the order of Features" |
| A section's kinds sorted by name | killed: `TestReferencePaths` |
| An `Omitted` kind's operation covered | killed: `TestReferencePaths/the layout's rules` |
| The root without the Overview | killed: "the Overview first" |
| A kind with no page keeps its folder | killed: `TestReferencePaths/the layout's rules` |
| A kind's other operations in reverse order of name | killed: `TestReferencePaths`, "workloads/function: invoke before get-logs" |
| A section's tags in reverse order of name | killed: `TestReferencePaths`, the disaster recovery section's order |
| Names not in lower case | killed: both subtests of `TestReferencePaths` |
| The `TagFeatures` operations dropped from the Spec tabs | killed: `TestReferencePaths`, "getHold: a Spec tab lists it" |
| A Spec tab's link one folder short; the link's text the operation ID | killed: `TestScenarioSpecTabLinksTheReference` |
| `Undescribed` skips the reference's fields | killed: `TestUndescribedCoversTheReferenceSchemas` |
| `check-generated.sh` without the `meta.json` pathspec | killed: `TestScenarioStaleGeneratedFileFailsCI` |
| specgen keeps a stale `meta.json` | killed: `TestWriteReferenceRemovesOnlyStaleMetaFiles` |
| `get` before `create` in `verbs` | survived (Minor 3) |
| A kind folder's title in lower case | survived (Minor 3) |

Each site mutant changed a copy of the built site, and `check-api-reference.ts` ran on it; each was killed by the
check named:

| Mutant | Killed by |
| --- | --- |
| An Answers row dropped; the Answers table's heading renamed | `reference-shows-an-operation` |
| A page deleted; a page added that no entry names | `reference-shows-an-operation` |
| The payload's `proxyUrl` renamed `bundled` | `reference-shows-an-operation` |
| The header parameters', authorization's or body's section dropped | `reference-shows-an-operation` |
| The call's 504 row dropped | `reference-shows-an-operation`, `function-call-in-reference` |
| The Function folder renamed in the call's page tree | `function-call-in-reference` |
| A case's entries dropped from the search index | `search-finds-a-case` |
| The copy's authentication, an Answers row, the body's JSON, the method and path, the `bearer` scheme, the `dryRun` row or the summary changed | `agent-reads-an-operation` |
| The Documentation root renamed; a guide page in the reference's root; the switcher's target changed | `reference-has-its-own-sidebar` |

The source mutant of Minor 2 ran on a scratch worktree of funcd-frontend at `26c61861`, built against the batch.

### Definition of Done

13 of the 13 items hold: the ADR's 9 checklist items and the 4 items of its Definition of done.

### Model scorecard

To be recorded with the batch's ledger rows: claude-opus-5-5 on ADR-0331 (implementation) → pass, 0/0/3, 3
model-attributed, DoD 13/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. This gate stamps ADR-0331 `Implemented (2026-11-15)` and moves the F02 row's "API reference" to `implemented`.

- The three Minors go back to the builder: two in funcd-frontend's `check-api-reference.ts`, and one in funcd's
  `TestReferencePaths`.
- The decider rules on the summary in search and on the copies' body schemas.

FEAT-0000/F02 is an infra row, so no feature issue exists, and neither pull request closes one.
