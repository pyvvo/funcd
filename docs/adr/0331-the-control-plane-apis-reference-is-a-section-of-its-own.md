# ADR-0331: The control plane API's reference is a section of its own on the docs site (amends ADR-0329)

- **Status**: Proposed
- **Date**: 2026-11-15
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, site
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; its own entry, beside ADR-0329's)
- **Supersedes (in part)**: [ADR-0329](0329-each-api-operation-documents-its-answers.md) — Decision 3 (each Spec tab's
  HTTP API section); the scenarios `operation-shows-its-answers` and `function-call-documented`; Scope's "An interactive
  page on the docs site"; the driver "The pages live in funcd as plain Markdown, readable on GitHub and by agents"; the
  two alternatives it rejected, the docs site rendered by fumadocs-openapi and a separate Reference section; the
  Contracts' row "The spec → the HTTP API section of `docs/guide/develop/**/spec.md`"; the Definition of done's "the KV
  Spec tab shows the HTTP API section of its operations"; and the Consequence "A reader gets each described operation's
  answers from the Spec tab or `/docs`".
- **Relates to**: [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (`kindref`, the Spec
  tabs, `Features` and the field check) · [ADR-0330](0330-each-local-api-route-declares-its-error-causes.md) (the
  Handler API's errors, which stay on the Spec tabs)

## Context & Need

ADR-0329 shows each described operation's answers in an HTTP API section on its feature's Spec tab, and rejected a
reference on the docs site until a proof showed static pre-rendering, search and an export for agents. Experiment 0021
built that reference with fumadocs-openapi on funcd-frontend's site. One pre-rendered page per operation works. As the
library's guide sets it up, every page embeds the whole spec, its Markdown copy for agents is that spec as JSON, which
fails the site's link check, the status tabs show an error's cases only after a click, and search finds a page by its
title only. With the spec loaded once and a Markdown copy of the site's own, the build passes its checks. The decider
chose a reference of its own, with its own sidebar, on 2026-11-15: readers who write a client look for an operation in
an API reference, beside the guide.

## Scenarios

- `scenario: reference-shows-an-operation` (replaces `operation-shows-its-answers`): given the API reference, when a
  reader opens Services › KVStore › Replace a KVStore, then they see its method, path, parameters, body and
  authentication, and an Answers table with one row per case (status, problem type, when, what to do) with no click.
- `scenario: function-call-in-reference` (replaces `function-call-documented`): given the API reference, when a reader
  opens Workloads › Function › Call a Function, then each answer of ADR-0329 Decision 2 is a row of its Answers table.
- `scenario: spec-tab-links-the-reference`: given the KV Spec tab, when a reader looks at its HTTP API section, then each
  KVStore operation is a row with its method and path, its summary linking to its reference page.
- `scenario: search-finds-a-case`: given the site's search, when a reader searches a case's when text, then a result is
  the reference page of an operation that answers it.
- `scenario: agent-reads-an-operation`: given an operation's Markdown copy, when an agent reads it, then it holds the
  method and path, the summary, the authentication, the parameters, the body and one Answers row per case.
- `scenario: reference-has-its-own-sidebar`: given a reference page, when a reader looks at the sidebar, then it lists
  the reference's sections only, and its switcher leads to the Documentation.

## Scope

Unchanged (ADR-0329), but for the parts it supersedes. In: the API reference on the docs site, its layout written by
`kindref`, the Spec tabs' linked tables, the Overview page. Out: the spec's server URL and the playground (Open
questions).

## Constraints & Decision drivers

ADR-0329's driver that the pages live in funcd as plain Markdown changes, by the decider's choice of 2026-11-15: the
answers live in the spec, and on the docs site with Markdown copies for agents; the layout, the Spec tabs' links and the
Overview stay in funcd's guide. The other drivers are unchanged (ADR-0329), with: the site is static, pre-rendered for
GitHub Pages; no placement or naming rule is written twice; fumadocs-openapi 12.6.4 is MIT, and every package it adds
has a permissive license (experiment 0021).

## Alternatives considered

- **Keep ADR-0329's Spec tab sections.** Built and live, but a reader finds the API only inside the guide's features;
  the decider wants a reference of its own.
- **fumadocs-openapi as its guide sets it up.** It embeds the spec in every page and exports it as JSON for agents: in
  experiment 0021 its site (`default-site`) was many times today's (`baseline-site`) and failed its own link check.
- **The status tabs alone.** fumadocs shows an error's cases only after a click and indexes a page's title only, so the
  static page, agents and search would lose them; the decider chose an Answers table.
- **Reference pages written by `kindref` as Markdown.** One renderer and readable on GitHub, but no interactive schema
  view; the decider chose the generated spec through fumadocs.
- **The frontend derives the layout from the Spec tabs.** It parses headings and names pages a second time, in
  TypeScript; `kindref` already knows `Features`, `TagFeatures` and `Omitted`.
- **fumadocs' default page names, the operation IDs.** No rule, but paths such as `kvstore/replaceKVStore` repeat the
  kind; the decider saw and kept the short names.
- **Drop the Spec tabs' HTTP API section** (the decider chose links), **the Overview in funcd-frontend** (not reviewed
  with the code), **the Omitted kinds under "Other kinds"** (four are being removed, #1438).

## Decision

3. **The control plane API has a reference of its own on the docs site.**
   1. **The pages.** funcd-frontend renders each described operation with fumadocs-openapi from the spec that its sync
      mirrors, at `/docs/api/<path>`, where `<path>` is the operation's entry in `api/openapi/reference-paths.json`.
      The reference is a sidebar root, "API reference", described as funcd's control plane API, beside a root that
      funcd-frontend wraps around the guide's pages, "Documentation"; the sidebar's switcher toggles them, and each
      shows only its own tree.
   2. **The layout, from `kindref`.** `just generate` writes `reference-paths.json` and the reference's `meta.json`
      files. It covers each described operation whose tag is not an `Omitted` kind: a kind's operations go to the
      section of its feature (`Features`), a tag's where `TagFeatures` places it. `<path>` is `<section>/<kind>/<name>`:
      `<kind>` is the kind or tag in lowercase, and `<name>` the operation ID without its kind's name or plural, in
      kebab case (`listKVStores` → `services/kvstore/list`, `getFunctionLogs` → `workloads/function/get-logs`). A
      section keeps the guide's title and icon and lists its kinds in the order of `Features`; a kind's folder is titled
      with the kind and lists its operations list, create, get, replace, delete, then by name. `kindref`'s field check
      (ADR-0323 Decision 2) also covers the request and response schemas of each operation in the reference, which no
      Spec page shows any more.
   3. **Every answer is on the page and in search.** Under fumadocs' rendering, each page shows an Answers table, in the
      static HTML: one row per case of `x-funcd-cases` and one per status with no case, with the status, the problem
      type, when and what to do; a case whose body is another schema names it in place of the problem type. One
      function builds the rows for the table and the Markdown copy. The page's structured data, which the site's search
      indexes, holds its summary and each case's texts.
   4. **The spec is loaded once.** The pages leave the spec out of their props (`includeDocument: false`); the build
      reads it from disk, and a browser fetches `/openapi.json` once per session.
   5. **Agents read Markdown.** Each page's Markdown copy, in the site's `llms` export, is written by the site from the
      spec: the method and path, the summary, the authentication (a bearer token or `X-Api-Key`), the parameters, the
      body's schema and the Answers rows.
   6. **Each Spec tab links the reference.** `kindref`'s HTTP API section becomes a table: one row per described
      operation of the tab's kinds and of the tags that `TagFeatures` places on it, its summary linking to
      `/docs/api/<path>`, and its method and path.
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

// ReferencePaths returns the reference page of each described operation whose tag is not an Omitted kind, by
// operation ID: "<section>/<kind>/<name>", such as "services/kvstore/list". Paths are unique.
func ReferencePaths(doc *huma.OpenAPI) map[string]string

// SectionMeta is a guide section's title and icon, from docs/guide/develop/<section>/meta.json.
type SectionMeta struct{ Title, Icon string }

// ReferenceFiles returns the reference's generated files by path: api/openapi/reference-paths.json, the map that
// ReferencePaths returns; docs/guide/api/meta.json ({root, title, description, icon "SquareCode", pages: index and
// the sections}); one meta.json per section ({title, icon, pages: its kinds}); and one per kind ({title: the kind,
// pages: its operations in order}). specgen passes the guide's sections, read from their meta.json files.
func ReferenceFiles(doc *huma.OpenAPI, sections map[string]SectionMeta) (map[string][]byte, error)

// Render (ADR-0323) writes the HTTP API section as the table of Decision 3.6.
```

funcd-frontend, `packages/docs`:

| File | Holds |
| --- | --- |
| `app/lib/openapi.ts` | `createOpenAPI` with `includeDocument: false`; `apiSource()`, the pages at the paths of `reference-paths.json` |
| `app/lib/collections.ts` | the guide's MDX collection, moved out of `source.ts`, which now reads files; it leaves out JSON that is no meta |
| `app/lib/source.ts` | the guide and the reference as two sources; the "Documentation" root; each page's cases in its structured data; the reference pages' Markdown copies; the bundled `SquareCode` icon |
| `app/components/api-page.tsx` | the page: fumadocs' `OpenAPIPage` with `loadDocument`, then the Answers table |
| `app/routes/docs.tsx`, `app/app.css` | a reference page's route branch; fumadocs-openapi's CSS preset |
| `app/routes.ts`, `app/routes/openapi-json.ts` | `/openapi.json`, the bundled spec |
| `react-router.config.ts` | the reference pages and their copies in the pre-render list |
| `scripts/sync-content.ts` | mirrors `api/openapi/funcd.v1alpha1.yaml` and `reference-paths.json` |
| `scripts/check-api-reference.ts` | one check per scenario of the site, on the built site |

| Consumes | Exposes | Read by |
| --- | --- | --- |
| The spec and the guide sections' `meta.json` | `reference-paths.json`, `docs/guide/api`'s `meta.json` files, each Spec tab's linked table | funcd-frontend, GitHub readers |
| The spec and `reference-paths.json` | The API reference, its Markdown copies, `/openapi.json` | Readers, agents, the site's search |

## Implementation plan

funcd, one pull request:
1. `internal/controlplane/kindref`: `ReferencePaths`, `ReferenceFiles`, the HTTP API table, and the field check over the
   reference's schemas. Tests: `TestReferencePaths` (one path per covered operation, unique, ordered, the plural
   stripped), `TestScenarioSpecTabLinksTheReference` (each link's path is in `reference-paths.json`).
   `TestScenarioOperationShowsItsAnswers` goes; `TestScenarioFunctionCallDocumented` keeps only its checks of ADR-0329
   Decision 2 (the `{gateway}` server, the optional security, the control plane neither documents nor routes the call),
   renamed `TestFunctionCallInTheSpec`.
2. The generate step writes `ReferenceFiles` and removes stale `meta.json` files under `docs/guide/api`;
   `scripts/check-generated.sh` also checks `docs/guide/api` and `api/openapi/reference-paths.json`.
3. `docs/guide/api/index.md`, the Overview, and `api` in `docs/guide/meta.json`.
4. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

funcd-frontend, one pull request after it: fumadocs-openapi 12.6.4 with `json-schema-typed`, the files of the table, and
`check-api-reference.ts` in the build after `check-md-links`, with one named check per scenario of the site:
`reference-shows-an-operation`, `function-call-in-reference`, `search-finds-a-case` (any page that answers the case),
`agent-reads-an-operation` and `reference-has-its-own-sidebar` (both roots, read from the page's serialized page
tree). funcd makes no release between the two pull requests, since the Spec tabs would link pages that the published
site does not have. The site is published at funcd's next release.

Definition of done: both builds and their checks pass; a second `just generate` changes nothing; the built site holds a
page for each entry of `reference-paths.json` and no other; the KV Spec tab's HTTP API section is the linked table.

## Review checklist

- [ ] `reference-paths.json` holds each described operation whose tag is not an `Omitted` kind, and nothing else; no
  name is plural.
- [ ] Each Answers table has one row per case and per status with no case, in the static HTML.
- [ ] A page's search entries hold each of its cases' when texts.
- [ ] A Markdown copy holds its operation's authentication, body and Answers rows, and the site's link check passes.
- [ ] No page embeds the spec; `/openapi.json` serves it.
- [ ] funcd-frontend writes no placement or naming rule of the reference: it reads `reference-paths.json` and the
  `meta.json` files.
- [ ] Each Spec tab's HTTP API section links each of its operations, and each link's path is in `reference-paths.json`.
- [ ] `docs/guide/api/index.md` says it is the control plane API, and `docs/guide/meta.json` lists `api`.
- [ ] ADR-0329 Decision 2 keeps a test in funcd.

## Consequences

- A reader finds the control plane API in a reference of its own, with each answer shown, searchable and exported for
  agents; a Spec tab points to it.
- On GitHub, the answers are in the spec, and a Spec tab's links lead to the docs site's paths: a GitHub reader follows
  them there.
- The site grows and builds longer: experiment 0021's `tuned-site` and `build-tuned` against `baseline-site` and
  `build-baseline`.
- funcd-frontend gains a renderer to keep in step with fumadocs-openapi's releases, and code of its own for the Answers
  table and the Markdown copies.

## Open questions

- The spec names no server for the control plane, so fumadocs' samples show `example.com`. Naming one changes
  `SpecAPI` and the document that clients get, as ADR-0329's `{gateway}` server did: a follow-up in funcd. Whether the
  site's samples hide the host meanwhile: the funcd-frontend pull request.
- The playground has no funcd to call on the public site; whether the reference hides it: the funcd-frontend pull
  request.

## References

- Experiment 0021, pyvvo/experiment at 0bb4f3dd (`0021-fumadocs-api-reference`): the three builds and their checks.
- fumadocs-openapi's React Router integration: https://www.fumadocs.dev/docs/react-router/integrations/openapi
- The decider's choices of 2026-11-15: the separate sidebar; 1A the Answers table and searchable cases; 2A the linked
  tables; 3A the Overview in the guide, as the control plane API; 4A the Omitted kinds out.
