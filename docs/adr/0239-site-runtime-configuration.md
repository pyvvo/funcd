# ADR-0239: Runtime configuration of a Site

- **Status**: Draft (2026-10-15). The file format (Decision 2) waits for the decider's choice; nothing is built
  before acceptance.
- **Date**: 2026-10-15
- **Deciders**: green-0-rabbit
- **Tags**: site, edge, static, config, app-templates
- **Realizes**: [FEAT-0003/F152](../feat/0003-feat-data-platform.md) (runtime configuration of a Site)
- **Relates to**: [ADR-0120](0120-static-asset-serving-route.md) (static serving, its cache tiers and its ban on
  transforms) · [ADR-0139](0139-site-declarative-static-web-app.md) (the Site) · [ADR-0140](0140-path-mounted-site.md)
  (`ingress.path`) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (the Route matcher) ·
  [ADR-0114](0114-edge-observability-shaping.md) (process-wide CORS) ·
  [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) (the node-private `Upstream`) ·
  [ADR-0217](0217-app-templates-values-and-rendering.md) (App template values) ·
  [ADR-0093](0093-function-configmap-consumption.md) (ConfigMaps are non-sensitive)

## Context & Need

A Site serves one prebuilt frontend bundle, pinned by digest. A team builds that bundle once and deploys it to several
environments; only configuration differs between them, first the address of the API the frontend calls. When that API
is a funcd Function, a Route on the same host sends `/api` to it and nothing differs per environment. When the API is
outside funcd, or the frontend needs other per-deployment values (an analytics key, feature flags), funcd offers no
clean path today:

- `SiteSpec` has no field for configuration (`api/types/v1alpha1/site.go:21-37`);
- funcd serves a bundle's bytes unmodified (ADR-0120 rules out on-the-fly transforms, ADR-0140 rejected injecting a
  `<base href>` for the same reason), so it cannot write values into the bundle;
- a user-authored Route cannot point at an outside URL: the edge's `Upstream` is node-private and set only by internal
  reconcilers (`internal/edge/router/router.go:51-56`, ADR-0138);
- CORS is process-wide (`server.shaping.cors`); per-Route CORS is deferred to V2 (ADR-0114).

Outside funcd, the common pattern is a small file the server renders at start, such as `config.js` rendered by
`envsubst` in an nginx image, which the frontend loads before its bundle and validates. This ADR gives a Site that
file: funcd serves one platform-generated file beside the bundle, from values in the Site's spec or a ConfigMap, and
changes none of the bundle's files.

## Scenarios

- `site-config-two-environments` — *Given* one Site bundle pushed once and an App template whose Site sets
  `runtimeConfig.values.API_URL: ${{ values.api.url }}`, *When* it is installed in namespace `staging` with
  `api.url: https://api.staging.example` and in `prod` with `https://api.example`, *Then* each Site serves its own
  config file with its own `API_URL`, both Sites serve the same bundle digest, and the frontend calls the right API
  with no rebuild.
- `site-config-configmap-change` — *Given* a Site with `runtimeConfig.configMap: web-config`, *When* a key of
  `web-config` changes, *Then* the next request for the config file returns the new value with a new ETag, with no new
  bundle digest and no Site rollout.
- `site-config-revalidates` — *When* a browser loads the config file, *Then* the answer carries
  `Cache-Control: no-cache` and a strong ETag, and a conditional request with that ETag answers `304`.
- `site-config-ahead-of-spa` — *Given* `spa: true`, *When* the config file is requested, *Then* the generated file
  answers with its own content type, never the SPA's index.
- `site-config-path-mounted` — *Given* `ingress.path: /bi`, *Then* the file is served at `/bi/<file>` and the Site
  serves nothing at `/<file>`.
- `site-config-refused` — *When* a Site sets a key that is not an identifier, sets a rule at the config file's path,
  or renders a file over the size cap, *Then* the write is refused with `400`, naming the key, the rule or the size.
- `site-config-shadows-bundle-file` — *Given* a bundle that ships its own file of the same name at its root, *Then*
  the generated file is served and the Site's status names the shadowed bundle file.

## Scope

**In**: a `runtimeConfig` field on `SiteSpec`; the generated file, its path, format, content type and cache policy;
its place ahead of the SPA fallback and under `ingress.path`; values from the spec or one ConfigMap; validation; the
ban on secrets; use from App templates.

