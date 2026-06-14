# Review report — ADR-0005 implementation (re-review #3)

- **ADR**: [ADR-0005 — API surface, code-first via huma](../adr/0005-api-surface-code-first-huma.md)
- **Phase**: implementation (ADR-0000 review gate #5, re-review after the client-deferral change)
- **Implemented by**: `deepseek-v4-pro`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` skill (`claude-opus-4-8`) — independent of the implementation
- **Realizes**: [FEAT-0000/F02](../feat/0000-feat-v1.md)
- **Supersedes verdict**: [adr-0005-implementation-deepseek-v4-pro-2.md](adr-0005-implementation-deepseek-v4-pro-2.md)

## Verdict: changes-requested — 1 Blocker (**immutability / process**), 0 Major, 1 Minor

The re-review #2 `adr` defect (oapi-codegen can't read huma's 3.1 spec) was resolved by **deferring the
client to P-R/F18** — the correct decision (my option 1). **But it was applied by editing ADR-0005's
frozen substance in place**, not by a superseding ADR. The code is clean and now conforms; the problem
is *how* it was made to conform.

## 🔴 Blocker — ADR-0005's frozen substance was amended in place · attribution: **process** (not deepseek's code)
ADR-0005 is `Reviewing` (frozen in substance — the immutability invariant allows only the
`Accepted → Reviewing` status bump). Yet multiple substance sections were rewritten, all marked
"**amended 2026-06-14**":
- **Scenarios** — `client-generated-from-spec` renamed to `spec-consumable-by-client` (`:59`).
- **Decision §5** — "Generated Go client (oapi-codegen)" rewritten to "Typed Go client deferred to
  P-R/F18" (`:164`).
- **Scope** — the generated client moved from *In* to *Out* (`:87`).
- **Alternatives** — a new chosen row "Defer to P-R/F18 … (amended 2026-06-14)" (`:125`).

The CLAUDE.md invariant is explicit: *"An Accepted ADR is frozen in substance. Context, Scenarios,
Decision, Contracts, Alternatives — none of it changes after acceptance … To change the decision, write
a new superseding ADR — never rewrite history."* The review-gate rule mirrors it: *any* change to
Decision/Scenarios/Contracts is a Blocker.

**This is not a code-quality finding** — deepseek's implementation is clean and conforms. It's a
**process** Blocker about the *mechanism*: a frozen ADR was rewritten instead of superseded. **Whose
call it is** decides the fix (see Recommendation): a deliberate, owned amendment (as the user did once
for ADR-0000) vs. a builder-autonomous edit (which the implement gate forbids).

## 🟡 Major — None.

## Minor — `model`
- **Round-trip request bodies remain `map[string]interface{}`** (`api_test.go` ×6, `client_test.go` ×5)
  rather than a typed `v1alpha1.Function` — passes forbidigo only via its `interface{}`-literal blind
  spot (ADR-0002's known limit). Carried over from re-review #2; non-blocking.

## ✅ Verified correct (keep it)
- **Code is green**: `golangci-lint` **0 issues**, `go build` + `go test` pass, **all 6 scenario tests**
  pass (incl. the legitimately-satisfied `spec-consumable-by-client` HTTP round-trip). The only `just
  ci` red is the tidy-gate — `go mod tidy` is a **no-op**, dirty only because the huma/chi deps are
  uncommitted (green on commit).
- huma code-first core, generated 3.1 spec, `api/fault` huma-free, ADR-0004 artifacts removed,
  `specgen` gitignored, MIT deps, identity clean — all intact.

## Definition of Done
The implementation conforms to the (amended) ADR and is clean — but the **"ADR substance unchanged"**
hygiene item **fails** (the substance was amended). ~9/10 hold; the miss is the immutability violation.

## Recommendation
**changes-requested — advance nothing** (ADR-0005 stays `Reviewing`). The deferral *decision* is right;
resolve the *mechanism*, which is the user's call:
1. **Own the amendment** — if this in-place edit was a deliberate, accepted deviation (as ADR-0000 was
   amended in place by choice), record it explicitly as such; then the implementation conforms + is
   clean, and a re-review passes once committed. *(Deviates from the supersede-don't-amend invariant —
   accept that consciously.)*
2. **Supersede properly** — restore ADR-0005 to its Accepted substance and write a superseding
   **ADR-0006** that narrows scope to defer the client; the current code then implements ADR-0006.

The by-the-book path is **2**; the pragmatic path (given the ADR is uncommitted and in-flight) is **1**.
Either way, no `model` rework is needed beyond the optional typed-body nit — deepseek's code is done.
