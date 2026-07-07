# ADR-0091 Judge Report — Function catalog consumer binding (`spec.catalogs`)

- **Verdict**: Accept with fixes (1 Major, 3 Minor, 2 Nits — no Blockers)
- **Judged against**: ADR-0000 (template/lifecycle), FEAT-0003 F61, blueprint.md (provider/consumer + binding-as-grant), ADR-0086 (CatalogService shape), ADR-0088 (provider identity + requeue), ADR-0057 (secret→env), ADR-0074/0080 (binding-as-grant), ADR-0011 (sandbox posture), and the real code: `api/types/v1alpha1/{function,catalogservice,egresspolicy}.go`, `internal/function/{function,secrets}.go`, `internal/services/catalog/reconcile.go`, `internal/controlplane/admission/bucket.go`, `internal/auth/cedar/schema.go`.
- **ADR status**: Proposed

## Goal alignment

Serves F61 exactly: the feat row (`docs/feat/0003-feat-data-platform.md:41`) asks for `spec.catalogs: [{alias, catalog}]` injecting `FUNCD_CATALOG_<ALIAS>_URL` (= `status.endpoint`) + `_TOKEN` (= `QUACK_TOKEN` from the catalog's Secret), requeue-until-Ready, binding-as-grant, admission-validates-exists, and "no egress grant in V1 (default-open lateral; the Cedar grant is the V2 egress ADR's work)". The ADR delivers each clause and no more. One altitude: it decides the consumer binding and explicitly defers the V2 `egress::connect` PEP grant, cross-namespace, and row-level governance (`0091:55-59`). Correctly closes ADR-0089's consumer follow-up.

**Egress claim is verified correct.** `EgressPolicySpec` is empty (`api/types/v1alpha1/egresspolicy.go:13`); the curated Cedar action set has no `egress::connect`/`egress::send` (`internal/auth/cedar/schema.go:38-44` — only kv/link/s3); no L7 egress PEP gates a node-internal function→catalog call. The ADR's load-bearing "V1 needs no grant" (`0091:108`) is sound — there is nothing to authorize against.

## Strengths — keep as-is

- The **security core is right**: rejecting `status.quackToken` in favor of resolving from the catalog's Secret for declared consumers only (`0091:77-79`) preserves binding-as-grant; the token never leaks to a namespace reader who can GET the CatalogService. This mirrors `spec.blob`→`s3::read` faithfully.
- **Requeue-until-Ready fail-closed** (`0091:104-106`) matches the established pattern: the catalog reconciler already returns `RequeueAfter` on not-Ready (`internal/services/catalog/reconcile.go:103-109`) and the Function reconciler already threads `RequeueAfter` up its Result (`internal/function/function.go:385-387`). The seam the ADR claims exists.
- **Reuse of the exact injection machinery** — `secrets.ResolveEnv` + the `system:secret-injector:<ns>` identity — is real and already used by *both* the Function reconciler (`internal/function/secrets.go:39,79-85`) and the catalog reconciler (`reconcile.go:158`), so cross-resource same-ns resolution needs no new authz.
- **Alternatives are honestly weighed** — status-token (leaks), spec.secrets-workaround (hand-wiring, and named as the retired stopgap), egress-now (nothing to grant against), cross-ns (namespace-scoped injector) each carry a real losing reason (`0091:77-86`). No straw men.

## Findings

### Blockers

None.

### Major

- **M1 — the `FUNCD_`-prefix reserved-key guard will silently drop the injected env if `addCatalogEnv` reuses the merge path; the ADR must state it writes to `env` directly.** `isReservedFuncdKey` is `strings.HasPrefix(k, "FUNCD_")` (`internal/function/secrets.go:64`) and `mergeSecretEnv` drops every reserved key (`secrets.go:50-57`). The ADR injects `FUNCD_CATALOG_<ALIAS>_URL`/`_TOKEN` — both reserved-prefixed. If the implementer routes them through `mergeSecretEnv` (the obvious sibling of the `_TOKEN` resolution, which *does* come out of `secrets.ResolveEnv`), the keys are **dropped and the function boots with neither URL nor token** — a fail-*open* silent break of exactly the feature. The contract `func (r *Reconciler) addCatalogEnv(ctx, env, fn) (requeue, err)` (`0091:143`) implies a direct `env[...] = ...` write (bypassing the guard) — which is correct and matches the catalog reconciler's own precedent (`reconcile.go:120-123` sets `FUNCD_QUACK_PORT`/`FUNCD_DUCKLAKE_CATALOG` directly, applying the guard only to *resolved third-party Data keys*). But the ADR nowhere says "write directly, do not pass through `mergeSecretEnv`", and the `_TOKEN` value specifically originates from `ResolveEnv` under key `QUACK_TOKEN` (`0091:102-103`) — the one that must be re-keyed to `FUNCD_CATALOG_<ALIAS>_TOKEN` and set directly. → *Fix*: add one Contract sentence: "`addCatalogEnv` writes `FUNCD_CATALOG_*` **directly** into `env` (never via `mergeSecretEnv`, whose `FUNCD_` guard would drop them); the resolved `QUACK_TOKEN` value is re-keyed to `FUNCD_CATALOG_<ALIAS>_TOKEN` before the direct write." Add a test asserting the keys survive (the `binding-injects-endpoint-and-token` test at `0091:184` should assert non-empty values specifically, to catch a regression into the guard).

