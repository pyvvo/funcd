# ADR-0120 implementation review — static-asset serving (`Bucket`-prefix `Route` backend, F82)

- **ADR**: [0120](../adr/0120-static-asset-serving-route.md) · **Realizes**: FEAT-0003/F82
- **Phase**: implementation · **Producing model**: claude-opus-4-8 · **Reviewer gate**: adr-impl-review (ADR-0000 gate 5)
- **Date**: 2026-07-10
- **Verdict**: **PASS** — 0 Blockers, 0 Majors, 1 Minor (model). DoD 7/8 (Venom lane deferred, env-attributed + substituted).

The gate REVIEWS and RECORDS; it did not edit code or the ADR.

## Verification (real exit codes, `nix develop -c`)

| Command | Exit |
|---|---|
| `go build ./...` | 0 |
| `go vet ./...` | 0 |
| `go tool golangci-lint run ./...` | 0 |
| `go test ./...` | 1 — **only** `TestPythonPoolSmoke` (`internal/testkit/bench`), the known pre-existing env flake (py shim did not become ready). An independent earlier full-suite run passed it (exit 0). NOT attributed. |
| `go mod verify` | 0 (`all modules verified`; `go.mod`/`go.sum` unchanged — no new dep) |

Every ADR-0120 package is green in isolation: `internal/edge/static`, `internal/route`, `internal/dataplane`, `api/types/v1alpha1`, `pkg/funcd` all `ok`.

## Conformance to Contracts, Scenarios, and the judge folds

### ✅ B1 — path-traversal defense (SECURITY, was the judge Blocker)

`internal/edge/static/static.go:150-178` (`resolveKey`) owns the defense as a stated handler contract:
1. `url.PathUnescape` the remainder (a bad decode ⇒ `bad`, `static.go:151-154`);
2. **reject any `..` segment BEFORE `path.Clean`** — `containsDotDot` (`static.go:159`, impl `static.go:265-275`, mirrors `net/http`);
3. root at `/` then `path.Clean` so it can never ascend (`static.go:164`);
4. compute `key = prefix + rel` and **assert `strings.HasPrefix(key, prefix)` before any `Get`** (`static.go:174-176`) — belt-and-suspenders past the `..` reject.

The `traversal-rejected` test (`static_test.go:115-125`) proves `/..%2f..%2fsecret`, `/../../secret`, `/../../x`, `/img/../../secret` all return 404/400 and **never** leak the `secret` object that actually exists at the bucket ROOT (seeded `static_test.go:29`, outside the `bi/` prefix). It also proves an **SPA** route does not leak the secret via the fallback (`static_test.go:123-124`) — because `resolveKey` rejects before the `stat`/SPA path is reached. No traversal gap. **Verified correct.**

### ✅ M2 — `public` vs explicit `authenticated` (SECURITY, was a judge Major)

Two layers, both present:
- **Admission**: `validateBackend` rejects `public:true` + an explicit Route `spec.auth.mode: authenticated` with `fault.Invalid` (`route.go:215-217`). Proven: `public-vs-explicit-authenticated-conflict` ⇒ `fault.Invalid` (`validate_test.go:361-364`); `public+open` and `public+unset` pass (`validate_test.go:365-370`).
- **Runtime backstop**: `serveStatic` resolves the stance and only relaxes to `open` when `stance != AuthAuthenticated` (`dataplane.go` serveStatic, `if m.Static.Public && stance != v1.AuthAuthenticated { stance = v1.AuthOpen }`). So a **namespace-default** `authenticated` (which `Validate` cannot see — it is namespace-agnostic) still wins at runtime and `public` never silently opens it. The PEP runs **before any byte is read** (reject-before-read, `FunctionRef{Namespace: ns}` namespace-scope authz).

The in-process e2e proves the full precedence over the live listener (`pkg/funcd/static_e2e_test.go`): the public BI route is served anonymously (`:118-122`); the non-public `docs` route in an `authenticated` namespace **401s anon** (`:142-144`) and **200s with a namespace-scoped bearer** (`:148-152`). No silent-open. **Verified correct.**

