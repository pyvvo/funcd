# ADR-0123: Runtime-compiled I/O validators (schema-only artifact)

- **Status**: Implemented
- **Date**: 2026-07-11 (**Implemented 2026-07-11**; **Accepted 2026-07-11** — adr-judge: no Blockers. Applied M1 (single-file schema delivered as a
  `.funcd-contract.json` **dotfile sidecar**, isolated from `entries[0]` handler resolution), m1 (`VerifyBundleContract`
  now also **adds** per-side `contract.Check`), m2 (the ADR-0060 supersession scope clarified — the *integrity
  invariant* is **preserved**, only the build→worker compile relocates), m3 (compile **reordered ahead** of the handler
  import), m4 (codegen cost = a self-cold-start-DoS, folded into the budget). Split out of ADR-0122 on the prior judge's
  one-topic-at-one-altitude ruling.)
- **Deciders**: green-0-rabbit
- **Tags**: contracts, runtime, shim, security
- **Realizes**: FEAT-0001/F88
- **Relates to**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (the pure-Go `funcdctl` that depends on
  this) · [ADR-0059](0059-contract-as-oci-metadata.md) (the schema blob delivered to the worker) ·
  [ADR-0071](0071-python-runtime-ships-fastjsonschema.md) (fastjsonschema ships) ·
  [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (pool — warm-up amortization) ·
  [ADR-0089](0089-python-function-dependency-bundling.md) (bundle materialization) ·
  [ADR-0090](0090-mandatory-single-io-schema.md) (mandatory both-sides — kept)
- **Supersedes (in part)**:
  [ADR-0060](0060-contract-validator-generation.md) — supersedes **Decision 1** (the baked `__funcd_validate_*`
  callable) and the **build-time compile/bake** of Decisions 2–3: the artifact no longer carries a baked validator; the
  shim compiles it from the embedded schema. It **preserves** Decision 3's *integrity invariant* (**funcd owns the
  compilation → advertised == enforced**, now enforced by compiling the pinned schema **at the worker** rather than at
  build) and Decision 4 (**gate-before-use**).
  [ADR-0058](0058-contract-codegen-from-code-types.md) — its **eval-free-in-the-sandbox** constraint and
  `eval-free-runtime` scenario (a schema-compile now runs in the worker — see the bounded-reversal argument below).

## Context & Need

ADR-0122 makes `funcdctl` a self-sufficient pure-Go binary: it bakes a **schema-only** contract from `funcdctl.yaml`
with **no language toolchain at push**. That removes the step (ADR-0060) that used to *precompile* the runtime
validator (AJV-standalone for Node, `fastjsonschema.compile_to_code` for Python) and bake it into the artifact. So a
decision is forced: **with no baked validator, how does the worker validate I/O?**

**Purpose.** Decide the validator's *production point*: the shim **compiles the validator from the contract's embedded
JSON Schema at worker warm-up** and reuses it for the worker's life. Who this serves: the runtime shims (python314,
nodejs22) that enforce the ADR-0058/0090 wire (input→422, output→500, void→204). This ADR owns only the
runtime/shim/artifact-delivery altitude; the manifest + `funcdctl types` DX is ADR-0122.

This **reverses two Implemented-ADR properties** — ADR-0060's *precompile-at-build* and ADR-0058's
*eval-free-in-the-sandbox*. The reversal is bounded and safe (the argument is on the page, below), which is why it is a
supersession, not an accident.

## Scenarios

- **scenario: runtime-compiles-validator** — Given a pushed function whose artifact carries **only the schema** (no
  baked validator), When a worker warms up, Then the shim compiles the validator from the embedded schema, caches it,
  and enforces it: bad input → **422** (handler not called), bad output → **500**, a void side → **204**.
- **scenario: schema-delivered-single-file** — Given a **single-file** function (not a bundle) with a contract, When a
  worker starts, Then the materializer has placed the exact ADR-0059 contract-blob bytes where the shim reads them, so
  the function validates — it does **not** run un-validated (no fail-open).
- **scenario: schema-tamper-evident** — Given a pushed artifact, When the worker compiles its validator, Then the bytes
  it compiles are the **same digest-pinned** contract blob that `funcdctl inspect` advertises — advertised == enforced.
- **scenario: eval-over-trusted-input** — Given the shim compiles a schema at init, When it runs, Then the compile
  happens **before any handler code is imported**, over a schema already `contract.Check`-gated and digest-pinned — the
  code-gen input is funcd-trusted, never attacker-supplied.
- **scenario: bundle-gate-relaxed** — Given a bundle carrying a valid `{input, output}` contract but **no** baked
  `__funcd_validate_input`/`__funcd_validate_output`, When `funcdctl push`, Then `VerifyBundleContract` succeeds; a
  bundle whose contract is missing a key or fails `contract.Check` still fails `fault.Invalid`.
