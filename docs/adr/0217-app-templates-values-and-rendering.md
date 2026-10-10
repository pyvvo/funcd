# ADR-0217: App templates — values and rendering

- **Status**: Accepted (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, template, values, json-schema, expressions, cli
- **Realizes**: [FEAT-0010/F120](../feat/0010-feat-apps.md) (App templates: values and rendering)
- **Source**: the section "The template and where files live", the decisions-table rows on values, a pushed
  template's content, the registry, the template version, values input, undeclared values, conditions, deploy and
  delete, and the scenarios `app-render-matches`, `app-render-refuses`, `app-deploy-waits` and `app-delete-reports`
  of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (a client file of the same kind) ·
  [ADR-0108](0108-eventsource-v2-named-events.md) (strict decoding) ·
  [ADR-0218](0218-app-template-images-lock-and-push.md) (F121, which extends this ADR) · ADR-0212 (F115 pause), ADR-0219
  (F122 requirements) and ADR-0220 (F123 dry run), all Proposed · ADR-0206 and ADR-0210 (both Accepted)
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md) (Implemented): render runs its store-free
  `App.Validate` (Decision 3) and fills `spec.version`, which the server still does not interpret.
  [ADR-0200](0200-app-revisions.md) (Implemented): the `funcdctl app` group (Decision 9) gains `render`, `deploy` and
  `delete`; deploy reads the AppRevision phases of its Decisions 5 and 6. Its two "F120" lock rows are ADR-0218's.
- **Extends (additive)**: [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (Implemented): `Expr.Idents`, and
  the schema resolver moved into `internal/expr` with a strict mode; the grammar and the typing rules are unchanged.

## Context & Need

One app installs in several places with different settings. ADR-0199 gives the server one App to apply, but writing
that App by hand per install copies every part.

**Purpose.** A template is a directory of App fragments plus a values schema. `funcdctl` renders it on the client
into one App, typed and checked, and deploys or deletes that App with one command each, waiting for the outcome. The
server never sees a template. Callers: a person, an agent, a Venom lane, a GitOps repository (`app render`).

## Scenarios

Fixture `testdata/app-todo/`: the design note's to-do template (`app/app.yaml`, `app/resources/*.yaml`,
`values/prod.yaml`) with `images.stats: todo-stats:1.0.3` and no `app.lock`, keeping only the sections and fields
`v1.AppSpec` holds when this ADR is built. It drops `resources/backup.yaml` (`backupSchedules`, planned for DR) and its
`when` key, `resources/secrets.yaml`, and the `configMaps`, `hooks` and `tests` sections (ADR-0213, ADR-0214, ADR-0216).
The e2e scenarios add `values/e2e.yaml`; its `registry` is a short temporary `oci-layout://` path to the to-do images.
`todo-api` still names Secret `todo-stripe-key` and ConfigMap `todo-settings`, so the e2e test creates both in
`team-a` before it deploys; they exist outside the App before and after ADR-0213.

- `scenario: app-render-matches` — `funcdctl app render ./app --name todo -n team-a -f values/prod.yaml` ⇒ prints
  the design note's App without what the fixture drops, with `resourceGroup: todo`, `spec.version: 1.2.0`, `todo-stats`
  included (`analytics.enabled` is true) and every image `registry.example/<repo>:<version>` without a digest
  (ADR-0218 adds digests); every expression without the roots `values`, `app` or `images` comes out byte for byte; exit 0.
- `scenario: app-render-refuses` — a values file with `minReplica: 2` ⇒ fails naming `minReplica`; `minReplicas: "2"` ⇒
  names `/minReplicas` and `integer`; `backup.enabled: true` without `target` ⇒ names `/backup` and `target`;
  `image: registry.example/todo-api:1.0.0` or `imageDigest: sha256:…` in `resources/api.yaml` ⇒ names the file, line and
  `functions[0].image` or `functions[0].imageDigest`; a key `minReplica` in that file's function ⇒ names the file;
  `images.stats: todo-stats:^1.0.0` ⇒ names `images.stats` and ADR-0218. Each prints nothing, exits 1.
