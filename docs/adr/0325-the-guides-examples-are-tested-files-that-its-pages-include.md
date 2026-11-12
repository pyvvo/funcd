# ADR-0325: The guide's examples are tested files that its pages include

- **Status**: Reviewing
- **Date**: 2026-11-12
- **Deciders**: green-0-rabbit
- **Tags**: docs, examples, testing
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as ADR-0323
  did)
- **Relates to**: [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md) (the Usage and Spec
  tabs) · [ADR-0248](0248-stores-at-the-root.md) (the binding-declared admission an example can break) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (the repos funcd pins in `go.mod`)

## Context & Need

The Usage tab of a feature shows manifests and handler code. ADR-0323 left the Usage pages out of its Scope: they are
written by hand, and their examples are inline code blocks today. The decider chose on 2026-11-12 the way Go and Rust
keep such examples true: an example that the docs show is a file that a test runs, kept beside the code it shows (a Go
`Example` function in `example_test.go`, a Rust doc test that `cargo test` runs). funcd already does this for one file:
`internal/platform/config/example_test.go` and `cmd/funcd/main_test.go` read `examples/funcdconfig.yaml`.

An inline example drifts: the KV pilot's first draft bound a table of a KVStore that its Function did not declare, and
only applying it showed the refusal (`admission.binding-declared`, ADR-0248). fumadocs includes a file into a page with
`::include[<path>]`, and one region of it with `#<region>`. A spike on 2026-11-12 (fumadocs-mdx 15.5.3, `defineDocs`
from `fumadocs-mdx/macro`) showed that the site needs `remark-directive` for it, added through `applyMdxPreset`, and that
the page's `.md` copy for an LLM then holds the included code. Without `remark-directive`, the directive stays on the
page as text, and nothing fails.

## Scenarios

- `scenario: examples-apply`: **Given** the example manifests and every lane manifest that a page includes, **when**
  `go test` runs, **then** a fresh platform accepts each document, in its file's order.
- `scenario: example-manifest-refused`: **Given** an example manifest whose binding names a store that its Function does
  not declare, **when** `go test` runs, **then** the examples test fails with the admission's refusal and the file's
  path.
- `scenario: include-resolves`: **Given** a page that includes a file that does not exist, or a region that its file
  does not mark, **when** `go test` runs, **then** a test fails and names the page and the include.
- `scenario: example-included`: **Given** a Usage page that includes an example file, **when** the site builds, **then**
  the page shows the file as a code block, its `.md` copy holds it, and no page keeps an `::include` line.
- `scenario: region-included`: **Given** a lane example with `// #region handler`, **when** a page includes it with
  `#handler`, **then** only the lines of that region show.
- `scenario: pinned-handler-code`: **Given** funcd's `go.mod` pinning funcd-typescript at a tag, **when** the site syncs,
  **then** the code it includes is that tag's.

## Scope

In: where the examples live, how they are tested, how pages include them, and the site's include support. Out: running
handler code in funcd's `go test` (the lanes run it); shell commands in a page, which stay inline.

## Constraints & Decision drivers

- An example has one source, and a test runs it.
- The test is cheap enough for `go test ./...`: no function code runs, and no lane.
- No new Go dependency; the site adds `remark-directive` (MIT).

## Alternatives considered

1. **Inline code blocks.** Rejected: nothing checks them, and the pilot's draft shows they drift.
2. **Examples beside the guide's pages, or in the root `examples/` folder.** Rejected by the decider: examples live
   beside the code they show, as in Go and Rust.
