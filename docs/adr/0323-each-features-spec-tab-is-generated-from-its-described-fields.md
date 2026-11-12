# ADR-0323: Each feature's Spec tab is generated from its described fields

- **Status**: Reviewing (2026-11-12, implemented); Accepted (2026-11-12), on the decider's go, after three rounds of an independent judge (a subagent), every
  finding folded
- **Date**: 2026-11-12
- **Deciders**: green-0-rabbit
- **Tags**: api, docs, codegen
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md) (API contracts & codegen; joined as a further entry, as ADR-0194
  and ADR-0196 did)
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md) (the generated, committed spec and its staleness gate)
  · [ADR-0048](0048-dto-validation-reference.md) (the per-field validation table; this ADR conforms to it) ·
  [ADR-0194](0194-api-duration-strings.md) (Decision 6: a string schema with a pattern, bounds in the `doc` tag, pinned
  by a test) · [ADR-0266](0266-volume-sizes-with-units.md) (the byte-size grammar, unchanged here) ·
  [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md) (Decision 2: a memory in the byte-size grammar) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (Decision 6: a quota refused at apply answers 422)
  · [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md) (behavior rows)

## Context & Need

The user guide's Developer chapter has one page per feature, as mantine.dev has one page per component, in the Platform
chapter's sections (the decider, 2026-11-12). A feature's page has two tabs, each at its own path and pre-rendered, so
that an LLM can fetch it: Usage, written by hand with inline examples, and Spec, which lists every field of the feature's
kinds, spec and status, with its type, its default and its limits, as the Kubernetes API reference does. Usage need not
cover every case, because Spec states the limits. This ADR decides the Spec tab and the descriptions behind it; the
site's tabs and its LLM routes are funcd-frontend's.

funcd generates its OpenAPI document from the Go types with huma (`just generate`, ADR-0005), and a pilot rendered the
Function and KVStore pages from it on 2026-11-12. Four gaps keep such pages incomplete:

- huma takes a description only from a field's `doc` tag. 12 of the 555 fields of `api/types/v1alpha1` carry one; 212
  have a Go comment written for maintainers, and 343 have none.
- Some limits live only in `Validate`: the grammar of `SizeQuota.MaxSize` (a volume's `size` and a namespace's
  `volumeBudget` carry it as a `pattern` tag), `minReplicas` not above `maxReplicas`, the meaning of 0 for a quota cap.
- A type that several kinds reach carries one description: `entrySchema` (`app.go`) writes "The part's name. The object
  takes the App's namespace and resource group." on the stores a Function declares.
- The `ci` recipe runs `generate` first and diff-checks only `*.go`, so `just ci` passes on a stale spec:
  `TestSpecGeneratedFromGo` (`api_test.go`) compares it with the copy `generate` just wrote. Only `gate.sh`'s
  tree-unchanged step compares the whole tree.

## Scenarios

- `scenario: spec-tab-generated`: **Given** the KVStore type with its `doc` tags, **when** `just generate` runs, **then**
  `docs/guide/develop/services/kv/spec.md` lists every field of KVStore's spec and status with its type, limits and
  description (`maxKeyBytes`: integer, 0 to 64000), and links `metadata` to `common-definitions.md`.
- `scenario: every-kind-in-one-feature`: **Given** a kind that `v1.AllKinds()` returns, **when** `go test` runs, **then**
  the test fails unless exactly one feature or `Omitted` lists it.
- `scenario: undescribed-field-refused`: **Given** a new field of a kind with no `doc` tag, **when** `go test` runs,
  **then** the test fails and names its schema path (`FunctionSpec.newField`).
- `scenario: undescribed-list-only-shrinks`: **Given** a listed field that gains a `doc` tag, **when** `go test` runs,
  **then** the test fails until the field leaves the list.
- `scenario: undescribed-list-never-grows`: **Given** a pull request that adds a line to the list, **when** CI runs,
  **then** it fails and names the added lines.
- `scenario: stale-generated-file-fails-ci`: **Given** a changed kind whose regenerated spec or Spec page is not
  committed, **when** `just ci` runs, **then** it fails and prints the files to commit.
- `scenario: byte-size-grammar-in-schema`: **Given** a KVStore with `spec.quota.maxSize: 2GB`, **when** it is applied
  through the API, **then** the answer is 422 from the schema check, naming the byte-size grammar.
- `scenario: doc-names-the-constant`: **Given** a `doc` tag or a `maximum` tag that copies a named constant, **when** the
  constant changes and the tag does not, **then** a test fails.

## Scope