### ✅ M1 — `(ModTime,Size)` weak validator, not per-request `sha256`

`weakETag` = `` W/"<size>-<modtime-unix>" `` (`static.go:196-198`); `http.ServeContent` is called with the **real `attrs.ModTime`** (`static.go:144`) so `Last-Modified`/`If-Modified-Since`/`If-Range` all work. No `crypto/sha256` in the file (only a comment naming the rejected approach). A matching `If-None-Match` short-circuits to 304 **without a body read** (`static.go:128-131`). Proven by `etag-conditional-304` (`static_test.go:74-84`) and the e2e weak-ETag asserts. **Verified correct.**

### ✅ M3 — three-tier `Cache-Control` (no blanket immutable)

`cacheControl` (`static.go:213-242`): `Index` + the stable set (`sw.js`/`service-worker`/`manifest*`/`favicon*`/`robots.txt`/`.well-known/*`) ⇒ `no-cache`; a content-hash-shaped filename (`[.\-][0-9a-f]{8,}\.`, `static.go:65`) ⇒ `public, max-age=31536000, immutable`; everything else ⇒ `public, max-age=300, must-revalidate`. All three tiers asserted in `TestCacheControlThreeTiers` (`static_test.go:142-163`), incl. a `favicon`/`sw.js`/`.well-known` = no-cache and a hashed `app.a1b2c3d4.js` = immutable. **Verified correct.**

### ✅ Router / reconcile / union

- `RouteBackend` exactly-one-of: `validateBackend` (`route.go:196-219`) — the old **unconditional** `dnsLabel.MatchString(Backend.Function)` is replaced by a per-arm check; neither/both ⇒ `Invalid`, function arm keeps DNS-1123, static arm requires DNS-1123 bucket + relative index. Matrix in `validate_test.go:342-359`.
- Static rule handler-owned **405** (not router 404): the handler returns 405 for non-GET/HEAD (`static.go:73-77`); `method-not-allowed` proves POST⇒405, HEAD⇒200 (`static_test.go:128-139`).
- `BucketNotFound` NotReady mirrors `BackendNotFound`: `firstBackendProblem` (`reconcile.go:157-179`) checks `KindBucket` for a static arm; `bucket-not-found-not-ready` proves NotReady+unprogrammed then Ready+programmed once the Bucket exists (`reconcile_test.go:92-115`).
- Router carries `*StaticBackend` Program→Resolve; `TestStaticRootCatchAllAndBackendCarried` (`router_test.go:51-68`) proves the Match carries it (nil for a function match).
- In-process **edge e2e drives the real listener** end-to-end (`static_e2e_test.go:101-152` via `p.DataPlaneAddr()` + `http.DefaultClient`): reconciler programs the static backend → data-plane front door dispatches to `internal/edge/static` → real bytes, 206 Range, weak ETag.

### ✅ Conventions

No `any` in exported/port signatures (the `any` hits are prose in comments); `api`/`fault` errors used (`fault.NotFoundf`/`Invalidf`/`WriteProblem`); ctx-first (`Serve` takes `*http.Request`, `stat` threads `r.Context()`); `slog`; no new `go.mod` dep (stdlib only); OpenAPI regenerated (`StaticBackend` schema added, `RouteBackend.required: function` removed). No identity/abs-path leak in any touched or new file.

## Step 4 — the ADR-0110 `matchPath` change → attributed **adr** (ADR-0110 gap)

`internal/edge/router/router.go:156-164` adds `if prefix == "/" { return true }` so a `/` **Prefix** rule is a catch-all subtree. Assessment:

