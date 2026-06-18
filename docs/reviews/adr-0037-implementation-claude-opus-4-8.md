# Review — ADR-0037 (TypeScript + Hono runtime shim) · implementation · model: claude-opus-4-8

## Verdict: pass — 0 blockers, 0 majors  (ADR-0037 implementation, model: claude-opus-4-8)

The reference Node shim is now a typed TypeScript + Hono source bundled by esbuild to the same
single self-contained `shim/nodejs/shim.mjs`. Every claim in the ADR was *executed*, not eyeballed.
The load-bearing claim — ADR-0030 §1's wire contract is preserved across all five Go launch paths,
including the embedded-bundle daemon path that originally caught the symlink run-guard bug — holds.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

- **`just build-shim` is not actually npm-presence-gated.** · attribution: `model` (minor) ·
  evidence: `justfile` recipe is `cd shim/nodejs && npm ci && npm run typecheck && npm test && npm run build`
  with no `command -v npm` guard. The ADR §4 / Review-checklist describe the recipe as "gated on npm
  presence" (skip-when-absent). As written it *fails* if npm is absent rather than *skipping*.
  Non-blocking because the real invariant — "building funcd needs no JS toolchain" — is satisfied a
  different way: `just ci` (`ci: tidy generate` → fmt/lint/test/`go build`/`go mod verify`) does **not**
  depend on `build-shim`, so pure-Go CI never invokes npm at all (verified: `grep build-shim` shows no
  `ci` dependency). The shim is consumed only as the committed `go:embed`'d `shim.mjs`. Fix (optional):
  prefix the recipe with a `command -v npm` skip-guard to match the doc wording, or soften the ADR
  wording in a future touch (the ADR is frozen, so this is a `build-shim` recipe nicety, not an ADR edit).

- **`package-lock.json` is untracked in the working tree** (`git status` → `??`). · attribution:
  `model` (minor, expected) · The ADR §4 says it must be committed; it exists and is staged-as-new
  alongside the rest of the new shim files (`src/`, `test/`, `package.json`, `tsconfig.json`), so it
  will be committed with the implementation. Flagged only so the commit does not accidentally drop it.

### ✅ Verified correct (keep it)

- **Shim self-tests + typecheck green.** `npm run typecheck` (tsc --noEmit) → exit 0. `npm test`
  (`node --test --experimental-strip-types test/*.test.ts`) → **7 pass / 0 fail / 0 skipped**.
  `npm run build` (esbuild → shim.mjs) → exit 0 (`shim.mjs 97.4kb`). (scenarios `shim-self-test`,
  `typed-authoring-contract`.)
- **Bundle is self-contained (zero runtime deps).** `package.json` has **no** `dependencies` key
  (`grep -c '"dependencies"'` → 0). `shim.mjs` inlines Hono + `@hono/node-server` (19 `node_modules/hono…`
  inlined segments; **no** remaining `import … from 'hono'`). Ran the committed bundle by **absolute
  path from an empty temp dir** (no `node_modules`): it bound `127.0.0.1:49205`, wrote its portfile,
  `POST /` → 200, `GET /health/readiness` → 200. (scenario `bundle-is-self-contained`.)
- **No bundle drift.** A fresh `npm run build` reproduces the committed working-tree `shim.mjs`
  byte-for-byte (`diff -q` → identical) — the committed generated artifact matches its source.
- **Contract preserved across all five Go launch paths** (the contract-preservation proof,
  scenario `shim-contract-unchanged`):
  - `go test ./internal/function/ -run 'Shim|Timer' -count=1` → `ok` (exit 0)
  - `go test ./internal/artifact/ -run 'SeamNode' -count=1` → `ok` (exit 0)
  - `go test ./pkg/funcd/ -run 'DataPlane' -count=1` → `ok` (exit 0)
  - `go test ./cmd/funcd/ -run 'DaemonExecutes' -count=1` → `ok` (exit 0) — **the embedded `go:embed`
    bundle**; its pass specifically proves the **realpath run-guard fix** (the symlinked-data-dir bug).
  - `go test ./tests/e2e/ -run 'UserJourney' -count=1` → `ok` (exit 0)
  - `go build ./...` → exit 0; `go test ./shim/...` → `[no test files]` exit 0 (`embed.go` compiles).
