# adr-judge — ADR-0128 (funcdctl dev — manifest interpreter config + seedable relaxed-write dev blob)

**Verdict: PASS-WORTHY.** A faithful retroactive-documentation ADR: every Contracts claim matches the
shipped tree (verified file-by-file; the three named tests pass, the `-tags dev` build is green), the
dev-only/prod-unchanged security boundary holds under scrutiny, the discovered ADR-0116 latent bug is
accurately root-caused (git-confirmed), and no identity/path leak. One **Major** (a documentation-honesty
gap, not a contract mismatch) and a couple of Minors — all quick fixes, none blocking acceptance.

## Judged against
- ADR-0000 (template/gates) · ADR-0126 (retroactive-doc precedent + funcdctl-dev lineage) · ADR-0125 (dev
  command + fidelity boundary) · ADR-0122 (the `dev:` block extended) · ADR-0116 (capability registry) ·
  ADR-0080 (S3 single-writer built-in).
- FEAT-0001 v1.1: the **F93** row it `Realizes` exists, sits in v1.1 scope, status `adr`, and describes
  exactly these two dev-only ergonomics + the ADR-0116 fix at the right altitude (what/why, no how).
- The actual shipped code (uncommitted working tree — the retroactive crux).

## Goal alignment
Advances F93 / the funcdctl-dev DX directly: pins the interpreter in the committable `funcdctl.yaml`
(zero-env from-source runs) and lets a developer seed a workflow's input blob locally — both real frictions
surfaced running `examples/python/releve-lakehouse` under `funcdctl dev`. Bundling the two, plus the
ADR-0116 prerequisite fix, in one ADR is defensible at one altitude (all three are "what a from-source
releve run needs"), mirroring ADR-0126's several-dev-UX-items-in-one-ADR precedent.

## Contracts vs shipped code — verified (the retroactive crux)
| ADR Contracts claim | Shipped code | Match |
|---|---|---|
| `sdk.Dev` gains `Python` + `Node` | `pkg/sdk/manifest.go:57,60` | ✓ |
| `builtin_s3_dev.cedar`: keeps read binding-grant, DROPS write forbid | `internal/auth/cedar/builtin_s3_dev.cedar:11-16` (read permit + guard kept; bare `permit(s3::write)`, no forbid) vs prod `builtin_s3.cedar:17-20` (forbid kept) | ✓ |
| `S3CapabilityDevRelaxedWrites()` + factored `s3Capability(builtin)` | `capabilities.go:202,206` | ✓ |
| `Deps.Builtins`; `New()` falls back to `defaultRegistry.Builtins()` when empty | `cedar.go:23,49-52` | ✓ |
| `policyCache.builtins`; `compile()` uses it, NOT the package global | `policies.go:43,66,77` (`compile(c.builtins, policies)`) | ✓ |
| **Key claim — pre-fix `compile()` used `defaultRegistry.Builtins()`, so a variant's built-ins were silently dropped** | `git show fa54ac9:internal/auth/cedar/policies.go` — pre-0128 `compile(policies)` hard-coded `defaultRegistry.Builtins()` and took no builtins param | ✓ **accurate** |
| `c.s3DevRelaxedWrites`; `S3Capability` vs `S3CapabilityDevRelaxedWrites` selection; `New(Deps{…, Builtins: cedarRegistry.Builtins()})` | `funcd.go:200,428-431,443-446` | ✓ |
| `WithDevS3RelaxedWrites` | `options.go:369` | ✓ |
| `resolveInterpreter`, `devShimOptions(op,dev,baseDir)` env>manifest>PATH, `plannedFunc.manifestDir`, `WithDevS3RelaxedWrites()` in bootDev opts | `dev.go:1230,1237/1252-1291,786,504` | ✓ |

Named tests all present and green: `TestScenarioDevRelaxedWritesDropOwnerGate`
(`s3_dev_relax_test.go`), `TestResolveInterpreter` (`dev_interpreter_test.go`),
`TestEmbedIncludesEveryRuntimeModule` (`shim/python/embed_test.go`). `-tags dev ./cmd/funcdctl/` builds.

## Strengths (keep)
- **Contracts match the tree exactly** — the pseudo-code fences (`New()` fallback, `compile()` signature,
  the two-line dev `.cedar`) are literal, not aspirational. Rare fidelity for a retroactive ADR.
- **Honest bug forensics** — the ADR-0116 defect (§Context "compiled … from a package-global
  `defaultRegistry`, ignoring the registry actually assembled") is git-accurate and correctly framed as a
  *prerequisite* ("Without this, #2 is inert"), not smuggled in.
- **Security boundary is real, not asserted** — `WithDevS3RelaxedWrites` defaults off (`funcd.go:200`
  bool) and is set in exactly one place, `dev.go:504`, which is `//go:build dev` (grep-confirmed sole
  caller); the thin release client never compiles it. Reads stay binding-gated in the dev variant, and the
  test asserts an **unbound** prefix stays default-deny under dev (`s3_dev_relax_test.go:80`) — so the
  relaxation provably does not widen reads.
- **Fidelity honesty mirrors ADR-0125** — documented as a boundary in Context, Constraints, Consequences
  (Negative/accepted), and a Temporary-workaround with a concrete exit (typed `owner` + external
  `S3Identity`, on the board).
- Concrete, strawman-free Alternatives; clean `s3Capability(builtin)` factoring (the two variants differ
  only in built-in text); the embed-completeness test is a good guard tied to the ADR-0127 `blob.py` miss.

## Findings

### Major
- **[Major · adr] The retroactive-documentation framing / honesty note is missing.** The task and the
  tree confirm the code already shipped this session, yet the ADR carries none of the retroactive
  disclosure that its own precedent **ADR-0126** established for this exact situation (0126's Date line:
  "*RETROACTIVE documentation ADR … the implementation + its tests already landed … No separate
  judge/adr-impl-review gate ran — the coverage is the shipped tests*"). ADR-0128's Date line
  (`0128-…md:4`) is a bare `2026-07-12`, and the Implementation-plan/DoD are written forward-tense
  ("Files", "the four Go sub-checks green") as if implemented-from. **Impact:** the doc trail reads as
  forward-looking when the code predates the ADR — the precise honesty gap the doc-layer invariants exist
  to prevent. **Fix:** add a one-line retroactive note to the Date/Status line mirroring ADR-0126 (code
  already landed on `feat/funcdctl-contract-codegen`; coverage is the three shipped tests; no
  implemented-from gate). Not a contract mismatch — the Contracts are correct — so this does not block a
  self-accept, but it should be folded before acceptance.

### Minor
- **[Minor · adr] Test plan over-claims env-override coverage.** §Implementation-plan says "`resolveInterpreter`
  unit test — **env**/manifest/PATH precedence … (dev-python-from-manifest / **dev-interpreter-env-overrides**)".
  But `resolveInterpreter` (`dev.go:1230`) contains no env logic — the env>manifest precedence lives in
  `devShimOptions` (`dev.go:1254-1257,1272-1275`) and is **untested**; `TestResolveInterpreter` only covers
  path resolution (its own comment concedes "The env-override precedence is enforced in devShimOptions (env
  checked before this)"). So the `dev-interpreter-env-overrides` Scenario has no direct test. **Fix:** add a
  small `devShimOptions` env-precedence test, or soften the mapping to "resolution half only".
- **[Minor · adr] Four PDP Scenarios collapse onto one assertion.** `dev-seed-no-owner-prefix`,
  `dev-producer-writes-inferred-owner`, `prod-single-writer-unchanged`, and `variant-builtins-take-effect`
  all map to `TestScenarioDevRelaxedWritesDropOwnerGate`, which exercises a single non-owner-write case
  over prod+dev registries. It is the same code path so coverage is genuine, but the test does not
  separately exercise a *no-owner* prefix (only a *non-owner writing an owned* prefix). Consider adding a
  no-owner-prefix write assertion so `dev-seed-no-owner-prefix` (the `landing` case) is literally covered.

### Considered, not a defect
- **Touching Implemented ADR-0116's code without superseding it** is correct: ADR-0116's Contracts already
  expose `Registry.Builtins()` "to the cedar driver assembly," so threading `Deps.Builtins` makes the
  implementation *conform to* 0116's stated intent — it does not change 0116's decision, hence no
  superseding ADR is owed. Worth a one-clause acknowledgment in §Context that this is a
  conformance fix to 0116 (not a decision change), but the ADR is already close ("threaded through
  `cedar.Deps`").
- The exported `WithDevS3RelaxedWrites` living in the always-compiled `options.go` (not behind `-tags dev`)
  is fine — it is data-only and off by default; the dev-tag guard is on the *caller*, matching
  `WithLogObserver`/`WithCatalogProviderRuntime`.

## Template & scenario conformance
All ADR-0000 sections present (Status→References). Six Scenarios, each with a mapped test (with the two
Minor caveats above). Cross-doc: additive on ADR-0122's `Dev` block; conforms to ADR-0116 intent; prod
ADR-0080 built-in unchanged; no accepted-ADR contradiction. Concision is good — dense but not bloated,
on par with ADR-0126. No absolute-path / local-username / personal-email leak (grep-clean; identity is
`green-0-rabbit` only).

## Recommendation
**Accept after folding the Major** (add the ADR-0126-style retroactive note). The two Minors are
test-coverage/mapping polish and may be applied in the same pass or tracked. No Blockers; the substance —
Contracts, security boundary, bug forensics — is sound and code-accurate.
