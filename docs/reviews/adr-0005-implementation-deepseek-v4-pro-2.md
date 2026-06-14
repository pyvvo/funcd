# Review report — ADR-0005 implementation (re-review #2)

- **ADR**: [ADR-0005 — API surface, code-first via huma](../adr/0005-api-surface-code-first-huma.md)
- **Phase**: implementation (ADR-0000 review gate #5, re-review after rework)
- **Implemented by**: `deepseek-v4-pro`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill (`claude-opus-4-8`) — authored/judged the ADR but did not write
  or rework the code; independent of the implementation
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)
- **Supersedes verdict**: [adr-0005-implementation-deepseek-v4-pro.md](adr-0005-implementation-deepseek-v4-pro.md) (changes-requested)

## Verdict: changes-requested — 1 Blocker (**`adr`-attributed**), 0 Major, 1 Minor

The rework is **excellent** — every `model` finding from review #1 is fixed (lint is **0 issues**, the
`specgen` binary is gitignored, the client config + 6th scenario were added). But it surfaced a genuine
**ADR defect**: ADR-0005 requires oapi-codegen to generate the client from huma's **OpenAPI 3.1** spec,
and **oapi-codegen does not support 3.1**. The implementer correctly could not produce the client and
substituted a documented consumability test. That blocker is the **ADR's** fault, not the model's — it
loops to a superseding/amending ADR, and the model's score is essentially clean.

## Verification run (captured)

| Check | Result |
|---|---|
| `golangci-lint` (via `just ci`) | **0 issues** — the review-#1 forbidigo/unused/staticcheck Blocker is fixed |
| `go build` / `go test ./...` | green; **6/6 scenario tests pass** |
| `just ci` | exit 1 — **only** the tidy-gate: `go.mod`/`go.sum` "not tidy". Verified `go mod tidy` is a **no-op** (genuinely tidy; red only because the huma/chi deps are uncommitted — the ADR-0002 situation, green on commit) |
| `specgen` binary | **gitignored** ✓ |
| `api/fault` | huma-free ✓ (M1 still honored) |

## 🔴 Blocker — the oapi-codegen client cannot be generated from huma's 3.1 spec · attribution: **`adr`**
ADR-0005 is internally contradictory:
- **C4 / `controlplane.go:133`** — huma emits `OpenAPI: "3.1.0"`.
- **Decision §5 / Scope / scenario `client-generated-from-spec`** — oapi-codegen generates the client
  from that spec.
- **But oapi-codegen does not support OpenAPI 3.1** (the same fact ADR-0004's research recorded;
  documented in `client_test.go:20-23`, citing oapi-codegen #373).

So no `generated/client.gen.go` exists; `TestClientGeneratedFromSpec` instead boots the huma server and
round-trips a `Function` over raw HTTP, decoding into `v1alpha1.Function` — proving the spec is
**consumable** but **not** that oapi-codegen produced a client. The scenario is satisfied in spirit, not
as written. This is an **ADR defect** (I authored *and* judged it; the judge gate missed it), not a
model defect — the implementer did the right thing.
- **Fix (superseding/amending ADR — small):** either (a) configure huma to emit **OpenAPI 3.0.3**
  (`controlplane.go:133`) so oapi-codegen *can* generate the client; or (b) **drop the oapi-codegen
  client and defer the SDK client to P-R/F18** (its rightful owner — it can use a 3.1-capable path).

## 🟡 Major — None.

## Minor — `model`
- **Round-trip request bodies are `map[string]interface{}`** (`api_test.go:20-30`, `client_test.go:34`)
  rather than a marshaled typed `v1alpha1.Function`. This passes forbidigo only because its `\bany\b`
  rule can't see the `interface{}` *literal* (ADR-0002's known heuristic blind spot) — it meets the
  letter of §4 but not its spirit. The responses *are* decoded into `v1.Function` (good); prefer the
  typed object for the request too. Non-blocking.

## ✅ Verified correct (keep it)
- **All review-#1 `model` findings fixed**: lint 0, binary gitignored, client config + the 6th scenario
  added. Clean, responsive rework.
- **The code-first core works**: huma on chi, the spec generated as OpenAPI 3.1 with a `DO NOT EDIT`
  marker by `specgen` (wired into `just ci`), 5 genuinely-satisfied scenarios (spec-generated,
  spec-reflects-go-shape, typed-roundtrip, error-is-problem-json, openapi-doc-served), `api/fault`
  huma-free (judge M1 honored), ADR-0004 artifacts removed, MIT deps, identity clean.

## Definition of Done
~8/10 hold. The misses — the generated client + its scenario (round-trip), and the unused oapi-codegen
dep — all trace to the **`adr`** 3.1/oapi-codegen contradiction, **not the model**. The only `just ci`
red is the benign tidy-gate (uncommitted deps).

## Recommendation
**changes-requested — advance nothing** (ADR-0005 stays `Reviewing`). The blocker is `adr`-attributed,
so it loops to **a superseding/amending ADR**, not back to the builder: decide between huma→3.0.3 (keep
oapi-codegen) or deferring the client to P-R. The implementation itself is done and high-quality; once
the client decision is made, this is a quick close. **No `model` rework needed** beyond the optional
typed-body nit.