In: the generator and its pages (a `spec.md` for each feature that has kinds, and `common-definitions.md`); the
described-field test and its list; `doc` tags for ObjectMeta, OwnerReference, Condition and the embedded `Status` type
(inlined into each kind's status table), and for KVStore with its quota; `ByteSize.Schema`; the constant test; the
staleness check and its CI filter; the agent rule.

Out: the Usage pages (written by hand) and the Examples page (links to the example repositories); the site's tabs and its `llms.txt`, `llms-full.txt` and
per-page `.md` routes (funcd-frontend, through fumadocs' `llms()`); the descriptions of the other fields (later PRs
shrink the list: Function, App and Workflow first); a schema for any other string with a grammar (Decision 4).

## Constraints & Decision drivers

- The pages cannot drift from the code: they come from the document the API serves (ADR-0005), committed and checked.
- One description per Go field, read by both the API document and the page.
- Descriptions are written for users: what a field does, its default and its limits, never internals or ADR numbers.
- `just generate` stays Go: the decider chose a Go generator beside specgen over the pilot's Python script.
- No new dependency: huma v2.40.0 (`Registry.Map`) is already in `go.mod`.

## Alternatives considered

1. **Go comments as descriptions** (specgen reads them with `go/doc`). Rejected: they are written for maintainers (ADR
   numbers, Badger prefixes, "the facade"), the API document would publish them, and 343 fields have none.
2. **Spec pages written by hand.** Rejected: nothing keeps them true when a type changes.
3. **Generate at site build in funcd-frontend.** Rejected by the decider: the guide on GitHub would have no Spec, and a PR
   that changes a type would not show its page change.
4. **A golangci-lint rule.** Rejected: no linter checks that a field has a given tag, and an analyzer cannot tell which
   structs the kinds reach; the test walks the published document itself.
5. **Describe every field before the check is on.** Rejected: it holds the generator back for about 540 fields; the
   list lets every new field be described from the start.
6. **Do nothing.** The pages keep 89 of Function's 128 fields empty, and nothing stops the gap from growing.

## Decision

1. **Spec pages.** `specgen -guide docs/guide` writes, from the document it builds, `docs/guide/develop/<path>/spec.md`
   for each feature below, and `docs/guide/develop/common-definitions.md` for ObjectMeta, OwnerReference and Condition.
   A Spec page links its Platform page, `docs/guide/platform/<path>.md`. For each kind of the feature it shows the
   apiVersion and kind, then the `spec` and `status` tables (field, type and limits, description) and one table per
   nested object, linked from its parent's row. A schema already on the page, or on `common-definitions.md`, is linked
   rather than repeated, and fields are sorted by name. The Usage page, `index.md` beside it, is written by hand.

   | Feature `<path>` | Kinds |
   | --- | --- |
   | `workloads/function` | Function, Revision, Invocation |
   | `workloads/app` | App, AppRevision |
   | `workloads/site` | Site |
   | `workloads/workflow` | Workflow, WorkflowRun |
   | `workloads/configuration` | ConfigMap |
   | `services/kv`, `services/bucket`, `services/sql`, `services/lake`, `services/secrets` | KVStore, Bucket, SQLDatabase, Lake, Secret (one each) |
   | `services/events` | EventSource, Sensor |
   | `security-and-networking/security` | Identity, Role, RolesAssignment, Policy |
   | `security-and-networking/networking` | Route, EgressPolicy |
   | `tenancy-and-limits/namespaces` | Namespace, ResourceGroup |
   | `disaster-recovery` | Backup, BackupSchedule, Restore |

   The kinds come from `v1.AllKinds()`. Five get no Spec. Service, WorkerNode, Gateway and Grant are unused, and
   pyvvo/funcd#1438 removes them (the decider, 2026-11-12). RuntimeClass has no fields that anything reads yet;
   FEAT-0012 F139 plans its fields and its first reader, and it gets a Spec then. An Invocation, the record of a call
   that a Sensor action or an App hook makes, is part of the Function feature.
2. **Every field described.** Every field that a Spec page shows carries a `doc` tag written for users: what it does,
   its default, and the limits the schema cannot hold. `TestEveryFieldIsDescribed` fails on a field with no description
   that `testdata/undescribed.txt` does not list, on a listed field that has one, and on a description that names an ADR.
3. **Shared types fit each user.** A type that several kinds reach is described without naming one of them: the
   `entrySchema` descriptions say "the resource that declares it", not "the App".
4. **Limits.** `ByteSize.Schema` returns `{type: string, pattern, patternDescription}`, a new one on each call, as
   `Duration.Schema` does (ADR-0194 Decision 6); its pattern is `byteSizeForm`'s, so the grammar of ADR-0266 has one
   source, and the `pattern` tags on a volume's `size` and a namespace's `volumeBudget` go. Another string with a
   grammar gets a schema only through its own decision: `ResourceSpec.memory`, in the grammar ADR-0291 Decision 2 names,
   is trimmed before `Validate` checks it (`resources.go`), so a pattern would refuse values funcd accepts today. A
   limit the schema cannot hold (a rule across fields, the meaning of 0) is written in the `doc` tag; a limit the schema
   holds (`minimum`, `maximum`, an enum) is not repeated there. A tag that copies a named constant is pinned by a test:
   the existing pins stay (`TestDurationFieldDocNamesBounds`, the `maxKeyBytes` maximum in `kvstore_test.go`), and
   `TestScenarioDocNamesTheConstant` covers the others.
5. **Staleness.** After `generate`, the `ci` recipe runs `scripts/check-generated.sh`: it fails when `api/openapi/` or a
   generated page differs from the commit, and prints those files (ADR-0005's staleness gate, extended to the pages).
   A `generated` output of ci.yml's changes job matches the generated pages, and the `ci` job and its `just ci` step
   run when it is true (an exclusion in `nondocs` is final, so it cannot count them). A CI step fails a pull request or
   a merge group whose diff adds a line to `testdata/undescribed.txt` against its base commit: it fetches that commit,
   fails when it cannot read it, and passes when the base has no list yet.
6. **Agents.** `.claude/rules/api-types.md`, a Claude Code rule scoped to `api/types/**/*.go`, tells an agent to write
   the tag with the field and to run `just generate`. It guides; the tests enforce.

## Design model

None: no lifecycle, race, timer or delivery; code generation and tests only.

## Behavior rows

`limits.md`, "A `spec.quota` refused at apply": a `maxSize` that the byte-size grammar refuses answers 422 from the
schema check (`body.spec.quota.maxSize`), as ADR-0292 Decision 6 says. Three cases still answer 400 from `Validate`, and
the row keeps `differs` for them: both forms together, a zero size, and a size past 2^63−1 bytes (`99999999999Gi`), which
ADR-0292 counts among the sizes that do not parse. `funcdctl apply` runs `Validate` offline first, so the CLI still shows
`Validate`'s message. An explicit empty `maxSize` now answers 422 too; an absent one still means no cap. A volume's
`size` and a namespace's `volumeBudget` keep the same pattern, now from the schema, and their 422, whose message now
gives the grammar in words (huma builds it from `patternDescription`). No other row changes.

## Temporary workarounds

`internal/controlplane/kindref/testdata/undescribed.txt` lists the fields that have no description when this ADR is
implemented (about 540). It only shrinks: the test fails on a listed field that has a description, and CI fails a pull
request that adds a line, a renamed or moved undescribed field included. Exit: the list is empty; the file, its
exemption and the CI step are then deleted.

## Contracts

```go
// Package kindref renders each feature's Spec page from funcd's OpenAPI document (ADR-0323).
package kindref

// Feature is one page of the Developer chapter and the kinds its Spec tab lists. Its Spec page is
// develop/<Path>/spec.md, and its Platform page platform/<Path>.md.
type Feature struct {
	Path  string    // under docs/guide/develop, e.g. "services/kv"
	Kinds []v1.Kind // in the order the page shows them, e.g. EventSource, Sensor
}

// Features is the table of Decision 1; with Omitted it covers v1.AllKinds() once.
var Features []Feature

// Omitted kinds get no Spec (Decision 1 gives each one's reason).
var Omitted = []v1.Kind{v1.KindGrant, v1.KindRuntimeClass, v1.KindService, v1.KindGateway, v1.KindWorkerNode}

// Marker is the first front-matter line of every generated page; specgen deletes a page that carries it and that
// Render no longer returns.
const Marker = "# Generated by specgen from api/openapi/funcd.v1alpha1.yaml: change the Go types, not this page."

// Render returns the generated files by path relative to docs/guide: each feature's spec.md and
// develop/common-definitions.md.
func Render(doc *huma.OpenAPI) (map[string][]byte, error)

// Undescribed returns, sorted, the schema path ("FunctionSpec.handler") of every field that Render shows with no
// description.
func Undescribed(doc *huma.OpenAPI) []string
```

```go
// package controlplane: the API that specgen documents (stub handlers and stub routes), shared with the tests.
func SpecAPI() huma.API

// package v1alpha1: the byte-size grammar of ADR-0266, byteSizeForm, as a pattern on every ByteSize field.
func (ByteSize) Schema(huma.Registry) *huma.Schema
```

| Consumes | Exposes | Read by |
| --- | --- | --- |
| huma's registry (`OpenAPI().Components.Schemas.Map()`) | `docs/guide/develop/**/spec.md`, `common-definitions.md` | funcd-frontend, which shows `index.md` and `spec.md` as tabs |
| The Go types' `doc`, `pattern`, `minimum` and `maximum` tags | Descriptions in `api/openapi/funcd.v1alpha1.yaml` | API clients |

## Implementation plan

1. `internal/controlplane/spec.go`: `SpecAPI`, moved out of `internal/controlplane/cmd/specgen/main.go`; `api_test.go`
   uses it.
2. `internal/controlplane/kindref/kindref.go` and `kindref_test.go`: the contracts above, with
   `TestScenarioSpecTabGenerated`, `TestScenarioEveryKindInOneFeature` (from `v1.AllKinds()`),
   `TestScenarioUndescribedFieldRefused` and `TestScenarioUndescribedListOnlyShrinks` (`TestEveryFieldIsDescribed` and
   `testdata/undescribed.txt`).
3. `internal/controlplane/cmd/specgen/main.go`: the `-guide` flag. `justfile`: `generate` passes `-guide docs/guide`;
   `ci` runs `scripts/check-generated.sh` after it. `scripts/check-undescribed.sh <base>` fails on a line added to the
   list since `<base>`. `scripts/generated_test.go`: `TestScenarioStaleGeneratedFileFailsCI` and
   `TestScenarioUndescribedListNeverGrows` run the two scripts on a scratch repository. `.github/workflows/ci.yml`: the
   `generated` filter output, ORed into the `ci` job's `if` and its `just ci` step's, not the light step's; a step that
   fetches the base commit (the pull request's, or the merge group's `base_sha`) and runs `check-undescribed.sh`.
4. `api/types/v1alpha1/bytesize.go`: `ByteSize.Schema`; the `pattern` tags go from `volume.go` and `namespace.go`.
   `internal/controlplane`: `TestScenarioByteSizeGrammarInSchema` applies the KVStore of the scenario.
5. `doc` tags on ObjectMeta, OwnerReference, Condition, `Status`, KVStore and its quota; `entrySchema` reworded;
   `api/types/v1alpha1/doc_test.go`: `TestScenarioDocNamesTheConstant`, first entries
   `DefaultMaxValueBytes`, `DefaultMaxKeyBytes` and `MaxValueBytesLimit`.
6. `docs/behavior/limits.md`: the row above. `.claude/rules/api-types.md`.
7. `scripts/agent/d just generate`, then `scripts/agent/d just ci`.

Definition of done: `just ci` passes; a second `just generate` changes nothing; every feature of Decision 1 has a
committed `spec.md`; the list holds only the fields not yet described; a KVStore `maxSize` of `2GB` gets 422 from the API.

## Review checklist

- [ ] `v1.AllKinds()` is covered once: each kind in one feature or in `Omitted`.
- [ ] Every feature of Decision 1 has a `spec.md` that links its Platform page.
- [ ] `TestEveryFieldIsDescribed` fails on a new undescribed field, on a listed field that has a description, and on a
  description that names an ADR.
- [ ] Every `doc` tag added is written for users: no package or internal name.
- [ ] No `entrySchema` description names the App.
- [ ] `ByteSize.Schema` returns a new schema on each call, its pattern is `byteSizeForm`'s, and no `pattern` tag repeats it.
- [ ] Each tag that copies a named constant is pinned by a test.
- [ ] `just ci` fails on a stale spec or Spec page and prints the files; a pull request that edits only a generated page
  runs it.
- [ ] A pull request or merge group that adds a line to the list fails CI, and so does a run that cannot read its base.
- [ ] `.claude/rules/api-types.md` has a block-style `paths` list.
- [ ] The `limits.md` row reads as Behavior rows says.

## Consequences

- Descriptions become API text: the OpenAPI document and anything built from it show them.
- About 540 undescribed fields are visible, listed debt; until the list empties, the published Spec tabs show those
  fields with an empty description.

## Open questions

- The order in which the listed fields get described: a task issue filed with the implementation PR, Function, App
  and Workflow first.

## References

- [ADR-0005](0005-api-surface-code-first-huma.md), [ADR-0048](0048-dto-validation-reference.md),
  [ADR-0194](0194-api-duration-strings.md), [ADR-0266](0266-volume-sizes-with-units.md),
  [ADR-0291](0291-a-functions-workers-get-cpu-and-memory-limits.md),
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md),
  [ADR-0313](0313-one-place-says-what-the-platform-does-and-who-decided-it.md).
- The page layout: mantine.dev's component pages, a Documentation tab and a Props tab
  (<https://mantine.dev/core/mask-input/>, checked on 2026-11-12).
- fumadocs' LLM support, `llms()` from `fumadocs-core/source` (<https://www.fumadocs.dev/docs/integrations/llms>,
  checked on 2026-11-12).
- Claude Code rules scoped by path (`.claude/rules/`, `paths` front matter), checked on 2026-11-12:
  <https://code.claude.com/docs/en/memory>.
- huma v2.40.0, `registry.go`: `Registry.Map`.