- `scenario: app-deploy-waits` — with `todo-2` current, `funcdctl app deploy ./app --name todo -n team-a -f
  values/prod.yaml -f values/e2e.yaml` of version `1.3.0` (a new `todo-api` image) ⇒ prints `todo-3`, then each
  part's state as it changes (`Function/todo-api Pending Progressing`, then `Ready`), and exits 0 once
  `currentRevision` is `todo-3`; the same deploy again ⇒ prints `no change`, stamps nothing, exits 0. With an image
  that never starts, `app.upgradeTimeout: 20s`, `runtime.bootTimeout: 10s` and `invoke.activationTimeout: 5s`
  (ADR-0200 Decision 10) ⇒ exits 1 once `todo-3` is `Failed`, printing `ChildNotReady` naming `Function/todo-api`.
- `scenario: app-delete-reports` — `funcdctl app delete todo -n team-a` ⇒ prints each object of the App's tree (its
  parts, their Functions, Routes and Revisions, the AppRevisions) as the GC removes it, returns once none is left, and
  ends with `kept KVStore/todo-store` and `kept Bucket/todo-files`; exit 0.

## Scope

**In**: the template directory, `app.yaml`, values files, the values schema, expressions and their routing, `when`,
`images` with exact versions and the image refusal, the `registry` value, `funcdctl app render|deploy|delete` from a
directory. Owning the image refusal keeps the feat exit criterion of F120 true as written. **Out**: ranges, `app.lock`,
digests, `funcdctl app lock`, `push --template` and a registry ref as a source (ADR-0218); `--dry-run` (ADR-0220
Decision 7; when ADR-0220 is built first, this ADR adds the flag and `app-deploy-dry-run`); hook input
`fromVersion`/`toVersion` (ADR-0214); `Backup` records kept on delete (the DR workload ADR); template includes; `--set`.

## Constraints & Decision drivers

Client only: `internal/app/template` is never imported by the server. Reuse the `${{ }}` engine (ADR-0095), the
manifest decoder (`pkg/sdk/sdk.go:343-440`), `App.Validate` (`api/types/v1alpha1/app.go:270`) and `sameAppSpec`
(`cmd/funcdctl/app.go:173`). Values only from files kept in git. No new module: indirect ones become direct
(`go.mod:98,333`). Fail before anything is printed or applied.

## Alternatives considered

| Option | Lost because |
|---|---|
| Inject `additionalProperties: false` into every object of the schema | it lands in `if`/`then`/`allOf` too: the example's `then` subschema declares only `backup`, so every other key would be refused; `unevaluatedProperties` counts the keys those subschemas declare |
| Go `text/template` or interpolation (`prefix-${{ x }}`) | a second template language that can emit invalid YAML; ADR-0095 lists interpolation Out, and `app.name + "-store"` does it in one expression |
| Schema resolver falling back to `string` for an untyped path, as for Workflow `when` (`internal/workflow/condition.go:108-110`) | a value would be typed by a guess; the decider wants values typed by the schema |
| Deploy correlating its revision by spec alone | a downgrade's spec equals an older revision's; a number at or above the one read before the apply, that one only when it already holds the spec, is unambiguous |
| A watch for deploy and delete | the SDK has none (`pkg/sdk/sdk.go:87-187`: Apply, Get, List, Delete); a deploy poll GETs the App and its AppRevisions from `n0`, a delete poll LISTs each kind of the App's tree in the namespace |
| `null` in a values file deletes the key (Helm) | one value with two meanings; the schema decides whether `null` is allowed |

## Decision

1. **The directory.** A template holds `app.yaml` and `resources/*.yaml` (one level). Another file or directory,
   a subdirectory of `resources/` or another extension in it is refused at load, naming it. `app.lock` is refused
   until ADR-0218 reads it, so a lock is never ignored. A source must be a directory; any other argument fails
   with "deploying from a registry ref is ADR-0218".
2. **`app.yaml`** is a plain client file decoded strictly as `funcdctl.yaml` is, `quoteStrings` first, so `on` in
   `valuesSchema` stays a string as in values (`pkg/sdk/manifest.go:145-153`): no `apiVersion` or `kind`, an unknown key
   refused. `name` (required) is the default App name and must be a valid one (ADR-0199 Decision 1). `version`
   (required) is strict semver (`semver.StrictNewVersion`: three parts, no `v`, pre-release and build allowed) and
   becomes `spec.version`; a fragment cannot set `version`, nor `paused` (ADR-0212: `app pause` and `resume` set it).
   `registry`, required when `images` is not empty, is a literal or one `${{ }}` whose roots are only `values`; it must
   give a non-empty string without a trailing `/`. `images` maps a name (`[A-Za-z_][A-Za-z0-9_]*`) to
   `<repo>:<version>`, split at the first `:` (a repo carries no host), the version parsed by `StrictNewVersion`;
   anything else, a range or a digest included, is refused naming the entry and ADR-0218, whose `ParseImage` replaces
   this check. `valuesSchema` is a JSON Schema (Decision 3); absent, it is `{"type": "object"}`, so no value is
   declared. `when` maps `resources/<file>.yaml` to one `${{ }}` condition over `values` and `app`; a key naming no file
   is refused; a file without a key is included.