3. **Static validation only** (decode and `Validate`). Rejected: the pilot's mistake passed `Validate` and was refused by
   admission. `TestExampleResourcesDecode` (`internal/testkit/langmod`, #1078) already decodes and validates the pinned
   repos' examples; admission is what this ADR adds.
4. **A copy of the daemon's admission list in the test.** Rejected: a second list drifts from the daemon's, which is the
   failure this ADR exists to stop. The platform itself is cheap: `TestScenarioLibraryCredentialsReplaceDevToken`
   assembles one in memory and answers two calls in 0.03 s.
5. **A funcd manifest beside the lane's handler.** Rejected: the binding's alias would live in two files that nothing
   compares, the pilot's failure class.
6. **Running every example with `funcdctl dev` in funcd's CI.** Rejected for manifests: an in-memory platform runs the
   same validation and admission in milliseconds; running handler code stays the lanes' job.
7. **Do nothing.** Rejected: the pages keep drifting from the code.

## Decision

1. **Manifests beside the kinds.** An example manifest that a page shows without its handler is a file under
   `api/types/v1alpha1/examples/<feature>/`, one scenario per file: its documents in the order they are applied, with
   every object they need on a fresh funcd. A region marks a part that a page shows alone.
2. **Tested on the platform.** `TestScenarioGuideExamplesApply` (`pkg/funcd`) assembles a fresh platform over memory for
   each file (`funcd.New(funcd.InMemory())`, as `pkg/funcd/credentials_test.go` does), so its write path runs the
   daemon's own validation and admissions, including the providers'. It applies the file's documents in order through
   `pkg/sdk`, as `funcdctl apply` sends them, and every document must be accepted. `InMemory` sets no runtime shim, so
   the platform fetches no image and runs no function code: a replica of an applied Function is a placeholder process
   that stops with the platform (`runtimeShim` and the materializer in `pkg/funcd/funcd.go`, `workerSpec` in
   `internal/function`). The test covers every file under `api/types/v1alpha1/examples/` and every lane manifest that a
   page includes; a new file needs no test change.
3. **Code and its manifest from the lane.** Handler code comes from an example that a lane runs, in a repo that `go.mod`
   pins (funcd-typescript, funcd-python, funcd-functions), at that version; a region marks the excerpt in the file that
   the lane's bundle is built from. When a page shows a Function with its handler, both come from the same lane example:
   a region of its handler and a region of its manifest. A region is `// #region <name>` … `// #endregion <name>` in
   TypeScript, and `# region <name>` … `# endregion <name>` in YAML and Python.
4. **Pages include.** A Usage page shows a manifest or handler code only through `::include[<path>]`, with `#<region>`
   for a part, the path relative to the page. A file of a pinned repo is written as if that repo sat at funcd's root
   under `_repos/<repo>/`; funcd never commits `_repos`. `TestScenarioGuideIncludesResolve` (`pkg/funcd`) checks every
   include of `docs/guide`: a funcd path exists, a `_repos` path exists in the pinned module
   (`internal/testkit/langmod`), and a named region is marked in its file.
5. **The site.** It adds `remark-directive` to fumadocs' preset (`applyMdxPreset({ remarkPlugins: [remarkDirective] })`).
   Its sync mirrors funcd's layout under one root: `docs/guide`, `api/types/v1alpha1/examples`, `go.mod`, and, under
   `_repos/<repo>/`, each file that a page includes from a pinned repo, fetched at the tag in that `go.mod`, with the
   tag it fetched recorded beside it. After `react-router build`, `scripts/check-includes.ts` reads the pre-rendered
   pages and their `.md` copies. It fails the build on an `::include` line left in any of them, on an include whose
   `.md` copy has no code block equal to its region as fumadocs-core's `extractCodeRegion` gives it (or to the whole
   file, for an include with no region), and on a recorded tag that differs from the mirror's `go.mod`.

## Design model

None: files and tests only.

## Behavior rows

None: no runtime behavior changes.

## Temporary workarounds

None.

## Contracts

```go
// TestScenarioGuideExamplesApply applies every file under api/types/v1alpha1/examples, and every lane manifest that a
// page of docs/guide includes, its documents in order, to an in-memory platform through pkg/sdk; each document must be
// accepted (ADR-0325).
func TestScenarioGuideExamplesApply(t *testing.T)

// TestScenarioGuideIncludesResolve checks that every ::include of docs/guide names a file that exists, in funcd or in
// the pinned module under _repos/<repo>/, and a region that the file marks (ADR-0325).
func TestScenarioGuideIncludesResolve(t *testing.T)
```