- **scenario: cold-start-within-budget** — Given a cold worker (empty pool, scale-from-zero), When it warms up, Then
  the added validator-compile time is within the benchmarked budget; if a future change breaches it, the named fallback
  applies.

## Scope

**In**: the schema-only artifact (no baked validator); the shim's **warm-up compile** from the embedded schema (Python
`fastjsonschema.compile`, Node `ajv.compile`); the **nodejs22 runtime shipping AJV**; **delivering** the ADR-0059
contract blob into the worker for **single-file and bundle** functions; **relaxing `VerifyBundleContract`** (drop the
validator-symbol check); the **cold-start benchmark + fallback**; the bounded eval-free-reversal argument.

**Out**: the `funcdctl.yaml` manifest, `Function`-CRD compile, and `funcdctl types` (ADR-0122); any change to the
on-wire codes or the `{dialect,input,output}` blob shape (ADR-0058/0059/0090 — unchanged); the esbuild builder and the
OCI plugin (deferred).

## Constraints & Decision drivers

- **Eval-free was a deliberate ADR-0058 property** (no runtime `new Function`/`eval` in the sandbox) — reversing it must
  be *bounded*: compile once at init, over a `contract.Check`-gated + digest-pinned schema, **before** untrusted handler
  code loads; never compile attacker-influenced input at request time.
- **advertised == enforced** (ADR-0060) must survive: the worker must compile the *exact* bytes `inspect` advertises.
- **No fail-open**: a contracted function that reaches a worker without its schema must **fail closed**, never validate
  nothing.
- **Scale-to-zero is a design pillar** — the added cold-path compile must be **measured** and bounded, with a fallback.
- Apache-2.0/MIT deps: **AJV** (MIT); **fastjsonschema** (BSD-3) already ships (ADR-0071).

## Alternatives considered

- **A — keep ADR-0060 precompiled validators via a push-time language toolchain/container** (`funcd-py`/`funcd-js`).
  *Pros*: preserves eval-free-in-sandbox and zero warm-up compile. *Cons*: reintroduces a language toolchain/container
  (docker-shaped) at push — defeats ADR-0122's pure-Go, plugin-free, containerd-native goal. **Rejected** (the decider
  chose the pure-Go path with eyes open to the eval-free trade).
- **B — runtime-compile from the embedded schema (chosen).** *Pros*: no push-time toolchain; funcdctl stays pure Go;
  advertised == enforced holds by construction (validator derived from the pinned schema). *Cons*: a bounded eval-free
  reversal; a per-warm-up compile; the Node worker ships full AJV. **Accepted** with the bounded-reversal argument.
- **Hybrid — precompile when a toolchain is present at push, else runtime-compile.** *Pros*: eval-free in the common/CI
  case. *Cons*: two validator paths to test; `advertised == enforced` must hold across both. **Held as the named
  fallback** if the cold-start benchmark ever breaches budget (Decision 7), not V1's default.
- **Deliver the schema via the bundle file only** (skip single-file). **Rejected** — single-file contracted functions
  would fail-open (M2); the blob must reach *every* worker.

## Decision

1. **Schema-only artifact.** The pushed artifact carries the `{dialect, input, output}` contract blob (ADR-0059,
   unchanged) and **no** baked `__funcd_validate_*` callable.
2. **Warm-up compile.** At worker init the shim reads the delivered contract schema and compiles a validator —
   Python `fastjsonschema.compile(schema)` (already shipped, ADR-0071), Node `ajv.compile(schema)` — caches it for the
   worker's life, and enforces input→**422** / output→**500** / void→**204** (wire byte-identical to ADR-0058/0090).
3. **nodejs22 ships AJV** (MIT) in the runtime image (was: only an inlined standalone validator). Python already ships
   `fastjsonschema` (ADR-0071), now used in its compile-at-init mode.
