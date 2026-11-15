# ADR-0331: The control plane API's reference is a section of its own on the docs site (amends ADR-0329)

- **Status**: Proposed
- **Date**: 2026-11-15
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, site
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as
  ADR-0329 did)
- **Supersedes (in part)**: [ADR-0329](0329-each-api-operation-documents-its-answers.md) — Decision 3 (each Spec tab's
  HTTP API section); the scenarios `operation-shows-its-answers` and `function-call-documented`; Scope's "An interactive
  page on the docs site"; the two alternatives it rejected, the docs site rendered by fumadocs-openapi and a separate
  Reference section; the Contracts' row "The spec → the HTTP API section of `docs/guide/develop/**/spec.md`"; and the
  Definition of done's "the KV Spec tab shows the HTTP API section of its operations".
- **Relates to**: [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (`kindref`, the Spec
  tabs and `Features`) · [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (the Handler API's errors,
  which stay on the Spec tabs)

## Context & Need

ADR-0329 shows each described operation's answers in an HTTP API section on its feature's Spec tab, and rejected a
reference on the docs site until a proof showed static pre-rendering, search and an export for agents. Experiment 0021
built that reference with fumadocs-openapi on funcd-frontend's site. One pre-rendered page per operation works, but as
the library's guide sets it up, every page embeds the whole spec (723 MiB against 37 MiB today), its Markdown copy is
that spec as JSON, which fails the site's link check, the status tabs show an error's cases only after a click, and
search finds a page by its title only. With the spec loaded once and a Markdown copy of the site's own, the build
passes at 97 MiB. The decider chose a reference of its own, with its own sidebar, on 2026-11-15: readers who write a
client look for an operation in an API reference, beside the guide.

## Scenarios

- `scenario: reference-shows-an-operation` (replaces `operation-shows-its-answers`): given the API reference, when a
  reader opens Services › KVStore › Replace a KVStore, then they see its method, path, parameters, body and
  authentication, and an Answers table with one row per case (status, problem type, when, what to do) with no click.
- `scenario: function-call-in-reference` (replaces `function-call-documented`): given the API reference, when a reader
  opens Workloads › Function › Call a Function, then each answer of ADR-0329 Decision 2 is a row of its Answers table.
- `scenario: spec-tab-links-the-reference`: given the KV Spec tab, when a reader looks at its HTTP API section, then each
  KVStore operation is a row with its method and path, its summary linking to its reference page.
- `scenario: search-finds-a-case`: given the site's search, when a reader searches a case's when text, then a result is
  the reference page of the operation that answers it.
- `scenario: agent-reads-an-operation`: given an operation's Markdown copy, when an agent reads it, then it holds the
  method and path, the summary, the parameters and one Answers row per case.
- `scenario: reference-has-its-own-sidebar`: given a reference page, when a reader looks at the sidebar, then it lists
  the reference's sections only, and its switcher leads to the Documentation.

## Scope

Unchanged (ADR-0329), but for the parts it supersedes. In: the API reference on the docs site, its layout written by
`kindref`, the Spec tabs' linked tables, the Overview page. Out: the interactive playground's target and the spec's
server URL (Open questions).

## Constraints & Decision drivers

Unchanged (ADR-0329), with: the site is static, pre-rendered for GitHub Pages; one renderer per page kind, with no
layout rule written twice; fumadocs-openapi 12.6.4 is MIT, and the 18 packages it adds have permissive licenses
(experiment 0021).

## Alternatives considered

- **Keep ADR-0329's Spec tab sections.** Built and live, but a reader finds the API only inside the guide's features;
  the decider wants a reference of its own.
- **fumadocs-openapi as its guide sets it up.** It embeds the spec in every page and exports it as JSON for agents:
  experiment 0021 measured a site 20 times today's, which failed its own link check.
- **The status tabs alone.** fumadocs shows an error's cases only after a click and indexes a page's title only, so the
  static page, agents and search would lose them; the decider chose an Answers table.
- **Reference pages written by `kindref` as Markdown.** One renderer and readable on GitHub, but no interactive schema
  view, samples or playground; the decider chose the generated spec through fumadocs.
- **The frontend derives the layout from the Spec tabs.** It parses headings and names pages a second time, in
  TypeScript; `kindref` already knows `Features`, `TagFeatures` and `Omitted`.
- **Drop the Spec tabs' HTTP API section** (the decider chose links), **the Overview in funcd-frontend** (not reviewed
  with the code), **the Omitted kinds under "Other kinds"** (four are being removed, #1438).

## Decision

3. **The control plane API has a reference of its own on the docs site.**
   1. **The pages.** funcd-frontend renders each described operation with fumadocs-openapi from the spec that its sync
      mirrors, at `/docs/api/<path>`, where `<path>` is the operation's entry in `docs/guide/api/operations.json`. The
      reference is a sidebar root, "API reference", described as every operation of funcd's control plane API, beside
      the guide's root, "Documentation"; the sidebar's switcher toggles them, and each shows only its own tree.
   2. **The layout, from `kindref`.** `just generate` writes `docs/guide/api/operations.json` and the reference's
      `meta.json` files. A kind sits in the section of its feature (`Features`), a tag of `TagFeatures` where that
      places it; `Omitted` kinds and undescribed operations stay out. `<path>` is `<section>/<kind>/<name>`, `<name>`
      the operation ID without its kind's name, in kebab case (`replaceKVStore` → `services/kvstore/replace`,
      `getFunctionLogs` → `workloads/function/get-logs`). The sections keep the guide's order, titles and icons, a kind
      the order of the guide's features, and a kind's operations go list, create, get, replace, delete, then by name.
   3. **Every case is on the page and in search.** Under fumadocs' rendering, each page shows an Answers table: one row
      per case of `x-funcd-cases`, with the status, the problem type, when and what to do, in the static HTML. The
      page's structured data, which the site's search indexes, holds its summary and each case's texts.
   4. **The spec is loaded once.** The pages leave the spec out of their props (`includeDocument: false`); the build
      reads it from disk, and a browser fetches `/openapi.json` once per session.
   5. **Agents read Markdown.** Each page's Markdown copy, in the site's `llms` export, is written by the site from the
      spec: the method and path, the summary, the parameters and the Answers rows.
   6. **Each Spec tab links the reference.** `kindref`'s HTTP API section becomes a table: one row per described
      operation of the tab's kinds, its summary linking to its reference page, and its method and path.
   7. **The Overview is in the guide.** `docs/guide/api/index.md` says that this is the control plane API, under
      `/apis/funcd.io/v1alpha1`, and that the call of a Function is its one operation on the gateway; then
      authentication, errors with the problem types, conditional writes and dry runs, and where each server serves
      `/openapi` and `/docs`. The guide's root `meta.json` lists `api`.

## Design model

None. The decision moves where answers are shown; it adds no lifecycle, race, timer or delivery.

## Behavior rows

None. No platform behavior changes.

## Temporary workarounds

None. ADR-0329's rollout list still holds the operations not yet described; they join the reference as they leave it.

## Contracts

```go
// package kindref

// ReferencePaths returns the reference page of each described operation of a kind not Omitted, by operation ID:
// "<section>/<kind>/<name>", such as "services/kvstore/replace". Paths are unique.
func ReferencePaths(doc *huma.OpenAPI) map[string]string

// Render (ADR-0323) also writes docs/guide/api/operations.json, the map ReferencePaths returns, and the reference's
// meta.json files: api/meta.json (root, title, description, icon, its index and sections), one per section (the
// guide's title and icon, its kinds) and one per kind (its operations in order). Its HTTP API section is the table of
// Decision 3.6.
```

funcd-frontend, `packages/docs`:

| File | Holds |
| --- | --- |
| `app/lib/openapi.ts` | `createOpenAPI` with `includeDocument: false`; `apiSource()`, the pages at the paths of `operations.json` |
| `app/lib/source.ts` | the guide and the reference as two sources and two sidebar roots; each page's cases in its structured data; the reference pages' Markdown copies |
| `app/components/api-page.tsx` | the page: fumadocs' `OpenAPIPage` with `loadDocument`, then the Answers table |
| `app/routes/openapi-json.ts` | `/openapi.json`, the bundled spec |
| `react-router.config.ts` | the reference pages and their copies in the pre-render list |
| `scripts/sync-content.ts` | mirrors `api/openapi/funcd.v1alpha1.yaml` |
| `scripts/check-api-reference.ts` | one check per scenario of the site, on the built site |

| Consumes | Exposes | Read by |
| --- | --- | --- |
| The spec | `docs/guide/api/operations.json`, its `meta.json` files, each Spec tab's linked table | funcd-frontend, GitHub readers |
| The spec and `operations.json` | The API reference, its Markdown copies, `/openapi.json` | Readers, agents, the site's search |

## Implementation plan

funcd, one pull request:
1. `internal/controlplane/kindref`: `ReferencePaths`, the generated layout, the HTTP API table;
   `TestScenarioSpecTabLinksTheReference` and `TestReferencePaths` (every described operation of a kind not `Omitted`
   has one path, the paths are unique, the order holds).
2. `docs/guide/api/index.md`, the Overview, and `api` in `docs/guide/meta.json`.
3. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

funcd-frontend, one pull request after it: fumadocs-openapi 12.6.4 with `json-schema-typed`, the files of the table,
and `check-api-reference.ts` in the build after `check-md-links`, with one named check per scenario of the site:
`reference-shows-an-operation`, `function-call-in-reference`, `search-finds-a-case`, `agent-reads-an-operation` and
`reference-has-its-own-sidebar`. The site is published at funcd's next release.

Definition of done: both builds and their checks pass; a second `just generate` changes nothing; the built site holds a
page for each operation of `operations.json` and no other; the KV Spec tab's HTTP API section is the linked table.

## Review checklist

- [ ] `operations.json` holds each described operation of a kind not `Omitted`, and nothing else.
- [ ] Each Answers table has one row per case of its operation, in the static HTML.
- [ ] A page's search entries hold each of its cases' when texts.
- [ ] A Markdown copy holds its operation's Answers rows, and the site's link check passes.
- [ ] No page embeds the spec; `/openapi.json` serves it.
- [ ] No layout rule is written in funcd-frontend: it reads `operations.json` and the `meta.json` files.
- [ ] Each Spec tab's HTTP API section links each of its operations, and its links resolve.

## Consequences

- A reader finds the control plane API in a reference of its own, with each answer shown, searchable and exported for
  agents; a Spec tab points to it.
- On GitHub, the answers are in the spec and no longer in the Spec tab's Markdown: ADR-0329's driver that the pages
  live in funcd as plain Markdown now holds for the layout and the links, not for the answers.
- The site grows and builds longer: in experiment 0021, 97 MiB against 37 MiB, and 18 s against 14 s, for 168 pages.
- funcd-frontend gains a renderer to keep in step with fumadocs-openapi's releases, and code of its own for the Answers
  table and the Markdown copies.
- ADR-0329's rollout goes on unchanged: an operation joins the reference when its pull request describes it.

## Open questions

- The spec names no server for the control plane, so fumadocs' samples show `example.com`; whether the spec names one:
  the funcd-frontend pull request.
- The playground has no funcd to call on the public site; whether the reference hides it: the funcd-frontend pull
  request.

## References

- Experiment 0021, pyvvo/experiment at 0bb4f3dd (`0021-fumadocs-api-reference`): the three builds and their checks.
- fumadocs-openapi's React Router integration: https://www.fumadocs.dev/docs/react-router/integrations/openapi
- The decider's choices of 2026-11-15: the separate sidebar; 1A the Answers table and searchable cases; 2A the linked
  tables; 3A the Overview in the guide, as the control plane API; 4A the Omitted kinds out.
