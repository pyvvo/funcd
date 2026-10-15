# ADR-0239: Runtime configuration of a Site

- **Status**: Accepted (2026-10-15, self-accepted under the decider's adr-batch delegation). The decider chose the
  `config.json` format and asked for a new use case in an existing Sites lane on 2026-10-15. Judged twice: round 1 asked for App-defined ConfigMaps, the route claim, the
  failure order and the superseded decisions; round 2 for a per-Site key and a bundler-safe fetch rule. All are folded
  below.
- **Date**: 2026-10-15
- **Deciders**: green-0-rabbit
- **Tags**: site, edge, static, config, app-templates
- **Realizes**: [FEAT-0003/F152](../feat/0003-feat-data-platform.md) (runtime configuration of a Site)
- **Supersedes (in part)**, each keeping its status with a `Superseded in part by: ADR-0239` back-link at acceptance:
  [ADR-0139](0139-site-declarative-static-web-app.md) Decision 8 (a third rule shape on the owned Route) ·
  [ADR-0213](0213-app-config-and-secret-declarations.md) Decisions 3 and 5 (a Site's `runtimeConfig.configMap` is
  repointed to a defined ConfigMap's stored name, and counts as a use of it).
- **Relates to**: [ADR-0120](0120-static-asset-serving-route.md) (static serving, its cache tiers and its ban on
  transforms) · [ADR-0140](0140-path-mounted-site.md) (`ingress.path`) ·
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) and [ADR-0176](0176-one-collision-rule-for-every-edge-source.md)
  (the matcher and route claims) · [ADR-0114](0114-edge-observability-shaping.md) (process-wide CORS) ·
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) (the node-private `Upstream`) ·
  [ADR-0217](0217-app-templates-values-and-rendering.md) and [ADR-0218](0218-app-template-images-lock-and-push.md)
  (App template values, pinned images) · [ADR-0093](0093-function-configmap-consumption.md) (ConfigMaps are
  non-sensitive)

## Context & Need

A Site serves one prebuilt frontend bundle, pinned by digest. A team builds that bundle once and deploys it to several
environments; only configuration differs between them, first the address of the API the frontend calls. When that API
is a funcd Function, a Route on the same host sends `/api` to it and nothing differs per environment. When the API is
outside funcd, or the frontend needs other per-deployment values (an analytics id, feature flags), funcd offers no
clean path today:

- `SiteSpec` has no field for configuration (`api/types/v1alpha1/site.go:21-37`);
- funcd serves a bundle's bytes unmodified (ADR-0120 rules out on-the-fly transforms, ADR-0140 rejected injecting a
  `<base href>` for the same reason);
- a user-authored Route cannot point at an outside URL: the edge's `Upstream` is node-private and set only by internal
  reconcilers (`internal/edge/router/router.go:51-56`, ADR-0138);
- CORS is process-wide (`server.shaping.cors`); per-Route CORS is deferred to V2 (ADR-0114).

This ADR gives a Site one platform-generated `config.json` beside the bundle, from values in the Site's spec or a
ConfigMap, served by the existing static handler, with no bundle file changed.

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
  ConfigMap that does not exist, *Then* within 5 s the Site is `Ready=False` reason `ConfigMapNotFound` while its bundle
  and its last `config.json` keep serving, and within 5 s of the ConfigMap's creation the file holds its keys and the
  Site is Ready.
- `site-config-configmap-deleted` — *When* the named ConfigMap is deleted, *Then* within 5 s the Site is `Ready=False`
  `ConfigMapNotFound`, and its last `config.json` keeps serving.
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
- `site-config-refused` — *When* a Site sets a key that is not an identifier, an `ingress.rules` path that equals the
  file's path with or without a trailing slash, or `values` that render over the cap, *Then* the write is refused with
  `400`, naming the key, the rule or the size.

## Scope

**In**: a `runtimeConfig` field on `SiteSpec`; the generated `config.json`, its path, content, cache policy, route claim
and order against the bundle; values from the spec or one ConfigMap, App-defined ones included; validation, failures
and removal; the ban on secrets; use from App templates.