3. **Values.** Each `-f` file holds one YAML document whose top level is a mapping, parsed with `yaml.v3` (YAML 1.2, so
   `on` stays a string); an empty file is `{}`; several documents or another top level is refused naming the file. Files
   merge in order: maps key by key; a list, a scalar or `null` replaces the earlier value. No `-f` gives `{}`.
   `santhosh-tekuri/jsonschema/v6` validates the merged values against `valuesSchema` and fills nothing. The schema is
   compiled as draft 2020-12 (a `$schema` naming another draft is refused) with a loader that refuses every non-local
   `$ref`. Before compiling, render adds `unevaluatedProperties: false` to the root and to every subschema reached
   through `properties`, `items` or `prefixItems` that has `properties`, `type: object` or a `$ref`, unless it sets
   `additionalProperties` or `unevaluatedProperties` itself; inside a `$defs` entry it walks the same way but skips the
   entry itself, which the schema holding its `$ref` covers. This is the decided "as if every object said
   `additionalProperties: false` unless it says otherwise", where keys declared under `if`/`then`/`allOf`/`$ref` count.
   A refusal names the instance location and the keyword.
4. **Typing.** `values` is typed by `valuesSchema` through `expr.NewSchemaResolver` with `StrictTypes`: each segment
   an expression reads must be declared under `properties` with one explicit `type`; a node typed only by `enum`,
   `const`, a type array, `oneOf` or `$ref`, or an object without `properties`, is refused naming the path. `app` is
   `name`, `namespace`, `version`, all required strings; `images` has one required string per key. ADR-0095's
   defaults rule holds and `required` comes only from `required` arrays, so a value required only by `if`/`then`
   (`backup.target`) needs a `default` or a guard; validation still requires it. Eval substitutes a declared default
   for an absent leaf (`internal/expr/eval.go:538-597`).
5. **Routing.** A scalar value of a fragment (never a key) that `expr.Parse` accepts as one `${{ … }}`
   (`internal/expr/expr.go:100`) is routed by `Idents()`: all roots in `values`, `app`, `images` ⇒ checked in Select
   mode, evaluated, and replaced by its typed result (a string stays a quoted string, a number a number, an object or
   list a node); none ⇒ passed through byte for byte (a Workflow `when`, a Sensor `input`, `${{ true }}`); both ⇒
   refused. A scalar whose trimmed text starts with `${{` but that `expr.Parse` refuses is refused with the parse error,
   as the server reads it as an expression too (`internal/sensor/sensor.go:597`); one with other text around a `${{`
   whose first identifier is `values`, `app` or `images` is refused as interpolation (ADR-0095 Out). Each refusal names
   the file, line and path. Any other scalar is text. `registry` (Select) reads only `values`, `when` (Condition,
   `EvalBool`) only `values` and `app`. Render never writes an expression; a field that needs one holds it literally.
