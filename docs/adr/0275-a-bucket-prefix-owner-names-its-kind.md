# ADR-0275: A Bucket prefix's owner names its kind

- **Status**: Reviewing (2026-10-24); Accepted 2026-10-24 by an `adr-batch` run the decider delegated ("you have batch"); judged twice:
  round 1's two Majors (a Lake applied again binds its retained bucket by `ref`; SQLDatabase owners are not
  Function-only, now #1235) and its Minors folded, round 2 accepted
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: s3, authorization, bucket, lake, api
- **Realizes**: [FEAT-0003/F188](../feat/0003-feat-data-platform.md) (a Bucket prefix's owner names its kind)
- **Supersedes (in part)**: [ADR-0175](0175-engine-is-its-own-principal.md) Decision 6 (a prefix owner resolved by name
  at evaluation, a Lake only while no Function has the name) and its accepted risk ·
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) `BucketPrefix.owner` (a bare Function name)
- **Relates to**: [ADR-0152](0152-runtime-worker-owner-kind.md) (a Function and a Lake may share a name) ·
  [ADR-0251](0251-catalogservice-declares-its-stores.md) (admission fills the owner of a bucket a Lake defines) ·
  [ADR-0250](0250-workflow-declares-its-step-resources.md) (a Workflow's declared lakes) ·
  [ADR-0136](0136-roles-and-role-assignments.md) (role writers) · [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md)
  (the Lake stop's saves) · #1231

## Context & Need

A Bucket prefix names its single writer by name: `owner: sales` (ADR-0080). Since ADR-0175, the S3 gateway's PEP
resolves that name on each request: a Function of that name if one exists, else a Lake of that name that binds the
prefix. A name can belong to both kinds (ADR-0152). Two failures follow (#1231, both seen on the duckdb lane):

- A Function named like a Lake's prefix owner takes the prefix. Every catalog save of the Lake gets 403, while the
  Lake stays Ready and keeps acknowledging commits, which the next engine does not have.
- A deleted Lake's engine stops after the Lake has left the store, so both saves of its stop get 403 and the commits
  since its last save are lost.

Kubernetes avoids the ambiguity by giving a reference its kind (RBAC subjects, owner references), and so do funcd's
`ObjectRef`, `PrincipalRef` and `OwnerReference`. The decider chose, on 2026-10-24: the typed owner in the structured
shape, no reader for the old string form (no installation runs funcd), and the Bucket as the authority for a Lake
owner, as it already is for a Function owner.

## Scenarios

- `scenario: same-name-function-keeps-off-a-lake-prefix` — Given prefix `gold` of bucket `lakehouse` owned by
  `{kind: Lake, name: lake}`, and Function `lake` in the namespace, When Lake `lake`'s engine saves its catalog, Then
  the gateway accepts the PUT, and a PUT to `gold/` signed by Function `lake` gets 403.
- `scenario: deleted-lake-saves-on-its-stop` — Given Lake `c` with prefix `gold` of a bucket that outlives it (one the
  Lake defines, retained by default, or a standalone Bucket), owned by `{kind: Lake, name: c}`, When `funcdctl delete
  lake c` stops its engine right after a commit, Then the stop's saves are accepted, and a Lake `c` applied again with
  that bucket by `ref` reads the commit.
- `scenario: function-owner-unchanged` — Given a prefix owned by `{kind: Function, name: ingest}`, When Function
  `ingest` and another principal write to it, Then only `ingest`'s write is accepted, as today.
- `scenario: definer-fills-the-owner` — Given a Lake or a Function that defines a bucket whose prefix names no owner,
  When it is applied, Then the stored prefix's owner is `{kind: Lake, name: <the Lake>}` or
  `{kind: Function, name: <the Function>}`.
- `scenario: workflow-owner` — Given a Workflow prefix owned by `{kind: Function, name: <a step>}` or
  `{kind: Lake, name: <a declared lake>}`, When the Workflow is applied, Then the materialized Bucket's owner names the
  step's Function or the Lake; another owner is refused, naming the field.
- `scenario: string-owner-refused` — Given a Bucket with `owner: ingest`, When it is applied, Then it is refused with
  a decode error naming `spec.prefixes.owner`.

## Scope

**In**: the shape of `BucketPrefix.owner` and its validation; the PEP's writer rule; the owner a definer fills; a
Workflow's mapping; the pool access index; the docs, examples and fixtures of funcd, funcd-python, funcd-typescript and
funcd-functions. **Out**: KVStore table owners, which can only name a Function (a Workflow's table owner must be a
step), so the field implies the kind; SQLDatabase owners, which a Workflow may give to a declared lake while the SQL
PEP reads every owner as a Function, a defect of its own ([#1235](https://github.com/pyvvo/funcd/issues/1235)); a
two-phase delete (Backlog card "Two-phase delete …"); the `lake::query` PEP, which keys on a Lake
binding, not on a prefix.

## Constraints & Decision drivers

- A Function and a Lake may share a name (ADR-0152), so a reference that can name either must say which.
- Fail closed: a prefix whose owner is gone stays unwritable to anyone else.
- No installation runs funcd: the stored form may change with no reader for the old one (the decider, 2026-10-24).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| Structured `owner: {kind, name}` | the Kubernetes and funcd reference shape; the schema checks it | every YAML prefix owner changes; stored Buckets and backups from before do not decode | chosen by the decider |
| `ownerKind` beside `owner` | additive; stored objects stay valid | two fields for one reference | rejected by the decider |
| A prefixed string, `owner: Lake/sales` | one field | a reference packed in a string and parsed by hand | rejected |
| The owner's UID | names one object | a Lake applied again gets a new UID and loses its prefix; nobody can write a UID in YAML | rejected |
| One name across kinds in a namespace | no ambiguity anywhere | flips ADR-0152's scenario; a Function a Workflow or an App generates could be refused | rejected |
| A two-phase delete for the delete case | the Lake exists until its engine stopped | a platform mechanism for every kind | Backlog card |

## Decision

1. **The shape.** `BucketPrefix.owner` is a `PrefixOwner`: `kind` (`Function` or `Lake`) and `name` (a DNS-1123
   label), both required when the owner is set; an omitted owner still means no writer. A string owner does not
   decode, and the API refuses it naming the field. `Validate` refuses another kind and an owner with only one of the
   two fields.
2. **The writer rule.** For each prefix, the PEP's `s3Resource` sets the Cedar `owner` attribute and adds to
   `writers` the owner's own entity: `Function::"<ns>/<name>"` or `Lake::"<ns>/<name>"`. It reads no other object: the
   Bucket is the authority for a Lake owner as for a Function owner, and `lakeOwnsPrefix` goes. A user policy that
   compares `principal == resource.owner` now matches a Lake's engine on its own prefix. Role writers (ADR-0136) are
   unchanged.
3. **The definer fills it.** A Lake or a Function that defines a bucket by name fills each prefix with no owner with
   `{kind: Lake}` or `{kind: Function}` and its own name (admission's `ownDefined`); a named owner is kept.
4. **A Workflow.** In `spec.buckets`, `{kind: Function, name: <step>}` maps to the step's materialized Function and
   `{kind: Lake, name: <declared lake>}` stays as written; `Validate` refuses a Function owner that is not a step and
   a Lake owner that is not a declared lake.
5. **The pool access index** counts a prefix only for a Function owner.
6. **The delete case.** An engine authenticates by its derived key, which needs no stored object (`iam.go`), so a
   deleted Lake's engine still writes its prefix while the Bucket exists: a bucket the Lake defines is retained by
   default (ADR-0251), and a standalone Bucket is not the Lake's. It keeps only the write: with no stored Lake its
   principal has no `blobBindings`, so its reads are refused, and the engine's save is a single PutObject. The retained
   bucket keeps the deleted Lake's marker, so a Lake applied again binds it by `ref`. A bucket defined with
   `deletion: delete` goes with the Lake and its objects with it, so a refused save there loses nothing kept.
7. **One change across the repos.** The examples, fixtures and docs of the four repos move to the new shape together;
   funcd bumps the three language modules in the same PR as the API change.

## Design model

None: no lifecycle, race or timer changes; the writer rule becomes a function of the Bucket alone.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1
// PrefixOwner is a Bucket prefix's single writer, named with its kind (ADR-0275).
type PrefixOwner struct {
	Kind Kind       `json:"kind" enum:"Function,Lake"`
	Name ObjectName `json:"name"`
}
func (o PrefixOwner) IsZero() bool

type BucketPrefix struct {
	Name  string      `json:"name" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"`
	Owner PrefixOwner `json:"owner,omitzero"` // zero ⇒ no writer
}
```

```yaml
spec:
  prefixes:
    - name: gold
      owner:
        kind: Lake
        name: lake
```

| Input | Output |
|---|---|
| a prefix's `owner` | the Cedar resource's `owner` attribute and its `writers` entry, by kind, with no lookup |
| a Lake or a Function defining a bucket by name | each prefix with no owner filled with its own kind and name |
| a Workflow prefix owner | the step's materialized Function, or the declared Lake |

## Implementation plan

1. `api/types/v1alpha1/bucket.go`: `PrefixOwner`, `BucketPrefix.Owner`, the doc comments (no longer "a Function"),
   `Validate`; `workflow.go` `Validate`; `just generate` and the ADR-0048 field reference.
2. `internal/auth/cedar/capabilities.go`: `s3Resource` by kind; `lakeOwnsPrefix` goes.
3. `internal/controlplane/admission/bindingdeclared.go`: `ownDefined` takes the definer's kind.
4. `internal/workflow`: a mapping of its own for a bucket owner (`reconcile_workflow.go:581`), while `ownerFor` stays
   for a database owner (`rootentries.go:49`), with the refusal of a lake named like a step; `internal/function/
   poolaccess.go`; `internal/site` (an owner's presence is `!IsZero()`).
5. YAML: `e2e/fixtures/cni-engines.yaml`, `cmd/funcdctl/testdata/app-todo` (sources and golden), the docs and
   examples; funcd-python (`examples/s3-lakehouse/bucket.yaml`, `examples/releve-lakehouse/bucket.yaml`),
   funcd-typescript (`examples/s3-roundtrip/bucket.yaml`) and funcd-functions (`apps/todo`: `dr/restore-lake.yaml`,
   `dr/restore-copy.yaml`, `app/resources/kept.yaml`, `lake.yaml`, `scratch.yaml`), each a PR and a release, then
   `go get` here.
6. Tests:
   - `internal/auth/cedar`: one test per writer scenario; for the deleted Lake, no stored Lake object, a bare
     principal, `s3::write` allowed and `s3::read` refused;
   - `api/types/v1alpha1`: `Validate` and `TestScenarioStringOwnerRefused`;
   - `internal/controlplane/admission`: `TestScenarioDefinerFillsTheOwner`;
   - `internal/workflow`: `TestScenarioWorkflowOwner`;
   - the duckdb lane: `same-name-function-and-lake-stay-ready` also checks that Lake `lake` still saves (a commit,
     an engine replacement, the read-back while Function `lake` exists), and `lake-recreate-keeps-url` commits a
     table just before the delete and reads it after the re-apply by `ref`.
7. At acceptance, ADR-0175 and ADR-0080 gain their `Superseded in part by ADR-0275` back-links.
8. Done when `just ci-full`, the duckdb lane and the language repos' checks pass, and no string prefix owner is left in
   the four repos.

## Review checklist

- [ ] A prefix owner is `{kind, name}` with kind `Function` or `Lake`; a string owner is refused, naming the field.
- [ ] A Lake owner's prefix: its engine writes with no stored Lake object read; a same-name Function gets 403.
- [ ] A definer fills the typed owner; a Workflow maps step and lake owners and refuses others.
- [ ] The pool access index counts only Function owners.
- [ ] The scenario tests pass, and no string prefix owner is left in the four repos.

## Consequences

- **(+)** A same-name Function no longer takes a Lake's prefix, and a deleted Lake's last save lands.
- **(−)** Every hand-written prefix owner gains two lines, and stored Buckets and backups from before do not decode.
- **(−)** A Lake owner is trusted on the Bucket's word, without the check that the Lake binds the prefix: whoever can
  write the Bucket names its writer, as for a Function owner today.

## Open questions

None.

## References

- #1231 · ADR-0080 · ADR-0136 · ADR-0152 · ADR-0175 · ADR-0250 · ADR-0251 · ADR-0273 · Kubernetes typed references
  (RBAC `subjects`, `ownerReferences`)