- **Symlink-safe run-guard present.** `src/shim.ts:92-95` —
  `pathToFileURL(realpathSync(process.argv[1])).href === import.meta.url`; tests import `createApp`
  from `../src/shim.ts` without triggering `main` (7-case suite runs server-lessly via `app.request()`).
- **Contracts match `src/` exactly.** `types.ts` exports `CloudEvent<T>` / `FunctionContext` /
  `Handler<In,Out>` verbatim per ADR Contracts. `shim.ts`: `resolveHandler` =
  `mod[name] ?? mod.default?.[name] ?? mod.default`, non-function throws; `createApp` = the three §1
  routes with object→200 JSON · null/undefined→204 · throw→500 `{error}` JSON · invalid JSON→400 ·
  readiness/liveness→200; `main` = artifact-missing→exit 2, shape-error→exit 3, `FUNCD_PORT>0`→bind
  `0.0.0.0:PORT` else `127.0.0.1:0` + write `FUNCD_PORTFILE`.
- **Build hygiene.** `shim.mjs` carries the generated-file banner
  (`// GENERATED from shim/nodejs/src/shim.ts by 'npm run build' (esbuild). Do not edit by hand.`);
  `embed.go` `//go:embed shim.mjs`; `node_modules/` gitignored (`git check-ignore` confirms).
- **Licenses + identity.** Installed devDeps: hono MIT, `@hono/node-server` MIT, esbuild MIT,
  typescript Apache-2.0, `@types/node` MIT — all MIT/Apache-2.0. Identity grep across shim `*.ts`,
  `*.json`, `*.mjs`, `*.go` (excluding `node_modules`): **no** `green-0-rabbit` / `/Users/` / `green-0-rabbit`
  leak; `package-lock.json` clean of local-path/username (registry URLs only, allowed).
- **ADR substance unchanged.** `git diff HEAD -- docs/adr/0037-*.md` empty — the builder made no
  substance edit (status `Reviewing` set in a prior commit). F12 feat row already `implemented`
  (multi-ADR row) and already links ADR-0037.

### Definition of Done

**11 / 11 hold.** ADR Review-checklist (6): contract-preserved ✓, self-contained ✓, symlink-safe
run-guard ✓, typed authoring contract + `tsc` clean ✓, build hygiene (banner/embed/gitignore/lockfile) ✓,
license + identity ✓. Generic DoD applicable items (5): every scenario has a named passing test
(7/7 + the 5 Go launch paths) ✓, real behavior no stubs ✓, contracts honoured ✓, tree matches the
ADR's source layout (`src/types.ts`, `src/shim.ts`, `test/shim.test.ts`, `package.json`, `tsconfig.json`,
`shim.mjs`, `just build-shim`) ✓, hygiene + tracking (ADR `Reviewing` substance-unchanged, feat row
consistent) ✓. The two Minors do not fail any item — `build-shim`'s gating wording is cosmetic given
`just ci` is JS-toolchain-free, and the lockfile is staged-as-new.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0037 (implementation) → pass, 0/0/2, 2 model-attributed (both Minor),
DoD 11/11. See docs/reviews/model-scorecard.md.

### Recommendation

**Sign off.** DoD fully met, no Blockers/Majors; the contract-preservation proof and the embedded-bundle
realpath-fix are both demonstrated by passing Go tests. Stamp ADR-0037 `Reviewing → Implemented`
(F12 feat row stays `implemented` — multi-ADR row, do not walk back). The two Minors are optional
polish for the next routine touch: add a `command -v npm` skip-guard to `build-shim` (or soften the
"gated" wording in a future doc pass), and ensure `package-lock.json` is included in the commit.
