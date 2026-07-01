# ADR-0090 Judge Report — Mandatory single I/O schema (void as `{"type":"null"}`)

**Verdict**: Sound, well-scoped decision that faithfully preserves the ADR-0058 wire and the ADR-0059 OCI
mechanism while making contracts mandatory — but it under-specifies two mechanics (how a Python author *declares*
a void **input**, and whether the void side keeps its hand-baked validator or compiles `{"type":"null"}` from the
schema), and those gaps must be closed before an implementer can hit the scenarios green.

**Judged against**: ADR-0000 (template/lifecycle); FEAT-0001 row F60; blueprint.md (CloudEvents-only contract
→ 204, library-first, JSON-Schema profile); the two frozen ADRs it touches (0058, 0059) + related 0060/0089/0031;
and the real code it changes — `internal/artifact/artifact.go:52-76`, `cmd/funcdctl/cli.go:189-241`,
`shim/python/src/funcd_shim/build.py:30-78`, `shim/python/src/funcd_shim/_poolworker.py:77-97`.

**ADR status**: Proposed.

## Goal alignment

Serves F60 exactly: one mandatory `{dialect, input, output}` document, both keys always present, void =
`{"type":"null"}`, single `--schema` flag, `push` refuses a contract-less function, examples migrated. Held at one
altitude — the contract **surface** — and correctly defers admission-time enforcement and the Node-bundle path
(Scope-out, Open questions). The frozen-ADR discipline is honored: `docs/adr/0058-*.md:14-17` and
`docs/adr/0059-*.md:4-7` carry **only** the permitted `Superseded in part by` back-link; no substance changed.
The supersede *relationship* is right — 0059 loses its two-flag opt-in surface (a full-topic supersede of that
slice), 0058 loses only its optional/unchecked path + gains explicit void (a refinement), and both keep their
validator-runtime / OCI cores. Cross-checked against the wire (0058: 204/422/500), the OCI mechanism (0059: blob +
`dev.funcd.contract.v1` annotation, `internal/artifact/artifact.go:43-49,114-121`), and 0060's funcd-owns-compilation
invariant — no contradiction with any of them.

## Strengths — keep as-is

- **The void-schema-vs-204-wire split** (Decision 2; Alternatives "void = 200 with null body"). `{"type":"null"}`
  is the *schema* (the value is null), 204 is the *wire* (a null result carries no body). This is the subtle,
  correct reconciliation and a later editor may wrongly "fix" it to a 200-with-null-body handler — **it must not**.
  It is airtight against the real runtime: `_poolworker.py:88-96` validates the result against the output validator
  *then* maps `result is None → 204`, so a null-typed output both passes `{"type":"null"}` and replies 204. Protect
  this.
- **Rejecting `false` / `{}` / a custom `"void"` type** each with a distinct, real losing reason
  (`0090:89-94`) — `{}` is the ADR-0058 `Json` escape hatch's opposite intent, `false` is "impossible", custom
  `"void"` breaks the eval-free compiler contract. Genuinely reasoned, not strawmen.
- **Single-source resolution order** (bundle `__funcd_contract.json` → `--schema` → code-derived) unifies the
  ADR-0089 bundle path and the single-file path onto one blob — no divergent surfaces.
- **Owning the breaking change honestly** (Consequences; "Temporary workarounds: None") — no opt-out, migrate the
  curated examples in-repo. Defensible: an opt-out would re-introduce the unchecked hole F60 exists to close.

## Findings

### Blockers

None.

### Major

- **Void *input* is introduced but the author-declaration mechanic is unspecified — no path in the real build to
  emit it.** Scenario `void-input-schema-explicit` (`0090:49-50`) and Decision 2 (`0090:110`) require an
  `input:{"type":"null"}` for "takes nothing", but `build.py` today has **only** a void-*output* concept
  (`build.py:65` `void_output = "FuncOutput" in ns and ns["FuncOutput"] is None`) — there is **no** `FuncInput = None`
  / void-input marker, and "no `FuncInput` declared" currently means *unchecked input* (`build.py:63,70`,
  `_poolworker.py:77`), which mandatory contracts now forbid. So today's most common function (declares no input)
  maps to *nothing* under ADR-0090: is a bare handler's input void, or is it a push error? The ADR must state the
  author surface for void input (e.g. `FuncInput = None`, mirroring `FuncOutput = None`) and that
  `_targets_contract_only`/the strip step handles it — otherwise the implementer cannot produce the void-input side
  the scenario demands. **Goal impact**: the F60 "both keys always present" guarantee is unreachable for input
  without a declared void marker. **Fix**: add a Decision/Contract line giving the void-input author surface and the
  `build.py` emission rule for it, symmetric to void output.

