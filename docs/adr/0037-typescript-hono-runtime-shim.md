# ADR-0037: The Node reference shim — TypeScript + Hono, esbuild-bundled (supersedes ADR-0030 §2)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16**, **Implemented 2026-06-16** — judge folded: reframed the ADR-0030 §2 relationship from "refines"
  to a narrow, contract-preserving **supersession**; clarified the 500 body is `{error}` JSON, not RFC 9457
  problem+json; noted ADR-0032's `FUNCD_PORT` bind is preserved too.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, nodejs, developer-experience, build
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — the platform runtime **shim**); also
  improves the **author-facing** half of [F13](../feat/0000-feat-v1.md) (the `handle(context, event)` contract) by
  shipping a typed authoring surface.
- **Supersedes (narrowly) / relates to**: [ADR-0030](0030-function-execution-runtime-shim-node.md) — **supersedes its
  §2 only** (the reference-shim *implementation*: the hand-written `shim.mjs` → a **TypeScript + Hono** source bundled
  by **esbuild** to the *same* `shim/nodejs/shim.mjs`). The supersession is **narrow and contract-preserving**:
  ADR-0030's §1 wire contract, `handle(context, event)` resolution, §3 materialization, §4 addressing/readiness, and
  §5 test lane — plus [ADR-0032](0032-curated-runtime-images-container-execution.md)'s `FUNCD_PORT`→`0.0.0.0` container
  bind — are unchanged and stay authoritative there (proven by every ADR-0030/0032 scenario test passing unchanged
  against the new bundle). Because the rest of ADR-0030 stands, its status is left untouched (no whole-document
  supersession). Also relates to ADR-0032 (bakes the shim into the curated image) and
  [ADR-0036](0036-daemon-execution-wiring.md) (`go:embed`s it).

## Context & Need

The runtime shim is the program that runs **inside** every function sandbox: it loads the user's artifact, resolves
the `handle(context, event)` export, and serves the runtime-shim HTTP contract (ADR-0030 §1). ADR-0030 shipped it as
a single dependency-free hand-written `shim.mjs` over `node:http`. That was the right first step — zero dependencies,
trivially embeddable — but it has two costs that grow as the platform does:

1. **Authoring is untyped.** A function author writing `handle(context, event)` has no types for `context`, no types
   for the CloudEvent `event`, and no compile-time contract — the shape is discovered by reading the shim source.
2. **The shim itself is untyped, hand-rolled HTTP.** Routing, body parsing, status mapping, and error handling are
   open-coded against raw `node:http` request/response objects — verbose, easy to get subtly wrong, and unpleasant to
   extend (the deferred KV/blob/secrets `context` SDK, future streaming, per-runtime health hooks).

The need: keep the **native `node:http` speed and the single-self-contained-file deployment** the platform depends on,
while giving both the shim author and the function author a typed, modern surface. This ADR decides that stack.

## Scenarios

- **scenario: shim-contract-unchanged** *(node-gated)* — *Given* the new bundled `shim.mjs`, *when* every ADR-0030
  scenario test (`shim-executes-handler`, `shim-readiness-gate`, `reconciler-materializes-and-runs`,
  `timer-invokes-real-handler`, plus the data-plane + journey + daemon e2e launches) runs against it, *then* all pass
  unchanged — the wire contract is identical (object→200 JSON · none→204 · throw→500 · invalid JSON→400; readiness +
  liveness 200).
- **scenario: shim-self-test** *(node-gated)* — *Given* the TypeScript source, *when* `node --test` runs the shim's own
  `test/*.test.ts` (via `--experimental-strip-types`), *then* `createApp` and `resolveHandler` are unit-tested
  server-lessly (Hono `app.request()`) and pass, and `tsc --noEmit` typechecks clean.
