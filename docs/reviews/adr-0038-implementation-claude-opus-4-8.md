## Verdict: pass — 0 blockers, 0 majors  (ADR-0038 implementation, model: claude-opus-4-8)

The event-data contract (a JTD `eventSchema` exported from the artifact, validated by a `jtd`
engine bundled in the node shim, mismatch → 422 before the handler, opt-in) is implemented
faithfully and verified by running every check. Every Scenario has passing evidence; the
Review-checklist and generic DoD hold; the engine is MIT + pure-JS + `eval`-free + bundled (no
runtime dep); backward-compat is real across all five Go launch paths; identity grep is clean.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **ADR Contract shorthand vs. the realized re-export** · attribution: **adr** · The ADR Contract
  writes `export type { EventSchema } from 'jtd'`, but the `jtd` package exports its schema type as
  `Schema` (no `EventSchema` member — `node_modules/jtd/lib/schema.d.ts`). The implementation
  correctly honors the *intent* with `export type { Schema as EventSchema } from 'jtd'`
  (`shim/nodejs/src/shim.ts:22`), which is the only way to re-export that type under the name
  `EventSchema`. `npm run typecheck` exits 0, proving the alias resolves. This is ADR-prose
  shorthand, not a model error — no action needed; recorded for accuracy.

### ✅ Verified correct (keep it)
- **Shim typecheck**: `cd shim/nodejs && npm run typecheck` (tsc --noEmit) → exit 0.
- **Shim tests 11/11, 0 fail, 0 skipped**: `npm test` → exit 0. The 4 new contract scenarios all
  present and passing: `contract-valid` (matching event.data → 200), `contract-mismatch` (mismatch
  → 422 with `details.length > 0` and `called === false` — the handler-not-called assertion is
  real and un-weakened, `test/shim.test.ts:73-82`), `no-schema` (backward compatible → 200),
  `schema-shape-gate` (`resolveSchema` returns/undefined/throws on malformed, `:92-96`).
- **Bundle**: `npm run build` (esbuild) → exit 0; `jtd` is **inlined** into `shim.mjs`
  (13 matches for `isSchema`/`validate`/JTD markers); `package.json` has **no runtime
  `dependencies`** (`dependencies: null`), `jtd@0.1.1` is a devDep only.
- **Engine constraints**: `jtd@0.1.1` license **MIT**; the built `shim.mjs` contains **0** `eval(`
  and **0** `new Function` — `eval`-free as the ADR's sandbox-hardening driver requires.
- **Live bundle behavior** (example handler.mjs through the real shim.mjs): valid
  `{"data":{"hello":"x"}}` → **HTTP 200** `{"echoed":{"hello":"x"},"by":"funcd"}` with the
  "handling event" log line; invalid `{"data":{"hello":123}}` → **HTTP 422**
  `{"error":"event data does not match the contract","details":[{"instancePath":["hello"],...}]}`
  with **no** handler log line (the handler never ran).
- **Contract-travels**: the example's `eventSchema` is inlined into
  `examples/js/hello-world/handler.mjs` by esbuild (`grep optionalProperties` → 1 match after
  rebuild); shipped + digest-pinned with the code, no registry, no platform schema field.
- **Contract-e2e**: `go test ./tests/e2e/ -run 'EventDataContract' -count=1` → exit 0 (1.122s on
  re-run). Deploys a contracted function via the real `funcdcli push`→`apply`→data plane and gets
  **200 then 422** end-to-end (`journey_test.go:118-150`), through the shared `execPlatform` rig.
  (First run failed transiently with `dial tcp …: can't assign requested address` — ephemeral-port
  exhaustion from the 100ms `requireCLIPhase` poll spawning a fresh CLI per tick; re-run green.
  **env**-attributed, not a logic defect.)
- **Opt-in / backward compatible — all five Go launch paths green with the new shim**:
  `internal/function` (Shim|Timer) exit 0, `internal/artifact` (SeamNode) exit 0, `pkg/funcd`
  (DataPlane) exit 0, `cmd/funcd` (DaemonExecutes) exit 0, `tests/e2e` (UserJourney) exit 0.
  No `eventSchema` → validation inert (the `schema !== undefined` guard, `shim.ts:67`).
- **go vet** `./tests/e2e/` → exit 0; `go build ./...` → exit 0.
- **Contracts honored**: `resolveSchema(mod): Schema | undefined` (undefined absent, throws
  malformed via JTD `isSchema`), `createApp(handler, schema?)` validates `event.data` → 422
  `{error, details}`, `main` wires `resolveHandler` + `resolveSchema` → `createApp` and exits 3 on
  any shape error (`shim.ts:99-106`) — exactly the ADR's §2/§3 shape-gate. 422 body shape matches.
- **Reject-before-invoke is structurally sound**: validation runs *before* the handler try-block
  (`shim.ts:67-72`), `return`-ing on mismatch — the handler is unreachable on a bad event.
- **Hygiene**: identity grep over all changed source (shim.ts, shim.test.ts, package.json, the
  example handler.ts/test, journey_test.go, demo/journey.sh) → **0** hits for
  `green-0-rabbit`/`/Users/`/`green-0-rabbit`; `shim.mjs` and the example `handler.mjs` bundles → 0 hits.
  F26 feat row at `reviewing`; ADR at `Reviewing` (the only edit beyond the authored doc is the
  `Accepted → Reviewing` status bump — ADR-0038 is a new, untracked file so there is no prior
  baseline whose substance could have been mutated; the documented judge-fold + acceptance are
  consistent with its lifecycle).
- **No blueprint sync owed**: the blueprint describes the CloudEvents handler contract abstractly
  and says nothing about event-data validation/JTD/422; ADR-0038 is additive + opt-in and refines
  ADR-0037's shim without contradicting any blueprint statement. Confirmed no sync needed.

### Definition of Done
**11 / 11 hold.** ADR Review-checklist (7): opt-in/backward-compat ✅, reject-before-invoke 422
`{error,details}` handler-not-called ✅, shape-gate exit 3 ✅, engine MIT/pure-JS/no-eval/bundled
✅, contract-travels (inlined by esbuild) ✅, end-to-end 200→422 ✅, no identity/path leak ✅.
Generic DoD (applicable, 4): full suite green (shim 11/11 + e2e + 5 Go paths, by sub-checks — not
`just ci`, which fails by design on the tracked-but-uncommitted `shim.mjs` per the documented
pitfall) ✅, every scenario un-skipped + passing none weakened ✅, real behavior no stubs ✅,
contracts honored + conventions ✅. Misses: none. The one Minor is **adr**-attributed ADR-prose
shorthand, not a DoD miss.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0038 (implementation) → pass, 0/0/1, 0 model-attributed,
DoD 11/11. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. The implementation is complete, evidence-backed, and conforms to the ADR's Contracts,
Scenarios, Review checklist, and Definition of Done. The single Minor is an ADR-prose shorthand
(`EventSchema` vs `jtd`'s `Schema`) correctly resolved in code — it loops to a future ADR-prose
correction if ever, never to the builder. Advance ADR-0038 `Reviewing → Implemented` and F26
`reviewing → implemented`.