4. **Schema delivery (advertised == enforced, no fail-open).** The materializer writes the **exact ADR-0059
   contract-blob bytes** into the worker and sets `FUNCD_CONTRACT_PATH` to them, for **both** single-file and bundle
   functions; the shim compiles **those** digest-pinned bytes. **Handler and contract resolve independently, and the
   contract file must never be taken for the handler**: a **bundle** carries the blob in its dir as
   `__funcd_contract.json` (unchanged); a **single-file** function gets the blob as a **dotfile sidecar**
   `.funcd-contract.json` (mirroring ADR-0089's `.funcd-entry`), and the single-file `FUNCD_ARTIFACT` resolver selects
   the lone **non-dotfile** entry — so the added contract file cannot become `entries[0]` and displace the handler
   (`internal/artifact` cache-hit path, artifact.go:417-424). A contracted function whose schema is absent **fails
   closed** (the shim refuses to serve).
5. **`VerifyBundleContract` relaxes and hardens.** Drop the "entry defines
   `__funcd_validate_input`/`__funcd_validate_output`" symbol check; keep ADR-0090's **both-keys-present** check; and
   **add** per-side `contract.Check` — today only the single-file `--schema` path (`gateSchema`) gates the profile, so a
   bundle can ship an out-of-profile schema. Adding it matters *more* here: an out-of-profile schema that slips through
   would otherwise fail at **worker compile-time** (cold-start fail-closed) instead of at push.
6. **Bounded eval-free reversal (documented).** The schema-compile (`ajv.compile` uses `new Function`;
   `fastjsonschema.compile` uses `exec`) runs **once at shim-init**, over a schema that is **`contract.Check`-gated at
   push and digest-pinned**, **before any handler module is imported**. The code-gen input is funcd-trusted, not
   attacker data; no per-request `eval`. This supersedes ADR-0058's `eval-free-runtime` scenario with an explicit,
   narrower property: *no eval of untrusted or request-time input*.
7. **Cold-start = a benchmark-gated hypothesis + a named fallback.** A micro-benchmark measures the warm-up compile on
   the **scale-from-zero** path (empty pool — the honest worst case, not warm-pool reuse). If a future change breaches
   the budget, the fallback is: **cache the compiled validator across pool workers**, or adopt the **Hybrid**
   (precompile at push when a toolchain is present). The fallback needs no artifact/wire change. Note `contract.Check`
   gates schema **structure**, not codegen **cost** — a large-but-in-profile schema (or a heavy permitted `pattern`) is
   a **self-DoS at the author's own cold-start**, not a cross-tenant surface (request-time regex is unchanged from the
   baked-validator world); its blast radius is that function's own warm-up, folded into the benchmarked budget.

## Temporary workarounds

None. (The Hybrid is a named future fallback, not a current stopgap.)

## Contracts

**Shim (Python & Node)** — at worker init:

```
blob    := parse(read(FUNCD_CONTRACT_PATH))  // the exact ADR-0059 {dialect,input,output} blob; absent+contracted ⇒ fail closed
vin, vout := compile(blob.input), compile(blob.output)       // fastjsonschema.compile / ajv.compile, cached
// per invoke: vin(event.data) → 422 on error (handler not called); vout(result) → 500 on error; void → 204
```

- Python: `funcd_shim` gains a `contract.py` that loads `FUNCD_CONTRACT_PATH` and builds two validators via
  `fastjsonschema.compile`. Node: `shim` loads the same path and builds two `ajv.compile` validators.
- **Validator** shape unchanged in spirit: `(data) -> errors[]` (`[]` ⇒ valid).

**Materializer** — deliver the contract blob to the worker (both single-file and bundle):

- The materialization path (`internal/function` / `internal/artifact`) resolves the ADR-0059 contract blob by the
  manifest's `dev.funcd.contract.v1` digest and delivers it: **bundle** → `<bundleDir>/__funcd_contract.json` (the dir
  already exists); **single-file** → a dotfile sidecar `<cacheDir>/.funcd-contract.json` beside the handler. It sets
  `FUNCD_CONTRACT_PATH` to the delivered path. The single-file `FUNCD_ARTIFACT` resolver (artifact.go:417-424) must
  select the lone **non-dotfile** entry — dotfiles (`.funcd-entry`, `.funcd-contract.json`) are metadata, never the
  handler — so contract delivery cannot break handler resolution.

**`VerifyBundleContract(dir, entry) ([]byte, error)`** — signature unchanged; behavior: require a resolvable
`{input, output}` document with **both keys** (ADR-0090) that **now also** passes `contract.Check` per side (**added** —
today only the single-file `--schema` path gated the profile); **no** validator-symbol check. Returns
`ContractBlob(input, output)`.

**Unchanged (ADR-0059/0090):** `contractMediaType`, `contractAnnotation`, `contractDialect`, `VoidSchema`,
`ContractBlob(input, output)` (both required), the wire codes.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the ADR-0059 contract blob (via manifest digest) · `internal/contract.Check` · the pool (ADR-0050) · `fastjsonschema` (ADR-0071) | a **schema-only** artifact · a worker that compiles+enforces the validator at init · `FUNCD_CONTRACT_PATH` · relaxed `VerifyBundleContract` |

New dep: **AJV** (MIT) in the `nodejs22` image. No Go additions; no docker/container/plugin.

## Implementation plan

