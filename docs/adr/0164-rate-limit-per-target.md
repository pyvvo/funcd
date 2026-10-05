# ADR-0164: Rate limit per resolved target, and a bucket table that never resets a drained bucket

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, rate-limit, dos, data-plane
- **Realizes**: [FEAT-0006/F75](../feat/0006-feat-ingress-hardening.md) (ingress protection)
- **Supersedes (in part)**: [ADR-0112](0112-ingress-protection-limits.md), which keeps `Implemented` and gets a
  `Superseded in part by: ADR-0164` back-link at acceptance; its size and in-flight caps, `fault` kinds and clientIP
  rate step keep their decision and chain position. Only these lines change:
  - Scenario `rate-key-function` (lines 45–47): the key is the resolved target, not the `/function/<name>` head;
    Constraints "entries are evicted (LRU/TTL)" (lines 72–73): only a refilled bucket is evicted (Decision 3);
    Alternatives "Limit inside `dataplane.Handler` (after route resolution)" (line 86): reversed for `key: function`.
  - Decision §1 (lines 91–97): under `key: function` the rate step leaves `limit.Chain` for the data plane; §2
    (lines 98–105): the function head (lines 99–101) and the "true LRU" (lines 101–103) are replaced; §6 (lines
    116–117) and Contracts lines 135, 144, 147: `maxKeys` joins `server.limits`, `Chain` has no rate step under
    `function`; Test plan line 182 (`rate-key-function`, the true-LRU test) and Review checklist line 194.
  - "The rate step runs before `dataplane.Handler`" (lines 19, 55, 68–69, 83, 192) and the acceptance note's head
    key and true LRU (line 8) now hold for `clientIP` only.
  - Corrections (never matched the code): Scope lines 54 and 62 say `clientIP`|`path`, but the keys are
    `clientIP`|`function` (`internal/edge/limit/limit.go:31-37`); Decision §2 (line 99) and Consequences (lines
    206–207) honor a trusted `X-Forwarded-For`, but no such setting exists
    (`internal/platform/config/config.go:56-62`) and `clientIP` reads `RemoteAddr` only (`limit.go:188-194`).
- **Relates to**: [ADR-0113](0113-edge-authn-pep.md) · [ADR-0114](0114-edge-observability-shaping.md) ·
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) · [ADR-0134](0134-gateway-normalized-invoke-body.md) ·
  Proposed, no ordering: [ADR-0151](0151-external-invoke-deadline.md) (also adds a `dataplane.Handler` parameter) ·
  [ADR-0171](0171-static-credential-list.md) (edits `serveFunction` next to `store.Get`) ·
  [ADR-0148](0148-size-caps-answer-413.md) (`limit.go` comments, the 413 detail)

## Context & Need

The ADR-0112 limiter runs before routing (`limit.Chain(c.limits)` wraps `dpCore` in `pkg/funcd/funcd.go`):
`key: function` takes the first two path segments, `key: clientIP` the `RemoteAddr` host. A new key gets a full
bucket; past `MaxKeys` = 4096 (`defaultMaxKeys`, `internal/edge/limit/limit.go:39`) the LRU tail is dropped
(`limit.go:163-165`), so a drained bucket comes back full. Verified on origin/main 1193be6, unchanged at 5dbb7fb:
#304 — `/a/1`, `/a/2`, `/a`, `/a/` under one Route are separate buckets, while `/h/1` on two hosts and `/function/x`
across namespaces share one; #88 — 4096 junk 404s evict a drained victim bucket (15 wakes instead of 5 over three
rounds), as do 4096 source IPs in `clientIP` mode; `MaxKeys` has no config key. Purpose: one bucket per target; no flood resets a drained
bucket; the table size is an operator setting.

## Scenarios

Unless stated, `ratePerMin: 1`.
- `scenario: route-paths-share-target-bucket` — Given `key: function`, `burst: 1`, a Route `/a` → cold Function A in
  an implicit namespace, When `GET /a/1`, `/a/2`, `/a`, `/a/` and `/function/A/x` (A's namespace in
  `X-Funcd-Namespace`) arrive, Then the first wakes A once and the other four get 429 with `Retry-After`, no wake.