**Out**: a Route backend to an outside URL (Alternative B); per-Route CORS (ADR-0114's V2 item); values per user or per
request; server-side rendering; changing any bundle file; a script form of the file.

## Constraints & Decision drivers

- One bundle digest for every environment: configuration never changes the bundle (ADR-0120, ADR-0139).
- The file is public to everyone who can load the app, so it can never carry a secret.
- A configuration change must reach browsers without a rebuild, a new digest or a stale cache.
- Reuse what exists: static serving and its cache tiers, App template values, ConfigMaps, the Route matcher. No new edge
  code and no new Route field.
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

## Decision

1. **`runtimeConfig` on a Site.** `spec.runtimeConfig` holds `values` (a map of strings) and `configMap` (the name of
   one ConfigMap of the Site's namespace). The keys of both are merged; a key in `values` wins.
2. **Format: `config.json`** (the decider's choice): one JSON object of the keys with their string values, in key
   order, from a JSON encoder. The frontend fetches the absolute URL built from the base path the bundle is built for
   (ADR-0140); a URL relative to the page breaks under a deep link. Framework snippets live in the example's README.
3. **Served as a static file.** The reconciler writes the rendered body to `<prefix>/_runtime/<site>/config.json` in
   the Site's Bucket, `<site>` being the Site's name, when it differs from the stored object, so two Sites on one prefix
   never share it. It compiles one more rule on the owned Route **exactly when `runtimeConfig` is set and that object
   exists**: path `<EffectivePath>/config.json` (`/config.json` when the path is `/`), `pathType: Exact`, methods `GET`
   and `HEAD`, a static backend over `<prefix>/_runtime/<site>/` with `index: config.json` and the bundle's `public`
   flag. The static handler serves it as an index: `no-cache`, the weak ETag from size and modification time,
   `304` on a match, `application/json` (ADR-0120 §3). On an `s3://` substrate the modification time has whole-second
   precision, so two rewrites of equal size within one second can keep one ETag until the next change. `_runtime/<site>`
   is never a digest slug, so the serving-digest record ignores it.
4. **Order and claims.** The matcher picks the longest path first (ADR-0110), so neither the bundle's rule nor its SPA
   fallback answers for the file, and the file moves with `ingress.path`; a bundle's own root `config.json` is shadowed.
   The rule is a claim of the owned Route like every other rule (ADR-0139 §8, ADR-0176): if another Route already claims
   the same host, path and method, the later Route in (namespace, name) order is `RouteConflict` and is not programmed:
   when that is the Site's Route, the Site shows `RouteNotReady`; otherwise the other Route goes dark. An `ingress.rules` path equal to the file's
   path, trailing slash trimmed, is refused while `runtimeConfig` is set.
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
   reconciler renders after the bundle's index is asserted and before the Route is written. A missing ConfigMap
   (`ConfigMapNotFound`), a file over the cap or a Put the Bucket refuses for its size (`RuntimeConfigTooLarge`)
   rewrites nothing: the bundle rule still rolls out, the config rule stays exactly when the last good object exists,
   and the Site is `Ready=False` with that reason after the Route step; a Route reason (`RouteNotReady`) wins over a
   config reason, since it decides whether anything is served. A failed bundle deploy returns before the render, so a
   ConfigMap change waits for the bundle to deploy while the previous file keeps serving. Removing `runtimeConfig` first
   deletes the object, then drops the rule, for as long as the current Route still carries it, so the Route stays the
   record.
8. **An outside API is called directly.** It sets its own CORS headers for the Site's origin; funcd's process-wide CORS
   applies only to answers funcd serves.
9. **A `values` edit is a new generation**, which re-resolves a tag image (ADR-0139 Decision 2). Pin the image by digest,
   as App templates do (ADR-0218), or keep values that change often in the ConfigMap, whose change re-resolves nothing.

## Temporary workarounds

- Alternative C until this lands. Exit: this ADR is implemented.

## Contracts

```go
// SiteSpec gains (api/types/v1alpha1/site.go):

	// RuntimeConfig makes the Site serve one generated config.json beside its bundle (ADR-0239). Not the function
	// runtime.
	RuntimeConfig *SiteRuntimeConfig `json:"runtimeConfig,omitempty" doc:"Values each deployment sets without a rebuild, served as config.json beside the bundle. The file is public: never put a secret in it."`

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
| `spec.runtimeConfig`; the named ConfigMap, watched so a change re-runs the Sites that name it | object `<prefix>/_runtime/<site>/config.json`; one rule on the owned Route | `GET`/`HEAD` `<EffectivePath>/config.json`; `Ready` reasons `ConfigMapNotFound`, `RuntimeConfigTooLarge` |

## Implementation plan

- `api/types/v1alpha1/site.go`: the types, constants and functions above, and the `Validate` rules, with unit tests.
- `api/types/v1alpha1/app.go`: `entries()` repoints a Site part's `runtimeConfig.configMap` on a copy of the struct,
  leaving the declared spec the AppRevision hashes untouched.
- `internal/gc/gc.go`: a Site whose `runtimeConfig.configMap` names a ConfigMap is one of its users.
- `internal/site`: render, write before the Route, compile the rule from the object's existence, delete then drop on
  removal, the two reasons, `MapConfigMap`; unit tests.
- `pkg/funcd/funcd.go`: watch ConfigMaps with `MapConfigMap`.
- Go e2e tests in `pkg/funcd`, `t.Parallel()`, covering every Scenario by name.
- A new use case in an existing Lima lane that serves Sites, as the decider asked (the lane is chosen with the decider
  at implementation): an example in pyvvo/funcd-functions (a static app that fetches `config.json`, an App template with
  `staging` and `prod` values files and an App ConfigMap), staged into that lane, whose Venom suite deploys it twice and
  checks both files, the shared digest, a ConfigMap change and the cache headers on real containerd. The example ships
  in a funcd-functions release that funcd takes with `go get`.
- The back-links in ADR-0139 and ADR-0213, the feat row, one clause in the blueprint's Site sentence, and OpenAPI
  regenerated.
- Done when: `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, and the lane with the new use case passes on real containerd.

## Review checklist

- [ ] No bundle byte changes; both Sites in `site-config-two-environments` serve one digest.
- [ ] The body comes from a JSON encoder; string values only; the cap is one shared constant.
- [ ] The key is per Site; the rule is `Exact`, `GET`/`HEAD`, compiled exactly when `runtimeConfig` is set and the
  object exists; the object is written first.
- [ ] A failure rewrites nothing and never blocks the bundle rule; removal deletes the object before the rule.
- [ ] An App-defined ConfigMap is repointed for a Site part and counted as used by it.
- [ ] `SiteRuntimeConfig` has no Secret reference; no edge code and no Route field were added.

## Consequences

- **Positive**: one bundle for every environment; values versioned and rolled back with the App; served by existing
  code; no outbound path from the edge.
- **Negative**: one more request before the app starts; the file's path is one more claim that can conflict; a bundle's
  own root `config.json` is shadowed.
- **Risks accepted**: a team may still put a secret in a value; the `doc` tag and the README are the guard. On an
  `s3://` substrate, two equal-size rewrites within one second can keep one ETag until the next change.

## Open questions

| Question | Answered in |
|---|---|
| A same-origin proxy to an outside API (Alternative B) | A separate ADR, if wanted |

## References

- Request from the funcd-frontend `fleet` work (2026-10-15): one build, per-deployment values.
- `internal/site/materialize.go` (`compileRoute`, `servingDigest`) · `internal/edge/static/static.go` (`cacheControl`,
  `weakETag`) · `internal/edge/router/aggregator.go` (claims) · `api/types/v1alpha1/app.go` (`entries`, `repoint`) ·
  `internal/gc/gc.go` (`InUse`).