### Minor

- **m1 — token resolution reads the *entire* `QUACK_TOKEN` from `cs.Spec.Secrets`, but the ADR does not pin which Secret key survives.** `secrets.ResolveEnv(...)` returns *all* Data keys of every named Secret (`reconcile.go:158-168`); the ADR says "read at key `QUACK_TOKEN`" (`0091:103`). The implementer must select `resolved["QUACK_TOKEN"]` out of the map, not merge the whole map. State this explicitly, and define behavior when the catalog's Secret has no `QUACK_TOKEN` key (requeue? Invalid? — currently unspecified; suggest fail-closed like a missing endpoint).
- **m2 — admission Handles-scope: the mirror should say Handles gates on `len(spec.catalogs)>0` cheaply.** The ADR's `catalog-binding-validity` (`0091:151-154`) mirrors `blob-binding-validity`, but that admission's `Handles` fires on *every* Function Create/Update and short-circuits inside `Admit` (`bucket.go:78-87`). The ADR text ("on Function Create/Update with len(spec.catalogs)>0", `0091:152`) reads as if `Handles` itself checks the length — it cannot (Handles sees only GVK+op). Minor wording; align with the real seam so the implementer registers it like `blob-binding-validity`, not with a phantom length gate in Handles.
- **m3 — a never-Ready catalog wedges the consumer Function forever; the ADR should name this as the accepted posture.** The requeue (`0091:104-106`) means a Function bound to a catalog that never publishes `status.endpoint` stays Pending indefinitely (re-requeuing). That is consistent with other fail-closed bindings (SecretResolveFailed holds not-Ready, `function.go:334-343`), but unlike the *secret* gate it never reaches a terminal `Failed` — it spins. Acceptable, but the ADR should state it (a one-liner in Consequences: "a bound catalog that never becomes Ready holds the consumer Pending — same fail-closed posture as an unresolved secret, surfaced via the Ready condition"). Confirm the requeue sets a Ready=False reason (e.g. `CatalogNotReady`) so the wedge is observable, not silent.

### Nits

- **n1 — the requeue interval is unspecified.** The ADR says `RequeueAfter` "like the ADR-0088 catalog wait" (`0091:104`); the catalog reconciler uses `2 * time.Second` (`reconcile.go:108`) while the Function readiness re-poll uses `200ms` (`function.go:386`). Pick/name one so the implementer doesn't guess.
- **n2 — `status.endpoint` is a raw `host:port` (`0091:33,101`) and the ADR notes "the client normalizes to `quack://`".** The catalog reconciler sets `status.Endpoint` to the ingress path *or* the netns `Address` (`reconcile.go:82-86`) — its exact form is provider-runtime-dependent. Harmless, but a scenario note that the consumer must tolerate whatever `status.endpoint` string the provider published (not assume a scheme) would de-risk the e2e.

## Template & scenario conformance

All ADR-0000 sections present and in order (Header/Context/Scenarios/Scope/Constraints/Alternatives/Decision/Temporary workarounds/Contracts/Implementation plan/Review checklist/Consequences/Open questions/References). Header `Realizes: FEAT-0003/F61` points at a real row. Six scenarios, each mapped to a named test in the Test plan (`0091:182-189`) — `binding-injects-endpoint-and-token`, `admission-rejects-unknown-catalog`, `token-only-to-declared-consumer`, `requeue-until-catalog-ready`, `alias-unique-dns1123`, `round-trips-sql` (e2e). Contracts compile against the real types: `FunctionCatalog{Alias, Catalog ObjectName}` matches the `FunctionKV`/`FunctionBlob`/`FunctionLink` field/tag/pattern convention (`function.go:81-114`) and the `Validate()` alias-unique/DNS-1123 loop is a faithful clone of the existing blob/kv loops (`function.go:180-222`). No bloat; concise throughout.

## Recommendation

**Accept after folding M1** (state the direct-`env`-write / no-`mergeSecretEnv` contract — this is the one finding that would produce a silently-broken feature) and, ideally, the three Minors (pin the `QUACK_TOKEN` key selection + missing-key behavior; correct the admission Handles wording; name the never-Ready wedge as accepted fail-closed posture with an observable Ready reason). The decision is sound, correctly scoped, and its security reasoning and egress deferral are verified against the real code. Nits are optional polish.
