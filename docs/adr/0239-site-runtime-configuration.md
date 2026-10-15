# ADR-0239: Runtime configuration of a Site

- **Status**: Implemented (2026-10-15, review passed). Accepted 2026-10-15 under the decider's adr-batch delegation,
  after two judge rounds; the decider chose `config.json`.
- **Amended before merge** (2026-10-16, at the decider's request): Decision 10, an opt-in `<base href>` for path
  mounts. Judged 2026-10-16; the decider put the flag on `ingress` and kept sub-directory pages. The branch merges
  after Decision 10's review passes. Decision 10 reviewed 2026-10-16, pass.
- **Date**: 2026-10-15
- **Deciders**: green-0-rabbit
- **Tags**: site, edge, static, config, app-templates
- **Realizes**: [FEAT-0003/F152](../feat/0003-feat-data-platform.md) (runtime configuration of a Site)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0239` back-link: [ADR-0139](0139-site-declarative-static-web-app.md)
  Decisions 2 and 8 (a third rule shape, the copy as the index, a root `_runtime` entry refused) ·
  [ADR-0213](0213-app-config-and-secret-declarations.md) Decisions 3 and 5 (a Site's `runtimeConfig.configMap` is
  repointed and counts as a use) · [ADR-0140](0140-path-mounted-site.md)'s rejected `<base href>` alternative and its
  no-rewrite rule · [ADR-0120](0120-static-asset-serving-route.md) §2–§3 (a nested index's file name).
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) ·
  [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) · [ADR-0114](0114-edge-observability-shaping.md) ·
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) ·
  [ADR-0217](0217-app-templates-values-and-rendering.md) · [ADR-0218](0218-app-template-images-lock-and-push.md) ·
  [ADR-0093](0093-function-configmap-consumption.md)

## Context & Need

A Site serves one prebuilt frontend bundle, pinned by digest. A team builds that bundle once and deploys it to several
environments; only configuration differs between them, first the address of the API the frontend calls. When that API
is a funcd Function, a Route on the same host sends `/api` to it and nothing differs per environment. When the API is
outside funcd, or the frontend needs other per-deployment values (an analytics id, feature flags), funcd offers no
clean path today:

- `SiteSpec` has no field for configuration (`api/types/v1alpha1/site.go:21-37`), and funcd serves a bundle's bytes
  unmodified (ADR-0120; ADR-0140 rejected injecting a `<base href>`);
- a user-authored Route cannot point at an outside URL: the edge's `Upstream` is node-private (`internal/edge/router/router.go:51-56`,
  ADR-0138), and CORS is process-wide (`server.shaping.cors`; per-Route CORS is ADR-0114's V2 item).

A bundle mounted under a path has a second problem. On a direct deep link such as `/bi/devices/42`, or at `/bi`
without its slash, the browser resolves the index's relative URLs (`assets/app.js`) against that page URL, so they
miss and the app never starts. ADR-0140 asks for a build per mount, which a team deploying one build to several mounts
cannot do.

## Scenarios

Every "within 5 s" is measured from the change that the scenario names.

- `site-config-two-environments` — *Given* one bundle pushed once and an App template whose Site sets
  `runtimeConfig.values.API_URL: ${{ values.api.url }}`, *When* it is deployed in `staging` with
  `api.url: https://api.staging.example` and in `prod` with `https://api.example`, *Then* each Site's `config.json`
  holds its own `API_URL`, and both Sites serve the same bundle digest and bytes.
- `site-config-revalidates` — *When* `config.json` is fetched, *Then* it carries `Cache-Control: no-cache`,
  `Content-Type: application/json` and an ETag, and a request with that ETag in `If-None-Match` answers `304`.
- `site-config-ahead-of-spa` — *Given* `spa: true`, *Then* `config.json` answers the generated file, never the index.
- `site-config-path-mounted` — *Given* `ingress.path: /bi`, *Then* the file is at `/bi/config.json`, and the Site
  serves no `/config.json`.
- `site-config-configmap-change` — *Given* a Site with `runtimeConfig.configMap: web-config`, *When* a key of
  `web-config` changes, *Then* `config.json` holds the new value with a new ETag within 5 s, and the bundle digest is
  unchanged.
- `site-config-configmap-missing` — *Given* a Site serving its `config.json`, *When* its `runtimeConfig` names a
  ConfigMap that does not exist, or that ConfigMap is deleted, *Then* within 5 s the Site is `Ready=False`
  `ConfigMapNotFound` while its bundle and its last `config.json` keep serving; within 5 s of the ConfigMap's creation
  the file holds its keys and the Site is Ready.
- `site-config-app-configmap` — *Given* an App that defines ConfigMap `web-config` and a Site part whose
  `runtimeConfig.configMap` names it, *Then* `config.json` holds that ConfigMap's keys; *When* the App changes the
  ConfigMap's data, *Then* within 5 s `config.json` holds the new keys, and the App prunes no ConfigMap a Site names.
- `site-config-removed` — *When* `runtimeConfig` is removed, *Then* within 5 s `/config.json` is no longer the generated
  file and the generated object is deleted.
- `site-config-shadows-bundle-file` — *Given* a bundle that ships its own root `config.json`, *Then* `/config.json`
  answers the generated file.
- `site-config-route-conflict` — *Given* another Route on the Site's host that claims `GET /config.json`, *When* the
  Site sets `runtimeConfig`, *Then* the later Route in (namespace, name) order is `RouteConflict` (ADR-0176) and, if it
  is the Site's, the Site reports `RouteNotReady`.
- `site-config-shared-prefix` — *Given* two Sites on one Bucket and prefix with different `runtimeConfig`, *Then*
  each serves its own `config.json`, and removing `runtimeConfig` from one leaves the other's file.
- `site-config-base-href` — *Given* a Site with `spa: true` at `ingress.path: /bi` with `ingress.baseHref: true`, and
  a bundle whose index loads `assets/app.js` and carries `<base href="/">`, *When* a browser opens `/bi/devices/42`,
  *Then* the index it gets carries one `<base href="/bi/">` and `Cache-Control: no-cache`, so `assets/app.js` and
  `config.json` resolve under `/bi/`.
- `site-config-one-digest-two-mounts` — *Given* one bundle digest on two Sites of one Bucket and prefix with
  `spa: true` and `baseHref`, at `/` and at `/bi`, *Then* a direct deep link on each serves an index with its own base,
  and no bundle file changes.
- `site-config-base-href-paths` — *Given* such a Site without `runtimeConfig`, and a bundle with its own
  `docs/index.html` and `config.json`, *Then* `/bi/docs/` and `/bi/config.json` answer the bundle's files; *When*
  `ingress.path` changes to `/b2`, *Then* within 5 s a deep link carries `<base href="/b2/">`.
- `site-config-runtime-reserved` — *Given* a Site without `baseHref`, *When* it deploys a bundle with a root `_runtime`
  entry, *Then* it is `Ready=False` `MaterializeFailed`, naming the entry, and the previous digest keeps serving.
- `site-config-refused` — *When* a Site sets a key that is not an identifier, an `ingress.rules` path that equals the
  file's path with or without a trailing slash, or `values` that render over the cap, *Then* the write is refused with
  `400`, naming the key, the rule or the size.

## Scope

**In**: a `runtimeConfig` field on `SiteSpec`; the generated `config.json`, its path, content, cache policy, route claim
and order against the bundle; values from the spec or one ConfigMap, App-defined ones included; validation, failures
and removal; the ban on secrets; use from App templates; an opt-in `ingress.baseHref` for path mounts.

**Out**: a Route backend to an outside URL (Alternative B); per-Route CORS (ADR-0114's V2 item); values per user or per
request; server-side rendering; changing any bundle file; a script form of the file.

## Constraints & Decision drivers

- One bundle digest for every environment: configuration never changes the bundle (ADR-0120, ADR-0139).
- A configuration change must reach browsers without a rebuild, a new digest or a stale cache.
- Reuse what exists: static serving and its cache tiers, App template values, ConfigMaps, the Route matcher. No new
  Route field; the edge changes only how a nested index's file name is used (Decision 10).
- No new path from the edge to the outside world.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A generated `config.json` beside the bundle** | No bundle change; declared in the Site, so it flows through App templates, AppRevision history and rollback; served by the existing static handler | The frontend fetches one small file before it starts; the file's path is one more route claim (Decision 4) | **Chosen** |
| **B. A Route backend to an outside URL** | Same origin for an outside API, so no CORS | The daemon would open connections for a tenant outside EgressPolicy, which governs workers only; a tenant could reach node-private listeners or metadata addresses (SSRF); the funcd origin's cookies and `Authorization` header would reach a third party unless stripped; it solves only the API address | Rejected here; a separate ADR if a same-origin proxy is wanted |
| **C. The workaround that exists**: an uploaded file served through `ingress.rules` | Works today | Not declared, so App templates, history and rollback miss it; a separate deploy step with a write credential; a non-index, non-hashed file is cached `max-age=300` (ADR-0120 §3), so a change can stay stale for 5 minutes | Kept as the temporary workaround |
| D. `config.js` setting `window.RUNTIME_CONFIG` | Synchronous, through a script tag | funcd would generate a script, and a global name becomes part of the contract | Rejected by the decider (2026-10-15) |
| E. A Function serving `config.json` from its env (ADR-0093) | Works today | A cold start on the fetch the app waits for; one more workload per environment; a ConfigMap change needs a new revision | Rejected |
| F. Build-time values per environment | No runtime file | One build per environment | Rejected |
| **G. Deep links: a copy of the index with `<base href>`, in the digest folder** | One bundle at any mount; a standard Angular reads already; a stored object the handler serves unchanged; switches with the Route | `_runtime` reserved in every bundle; the bundle needs relative URLs and `document.baseURI`; a nested index's file name serves sub-directories | **Chosen** (Decision 10) |
| H. Deep links: a Route field naming the index served at `/` and on the SPA fallback, kept outside the digest folder | No reserved name in bundles | A new field on a user-facing kind; a key outside the rule's prefix, an exception to ADR-0120's prefix containment; still one key per digest to switch with the Route | Rejected |
| I. Deep links: retry a missed path without its leading segments | No HTML change | Ambiguous (the first suffix that exists wins), one lookup per segment, an asset URL per deep path, and `import.meta.url` yields the wrong base | Rejected |
| J. Deep links: redirect to the mount root, the path in the query | No HTML change | A funcd-specific boot protocol the app must implement, and a visible redirect per deep link, where `<base>` is a standard | Rejected |
| K. Deep links: a host per Site | No change: a bundle built for `/` works at any host's root | No path mount | The advice when DNS is available |

## Decision

1. **`runtimeConfig` on a Site.** `spec.runtimeConfig` holds `values` (a map of strings) and `configMap` (the name of
   one ConfigMap of the Site's namespace). The keys of both are merged; a key in `values` wins.
2. **Format: `config.json`** (the decider's choice): one JSON object of the keys with their string values, in key
   order, from a JSON encoder. The frontend fetches it from the base it is built for (ADR-0140), or relative to
   `document.baseURI` under Decision 10; a URL relative to the page breaks under a deep link.
3. **Served as a static file.** The reconciler writes the rendered body to `<prefix>/_runtime/<site>/config.json` in
   the Site's Bucket, `<site>` being the Site's name, when it differs from the stored object, so two Sites on one prefix
   never share it. It compiles one more rule on the owned Route **exactly when `runtimeConfig` is set and that object
   exists**: path `<EffectivePath>/config.json` (`/config.json` when the path is `/`), `pathType: Exact`, methods `GET`
   and `HEAD`, a static backend over `<prefix>/_runtime/<site>/` with `index: config.json` and the bundle's `public`
   flag. The static handler serves it as an index: `no-cache`, the weak ETag from size and modification time,
   `304` on a match, `application/json` (ADR-0120 §3). `_runtime` is never a digest slug, so the serving-digest record
   ignores it.
4. **Order and claims.** The matcher picks the longest path first (ADR-0110), so neither the bundle's rule nor its SPA
   fallback answers for the file, and the file moves with `ingress.path`; a bundle's own root `config.json` is shadowed.
   The rule is a claim of the owned Route like every other rule (ADR-0139 §8): a clash with another Route resolves as
   ADR-0176 says, and when the Site's Route loses, the Site shows `RouteNotReady`. An `ingress.rules` path equal to the
   file's path, trailing slash trimmed, is refused while `runtimeConfig` is set.
5. **App templates and App ConfigMaps.** A template sets `runtimeConfig.values.<KEY>: ${{ values.<path> }}`; each value
   must render as a string (ADR-0217 Decision 5 keeps types), so a flag is written `"true"`. When an App defines the
   ConfigMap a Site part names, the App points the Site at its stored name, as it does a Function's config (ADR-0213
   Decision 3), and a Site counts as a use of the ConfigMap it names, so prune and the garbage collector keep it
   (ADR-0213 Decision 5).
6. **No secrets.** `runtimeConfig` names no Secret; the file is served under the Site's Route to everyone who can load
   the app. The field's `doc` tag, which becomes its OpenAPI description, and the example's README say so.
7. **Validation, failures and removal.** A key passes the ConfigMap key rule (`validateEnvKey`); `configMap` is a valid
   object name; the rendered file is at most 32 KiB, a fixed constant shared by admission, the reconciler and the
   tests. Admission refuses with `400` a bad key, a clashing rule, or `values` that alone render over the cap. The
   reconciler renders after the bundle's index is asserted and before the Route. A missing ConfigMap
   (`ConfigMapNotFound`), a file over the cap or a Put the Bucket refuses for its size (`RuntimeConfigTooLarge`)
   rewrites nothing: the bundle rule still rolls out, the config rule stays exactly when the last good object exists,
   and the Site is `Ready=False` with that reason; a Route reason (`RouteNotReady`) wins, since it decides whether
   anything is served. A failed bundle deploy returns before the render, so the previous file keeps serving. Removing
   `runtimeConfig` deletes the object, then drops the rule, while the current Route carries it: the Route is the record.
8. **An outside API is called directly**, with its own CORS headers for the Site's origin.
9. **A `values` edit is a new generation**, which re-resolves a tag image (ADR-0139 Decision 2). Pin the image by digest,
   as App templates do (ADR-0218), or keep values that change often in the ConfigMap, whose change re-resolves nothing.
10. **A base for path mounts (opt-in).** With `spec.ingress.baseHref: true` the Site serves, at `/` and as its SPA
    fallback, a copy of the bundle's index carrying its mount as `<base href>`: `<EffectivePath>/` (`/` at the root),
    percent-encoded as a URL path, then HTML-escaped. Every `<base>` element of the index is removed; one `<base>`,
    keeping the first one's `target`, goes first in `<head>` (after a `<meta charset>` that opens it), else right after
    `<html>`, else before the first element; every other byte stays. The reconciler writes the copy into the digest
    folder at `_runtime/<site>/<h>/<index>`, `<h>` the first 12 hex digits of the mount's SHA-256, when it differs,
    after the bundle is complete and before the Route, and sets the bundle rule's `index` to it, so a new digest or
    mount switches with the Route. A copy the Bucket refuses fails the deploy like a bundle object; `status.objects`
    and `status.bytes` leave `_runtime/` out. No bundle file changes: funcd reserves `_runtime` at the root of every
    bundle and refuses one that holds it (`MaterializeFailed`), checked when a digest is unpacked. A nested index's file
    name serves the cache tier (`no-cache`) and sub-directories (`docs/` serves `docs/index.html`). The bundle uses
    relative URLs and reads its base from `document.baseURI`; the example's README shows how.

## Temporary workarounds

- Alternative C until this lands. Exit: this ADR is implemented.

## Contracts

```go
// SiteSpec gains (api/types/v1alpha1/site.go):

	// RuntimeConfig makes the Site serve one generated config.json beside its bundle (ADR-0239). Not the function
	// runtime.
	RuntimeConfig *SiteRuntimeConfig `json:"runtimeConfig,omitempty" doc:"Values each deployment sets without a rebuild, served as config.json beside the bundle. The file is public: never put a secret in it."`

// SiteIngress gains:

	// BaseHref makes the Site serve a copy of its index whose <base href> is its mount (Decision 10).
	BaseHref bool `json:"baseHref,omitempty"`

// SiteRuntimeConfig holds the members of the generated config.json.
type SiteRuntimeConfig struct {
	// Values are keys and their string values; a key here wins over the same key from ConfigMap.
	Values map[string]string `json:"values,omitempty"`
	// ConfigMap names a ConfigMap of the Site's namespace whose data adds keys.
	ConfigMap ObjectName `json:"configMap,omitempty"`
}

const (
	SiteRuntimeConfigFile     = "config.json" // the generated file's name
	SiteRuntimeConfigMaxBytes = 32 << 10      // the rendered file's cap
)

func (s *Site) RuntimeConfigPath() string                      // <EffectivePath>/config.json
func RenderSiteRuntimeConfig(members map[string]string) []byte // the body: json.Marshal of the members
```

| Consumes | Writes | Exposes |
|---|---|---|
| `spec.runtimeConfig`; the named ConfigMap, watched so a change re-runs the Sites that name it; with `ingress.baseHref`, the bundle's index | object `<prefix>/_runtime/<site>/config.json`; the copy `<prefix>/<digest-slug>/_runtime/<site>/<h>/<index>`; one rule on the owned Route | `GET`/`HEAD` `<EffectivePath>/config.json`; the copy as the bundle rule's index; `Ready` reasons `ConfigMapNotFound`, `RuntimeConfigTooLarge` |

## Implementation plan

- `api/types/v1alpha1`: the types, constants and functions above, the `Validate` rules, the `Path` comment; `entries()`
  repoints a Site part's `runtimeConfig.configMap` on a copy, leaving the declared spec untouched; unit tests.
- `internal/gc/gc.go`: a Site whose `runtimeConfig.configMap` names a ConfigMap is one of its users.
- `internal/site`: render, write before the Route, compile the rule from the object's existence, delete then drop on
  removal, the two reasons, `MapConfigMap`; the copy with the `golang.org/x/net/html` tokenizer (BSD-3-Clause, already
  in `go.mod`), the reserved entry, status counts without `_runtime/`; unit tests.
- `internal/edge/static`: a nested index's file name for the cache tier and sub-directories; `pkg/funcd/funcd.go`:
  watch ConfigMaps with `MapConfigMap`.
- Go e2e tests in `pkg/funcd`, `t.Parallel()`, covering every Scenario by name.
- The app-todo lane (chosen with the decider) deploys the `site-config` example of pyvvo/funcd-functions into `staging`
  and `prod` and at a path mount, from a funcd-functions release funcd takes with `go get`.
- The back-links in ADR-0120, ADR-0139, ADR-0140 and ADR-0213, the feat rows, the blueprint's Site sentence, OpenAPI.
- Done when: `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, and the lane with the new use case passes on real containerd.

## Review checklist

- [ ] No bundle byte changes; both Sites in `site-config-two-environments` serve one digest.
- [ ] The body comes from a JSON encoder; string values only; the cap is one shared constant.
- [ ] The key is per Site; the rule is `Exact`, `GET`/`HEAD`, compiled exactly when `runtimeConfig` is set and the
  object exists, which is written first; a failure rewrites nothing; removal deletes the object before the rule.
- [ ] An App-defined ConfigMap is repointed for a Site part and counted as used by it.
- [ ] `SiteRuntimeConfig` has no Secret reference; no Route field was added; the edge changes only how a nested
  index's file name is used.
- [ ] With `baseHref`, the copy carries exactly one `<base>`, lives under `_runtime/<site>/<h>/` in the digest folder
  and switches with the Route; sub-directories serve their own index; a root `_runtime` entry is refused for every Site.

## Consequences

- **Positive**: one bundle for every environment and, with `baseHref`, every mount; values versioned and rolled back
  with the App.
- **Negative**: one more request before the app starts; the file's path is one more claim that can conflict; a bundle's
  own root `config.json` is shadowed; `_runtime` is reserved in every bundle, also for Sites without `baseHref`. With
  `baseHref`, `#anchor` and `?query` links resolve against the mount, a CSP header with `base-uri 'none'` blocks the
  element, and the bundle rule also serves the other Sites' copies of a shared folder (same namespace and bundle).
- **Risks accepted**: a team may still put a secret in a value; the `doc` tag and the README are the guard. On an
  `s3://` substrate, two equal-size rewrites within one second can keep one ETag until the next change.

## Open questions

- A same-origin proxy to an outside API (Alternative B): a separate ADR, if wanted.

## References

- Request from the funcd-frontend `fleet` work (2026-10-15 and 2026-10-16): one build, per-deployment values, any mount.
