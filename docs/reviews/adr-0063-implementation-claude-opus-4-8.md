# ADR-0063 Implementation Review — Control-plane admission framework

**Verdict**: **pass** — the implementation adds `internal/controlplane/admission` (the `Admission` port + two-phase `Pipeline` + the built-in validate admission) exactly per Contracts and wires it into the control-plane write path, generalizing the former inline `obj.Validate()` with full behavior parity. Every scenario is a passing test (matrix where the contract is a closed set), the judge's Major (`Request.Old`/`Identity` wiring) is proven by a named test, and the four sub-checks are green module-wide. No Blockers, no Majors, no Minors.
**Reviewed**: ADR-0063 (status `Reviewing`) · FEAT-0001/F32 · ADR-0018 (refined) · ADR-0002 conventions
**Producing model**: claude-opus-4-8

## Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | **exit 0** |
| tests (scoped) | `go test ./internal/controlplane/...` | **ok** — admission + controlplane green |
| tests (full) | `go test ./...` | **ALL GREEN** (no FAIL/panic across the module) |
| lint | `go tool golangci-lint run ./internal/controlplane/...` | **0 issues** |
| mod | `go mod verify` | **all modules verified** |
| leaf | import graph | `internal/controlplane/admission` imports only `api/types/v1alpha1`, `internal/auth` — **no** controlplane/store import |

## Scenario → test traceability (all passing, none skipped)

| ADR Scenario | Test | Form |
|---|---|---|
| admission-allows-valid-resource | `TestScenarioCrudRoundtripsThroughStore` (pre-existing) + `TestValidateAdmission` | — |
| admission-rejects-invalid-resource | `TestScenarioAdmissionRejectsInvalid` (pre-existing) + `TestValidateAdmission` | **matrix** (valid/invalid objects) |
| authz-precedes-admission | `TestScenarioAuthzPrecedesAdmission` (spy not called on 403) | scenario |
| mutating-precedes-validating | `TestScenarioMutatingPrecedesValidating` (registered validating-first) | scenario |
| first-denial-short-circuits | `TestScenarioFirstDenialShortCircuits` | scenario |
| delete-runs-registered-admission | `TestScenarioDeleteRunsRegisteredAdmission` + `TestHandlesMatrix` | **matrix** (op→handles) |
| request-carries-old-and-identity | `TestScenarioRequestCarriesOldAndIdentity` (the judge's Major) | scenario |

## Review checklist (ADR-0063) — all 7 hold

- ✅ The `Admission` port + two-phase `Pipeline` match the Contracts verbatim; no `any`; ctx-first; near-leaf import graph.
- ✅ Two-phase order proven — a mutating admission's change is visible to a validating one even when registered validating-first (`TestScenarioMutatingPrecedesValidating`).
- ✅ First fault error short-circuits deterministically; the second admission does not run (`TestScenarioFirstDenialShortCircuits`).
- ✅ The validate admission reproduces `obj.Validate()` for Create+Update; invalid resources → `fault.Invalid`, not persisted (`TestValidateAdmission` matrix + the pre-existing controlplane tests still green = parity).
- ✅ Authz runs before admission — a spy admission is not called when authz denies (`TestScenarioAuthzPrecedesAdmission`).
- ✅ `Request.Old` (Update) and `Request.Identity` are populated; `replaceObj` reuses its pre-update `Get` for `Old` (`TestScenarioRequestCarriesOldAndIdentity`).
- ✅ Delete runs the pipeline only when an admission handles Delete (`h.admit.Handles(gvk, Delete)` gate in `deleteObj`); no extra store fetch otherwise. Zero new deps; full suite green.

## ✅ Verified correct — keep it

- **The two-phase pipeline is exactly as specified** — `NewPipeline` partitions by `Phase`, `Admit` threads the object through Mutating then Validating, the first `fault` short-circuits. The `mutating-precedes-validating` test registers the validating admission *first* to prove phase order beats registration order — the right guard.
- **The judge's Major is genuinely closed** — `replaceObj` was reordered to fetch the stored object *before* `admit` (reusing the existing RV read, no second round-trip) and a spy test asserts `Request.Old != nil` + `Request.Identity.Subject == "dev"` on Update. This is what protects the ADR-0064 consumer; keep the test.
- **Behavior parity** — the pre-existing controlplane suite passes unchanged, confirming the refactor of the inline `obj.Validate()` into the pipeline changed no observable behavior.
- **Matrix discipline** — `TestValidateAdmission` (valid/invalid objects) and `TestHandlesMatrix` (op→handles) are parametrized where the contract is a closed set; the ordering/wiring proofs stay discrete (correctly — they are one-assertion spy tests, not value matrices).

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None blocking. (Observation: the implementation adds `Deps.Admissions []admission.Admission` — a registration seam not named in the ADR Contracts but consistent with the Decision's "the server constructor builds the pipeline". It is additive, required by the ADR-0064 consumer to register the link admissions, and is what lets the spy scenarios drive the full HTTP path. A justified realization, not a deviation.)

## Recommendation

**pass** → stamp ADR-0063 `Reviewing → Implemented` and feat row F32 → `implemented`. No board item (F32 originated from no Project #4 ticket).