- **scenario: bundle-is-self-contained** — *Given* `npm run build`, *when* the resulting `shim.mjs` is run by absolute
  path from a directory with **no** `node_modules` (the daemon's extracted-shim case), *then* it loads a handler,
  binds, and writes its port — Hono and `@hono/node-server` are inlined; the sandbox needs no `npm install`, no
  registry, no runtime dependency.
- **scenario: typed-authoring-contract** — *Given* `import type { Handler } from '@funcd/shim-nodejs'`, *when* an author
  writes `const handle: Handler<In, Out> = (context, event) => …`, *then* `context`, the CloudEvent `event`, and the
  return type are typed at compile time, against the same contract the shim enforces at runtime.

## Scope

**In:** the reference shim's source language (TypeScript), HTTP framework (Hono over `@hono/node-server` = `node:http`),
bundler (esbuild → one `shim.mjs`), self-test runner (`node --test`), the typed authoring contract (`types.ts`), and the
build integration (committed generated `shim.mjs`, `go:embed`, a `just build-shim` recipe, node-gated staleness).

**Out (unchanged — still ADR-0030, re-affirmed not modified):** the wire HTTP contract *semantics* (§1); the
`handle(context, event)` resolution shape and the readiness shape-gate; the `Materializer` seam and its drivers (§3);
per-replica loopback addressing + `Instance.Port` (§4a); readiness = shape-gate (§4b); the L3-runtime test lane (§5).
Also out: the in-handler KV/blob/secrets `context` SDK (still a deferred follow-up); Python shape (P-V-3); any change to
the artifact-bundling model (authors still ship one bundled artifact — ADR-0031).

## Constraints & Decision drivers

- **Native speed, single file.** The shim runs per-replica on the cold-start path (scale-to-zero); it must keep
  `node:http`'s throughput and deploy as one self-contained file with **no runtime dependency** in the sandbox.
- **No new runtime dependency.** ADR-0030's invariant holds: the shipped `shim.mjs` and the `funcd` binary gain zero
  runtime deps. New deps are **build-time devDependencies** only, bundled away.
- **Apache-2.0/MIT only.** Verified: Hono MIT, `@hono/node-server` MIT, esbuild MIT, TypeScript Apache-2.0,
  `@types/node` MIT.
- **Building `funcd` must not require a JS toolchain.** The Go binary embeds a *committed* `shim.mjs`; only
  *regenerating* it needs node/npm. Pure-Go `just ci` stays green-without-node (ADR-0030 §5 philosophy).
- **Web-standard, so the contract survives a runtime swap.** Hono is a `Request`→`Response` Web-standard router; the
  same app object runs on `node:http` today and could run elsewhere without rewriting the contract.

## Alternatives considered

- **Keep the hand-written `node:http` shim (status quo).** Pros: zero deps, nothing to build. Cons: untyped on both
  sides; open-coded routing/parsing/error-mapping grows error-prone as `context` SDK / health hooks / streaming land.
  Rejected: the DX and maintainability cost compounds; the typed contract is the actual ask.
- **Express / Fastify instead of Hono.** Pros: ubiquitous, familiar. Cons: Express is callback-era and not
  Web-standard; Fastify is heavier and its plugin/types model is more than a 3-route shim needs. Neither is
  Web-standard `Request`/`Response`, so the contract wouldn't survive a non-node runtime. Rejected on weight +
  non-portability.
- **A different bundler (Rolldown / Vite / Bun build).** Rolldown is pre-1.0; Vite is a dev-server-first tool whose
  bundle path is heavier than needed for one entrypoint; Bun's bundler would couple the build to the Bun toolchain.
  esbuild is a single fast purpose-built bundler with an MIT license and one job here (TS+Hono → one `.mjs`).
  Rejected the alternatives on maturity/fit; **esbuild chosen** (also the user's explicit call after weighing them).
- **Run the shim on Bun instead of node.** Pros: built-in TS, fast start. Cons: adds a second runtime to ship/curate,
  and the platform's curated images + `go:embed` + process driver are all node-shaped. Rejected: not worth a second
  runtime for the shim; node stays the reference (Bun remains a possible *author* choice for their own bundling).
- **Publish the types as a separate npm package only (no shim rewrite).** Solves author typing but not the shim's own
  untyped HTTP. Rejected: half the need; the shim source is where most of the maintainability cost lives.

## Decision

**Rewrite the reference shim in TypeScript, served by Hono on `@hono/node-server` (= `node:http`), bundled by esbuild
to the same single `shim/nodejs/shim.mjs`. Ship a typed authoring contract. Preserve ADR-0030 §1's wire contract
exactly.**

### 1. Source layout (`shim/nodejs/`)
- `src/types.ts` — the **authoring contract**: `CloudEvent<T>`, `FunctionContext`, `Handler<In, Out>`. The one surface
  a function author imports for compile-time types; the same shape the shim enforces at runtime.
- `src/shim.ts` — the implementation. Exports `resolveHandler(mod, name)` (the materialization shape-gate: picks
  `mod[name] ?? mod.default?.[name] ?? mod.default`; a non-function throws) and `createApp(handler): Hono` (the three
  routes of §1), re-exports the types, and `main()` (load `FUNCD_ARTIFACT`, resolve the handler, `serve`).
- `test/shim.test.ts` — `node --test` unit tests over `createApp`/`resolveHandler` via Hono's `app.request()`.
- `package.json` / `tsconfig.json` — `@funcd/shim-nodejs`, `private`, `type: module`; the devDependencies + scripts.

### 2. HTTP via Hono (the contract is unchanged)
`createApp(handler)` maps ADR-0030 §1 onto Hono routes: `GET /health/liveness` → 200; `GET /health/readiness` → 200
(reached only once `main` resolved the handler — the shape-gate); `POST /` → parse the body as a CloudEvent (parse
failure → **400**), call `handler(context, event)`, then map the return: `undefined`/`null` → **204**, an object →
**200 JSON**, a thrown error → **500** with a `{ "error": string }` JSON body. `serve({ fetch: createApp(handler).fetch, … })`
runs it on `node:http`. The bytes on the wire are identical to ADR-0030's *shipped* shim — that identity is the
`shim-contract-unchanged` scenario. (The 500 body is `{error}`, **not** RFC 9457 `application/problem+json`; that
format is the platform's *gateway/API edge* contract, not the in-sandbox shim's. ADR-0030 §1's "problem+json" wording
was aspirational and never shipped — the new bundle matches the real shim, not that wording.)

### 3. The entrypoint guard (symlink-safe)
`main()` runs only when the module is the process entrypoint (not when `test/` imports `createApp`). The guard
compares `import.meta.url` against the **realpath** of `process.argv[1]`:
`pathToFileURL(realpathSync(process.argv[1])).href`. `realpathSync` is required because Node reports `import.meta.url`
in realpath form, so when the shim is launched by **absolute path from a symlinked directory** (the daemon's data dir,
e.g. macOS `/var → /private/var`), a naïve `pathToFileURL(argv[1])` comparison silently fails and `main` never runs.
(This was a real defect found wiring the daemon test; the realpath guard fixes it.)

### 4. Build = one committed, generated `shim.mjs`
`npm run build` = `esbuild src/shim.ts --bundle --platform=node --format=esm --target=node20 --outfile=shim.mjs`
(with a generated-file banner). The output `shim.mjs` is **committed** (a generated artifact, like the OpenAPI spec)
and is what `go:embed` (ADR-0036) and the curated image (ADR-0032) consume — so **building `funcd` needs no JS
toolchain**. A `just build-shim` recipe runs typecheck + `node --test` + the bundle, **gated on npm presence**; a
staleness check (rebuild → `git diff`) belongs to the runtime-enabled (node-present) CI job, **not** pure-Go `just ci`.
The shim's `node_modules` is gitignored; `package-lock.json` is committed to pin the build toolchain. The shipped
`shim.mjs` has `dependencies: {}` (Hono + `@hono/node-server` inlined) — the sandbox runs one file with no install,
so ADR-0030's "no runtime dependency" invariant holds; only the shim's **build** gains devDependencies.

## Temporary workarounds

- **`@types/node` v22 against a newer local node.** The shim targets `node20` and types against `@types/node@22`; if
  the platform's reference node major advances, bump `@types/node` in lockstep. Exit criterion: pin both to the
  curated image's node major when ADR-0032's image is the reference (P-V-2).
- **Staleness of the committed `shim.mjs` is checked only in the node-present lane.** Pure-Go `just ci` cannot detect a
  `src/shim.ts` edit that wasn't rebuilt. Exit criterion: the runtime-enabled CI job runs `just build-shim` and fails
  on a `git diff` in `shim.mjs` (same shape as `just generate`'s staleness gate, gated on node).

## Contracts

### Authoring contract (`shim/nodejs/src/types.ts`)
```ts
export interface CloudEvent<T = unknown> {
  id: string; source: string; type: string;
  specversion?: string; time?: string; datacontenttype?: string; subject?: string;
  data?: T; [key: string]: unknown;
}
export interface FunctionContext { log(...args: unknown[]): void; }
export type Handler<In = unknown, Out = unknown> =
  (context: FunctionContext, event: CloudEvent<In>) => Out | Promise<Out>;
```

### Shim API (`shim/nodejs/src/shim.ts`)
```ts
export function resolveHandler(mod: Record<string, unknown>, name: string): Handler; // shape-gate; non-function throws
export function createApp(handler: Handler): Hono;                                    // the §1 routes
export type { CloudEvent, FunctionContext, Handler };
// main(): load FUNCD_ARTIFACT → resolveHandler(FUNCD_HANDLER|"handle") → serve on node:http;
//         FUNCD_PORT>0 → bind 0.0.0.0:PORT ; else bind 127.0.0.1:0 + write FUNCD_PORTFILE.
//         exit 2 (no FUNCD_ARTIFACT) · exit 3 (shape error). Run-guard = realpath(argv[1]) === import.meta.url.
```

### Wire contract — **unchanged from ADR-0030 §1**
```
POST /                 CloudEvent → handler → (object→200 JSON · none→204 · throw→500 {error} · invalid JSON→400)
GET  /health/readiness 200 once handle resolved
GET  /health/liveness  200 while up
env: FUNCD_ARTIFACT · FUNCD_HANDLER (default "handle") · FUNCD_PORT | FUNCD_PORTFILE
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes (build) | esbuild, TypeScript, Hono, `@hono/node-server`, `@types/node` — **devDependencies** | MIT / Apache-2.0; bundled away |
| Consumes (runtime) | `node` + the one bundled `shim.mjs` | **zero** runtime deps in the sandbox |
| Produces | `shim/nodejs/shim.mjs` (committed, generated) | consumed by `go:embed` (ADR-0036) + curated image (ADR-0032) |
| Exposes | the typed authoring contract (`types.ts`) + the unchanged §1 wire contract | what authors import; what the platform launches |

## Implementation plan

> A working implementation already exists and is green — this plan documents what realizes the Contracts (the review
> gate verifies it).

1. **`shim/nodejs/src/types.ts`** — the authoring contract (above).
2. **`shim/nodejs/src/shim.ts`** — `resolveHandler`, `createApp` (Hono, §1), `main`, the realpath run-guard.
3. **`shim/nodejs/test/shim.test.ts`** — `node --test` over `createApp`/`resolveHandler` (`app.request()`); 7 cases
   covering the full §1 mapping + handler resolution.
4. **`shim/nodejs/package.json` / `tsconfig.json`** — scripts (`typecheck`, `test`, `build`), devDeps, strict TS.
5. **`npm run build`** → committed `shim.mjs`; **`just build-shim`** recipe (npm-gated: typecheck + test + bundle).
6. **`.gitignore`** — `node_modules/` (already present); commit `package-lock.json`.
7. **Verify**: `tsc --noEmit` clean; `node --test` 7/7; the bundle runs self-contained from an empty dir; **and every
   ADR-0030 Go scenario test passes unchanged** against the new bundle (the contract-preservation proof) — across all
   five launch paths (`internal/function`, `internal/artifact`, `pkg/funcd`, `cmd/funcd` embedded, `tests/e2e`).
8. **Definition of done**: shim self-tests + typecheck green; the bundle is self-contained (zero runtime deps); all
   ADR-0030 scenarios green against it; no new *runtime* dependency; devDeps Apache-2.0/MIT; no identity/path leak.

## Review checklist

- [ ] **Contract preserved**: every ADR-0030 scenario test passes unchanged against the new `shim.mjs` (object→200 ·
      none→204 · throw→500 · invalid→400 · readiness/liveness 200), across all five Go launch paths.
- [ ] **Self-contained**: the bundle runs by absolute path from a `node_modules`-free dir; `package.json`
      `dependencies` is empty; Hono is inlined.
- [ ] **Symlink-safe run-guard**: `main` fires when launched by absolute path from a symlinked dir (the daemon case);
      `test/` imports `createApp` without triggering `main`.
- [ ] **Typed authoring contract**: `types.ts` exports `Handler`/`CloudEvent`/`FunctionContext`; `tsc --noEmit` clean.
- [ ] **Build hygiene**: `shim.mjs` is the committed esbuild output (banner present); `go:embed` consumes it; building
      `funcd` needs no JS toolchain; `node_modules` gitignored, `package-lock.json` committed.
- [ ] **License + identity**: all devDeps MIT/Apache-2.0; no local username/path leak in any shim file.

## Consequences

- (+) **Typed on both sides**, same speed, same deployment: authors get `Handler`/`CloudEvent` types; the shim is typed
  Hono (`= node:http`) instead of open-coded `node:http`, so the deferred `context` SDK / health hooks are cheap to
  add — and the output is still one self-contained `shim.mjs` with zero runtime deps (cold-start + embed unchanged).
- (+) **A stack swap, not a behavior change**: ADR-0030 §1 + every seam are *proven* preserved by its own tests; the
  symlink-fragile entrypoint guard that broke the daemon's extracted-shim launch is fixed in passing.
- (−) **A JS build toolchain enters the repo** and `shim.mjs` is now generated — but gated: needed only to *regenerate*
  the shim, never to build/run `funcd`; pure-Go `just ci` is untouched; drift caught by `just build-shim` + the
  node-present staleness gate.

## Open questions

- **Native addons (`.node` binaries) in author bundles** — esbuild can't inline a compiled `bcrypt`/`sharp`; such
  functions need a pure-JS alternative or a curated image carrying the binary. Decide when a real workload needs one
  (a follow-up to ADR-0032's curated images).
- **Publishing `@funcd/shim-nodejs`** so authors `npm i -D` the types instead of vendoring — deferred until there's an
  external author audience (today the types live in-repo); a packaging follow-up.
- **Python shape (P-V-3)** keeps the §1 contract but resolves a `new()` factory + lifecycle hooks; whether it gets an
  analogous typed contract is its own ADR.

## References

- [ADR-0030](0030-function-execution-runtime-shim-node.md) — the runtime-shim contract + the original hand-written shim (§2 superseded here).
- [ADR-0032](0032-curated-runtime-images-container-execution.md) — curated images that bake the shim. · [ADR-0036](0036-daemon-execution-wiring.md) — `go:embed` of the shim.
- [ADR-0023](0023-eventing-core.md) — the CloudEvents envelope the `POST /` body carries. · [ADR-0025](0025-testing-strategy-e2e-harness.md) — the test taxonomy the shim self-test + node-gated lane extend.
- Hono <https://hono.dev> (MIT) · `@hono/node-server` (MIT) · esbuild <https://esbuild.github.io> (MIT) · TypeScript (Apache-2.0). Verified 2026-06-16.