**Files**: `shim/python/src/funcd_shim/` (init-time compile from `FUNCD_CONTRACT_PATH`, run **ahead of** the handler
`spec_from_file_location` import — a reorder from today's resolve-from-loaded-module at `_poolworker.py:45`; fail-closed
when absent) · `shim/nodejs/src/` (same via `ajv.compile`) · the `nodejs22` runtime image (ship `ajv`) ·
`internal/artifact/bundle.go` (relax + harden `VerifyBundleContract`) · the materialization path delivering the blob
(`__funcd_contract.json` in-bundle / `.funcd-contract.json` dotfile sidecar for single-file) + `FUNCD_CONTRACT_PATH`,
with the single-file `FUNCD_ARTIFACT` resolver skipping dotfiles · a cold-start micro-benchmark.

**Deps**: add `ajv` to `shim/nodejs` + the `nodejs22` image; no Go/Python additions.

**Test plan** — one acceptance test per Scenario (`runtime-compiles-validator`, `schema-delivered-single-file`,
`schema-tamper-evident`, `eval-over-trusted-input`, `bundle-gate-relaxed`, `cold-start-within-budget`), each written to
pass; contract/unit tests for the relaxed `VerifyBundleContract`, the fail-closed-when-schema-absent path, and the
materializer's blob delivery; the **scale-from-zero** cold-start benchmark asserting the compile delta is within budget.
Linux-only / image-dependent scenarios (the runtime-image AJV ship, the containerd worker) ship contract/unit tests and
**defer** the e2e lane per the roadmap's test-sequencing note, recording the deferral.

**Definition of done**: `go build/vet/test` + `just lint` green (`just ci` after commit); a Python and a Node function
validate from a runtime-compiled validator (422/500/204); single-file + bundle both receive their schema (no fail-open);
`VerifyBundleContract` no longer needs validator symbols; cold-start benchmark present and within budget; ADR-0060 and
ADR-0058 back-links added at acceptance; FEAT-0001/F88 linked.

## Review checklist

- [ ] Artifact carries **no** baked validator; the shim compiles from `FUNCD_CONTRACT_PATH` at init and caches it.
- [ ] Enforcement unchanged: input 422 / output 500 / void 204; handler not called on bad input.
- [ ] Schema delivered for **single-file and bundle**; a contracted function with no schema **fails closed** (not open).
- [ ] The compiled bytes == the digest-pinned advertised blob (advertised == enforced).
- [ ] Compile runs before any handler import — **reordered** from today's post-import resolve (`_poolworker.py:45`),
      asserted; input is `contract.Check`-gated + pinned (bounded eval-free reversal).
- [ ] Single-file contract delivery uses the `.funcd-contract.json` dotfile sidecar; the `FUNCD_ARTIFACT` resolver
      skips dotfiles so the handler still resolves (no `entries[0]` collision).
- [ ] `nodejs22` ships AJV; Python uses shipped fastjsonschema; **no** language toolchain at push; no docker/plugin.
- [ ] `VerifyBundleContract` drops the validator-symbol check; keeps both-keys + `contract.Check`.
- [ ] Scale-from-zero cold-start benchmark present and within budget; fallback named.
- [ ] ADR-0060 + ADR-0058 back-links added; FEAT-0001/F88 linked.

## Consequences

- (+) Enables ADR-0122's pure-Go, plugin-free, containerd-native `funcdctl` — no push-time language toolchain.
- (+) `advertised == enforced` holds by construction (validator derived from the pinned, inspect-able schema).
- (+) One validator source of truth (the schema) — no "validator vs schema" drift possible.
- (−) A **bounded eval-free reversal**: a schema-compiler runs once at worker init (mitigated: trusted, gated, pinned,
  pre-handler; no request-time eval).
- (−) A per-warm-up compile on the cold path (benchmark-gated; fallback named); the Node worker ships **full AJV**
  (larger image) instead of an inlined standalone validator.
- (−) Supersedes two Implemented ADRs (0060 precompile, 0058 eval-free) — a real runtime-behavior change to both shims.

## Open questions

- Whether to cache the compiled validator across pool workers pre-emptively or only if the benchmark breaches budget.
  *(Answered at: the cold-start benchmark result.)*

## References

- ADRs: [0058](0058-contract-codegen-from-code-types.md) (eval-free-runtime — superseded in part),
  [0059](0059-contract-as-oci-metadata.md), [0060](0060-contract-validator-generation.md) (precompile — superseded in
  part), [0071](0071-python-runtime-ships-fastjsonschema.md), [0050](0050-python-worker-pooling-subinterpreters.md),
  [0089](0089-python-function-dependency-bundling.md), [0090](0090-mandatory-single-io-schema.md),
  [0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md).
- AJV (MIT), fastjsonschema (BSD-3, already shipped per ADR-0071).