**Out**: a Route backend to an outside URL (Alternative B, a separate ADR if wanted); per-Route CORS (ADR-0114's V2
item); values per user or per request; server-side rendering; changing any bundle file.

## Constraints & Decision drivers

- One bundle digest for every environment: configuration never changes the bundle (ADR-0120, ADR-0139).
- The file is public to everyone who can load the app, so it can never carry a secret.
- A configuration change must reach browsers without a rebuild, a new digest or a stale cache.
- Reuse what exists: App template values (ADR-0217), ConfigMaps (ADR-0093), the Route matcher (ADR-0110).
- No new path from the edge to the outside world.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A generated file beside the bundle** (`runtimeConfig`) | No bundle change; declared in the Site, so it flows through App templates, AppRevision history and rollback; no outbound path; the frontend pattern exists already | The frontend must load one more small file before it starts | **Chosen** |
| **B. A Route backend to an outside URL** | Same origin for an outside API, so no CORS | The daemon would open connections on a tenant's behalf, outside EgressPolicy, which governs workers only; a tenant could reach node-private listeners or metadata addresses (SSRF); the funcd origin's cookies and `Authorization` header would reach a third party unless stripped; it needs TLS checks, timeouts and size caps; and it solves only the API address, not other values | Rejected here; a separate ADR if a same-origin proxy is wanted |
| **C. The workaround that exists** | Works today: upload `config.js` to a prefix the Site lists in `bucket.prefixes` and serve it through `ingress.rules` | Not declared: the values live outside the Site, so App templates, history and rollback miss them; a separate deploy step with a write credential; a non-hashed file gets `public, max-age=300, must-revalidate` (ADR-0120 §3), so a change can stay stale for 5 minutes; and the file lives at a rule's path, not beside the bundle | Kept as the temporary workaround below |
| D. Build-time values per environment | No runtime file | One build per environment, against the one-bundle goal | Rejected |

## Decision