```text
api/types/v1alpha1/examples/
└── kv/
    └── own-store.yaml           a KVStore applied on its own, then a Function that declares it by ref
```

The includes of the KV Usage page, `docs/guide/develop/services/kv/index.md`:

```text
::include[../../../../../_repos/funcd-typescript/examples/kv-counter/counter.yaml#manifest]
::include[../../../../../_repos/funcd-typescript/examples/kv-counter/src/main.ts#handler]
::include[../../../../../_repos/funcd-typescript/examples/kv-claim/src/main.ts#claim]
::include[../../../../../api/types/v1alpha1/examples/kv/own-store.yaml#kvstore]
::include[../../../../../api/types/v1alpha1/examples/kv/own-store.yaml#binding]
```

| Consumes | Exposes |
| --- | --- |
| `api/types/v1alpha1/examples/**/*.yaml` | the include paths of the guide's pages |
| The tags of the pinned repos in `go.mod`, and the regions of their lane examples | `_repos/<repo>/` in the site's mirror |

## Implementation plan

1. funcd: `api/types/v1alpha1/examples/kv/own-store.yaml`, from the KV page's own-store manifests;
   `pkg/funcd/guide_examples_test.go` with `TestScenarioGuideExamplesApply`, `TestScenarioExampleManifestRefused` (on a
   copy in `pkg/funcd/testdata/` whose binding names an undeclared store) and `TestScenarioGuideIncludesResolve`.
2. funcd-typescript: the regions `manifest` in `examples/kv-counter/counter.yaml`, `handler` in
   `examples/kv-counter/src/main.ts` and `claim` in `examples/kv-claim/src/main.ts`, with the comments inside them
   written for readers; a release; the `go get` bump in funcd.
3. funcd-frontend: `remark-directive` in fumadocs' preset; the mirror of Decision 5; `scripts/check-includes.ts` in the
   docs package's `build`, after `react-router build` (`scenario: example-included`, `scenario: region-included`,
   `scenario: pinned-handler-code`).
4. funcd: the KV Usage page switches to the five includes, after step 3 is merged.
5. `scripts/agent/d just ci` in funcd; the site's build in funcd-frontend.

Definition of done: `just ci` passes; the KV Usage page shows its manifests, its handler and its claim excerpt through
includes, on the site and in its `.md` copy; a broken example or include fails `go test`.

## Review checklist

- [ ] Every manifest and handler excerpt on a Usage page comes through an `::include`.
- [ ] `TestScenarioGuideExamplesApply` runs on `funcd.New(funcd.InMemory())`, applies every example file and every
  included lane manifest, and fails with the file's path on a refused document.
- [ ] A Function shown with its handler takes both from the same lane example, through regions.
- [ ] `TestScenarioGuideIncludesResolve` fails on a missing file or region.
- [ ] The site's build fails on a leftover `::include`, and its `.md` copies hold the included code.

## Consequences

- An example or an include that stops matching the API or its file fails `go test` in the PR that broke it.
- A handler excerpt changes only through a release of its repo and a funcd bump.
- In funcd itself, the guide's `.md` files show `::include` lines instead of the code, on GitHub and to an agent that
  reads the repository; only the site and its `.md` copies hold the code.
- A Function shown with its handler shows the lane's image reference (`oci-layout:///mnt/funcd-deps/registry:counter`),
  which the page explains.

## Open questions

None.

## References

- [ADR-0323](0323-each-features-spec-tab-is-generated-from-its-described-fields.md),
  [ADR-0248](0248-stores-at-the-root.md), [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md).
- fumadocs-core 16.18, `dist/mdx-plugins/remark-include.js`: a path resolves against the page's folder (L33), and the
  region markers (`regionMarkers`, L143-175). <https://www.fumadocs.dev/docs/mdx/include>, checked on 2026-11-12.
