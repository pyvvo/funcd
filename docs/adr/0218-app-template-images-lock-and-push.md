# ADR-0218: App templates — images, lock and push

- **Status**: Accepted (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, template, artifact, oci, cli, semver
- **Realizes**: [FEAT-0010/F121](../feat/0010-feat-apps.md) (App templates in an OCI registry, with pinned images)
- **Source**: the Images, Pinning and Commands parts of the template section and the decisions rows "A template in a
  registry", "What a pushed template holds", "Who sets component versions", "Digests", "Template version and tag",
  "Version ranges in `images`", "Images from two registries", "A platform mapping of registries" and "Where the pins
  live" of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: [ADR-0031](0031-oci-artifact-distribution-oras.md) (OCI artifacts) ·
  [ADR-0089](0089-python-function-dependency-bundling.md) (the deterministic bundle) ·
  [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (a client file, as `app.lock` is)
- **Builds on (additions only)**: [ADR-0217](0217-app-templates-values-and-rendering.md) (Proposed): Decision 1 reads
  `app.lock` (refused until this ADR) and takes a registry ref as a source; Decision 2's `images` entry gains ranges
  and `registry` gains three refusals; Decision 6's rendered image gains `@<digest>`, so its workaround exits here,
  and a directory without `app.lock` now resolves its images at the registry (this ADR's Decision 4); `Template` gains
  `Parsed` and `Lock`; `EvalRegistry` exports Decision 7's registry step; Decision 10 makes `Masterminds/semver/v3`
  direct. [ADR-0200](0200-app-revisions.md) (Implemented): its workaround's exit, "F120's template lock", is this
  ADR's lock (F121 in the feat doc); ADR-0200 and [ADR-0199](0199-app-resource.md) stay as written.
- **Extends (additive)**: [ADR-0139](0139-site-declarative-static-web-app.md) (Implemented): `push --template` beside
  `push --site`, a third artifact type · [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (Implemented):
  `OrasMaterializer.Resolve` delegates to the new `ResolveDigest`, with the same behavior.

## Context & Need

ADR-0217 renders a template into an App, but its images name tags: a tag moved after a release changes what one
template version or one commit installs, and an App rollback re-resolves the tag (ADR-0200's workaround). A builder
also cannot publish a template version or accept compatible patch releases.

**Purpose.** `funcdctl app lock` pins each `images` version or range to a version and a digest in a committed
`app.lock`, which render and deploy write into the App; `funcdctl push --template` publishes the directory unchanged
as an OCI artifact tagged with its version, a source for `app render` and `app deploy`. Callers: a builder (lock,
push) and an installer (render, deploy), a person or an agent, all in funcdctl; the server never pulls a template.

## Scenarios

Fixture: the design note's `./app`: `version: 1.2.0`, `registry: ${{ values.registry }}` with the schema default
`registry.example`, a schema that requires `host` (set by `values/prod.yaml`), `images.api: todo-api:1.0.0`,
`images.stats: todo-stats:^1.0.0`, and a fragment that gives Function `todo-api` the `image: ${{ images.api }}`.

- `scenario: app-template-pinned` — `registry.example/todo-api:1.0.0` resolves to `sha256:4f1c…` ⇒ `funcdctl app lock
  ./app` writes `images.api` with `requested: todo-api:1.0.0`, `version: 1.0.0` and `digest: sha256:4f1c…`; `funcdctl
  push --template ./app registry.example/todo-app:1.2.0` prints `registry.example/todo-app:1.2.0@sha256:…`, and the
  pulled files equal `./app` byte for byte. Then the tag `todo-api:1.0.0` moves to `sha256:9e0b…` ⇒ `funcdctl app
  deploy <src> -f values/prod.yaml` from `registry.example/todo-app:1.2.0` and from `./app` writes Function `todo-api`
  with `image: registry.example/todo-api:1.0.0@sha256:4f1c…`, and its Revision runs `sha256:4f1c…`; only the directory
  deploy warns, naming `api` and both digests. A push to `registry.example/todo-app:1.3.0` fails naming `1.3.0` and
  `1.2.0`, and a push of a copy without `app.lock` fails naming `app.lock`; neither writes to the registry.
- `scenario: app-registry-value` — the same sources pushed to `oci-layout:///mnt/funcd-deps/apps/<repo>:<version>`
  (one layout per repo), and `-f values/e2e.yaml` sets `registry: oci-layout:///mnt/funcd-deps/apps` ⇒ every image
  renders as `oci-layout:///mnt/funcd-deps/apps/<repo>:<version>@<digest>` with the lock's digest (reproducible
  packing gives the same digest), and the App deployed from it becomes `Ready`.
- `scenario: app-template-range` — the registry holds the `todo-stats` tags `1.0.0`, `1.0.3` and `2.0.0` ⇒ `funcdctl
  app lock ./app` records `stats` with `version: 1.0.3` and its digest; with `todo-stats:^3.0.0` it fails naming
  `stats` and `^3.0.0` and leaves `app.lock` unchanged; after `images.stats` becomes `todo-stats:^2.0.0`, `funcdctl
  app render ./app -f values/prod.yaml` fails naming `stats` and the stale lock and prints nothing, until `funcdctl
  app lock` runs again; then it renders `todo-stats:2.0.0@<digest>`.

## Scope

**In**: ranges in `images`; `app.lock` and `funcdctl app lock`; the pinned image value; the stale-lock and moved-tag
checks; `funcdctl push --template` and the `internal/artifact` functions; a ref source for `app render|deploy`.
**Out**: the rest of templates (ADR-0217: the layout, `app.yaml` keys, values, `when`, the `images` root and the
image-field refusal); images from two registries (a board card); a platform registry mapping (funcd redirects only
runtime images, `runtime.containerd.imagePrefix`); transfer speed (#820); plain-HTTP registries; server changes.

## Constraints & Decision drivers

What is pushed equals what git holds (push never rewrites a file, decider); one commit or one template version always
installs the same bytes; the server never pulls a template; reuse the site path (`internal/artifact/site.go`) and its
helpers; ranges use `Masterminds/semver/v3` (MIT); no new module. F121 is client-only, so the platform hold
(ADR-0206 Decision 6: the `hold.Gate` that `serve` passes to `funcd.WithHold`) has no runner to gate here.

## Alternatives considered

| Option | Lost because |
|---|---|
| Image refs in the install values | an installer could mix versions never tested together; only the registry is a value (decider) |
| The registry inside each image expression | versions spread across files, and pinning would rewrite the tag inside each expression |
| Pins written into a rewritten `app.yaml` at push (decided first, replaced the same day) | the pushed artifact would differ from git, and a deploy from a directory would stay unpinned |
| Ranges resolved at every render | two deploys of one commit could run different code |
| The lock records the registry it resolved at | the digest is the identity, and a copy in another registry keeps it; a registry value change would make the lock stale for no reason |
| Lenient tag parsing (`semver.NewVersion` accepts `v1` and `1.0`) | `v1.0.0` and `1.0.0` would be one version, and the lock could not name the tag it resolved |

## Decision

1. **Entries.** ADR-0217 Decision 2's entry `<repo>:<version>` becomes `<repo>:<spec>`, split at the first `:` (a
   repo carries no host). `<repo>` must pass oras' `Reference.ValidateRepository` (`registry/reference.go:202`), so
   `team/todo-api` is allowed. `<spec>` is exact when `semver.StrictNewVersion` parses it (the tag is that string),
   else a range when `semver.NewConstraint` parses it (`^1.0.0`, `~1.2.0`, `>=1.0.0 <2.0.0`, `||`; a partial `1.0`
   is the range 1.0.x); otherwise the entry is refused naming it. Also refused: a `v` before a version (`v1.0.0`,
   `^v1.0.0`, which the constraint grammar accepts, `constraints.go:202`; ADR-0217 refuses it in `version`), a `+`
   (build metadata: an OCI tag cannot hold it) and a digest. `Load` keeps each result in `Template.Parsed`.
2. **Lock.** `funcdctl app lock <dir> [-f values.yaml]…` loads the template, takes `registry` from `EvalRegistry`
   (Decision 4) and resolves each image in name order. An exact entry resolves `<registry>/<repo>:<tag>` to its
   manifest digest. A range lists the repo's tags, keeps those that `StrictNewVersion` parses (others, such as
   `latest` or `v1.0.0`, are skipped) and that the range accepts (a prerelease only when the range names one, the
   library default, `constraints.go:101`), and takes the highest. No match fails naming the image, the range and the
   repo. Any failure writes nothing; otherwise the command writes `<dir>/app.lock` through a temporary file and a
   rename, and prints one line per image (name, version, digest). Every entry is locked, whether a fragment uses it or
   not. Lock checks no artifact type; Site materialization does (`site.go:143-152`), `artifact.Pull` does not.
3. **The lock file** is YAML: `images.<name>` with `requested` (the entry as written), `version` (the tag) and
   `digest` (`sha256:` and 64 hex digits). It is decoded strictly (`sigs.k8s.io/yaml.UnmarshalStrict`, as
   `pkg/sdk/manifest.go:153`) and written with sorted keys, so the same resolution writes the same bytes. It is
   **stale** when its names differ from the `images` names or when an entry's `requested` differs from its `images`
   entry; the refusal names the first such image in name order and says to run `funcdctl app lock`. A registry change
   does not make it stale.
4. **Rendered value.** `Render` takes `${{ images.<name> }}` from `Template.Lock` and writes
   `<registry>/<repo>:<version>@<digest>`; it refuses a template with no lock or a stale one. Render, `lock`, the
   in-memory lock and the moved-tag check all take the registry from `EvalRegistry`: ADR-0217 Decisions 3-5 (merge and
   validate the values, evaluate `registry` in Select mode over `values` only, so a declared default applies), then,
   besides ADR-0217's checks, a refusal of a scheme other than `oci-layout://` (`file://`, `oci://`), a digest (`@`)
   or a tag: a `:` after the last `/` of an `oci-layout://` value (`parseLocalRef`'s rule) or after the first `/` of
   any other, as a host's `:` is a port (`registry.example:5000` passes). `lock` validates only the values `registry`
   reads, against their subschemas, so `funcdctl app lock ./app` needs no `-f` although the schema requires `host`
   (the design note's Pinning). Without `app.lock`, funcdctl resolves one in memory as `lock` does, prints each pick
   and a missing-lock note to stderr, and renders or deploys with it. The digest is honored by resolution, not by
   ADR-0035's `spec.imageDigest` (ADR-0097; `internal/function/function.go:2198-2206`), which ADR-0217 Decision 6
   refuses in a fragment: `OrasMaterializer.Resolve` resolves the reference (`internal/artifact/artifact.go:748-762`),
   oras drops the tag of `repo:tag@digest` (`registry/reference.go:119`) and `parseLocalRef` lets a digest win
   (`artifact.go:580-594`); a Site resolves the same way (`internal/site/reconcile.go:118-126`).
5. **Moved tag.** A render or deploy from a directory with a lock resolves each `<registry>/<repo>:<version>` once. A
   digest other than the locked one prints a warning to stderr naming the image and both digests, and says that
   `funcdctl app lock` would take the new one; an unreachable registry prints a warning that the check failed. Neither
   changes the output. A source that is a registry ref makes no check: the pushed lock is the record of that release.
6. **Push.** `funcdctl push --template <dir> <ref>` is exclusive with `--site`, `--schema`, `--runtime`, `--platform`
   and `--entry`. Before any write it refuses: a template that `Load` refuses (ADR-0217 Decision 1 refuses any path
   but `app.yaml`, `app.lock` and `resources/*.yaml`; `packDir` refuses a symlink); a missing or stale lock; a
   `version` with build metadata (ADR-0217 allows it, an OCI tag has no `+`); and a ref without a tag, with a digest,
   or whose tag is not `version`. It then packs the directory as it is (`packDir(op, dir, "")`,
   `internal/artifact/bundle.go:91`) into one `BundleTarMediaType` layer under
   `application/vnd.funcd.app-template.artifact.v1` with the reproducible manifest (`artifact.go:114-121`), so the
   digest is known before any write. It resolves the version tag at the target: a tag at another digest is refused as
   `fault.Conflict` naming the tag and both digests, and the same digest succeeds with no write. Otherwise it pushes,
   tags and prints `<ref>@<digest>`, as `PushSite` does (`site.go:31-67`). Push contacts no image registry.
7. **A registry ref as the source.** `app render` and `app deploy` take `<dir|ref>`: a directory when one exists at
   that path, otherwise a ref written as an `image` field is written (`registry/repo:tag`, with `@sha256:…`, or
   `oci-layout://<dir>[:<tag>]`; there is no `oci://` scheme); this replaces ADR-0217's "deploying from a registry
   ref is ADR-0218". funcdctl calls `ResolveTemplate`, which refuses another artifact type as `fault.Invalid` (as
   `assertSiteManifest`, `site.go:143-152`), prints `template <ref>@<digest>` to stderr, calls `PullTemplate` into a
   new `os.MkdirTemp` directory that it removes on return, and loads it; a pulled template without `app.lock` is
   refused. Nothing records which template made an App (the design note rejected template provenance).

## Temporary workarounds

- One registry per template: every image comes from `registry`. Exit: an ADR decides the two-registry board card.

## Contracts

```go
// internal/app/template (additive to ADR-0217)
type Template struct {
	// ADR-0217's fields: Name, Version, Registry, Images (the entries as written), ValuesSchema, When, Files.
	Parsed map[string]Image       // name → ParseImage(Images[name]); Load fills it
	Lock   map[string]LockedImage // app.lock; nil when the directory has none
}
type Image struct {
	Repo  string              // passes Reference.ValidateRepository
	Exact string              // the tag when the spec is a version; "" for a range
	Range *semver.Constraints // nil when Exact is set
}
type LockedImage struct {
	Requested string `json:"requested"` // the images entry as written; a mismatch makes the lock stale
	Version   string `json:"version"`   // the tag it resolved to
	Digest    string `json:"digest"`    // sha256:<64 hex>
}
type ImageResolver interface {
	Tags(ctx context.Context, repo string) ([]string, error) // every tag of <registry>/<repo>
	Digest(ctx context.Context, ref string) (string, error)  // the manifest digest of <registry>/<repo>:<tag>
}

func ParseImage(entry string) (Image, error) // Decision 1; Load calls it per entry
func EvalRegistry(t *Template, values []json.RawMessage, lock bool) (string, error) // Decision 4; true for app lock
func Lock(ctx context.Context, t *Template, registry string, r ImageResolver) (map[string]LockedImage, error)
func CheckLock(t *Template) error                                   // Decision 3; a nil lock is an error
func ReadLock(dir string) (map[string]LockedImage, error)           // nil, nil when app.lock is absent
func WriteLock(dir string, lock map[string]LockedImage) error       // temporary file and rename, sorted keys
func ImageRef(registry, repo string, l LockedImage) (string, error) // Decision 4's value
func Moved(ctx context.Context, t *Template, registry string, r ImageResolver) []string // Decision 5's warnings
func CheckPush(dir string) (*Template, error) // Decision 6: Load, a fresh lock, no build metadata

// internal/artifact/template.go
const AppTemplateArtifactType = "application/vnd.funcd.app-template.artifact.v1"

func PushTemplate(ctx context.Context, ref, dir, version string) (digest string, err error) // tag must be version
func ResolveTemplate(ctx context.Context, ref string) (digest string, err error)
func PullTemplate(ctx context.Context, ref, digest, dir string) error // fetches the manifest by digest
func ListTags(ctx context.Context, repo string) ([]string, error)   // registry.Tags on resolveReadTarget's target
func ResolveDigest(ctx context.Context, ref string) (string, error) // OrasMaterializer.Resolve delegates here

type TagResolver struct{} // satisfies template.ImageResolver

func (TagResolver) Tags(ctx context.Context, repo string) ([]string, error) // ListTags
func (TagResolver) Digest(ctx context.Context, ref string) (string, error)  // ResolveDigest
```

`ListTags` collects the paginated listing with oras' `registry.Tags` (`registry/repository.go:132`), which both
`remote.Repository.Tags` (`registry/remote/repository.go:399`) and the read path's `oci.ReadOnlyStore.Tags`
(`content/oci/readonlyoci.go:127`) satisfy; the design note cites `oci.go:350`, the writable store.

| Refusal (`fault.Invalid` unless noted; funcdctl exits 1) | Message names |
|---|---|
| bad entry | the image and the entry |
| no tag satisfies a range (`fault.NotFound`) | the image, the range and `<registry>/<repo>` |
| stale lock | the image, its entry, the locked `requested`, and `funcdctl app lock` |
| no lock at push or in a pulled template | `app.lock` |
| tag other than `version`, no tag, a digest; build metadata | the tag and `version` |
| version tag already at another digest (`fault.Conflict`) | the tag and both digests |
| another artifact type | the digest, its type and `AppTemplateArtifactType` |
| bad registry value | `registry` and its value |

No config key, API field or server reason is added.

| Consumes | Exposes |
|---|---|
| a template directory or ref · ADR-0217's `Load`, `Render`, values and deploy write (ADR-0210's `If-Match` rules) · OCI registries and layouts (Docker credentials, as `resolveTarget`, `artifact.go:416-442`) | `app.lock` · `funcdctl app lock` · `funcdctl push --template` · a ref source for `app render` and `app deploy` · the artifact type · `ListTags`, `ResolveDigest`, `TagResolver` |

## Implementation plan

1. No `go.mod` change: ADR-0217 Decision 10 makes `Masterminds/semver/v3` direct.
2. `internal/app/template`: `images.go` (`ParseImage`, `EvalRegistry`, `ImageRef`), `lock.go` (`Lock`, `CheckLock`,
   `ReadLock`, `WriteLock`, `Moved`), `push.go` (`CheckPush`); `Load` accepts `app.lock` and fills `Parsed` and
   `Lock`; `Render` takes the registry from `EvalRegistry` and `images` from `Lock`.
3. `internal/artifact/template.go` with the functions above; `OrasMaterializer.Resolve` calls `ResolveDigest`.
4. `cmd/funcdctl`: `--template` beside `--site` (`cli.go:222-298`); `app lock` in `app.go`, added to `Short` beside
   other ADRs' subcommands; the source rule, the in-memory lock and the moved-tag warning in `app render|deploy`.
5. Tests:
   - `internal/app/template`: `ParseImage` table (exact, `^`, `~`, a space AND, `||`, `1.0` a range, a path repo; no
     `:`, a bad repo, a bad spec, `v1.0.0`, `^v1.0.0`, `1.0.0+b1` and a digest refused); `EvalRegistry`
     (`testdata/app-todo/` requires `host`: no `-f` gives the default `registry.example` with `lock` and refuses
     naming `host` without it, `-f` overrides it, `registry.example:5000` accepted; `registry.example/team:1`, a
     digest, `file://`, `oci://` and an `app` or `images` root refused); `Lock` on a fake resolver (the highest match;
     `latest`, `v1.0.0` and `1.1.0-rc.1` skipped, a prerelease kept when the range names one; an exact entry resolved
     without a tag list; no match; nothing written on failure); `CheckLock` (a changed `requested`, a missing name, an
     extra name; a registry change is not stale); `ReadLock` strict (an unknown key, a bad digest); `WriteLock` twice
     gives the same bytes; `ImageRef` forms; `CheckPush` refusals (no lock, a stale lock, build metadata).
   - `internal/artifact/template_test.go`, as `site_test.go:36,78,95`: a layout round trip whose pulled files equal
     the source; the same directory twice gives one digest; `ResolveTemplate` refuses a site and a function artifact;
     `PullTemplate` refuses a digest mismatch and unpacks traversal-safely; `PushTemplate`'s tag refusals create no
     layout; a push over the version tag at another digest is `fault.Conflict`, at the same digest it writes nothing;
     `ListTags` and `ResolveDigest` on a layout.
   - ADR-0217's tests: `testdata/app-todo/` gains an `app.lock`, and the golden App of `app-render-matches` its
     digests; the range case of `app-render-refuses` becomes a bad spec (`todo-stats:latest`); `app-deploy-waits`
     deploys with a lock; Load's `app.lock` and range refusals become `ParseImage` and `ReadLock` tests.
   - `cmd/funcdctl`: flag exclusivity, `app lock` output and file, warnings on stderr only; a copy of
     `testdata/app-todo/` without `app.lock`, with `values/prod.yaml` and a layout `registry`, renders what a locked
     copy renders, prints each pick and the missing-lock note to stderr, and writes no `app.lock`.
   - `TestScenarioAppTemplatePinned` and `TestScenarioAppRegistryValue` in `pkg/funcd` (e2e tag; short temporary
     layouts replace `registry.example` and `/mnt/funcd-deps/apps`), and `TestScenarioAppTemplateRange` in
     `cmd/funcdctl`.
6. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; no `go.mod` change.

## Review checklist

- [ ] `PushTemplate` packs with `packDir(op, dir, "")` and `reproducible`, one `BundleTarMediaType` layer, under
      `AppTemplateArtifactType`; it refuses a missing tag, a digest, a tag other than `version`, or a version tag at
      another digest (`fault.Conflict`) before any write, and writes nothing for the same digest.
- [ ] `ParseImage` refuses a `v` before a version and build metadata, in an exact entry or a range.
- [ ] `CheckPush` runs `Load` and refuses a missing or stale lock and build metadata; a pushed template holds only
      `app.yaml`, `app.lock` and `resources/*.yaml`, and its lock covers every image.
- [ ] `Lock` takes the highest `StrictNewVersion` tag the range accepts, fails naming the image and the range when
      none does, writes nothing on a failure, and writes the same bytes for the same resolution.
- [ ] Render, deploy and push refuse a stale lock naming the image; every rendered image ends in `@sha256:`; no
      rendered Function sets `imageDigest`; every digest comes from `app.lock`.
- [ ] Only a directory source checks for a moved tag; every warning goes to stderr, never into the rendered App.
- [ ] `ResolveTemplate` refuses a function and a site artifact; `PullTemplate` requires a digest and fetches by it.
- [ ] `OrasMaterializer.Resolve` delegates to `ResolveDigest`, and the ADR-0035 tests pass unchanged.
- [ ] `grep -rn PullTemplate --include='*.go'` finds callers only in `cmd/funcdctl` and tests.

## Consequences

**Positive**: one commit or one template version installs the same bytes, whatever the tags do later; every App rendered
from a template carries digests, so an AppRevision rollback runs the same bytes again (ADR-0200's workaround ends for
these Apps), and so does an App restored from a metastore backup (ADR-0206); the pushed artifact is the git content.
**Negative**: `lock` and a directory render contact the registry; only `lock` takes a newer release that a range
accepts, and render does not report one; a remote registry over plain HTTP is not reachable (`resolveTarget` never
sets `repo.PlainHTTP`, `artifact.go:434-441`); App lanes push one layout per repo, unlike today's single
`/mnt/funcd-deps/registry` (`scripts/lanes.yaml:16`).
**Risks accepted**: the tag beside a digest is never checked; OCI has no conditional tag, so two concurrent pushes of
one version can race past the existing-tag check (a deploy of `…@sha256:` pins the template); the template layer has no
size cap, as a site's (`untarBundle`, `bundle.go:334`); a digest missing at the chosen registry fails at the Function's
Revision stamp (ADR-0035's `unresolvable-ref-fails`), not at render; a Function image that names a site or template
artifact passes lock, render and the Revision stamp, and fails only when the function starts; templates and images in a
registry are outside funcd's backup and restore (ADR-0206 Decision 5).

## Open questions

1. Plain-HTTP registries and a size cap on the template layer → a follow-up issue.
2. A function artifact type check in `artifact.Pull` (`artifact.go:49`) → a follow-up issue (fix pipeline).
3. The `scripts/lanes.yaml` push form for App lanes (one layout per repo) → the first App Lima lane.

## References

- [App design note](../reports/app-design.md) (template section, decisions table, scenarios, contracts) ·
  [FEAT-0010](../feat/0010-feat-apps.md) (F121) · `internal/artifact/site.go:26-152` · `artifact.go:53`, `:530-549`
  · `bundle.go:91-131` · `cmd/funcdctl/app.go:20-29`; other lines are cited where used.
- oras-go v2.6.1 (lines cited where used) · Masterminds/semver v3.5.0: `constraints.go:17-21`, `:41`, `:101`;
  `version.go:98` · Helm `Chart.lock`, npm `package-lock.json`.