6. **Images.** The typed image fields of `v1.AppSpec`, `functions[].image`, `workflows[].steps[].function.image` and
   `sites[].image`, must each be exactly `${{ images.<name> }}` with a declared name; render refuses any other value
   there. It renders to `<registry>/<repo>:<version>`. Any `functions[].imageDigest` in a fragment is refused naming
   `<file>:<line>` and the path: a digest comes only through `images` (ADR-0218 Decision 4). A key `image` inside a
   `json.RawMessage` field (a contract schema, a step's `params`, a Sensor `input`) is data, routed by Decision 5.
7. **Render** loads the template, merges and validates the values, evaluates `registry` and every `when`, then parses
   each included file in lexical order (one `yaml.v3` document whose top-level keys are App section names) and routes
   its scalars; a routing or image error names `<file>:<line>: <path>`. Each routed fragment is decoded alone as the
   `spec` of an App by `sdk.DecodeManifest` (YAML 1.1 words quoted, strict, ADR-0108), so an unknown key fails as
   `<file>: <message>`. Render appends the typed sections and the lists of `hooks` (ADR-0214) in file order, sets
   `apiVersion`, `kind: App`, `metadata` (`name`, `namespace`, `resourceGroup`) and `spec.version`, and runs
   `App.Validate`, rewriting each `spec.<list>[i]` in its error to `<file>: <list>[j]` (`<list>` a section or
   `hooks.<point>`, `j` its index in that file), so a name two files declare names both. The App is printed only once
   all succeeded, so a failure prints nothing and deploy applies nothing.
   `funcdctl app render <dir> [--name] [-n] [--resource-group] [-f]… [-o json]` prints YAML (JSON with `-o json`); its
   flags are deploy's, `--resource-group` defaulting to the App's name.
8. **`funcdctl app deploy <dir> [--name] [-n] [--resource-group] [-f]… [--no-wait]`.** `--name` defaults to `app.yaml`'s
   `name`, `-n` to `default` (`nsOrDefault`, `cmd/funcdctl/workflow.go:373`), `--resource-group` to the App's name, or
   to the stored App's group when it exists; a flag naming another group than the stored one is refused naming both.
   They fill `app` before render. Deploy gets the App and notes `n0`, the number of its `status.latestRevision` (0 when
   none). Once `AppSpec` has `paused` (ADR-0212), deploy copies the stored `spec.paused` into the applied spec, so a
   deploy never resumes an App, and compares a revision's spec with `WithoutPause()` (ADR-0212 Decisions 3 and 9). It
   applies (`sdk.Client.Apply`, so the admission runs with the caller's rights; on the read `resourceVersion`, retrying
   a conflict as ADR-0210's `app rollback` does; a refusal, such as ADR-0219's 409, is printed with exit 1) unless the
   stored spec already equals the applied one (`sameAppSpec`), prints `no change` only when the stored spec and revision
   `n0` both hold the applied spec, and with `--no-wait` returns. Then every `deployPoll` it gets the App and follows
   the highest AppRevision `<app>-<k>`, `k` from `n0` up to `latestRevision`, controlled by the App's UID, that holds
   the applied spec, printing its name: `n0` qualifies only when it already holds that spec, since ADR-0200's stamp then
   stamps nothing, and an older spec is restamped above `n0`. It prints each `status.children` entry whose `state` or
   `reason` changed, as `<Kind>/<name> <state> <reason>`; it exits 0 once `currentRevision` names that revision and its
   post-hooks (ADR-0214) are done; 1 when the revision is `Failed` (printing its `ChildrenReady` or `Current` reason and
   message) or a post-hook of it failed (printing `HookFailed`, its message and `funcdctl app retry <app>`), when the
   App's spec no longer equals the applied one before such a revision exists, or when the App is gone. The client has no
   timeout: the server's deadline ends a rollout (ADR-0212 Decision 6, with ADR-0206's max(`startedAt`, `ReleasedAt`) +
   `app.upgradeTimeout`); an interrupt stops the wait, not the rollout; a platform hold (ADR-0206), a paused App or an
   unmet requirement (ADR-0219) keeps it waiting, and deploy prints the App's `Ready` reason each time it changes, so
   such a wait is visible. Deploy, upgrade and downgrade are this one command.