- `scenario: tenants-do-not-share-bucket` — Given `key: function`, `burst: 1`, Routes `h1.test` `/h` → A in `t1` and
  `h2.test` `/h` → B in `t2`, and a Function `x` in namespaces `a` and `b`, When `GET /h/1` goes to each host and
  `/function/x` with `X-Funcd-Namespace: a`, then `b`, Then all four are served; a second `GET /h/1` on `h1.test`
  gets 429.
- `scenario: junk-names-create-no-bucket` — Given `key: function`, `maxKeys: 2`, `burst: 2`, Functions f1 and f2,
  When 100 calls to `/function/junk-<i>` precede one call each to f1 and f2, Then every junk call gets 404 (never
  429) and f1 and f2 are served.
- `scenario: drained-bucket-survives-flood` — Given `key: function`, `burst: 5`, When 10 calls go to
  `/function/victim`, then 4096 to `/function/junk-<i>`, then 5 to the victim, Then 5 victim calls are served in
  total and the last 5 get 429.
- `scenario: full-map-refuses-new-key` — Given `key: clientIP`, `ratePerMin: 60`, `burst: 1`, `maxKeys: 2`, clients
  A and B that each sent one call, When C calls within the same second, Then C gets 429 with `Retry-After: 1` and
  the detail `rate limit: no free bucket for a new key`; one second later C is served.
- `scenario: client-ip-drained-bucket-survives-ip-flood` — Given `key: clientIP`, `burst: 5`, `maxKeys: 4096`, When
  X sends 10 calls, 4096 distinct IPs call, then X sends 5 more, Then X is served 5 times in total and the flood IPs
  beyond the free buckets get 429.
- `scenario: max-keys-from-config` — Given `server.limits.maxKeys: 2` or `FUNCD_LIMITS_MAX_KEYS=2`, When funcd loads
  its config, Then `Server.Limits.MaxKeys` is 2; unset gives 4096; `0` or `-1` fails with `fault.Invalid` naming
  `server.limits.maxKeys`.
- `scenario: fn-to-fn-not-rate-limited` — Given `key: function`, `burst: 1`, Function A drained by an external call,
  When A is invoked three times fn-to-fn (ADR-0064), Then all three are served and the next external call gets 429.

## Scope