1. **`runtimeConfig` on a Site.** A Site may set `spec.runtimeConfig`: `values` (a map) and `configMap` (the name of
   one ConfigMap of the Site's namespace). The keys of both are merged; a key in `values` wins over the ConfigMap's.
   When `runtimeConfig` is set, the edge serves one generated file at the Site's `EffectivePath`, holding every key,
   even when there are none. When it is unset, nothing changes.
2. **Format — open for the decider.** Recommended: **`config.js`**, served as `text/javascript`, whose whole body is
   `window.RUNTIME_CONFIG = <the keys as one JSON object>;`. The frontend loads it with a script tag before its
   bundle, so the values exist before the app runs, and the pattern used with nginx and `envsubst` works unchanged.
   The alternative is **`config.json`** (`application/json`), which the frontend fetches before it starts: pure data,
   no script, but every app needs an asynchronous bootstrap. Serving both from the same keys is a third choice.
   Either way the body is generated from a JSON encoding, so no value can break out of the file's syntax.
3. **Cache: `no-cache` with a strong ETag** (a hash of the body), as the index document has (ADR-0120 §3): every load
   revalidates and an unchanged file answers `304`. `no-store` gives no fresher answer and forbids even a `304`.
4. **Ahead of the SPA fallback and under `ingress.path`.** The file is served at `<EffectivePath>/<file>` as its own
   entry in the Site's owned Route. The matcher picks the longest path first (ADR-0110), so neither the bundle's rows
   nor the SPA fallback ever answer for it. A frontend that loads the file declares `runtimeConfig`, even empty;
   without it, the request reaches the bundle as any other path does. A rule at the file's path is refused. A bundle
   file of the same name at the root is shadowed, and the Site's status names it.
5. **Per-environment values come from App templates.** An App template sets
   `runtimeConfig.values.<KEY>: ${{ values.<path> }}` (ADR-0217), and each environment's values file supplies its own
   value. funcd adds no templating of its own.
6. **No secrets.** `runtimeConfig` names no Secret, and the file is served under the Site's Route, to everyone who can
   load the app. A ConfigMap is non-sensitive by definition (ADR-0093). The CLI help and the docs say so.
7. **Validation.** A key matches `^[A-Za-z_][A-Za-z0-9_]*$`, the rule ConfigMap keys already follow; a value is a
   string. The rendered file is at most 32 KiB, since it blocks the app's start. Admission refuses a bad key, a rule
   at the file's path, or an oversized `values` with `400`. A ConfigMap that makes the file exceed the cap leaves the
   last good file served and sets the Site's `RuntimeConfigReady` condition `False`, naming the size.
8. **An API outside funcd is called directly.** The frontend reads its address from the file and calls it. That API
   sets its own CORS headers for the Site's origin; funcd's process-wide CORS applies only to answers funcd serves.

## Temporary workarounds

- Until this lands, a team uses Alternative C: a separate prefix with the file, served through `ingress.rules`.
  Exit: this ADR is implemented and the `fleet` demo loads the generated file.

## Contracts

```go
// SiteSpec gains (api/types/v1alpha1/site.go):

	// RuntimeConfig, when set, makes the edge serve one generated file beside the bundle (ADR-0239): values each
	// deployment sets without rebuilding it. The file is public; it never holds a secret.
	RuntimeConfig *SiteRuntimeConfig `json:"runtimeConfig,omitempty"`

// SiteRuntimeConfig holds the keys of the generated file.
type SiteRuntimeConfig struct {
	// Values are keys and their string values. A key in Values wins over the same key from ConfigMap.
	Values map[string]string `json:"values,omitempty"`
	// ConfigMap names a ConfigMap of the Site's namespace whose data adds keys.
	ConfigMap ObjectName `json:"configMap,omitempty"`
}
```

```yaml
kind: Site
metadata:
  name: fleet
  namespace: prod
spec:
  image: registry.example/fleet-web@sha256:4be1…
  bucket:
    name: fleet-web
  prefix: web
  spa: true
  ingress:
    host: fleet.example
  runtimeConfig:
    values:
      API_URL: https://api.example
    configMap: fleet-flags
```

| Consumes | Exposes |
|---|---|
| `spec.runtimeConfig`, the named ConfigMap (watched) | `GET`/`HEAD` `<EffectivePath>/config.js` (or `.json`, Decision 2): `Cache-Control: no-cache`, strong ETag, `X-Content-Type-Options: nosniff` |
| The Site's owned Route (one more entry, written by the Site reconciler only; a user-authored Route cannot carry a generated body, as it carries no `Upstream`) | Site condition `RuntimeConfigReady`, and the name of a shadowed bundle file in status |

## Implementation plan

- `api/types/v1alpha1/site.go`: the types above and their `Validate` rules (key rule, rule-path clash, size of
  `values`); OpenAPI regenerated.
- The Site reconciler: render the body from `values` and the ConfigMap, watch that ConfigMap, write the generated
  entry on the owned Route, set `RuntimeConfigReady`, and name a shadowed bundle file.
- The edge: serve a generated entry from memory with the headers in Contracts, for `GET` and `HEAD` only.
- Tests: unit tests for rendering, escaping, merging and the size cap; one acceptance test per Scenario; a lane case
  where one bundle is served by two Sites with different `API_URL` values.
- Docs: the Site guide, with the no-secrets rule and the `fleet` example.

## Review checklist

- [ ] No bundle byte is changed; both Sites in `site-config-two-environments` serve one digest.
- [ ] The body is built from a JSON encoding; no value can end the statement or the object.
- [ ] `Cache-Control: no-cache` and a strong ETag; `304` on a match.
- [ ] The SPA fallback never answers for the config path; `ingress.path` moves the file with the bundle.
- [ ] No Secret reference exists in `SiteRuntimeConfig`; a user Route cannot carry a generated entry.
- [ ] A ConfigMap change reaches the file without a new digest or rollout.

## Consequences

- **Positive**: one bundle for every environment; values are declared, versioned with the App and rolled back with
  it; no outbound path from the edge; the frontend pattern used with nginx carries over.
- **Negative**: one more request before the app starts; a frontend that loads the file must declare `runtimeConfig`.
- **Risks accepted**: a team may still put a secret in a value; the rule and the docs are the guard.

## Open questions

| Question | Answered in |
|---|---|
| The format: `config.js`, `config.json`, or both | The decider, before this ADR is Proposed |
| The global's name, if `config.js`: fixed `RUNTIME_CONFIG`, or set per Site | The decider, with the format |
| The 32 KiB cap | The judge gate |
| A same-origin proxy to an outside API (Alternative B) | A separate ADR, if wanted |

## References

- Request from the funcd-frontend `fleet` work (2026-10-15): one build, per-deployment values.
- `api/types/v1alpha1/site.go` · `internal/edge/router/router.go` · `internal/edge/static/static.go` ·
  `docs/reports/app-design.md` (the same-host `/api` pattern).