9. **`funcdctl app delete <app> [-n] [--no-wait]`** gets the App (absent: fails naming it), notes its UID and its
   non-`ref` `kv` and `buckets` entries, and notes its tree: in the namespace it lists each child kind of `gc.Pairs()`
   reachable from App (`internal/gc/gc.go:33-42`, read at run time; Secret excluded) and keeps the objects controlled
   (`v1.ControlledBy`, as `appHistory`, `cmd/funcdctl/app.go:65-89`) by a noted UID, until no new owner appears (App →
   Workflow → Function → Revision, App → Site → Route). It deletes the App (`sdk.Client.Delete`; a refusal, such as
   ADR-0219's 409, is printed with exit 1); `--no-wait` returns. Else every `deployPoll` it walks again, noting new
   descendants, prints `deleted <Kind>/<name>` for each noted object gone or held under another UID, prints
   `waiting <Kind>/<name>` once for an object still left after a poll that removed nothing, returns once none is left,
   then gets each noted store and prints `kept <Kind>/<name>` for each that exists. The GC removes the tree level by
   level; no client timeout. `ref` stores are not the App's and are not listed.
10. **Modules.** `santhosh-tekuri/jsonschema/v6` (Apache-2.0) becomes direct; whichever of F120 and F122 lands first
    drops `// indirect` from `Masterminds/semver/v3` (MIT), the other changes no line for it; ADR-0218 reuses both.

## Temporary workarounds

- Images render as `<repo>:<version>` without a digest, so a moved tag runs other bytes, and ADR-0200's rollback
  workaround stays open. Exit: ADR-0218's `app.lock`, which adds `@sha256:` to every rendered image.

## Contracts

```go
// internal/app/template (new; used by cmd/funcdctl only)
type Template struct {
	Name         v1.ObjectName
	Version      string            // strict semver
	Registry     string            // a literal or one ${{ }} over values
	Images       map[string]string // name → "<repo>:<version>"; ADR-0218 adds ranges
	ValuesSchema json.RawMessage   // empty ⇒ {"type":"object"}
	When         map[string]string // "resources/<file>.yaml" → ${{ }} condition
	Files        map[string][]byte // "resources/<file>.yaml" → fragment
}
type RenderInput struct {
	Name          v1.ObjectName        // empty ⇒ Template.Name
	Namespace     v1.NamespaceName     // empty ⇒ "default"
	ResourceGroup v1.ResourceGroupName // empty ⇒ Name
	Values        []json.RawMessage    // the -f files in order, each from ReadValues
}

func Load(dir string) (*Template, error)                  // Decisions 1, 2
func ReadValues(path string) (json.RawMessage, error)     // Decision 3: one YAML mapping as JSON
func Render(t *Template, in RenderInput) (*v1.App, error) // Decisions 3-7; fault.Invalid, op "app.render"

// internal/expr (additive). schemaResolver and schemaNode move here from internal/workflow/condition.go:63-131.
type SchemaOption func(*schemaConfig)
type schemaConfig struct{ strict bool }

func NewSchemaResolver(schemas map[string]json.RawMessage, optional map[string]bool, opts ...SchemaOption) Resolver
func StrictTypes() SchemaOption // a path without properties or one explicit type ⇒ fault.NotFound, never "string"
// Idents: each reference's root (undefined excluded), deduplicated in source order; valid after Parse, before Check.
func (e *Expr) Idents() []string

// cmd/funcdctl
const deployPoll = time.Second // deploy and delete wait loops
```

| Refusal (render, exit 1) | Message names |
|---|---|
| template entry, `app.lock`, `app.yaml` key, `version` (also in a fragment), `paused` in a fragment, `images` entry | the path or key, and ADR-0218 for a lock, range or digest |
| undeclared or invalid value | the instance location (`/minReplicas`) and the schema keyword |
| untyped, optional-without-default or mixed-root expression; a `${{` scalar `expr.Parse` refuses; interpolation | `<file>:<line>`, the field path and the reference or parse error |
| `functions[].image`, `workflows[].steps[].function.image` or `sites[].image` not `${{ images.<name> }}`; any `functions[].imageDigest` | `<file>:<line>` and the field path |
| unknown key in a fragment, or an `App.Validate` refusal | `<file>` and the section entry |

| Consumes | Exposes |
|---|---|
| a template directory and `-f` files · `internal/expr` · `sdk.DecodeManifest`, `sdk.Client` (Apply, Get, List, Delete) · `App.Validate` · `gc.Pairs()` · App and AppRevision status (ADR-0200) | `funcdctl app render`, `app deploy`, `app delete` (exit 0 on success or no change, 1 otherwise) · `internal/app/template` · `expr.NewSchemaResolver`, `expr.StrictTypes`, `Expr.Idents`; no config key, kind, reason or server change |

## Implementation plan

1. `internal/expr/schema.go`: the moved resolver, `NewSchemaResolver`, `StrictTypes`; `whenSchemaResolver`
   (`internal/workflow/condition.go:42`) calls it unchanged in behavior; `Idents` in `expr.go`, built on `identsOf`
   (`internal/expr/check.go:657`). Tests: strict refusals per untyped form; `Idents` on member, index, guarded and
   literal-only expressions; the workflow tests pass unchanged.
2. `internal/app/template/{load,values,render}.go` with unit tests: strict `app.yaml` and every Decision 1-2 refusal;
   values merge (maps, lists, `null`); `minReplica` refused at the root and nested; a key declared only under `then` or
   beside a `$ref` accepted; another draft and a remote `$ref` refused; routing (rendered, pass-through byte for byte,
   mixed refused, `${{ true }}` passed, an unparsable `${{` and `x-${{ values.a }}` refused); typed substitution
   (integer, boolean, the string `"true"`, an object); `when` include, exclude and unknown file; `version` and `paused`
   in a fragment refused; the image rule on the three fields, `imageDigest` (literal or `${{ values.d }}`) and
   `registry` reading `app` or `images` refused, plus a contract property `image` and a Sensor input `image: ${{
   event.data.image }}` that render unchanged; a key `minReplica` in `resources/api.yaml` naming that file, a name two
   files declare naming both; two files' `hooks.preApply` appended in order (ADR-0214 first); nothing returned on error.
3. `cmd/funcdctl/app.go`: `render`, `deploy` and `delete`, added to `Short`; CLI tests through `controlplane.NewServer`
   with the test writing AppRevision status: no change, `--no-wait`, failed revision, spec changed before a stamp, App
   gone, a stored spec equal to the applied one but unstamped (waits, no `no change`), an applied spec equal to the
   latest revision's but not the stored one (follows it), another `--resource-group` refused, a due and a failed
   post-hook (ADR-0214 first); delete refusal surfaced, a grandchild Revision waited on, `kept` only for a store left.
4. Scenarios: `TestScenarioAppRenderMatches` and `TestScenarioAppRenderRefuses` in `cmd/funcdctl` on
   `testdata/app-todo/` with the golden App; `TestScenarioAppDeployWaits` and `TestScenarioAppDeleteReports` (e2e tag;
   the test creates Secret `todo-stripe-key` and ConfigMap `todo-settings`, writes `values/e2e.yaml` and asserts `todo-plan-due` and the Revisions gone) in `pkg/funcd`.
5. `go.mod`: `jsonschema/v6` direct, and `semver/v3` unless ADR-0219 landed first; `go mod tidy` changes nothing else.
6. **Done**: `just ci` and `just ci-full` green; one passing test per scenario name (`app-deploy-dry-run` too when
   ADR-0220 is built first); no server package changed except the resolver move.

## Review checklist

- [ ] `go list -deps ./cmd/funcd` lacks `internal/app/template`; `go.mod` as Decision 10 says, nothing new.
- [ ] Load refuses an extra file or directory, `app.lock`, an unknown `app.yaml` key, a non-strict version, and a range
      or digest in `images`.
- [ ] The compiler is draft 2020-12 only, refuses non-local `$ref`, and `unevaluatedProperties: false` is added only
      where Decision 3 says; a test accepts a key declared under `then` and refuses `minReplica`.
- [ ] `StrictTypes` never returns Type `string` for an undeclared path; workflow resolution is unchanged.
- [ ] Render leaves expressions without `values`/`app`/`images` byte for byte, refuses mixed roots, an unparsable `${{`
      and interpolation, and refuses a value of the three image fields only that is not `${{ images.<name> }}`, and any
      `functions[].imageDigest`; `registry` reads only `values`, `when` also `app`.
- [ ] Each fragment passes `sdk.DecodeManifest` alone and the App `App.Validate`, an error naming the file; on any error
      stdout is empty and nothing applies.
- [ ] `funcdctl app deploy` follows the highest revision from `n0` that holds the applied spec, exits 0 once it is
      current and its post-hooks are done (no change included), and non-zero when it or a post-hook fails, it times
      out, the spec changes before it exists, or the App goes; `deployPoll` is the only interval.
- [ ] `funcdctl app delete` notes the App's tree (the kinds `gc.Pairs()` reaches from App, Secret excluded, by
      controller UID; a test derives them from `gc.Pairs()`, no count) and its non-`ref` stores before deleting, waits
      until none of the tree is left, and prints `kept` only for stores left.

## Consequences

**Positive**: one directory installs anywhere with values kept in git; a values mistake fails on the client, before any
write, naming the key; the server, its API and its config are unchanged; deploy and delete report an outcome a script
can test by exit code. **Negative**: every value an expression reads needs an explicit `type`, so a map-shaped value
cannot be read; a value required only by `if`/`then` also needs a `default`; until ADR-0218 a tag can move under a
template version. **Risks accepted**: deploy and delete can wait without bound while the server does (an ADR-0206 hold,
a paused App, an unmet requirement, a slow purge, a store an object outside the App binds: ADR-0199
`app-store-in-use-kept`); polling once a second per running command.

## Open questions

1. Reading map-shaped values (`additionalProperties` with a type) in expressions → when a template needs it.

## References

- [App design note](../reports/app-design.md) · [FEAT-0010](../feat/0010-feat-apps.md) · `internal/expr/expr.go:36-131`,
  `check.go:422-431`, `:482`, `:657-665` · `condition.go:42-131` · `sdk.go:343-440` · `app.go:21-183` · `gc.go:22-42`.