In: the `key: function` check and key; eviction in both modes (at most `maxKeys` buckets, none below `burst`
tokens evicted, no scan); the full-table 429; `maxKeys`. Out: per-Route/per-namespace values; size and in-flight
caps (ADR-0112; 413 is ADR-0148's); static/Upstream under `function`; trusted-proxy `X-Forwarded-For`; multi-node state.

## Constraints & Decision drivers

One bucket per target (hosts, paths and both invoke forms share it; tenants never do); reject before wake (before
`activator.WithFunction` and the activator call, as ADR-0112 requires); no flood resets a drained bucket and the table
stays bounded (#88/#89); no config break (`key` keeps `clientIP` | `function`; `maxKeys` is additive); only the data
plane knows (namespace, function), as with the ADR-0113 PEP.

## Alternatives considered

| Option | Pro | Con | Outcome |
|---|---|---|---|
| Check in `serveFunction` after `store.Get`, key (namespace, function) | one bucket per target; junk mints none; matches nginx `limit_req`, Envoy, Traefik (after route match) | a reject costs routing, the PEP and a store read | **chosen** |
| Check before `store.Get` | cheaper reject | junk names mint buckets (#88) | rejected |
| Host and namespace added to the outer head key | stays in `limit.Chain` | `/a/1` and `/a/2` still differ (#304) | rejected |
| Per-Route values now | per-Route control | V2 scope | rejected (open question) |
| Table: min-heap on `fullAt`, evict only refilled buckets, else 429 | no flood resets a drained bucket; O(log n) | a flood wider than `maxKeys` refuses new keys (fail-closed) | **chosen** |
| Table: true LRU | simple, never refuses | resets a drained bucket (#88) | rejected |
| Table: check only the LRU tail | O(1) | one drained tail bucket refuses every new key | rejected |
| Table: shared overflow bucket | never refuses outright | a flooder drains it for all | rejected |
| Table: fixed hashed array | no eviction | colliding keys share a bucket | rejected |
| Table: LRU plus a Bloom filter | remembers drained keys | most code, false positives, rotation | rejected |

## Decision

1. **Where.** Under `key: function` the rate check runs in `dataplane.serveFunction`, for an external call only:
   after the ADR-0113 PEP and a successful `store.Get` of the Function, before the ADR-0134 body read and the
   activator. `limit.Chain` skips its rate step under `function`; under `clientIP` it keeps it at the
   ADR-0112/ADR-0114 position. The size and in-flight caps stay in `limit.Chain` in both modes.
2. **The key.** The resolved target, `<namespace>/<function>`: from `router.Match.Namespace`/`.Function` on a Route
   hit (any host, path or method of any Route to that Function), from `X-Funcd-Namespace` (default `default`) and
   `<name>` on the `/function/<name>` form. A request that ends before the lookup succeeds (404 for an unknown name,
   the explicit-namespace gate, a Route to a missing Function, a PEP 401/403) creates no bucket and takes no token.
   Static and Upstream matches are not rate-limited under `function`; `dataplane.WithInternal` calls are never checked.
3. **The bucket table (both modes).** At most `maxKeys` buckets. Each bucket records `fullAt`, the time it holds
   `burst` tokens again: after every successful take, `fullAt = now + (burst − TokensAt(now)) / rate`; a refused
   take changes nothing. A min-heap orders buckets by `fullAt`. For a new key with the table at `maxKeys`: if the
   heap root's `fullAt ≤ now`, the root is removed and the key gets a new bucket; otherwise the request gets 429 with
   `Retry-After` = root `fullAt − now`, rounded up to seconds, and no bucket is created. Lookup is a map read,
   eviction a heap pop, each take ends with `heap.Fix`, all under the table's one mutex.
4. **The 429.** `fault.ResourceExhausted` (`urn:funcd:problem:resource-exhausted`), with `Retry-After` set before
   `fault.WriteProblem`, as ADR-0112 §2. Detail: `rate limit exceeded` for a drained bucket;
   `rate limit: no free bucket for a new key` for a full table.
5. **Config.** `server.limits.maxKeys` (`FUNCD_LIMITS_MAX_KEYS`), default 4096, validated `min=1`; `cmd/funcd`
   passes it to `limit.Config.MaxKeys` through a pure `limitsConfig`. A library `MaxKeys ≤ 0` still means 4096.
6. **clientIP** is the `RemoteAddr` host; no forwarding header is read.

## Temporary workarounds

None. ADR-0112's two (process-level values, single-node state) stand.

## Contracts

```go
// internal/edge/limit — names marked new are unbuilt; the rest exist at origin/main 5dbb7fb.
const (
	KeyClientIP Key = "clientIP" // the RemoteAddr host; checked by Chain
	KeyFunction Key = "function" // the resolved (namespace, function); checked by TargetLimiter
)
type Config struct {
	RatePerMin, Burst, MaxInFlight int // unchanged, as are Key Key and MaxBodyBytes int64
	MaxKeys                        int // buckets kept (ADR-0164 eviction); ≤ 0 ⇒ 4096
}
// Chain: signature unchanged; its rate step runs only when cfg.Key is not KeyFunction.
func Chain(cfg Config) func(http.Handler) http.Handler
// TargetLimiter (new): the key: function step; NewTargetLimiter returns nil unless RatePerMin > 0 and Key == KeyFunction.
type TargetLimiter struct{ t *bucketTable }
func NewTargetLimiter(cfg Config) *TargetLimiter
// Throttle (new) takes one token from the ns/name bucket. When the bucket is drained, or the key is new and the
// table is full of buckets that have not refilled, it writes the 429 and returns true. A nil receiver returns false.
func (l *TargetLimiter) Throttle(w http.ResponseWriter, ns v1.NamespaceName, name v1.ObjectName) bool
// bucketTable (new, unexported) replaces rateLimiter and its container/list LRU; Chain and TargetLimiter share it.
type bucketTable struct {
	mu             sync.Mutex
	clk            clock.Clock // internal/platform/clock; clock.System() in production
	limit          rate.Limit
	burst, maxKeys int
	byKey          map[bucketKey]*bucket // bucketKey: SHA-256 of the key string, as today
	byFull         fullHeap
}
type bucket struct {
	key    bucketKey
	lim    *rate.Limiter
	fullAt time.Time // when lim holds burst tokens again; fullAt ≤ now ⇒ equal to a new bucket
	index  int       // position in byFull
}
// fullHeap (new) implements container/heap.Interface; Less is h[i].fullAt.Before(h[j].fullAt).
type fullHeap []*bucket
// take reserves one token for key at clk.Now(); err is a fault.ResourceExhausted with the Decision 4 detail.
func (t *bucketTable) take(key string) (retryAfter time.Duration, err error)

// internal/dataplane/dataplane.go — Handler gains lim (nil ⇒ no per-target check); Server gains limiter.
// With ADR-0151 the signature becomes (…, enf, lim, stat, defaultTimeout, logger), whichever lands first.
func Handler(st store.Store, act *activator.Activator, rtr router.Router, enf *authn.Enforcer,
	lim *limit.TargetLimiter, stat *static.Handler, logger *slog.Logger) http.Handler
// serveFunction, directly after the store.Get check:
	if !internal && s.limiter.Throttle(w, ns, name) {
		return
	}
// pkg/funcd/funcd.go, the dpCore assignment (the limit.Chain wrap and dpHolder.Set lines are unchanged):
dpCore := dataplane.Handler(c.store, act, p.edgeRouter, edgeEnforcer, limit.NewTargetLimiter(c.limits), staticHandler, p.logger)

// internal/platform/config/config.go, server.limits; defaults() sets c.Server.Limits.MaxKeys = 4096 (the literal:
// internal/platform must not import internal/edge/limit). The section comment ("Absent/zero ⇒ off") becomes: the
// rate, size and in-flight limits are off at 0; maxKeys is a table size (min 1, default 4096) with no off switch.
MaxKeys int `json:"maxKeys,omitempty" env:"FUNCD_LIMITS_MAX_KEYS" validate:"min=1"`
// cmd/funcd/main.go (new): the server.limits → limit.Config mapping, moved out of buildOptions so a test sees it.
func limitsConfig(cfg config.Config) limit.Config
```
```yaml
server:
  limits:
    ratePerMin: 60
    burst: 10
    key: function
    maxKeys: 4096
```

Consumes `server.limits` / `WithLimits`, `router.Match`, `store.Get`, `golang.org/x/time` v0.15.0 (`TokensAt`,
`ReserveN`, `DelayFrom`, `CancelAt`), `container/heap`. Exposes a 429 `resource-exhausted` with `Retry-After`, from
`limit.Chain` (clientIP) or `serveFunction` (function), always before the activator.

## Implementation plan

1. `internal/edge/limit/limit.go`: the Contracts types replace `rateLimiter` and `container/list`; one 429 helper
   for both modes; comments cite ADR-0164; `export_test.go` takes a `clock.Clock` (`clock.NewManual`).
2. `internal/dataplane/dataplane.go`: the `Handler` parameter, `Server.limiter`, the call in `serveFunction`; the 12
   test call sites (11 under `internal/dataplane`, one in `internal/function/pool_test.go`) pass `nil`.
3. `pkg/funcd/funcd.go`: the `dpCore` assignment as in Contracts; the limits comment names ADR-0164.
4. `internal/platform/config/config.go` (field, `defaults()`, section comment); `cmd/funcd/main.go:251-257` becomes
   `limitsConfig`, which adds `MaxKeys: l.MaxKeys`; `examples/funcdconfig.yaml:30-35` gains a `maxKeys` line.
5. Tests, one per scenario. `internal/dataplane/ratelimit_test.go` (`frontDoor`, `TargetLimiter`, spy scaler):
   `TestScenarioRoutePathsShareTargetBucket`, `TestScenarioTenantsDoNotShareBucket`,
   `TestScenarioJunkNamesCreateNoBucket`, `TestScenarioDrainedBucketSurvivesFlood`, `TestScenarioFnToFnNotRateLimited`
   (`dataplane.WithInternal`). `internal/edge/limit/limit_test.go`: `TestScenarioFullMapRefusesNewKey`,
   `TestScenarioClientIPDrainedBucketSurvivesIPFlood`, a table test that no bucket with `fullAt > now` is evicted;
   `TestScenarioRateKeyFunction` and `TestRateLimiterLRUBound` are removed; `TestIssue89_FunctionKeyMemoryBounded`
   moves from `Chain` to `TargetLimiter.Throttle` with 64 KiB names (the SHA-256 key bound stays tested).
   `config_test.go`: `TestScenarioMaxKeysFromConfig`; `cmd/funcd/main_test.go`: `TestLimitsConfigCarriesMaxKeys`.
   Unchanged and green: `TestIssue87_FnToFnInvokeBypassesIngressLimits`, `TestScenarioE2ELimitsRateLimit`.
6. No `go.mod` change. Done when every scenario test passes, `just ci`/`just ci-full` are green, the F75
   row moves with each gate.

## Review checklist

- [ ] `Chain` keeps its rate step under `clientIP` only; size and in-flight caps stay in `Chain` in both modes.
- [ ] `Throttle` runs only for non-internal calls, after the PEP and `store.Get`, before the body read and the
      activator; static and Upstream matches never call it. The key is `<namespace>/<function>`, no path or host.
- [ ] After each successful take, `fullAt` is recomputed with `TokensAt`, then `heap.Fix`; a refused take changes
      nothing. No bucket with `fullAt > now` is evicted; no scan; the two 429 details and `Retry-After` match
      Decision 4.
- [ ] `maxKeys` defaults to 4096 and rejects `≤ 0` from config; the example config has the line; a nil
      `TargetLimiter` checks nothing; no `container/list` remains; each scenario has one passing
      `TestScenario<Name>`; #87 stays green.

## Consequences

- Positive: one bucket per target across hosts, paths and both invoke forms; tenants never share a bucket; junk
  cannot fill the function-mode table; no flood resets a drained bucket; the 429 carries the target label in the ADR-0114 edge metrics.
- Negative: a function-mode reject costs routing, the Namespace reads, the PEP when enabled and a `store.Get` (not
  measured); size and in-flight rejects stay outermost; an over-rate request holds an in-flight slot until its 429.
  `dataplane.Handler` gains a parameter (13 call sites).
- Negative: under `function`, 404/401/403 floods take no token and are not rate-limited (they never wake a sandbox);
  static and Upstream Routes, including the ADR-0138 catalog ingress whose handshakes ADR-0153 leaves to F75, lose
  their head-keyed limit; only `key: clientIP` limits them until a follow-up.
- Risk accepted: over `maxKeys` client IPs in a refill window refuse new IPs; one IPv6 host with a /64 can do it.
- Blueprint sync at acceptance: the ingress middleware sentences (Ingress controller / API Gateway, and the gateway
  `middleware.go` note) gain one list of handler-step exceptions in `serveFunction`: the ADR-0113 PEP (existing
  drift, corrected here), the `key: function` rate step and the ADR-0151 invoke deadline; the `clientIP` rate step
  and the size and in-flight caps stay `limit.Chain` middleware. Whichever of 0151/0164 is accepted second extends
  the list. `docs/PROJECT-SUMMARY.md` (ADR-0112 row) is refreshed at acceptance.

## Open questions (each a follow-up ADR)

A per-(namespace, bucket) key for static and a per-(namespace, upstream) key for Upstream routes (the ADR-0138
catalog ingress, the ADR-0153 pre-auth handshake path) under `function`; per-Route limit values (ADR-0112's V2
follow-on, read at this ADR's inner step); an IPv6 /64 `clientIP` key with ADR-0112's trusted-proxy question.

## References

- Issues [#88](https://github.com/pyvvo/funcd/issues/88), [#304](https://github.com/pyvvo/funcd/issues/304),
  [#87](https://github.com/pyvvo/funcd/issues/87), [#89](https://github.com/pyvvo/funcd/issues/89);
  `golang.org/x/time/rate`, `container/heap`
- <https://nginx.org/en/docs/http/ngx_http_limit_req_module.html> ·
  <https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/local_rate_limit_filter> ·
  <https://doc.traefik.io/traefik/v3.4/reference/routing-configuration/http/middlewares/ratelimit/>