- **Correct + minimal.** The prior expression `path == prefix || strings.HasPrefix(path, prefix+"/")` reduced a `/` prefix to matching only `/` itself (`HasPrefix(path, "//")` is false for real paths) — a latent degenerate bug: a `/` Prefix rule could not serve a subtree. The new clause changes **only** the `prefix == "/"` case; every other prefix keeps the exact/prefix/segment semantics untouched.
- **No regression.** `go test ./internal/edge/router/ -count=1 -v` — **all 8 tests pass**, incl. the pre-existing ADR-0110 `TestScenarioMatchExactVsPrefix`, `MatchLongestPrefix`, `StripPrefix`, `MatchHost`, `MatchMethod`. Longest-path-first sort means `/` (len 1) is only reached when nothing more specific matches, so it does not shadow other routes.
- **Attribution: `adr` (ADR-0110 latent gap)**, analogous to a bug-fix to implemented code surfaced by new work — F82 legitimately requires a `/`-mounted static site. It does not regress or over-reach, so it is not a `model` fault.

## Step 5 — Venom lane deferral → **env/scope** (not model)

The impl deferred the Venom containerd lane. Justification assessed and accepted: the `scripts/lanes.yaml` framework only pushes OCI artifacts + applies manifests — it cannot **seed blob bytes into a Bucket in the VM** (that needs the S3 write path / a writer function, a new lane pattern). Static serving needs **no kernel/sandbox** — it is a byte read through the edge — and the in-process edge e2e (`static_e2e_test.go`) exercises it **fully end-to-end through the live data-plane listener** (`p.DataPlaneAddr()`, real HTTP client, real reconcile→program→dispatch, 200/206/401/200-with-bearer). The DoD's serving guarantees are met by that substitute; the deferral is environmental, consistent with the ADR-0119 precedent. Not model-attributed.

## Findings

### Blockers — none
### Majors — none
### Minors

- **[Minor · model]** `compile` (`internal/route/reconcile.go:224-238`) copies `rule.Methods` into the compiled `mset` **unconditionally**, including for a static arm. ADR-0120 §1 states a static rule "is compiled with **no** method restriction at the router" precisely so the handler (not the router's method filter) owns method rejection with a **405** — if a user set `methods:` on a static rule, the router would 404 a disallowed method before the handler's 405. Non-security, non-scenario-breaking: static routes omit `methods` in normal use (and in every test), so the `method-not-allowed` scenario's 405 holds. A one-line guard (skip `mset` when `rule.Backend.Static != nil`) would match §1's letter. Note only; does not block.

## Definition of Done

| # | DoD clause | Status |
|---|---|---|
| 1 | all scenario tests green | ✅ 11/11 scenarios each a named passing test |
| 2 | `go build/test/lint/mod` green | ✅ (only failure = the known `TestPythonPoolSmoke` env flake) |
| 3 | in-process e2e green | ✅ drives the live listener end-to-end |
| 4 | Venom lane green | ⚠️ **deferred (env)** — framework cannot seed Bucket bytes in-VM; in-process e2e substitutes |
| 5 | OpenAPI regenerated | ✅ `StaticBackend` added, union `required` relaxed |
| 6 | F82 row → reviewing | ✅ |
| 7 | FEAT-0003 diagram note reconciled | ✅ *"BI served by a function"* → *"static Route (F82)"* |
| 8 | no identity/path leak | ✅ grep clean |

**DoD: 7/8 passed, 1 deferred (env, substituted).**

## Verdict

**PASS.** The security-critical folds are honestly implemented and tested: B1 traversal (reject-`..` + `HasPrefix` before any `Get`, out-of-prefix secret proven unreachable), M2 auth (admission reject + runtime backstop, no silent-open), M1 weak `(ModTime,Size)` validator, and M3 three-tier cache. The ADR-0110 `matchPath` change is correct, minimal, regression-free, and correctly a latent-ADR-0110 fix (attributed `adr`). The one Minor is a cosmetic §1-letter deviation with no runtime impact under normal use. The Venom deferral is env/scope, fully substituted by an end-to-end in-process edge e2e.

Recommend advancing ADR-0120 `Reviewing → Implemented` and the F82 row → `implemented` (performed by the gate owner, not this document).