- **The void side's runtime validator vs. the ADR-0060 integrity invariant is left ambiguous.** ADR-0090 says
  build.py "changes only in that it now emits the `{"type":"null"}` schema … the existing baked void validator
  (`d is None`) already matches" (`0090:112-116,160`). But the existing void validator is a **hand-written string**
  (`build.py:34-37,74-75` `_VOID_VALIDATOR`), *not* compiled from a schema — whereas every other side is
  fastjsonschema-compiled *from the gated schema* (`build.py:70-73,211-227`), which is exactly ADR-0060's
  "funcd-owns-compilation / validator ≡ advertised schema" invariant (`docs/adr/0060-*.md:16-20`). Once the void
  side *advertises* `{"type":"null"}`, keeping a hand-baked validator makes the void side the one place where the
  runtime validator is not compiled from the advertised schema — a silent hole in the invariant ADR-0090 claims to
  keep. **Goal impact**: "advertised == enforced" (the reason 0059/0060 matter for policy) has a void-shaped
  exception. **Fix**: state explicitly whether the void side now bakes `_validator_source({"type":"null"}, …)` (the
  clean, invariant-preserving choice — retire `_VOID_VALIDATOR`) or deliberately keeps the hand-baked validator with
  a one-line justification that its behavior is provably identical to a compiled `{"type":"null"}`.

### Minor

- **`--schema` gating vs. the real `contract.Check` shape is stated but thin.** Decision 3 / Contracts say `--schema`
  holds one `{input, output}` doc and "the funcd-profile gate (`contract.Check`) validates **each** side"
  (`0090:118-122,149-152`). The real `gateContract` (`cli.go:229-241`) calls `contract.Check(schema)` on **one**
  schema; the new path must *parse the `--schema` document, then call `contract.Check` per side* (and reject
  `{"type":"null"}`? — note the void schema must be **accepted** by the profile gate, which today only sees
  generator output). The direction is coherent but the ADR should say the resolver parses the doc and profile-checks
  `input` and `output` independently, and that `{"type":"null"}` is a profile-valid side. **Fix**: one Contract
  sentence pinning "parse doc → `contract.Check` each side; `{"type":"null"}` is profile-accepted."

- **`VoidSchema = ` backtick literal is valid Go but the profile gate's acceptance of it is unverified.** `const
  VoidSchema = `{"type":"null"}`` (`0090:137`) compiles fine. But nothing in the ADR confirms `internal/contract`'s
  `Check` *accepts* `{"type":"null"}` (the profile is a bounded allow-list; a naked `{"type":"null"}` document with
  no `properties`/`type:object` may or may not be in-profile). **Fix**: add a review-checklist line "the profile gate
  accepts `{"type":"null"}` as a valid side" so the implementer verifies it rather than assuming.

- **Node void emission is named but not specified.** The Implementation plan lists "Node shim build — the analogous
  void-schema emission" (`0090:183`) but `shim/nodejs/src/build.ts:27-29` returns `outputSchema: null` for an absent
  `FuncOutput` and has no void marker. The ADR scopes the "JS/Node bundle contract path beyond esbuild" **out**
  (`0090:68`) yet asks the plan to touch Node void emission — a mild scope/plan tension. **Fix**: either move Node
  void emission fully out (Python-only this ADR, Node as a follow-up) or in — not half-in.

### Nits

- Example blast-radius is larger than "the curated examples" implies: `examples/{js,python}/{hello-world,
  kv-counter,log-burst,catalog-quack,fn-to-fn,s3-roundtrip}` — several declare no contract today. The migration is
  real work; the DoD ("all curated examples push green") should name that a handler with neither side declared now
  needs an explicit `{"type":"null"}`/`{"type":"null"}` pair, so none is silently missed. (`0090:124-125,193`.)
- `0090:33` cites `artifact.go:57` for the `(nil,nil)` path — correct (`ContractBlob` returns `nil,nil` at
  `artifact.go:57-59`); good, evidence-accurate.

## Template & scenario conformance

All ADR-0000 sections present and in order (Header, Context & Need, Scenarios, Scope, Constraints, Alternatives,
Decision, Temporary workarounds, Contracts, Implementation plan, Review checklist, Consequences, Open questions,
References). Concise, no bloat. Every scenario maps to a named test in the Test plan (`0090:187-193`):
`contract-mandatory-push-gate`, `both-keys-required`, `void-output-schema-explicit`, `void-input-schema-explicit`,
`single-schema-surface`, `bundle-schema-satisfies-gate`, `inspect-always-has-contract` (covered by
`single-schema-surface`/`examples-migrated` inspection assertions), `examples-migrated`. The one load-bearing
external claim — JSON Schema 2020-12 `{"type":"null"}` is the standard null-only assertion compiled natively by
AJV-standalone and fastjsonschema, and null-output→204 (not 200-with-null-body) is consistent with the kept 0058
wire — is correct and matches the real runtime (`_poolworker.py:88-96`).

## Recommendation

**Accept after the two Majors are folded** — they are specification gaps, not design flaws: (1) give the void-input
author surface + `build.py` emission rule (symmetric to void output), and (2) resolve the void-side validator
question against ADR-0060's integrity invariant (prefer compiling `{"type":"null"}` and retiring `_VOID_VALIDATOR`).
Fold the Minors (parse-and-check-each-side wording; a checklist line that the profile gate accepts `{"type":"null"}`;
Node-void in-or-out). The core decision — mandatory single schema, void as `{"type":"null"}` schema with the 204
wire unchanged, single `--schema`, OCI mechanism kept, examples migrated, no opt-out — is sound and should be kept
intact; in particular the schema-vs-wire split must survive any later edit.
