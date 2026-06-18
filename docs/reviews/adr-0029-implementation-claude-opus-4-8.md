# ADR-0029 Implementation Review — Gateway: drop the Lura driver (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0029 implementation, model: claude-opus-4-8)

The Lura gateway driver and its `luraproject/lura/v2` dependency are removed; the gateway is now a single
embedded `httputil` driver. The removal is clean — `go build`/`go test`/`golangci-lint`/`go mod verify` are
all green, the embedded driver still passes the shared `gatewaycontract` + the streaming test (no
regression), a depguard deny + a `go.mod` guard test keep the dependency from creeping back, and the
blueprint + both stale doc comments are synced to one driver. Both judge Majors were folded pre-accept. No
new dependency (a *removal* of one + its transitive).

**Reviewed against**: ADR-0029 Contracts/Scenarios/Review-checklist/DoD · ADR-0013 (superseded on the
driver-set question; its httputil-primary/middleware/TLS decisions re-affirmed) · ADR-0002 §ports/drivers ·
blueprint "Ingress / API Gateway" · FEAT-0000/F10.
**Date**: 2026-06-15

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite, exit 0, no failures) |
| scenarios | `lura-dependency-gone` (`TestScenarioLuraDependencyGone` ✓), `gateway-contract-holds-single-driver` (`TestEmbeddedDriverContract` ✓), `streaming-preserved` (`TestScenarioStreamingPassthrough` ✓); `lura-import-denied` = depguard `main`-deny (mechanism proven by the existing `mock-framework` fixture) |
| `golangci-lint run ./...` | **0 issues** — incl. the new `deny: github.com/luraproject` |
| `go mod verify` + `git diff go.mod go.sum` | verified; **−9 lines** — `luraproject/lura/v2` + transitive `krakend/flatmap` removed (a dependency *dropped*, none added) |
| removal | `internal/gateway/lura/` deleted; the only non-lura reference to it (the `gateway.go` doc) is fixed |
| blueprint | synced to a single `httputil` driver — the 11 stale Lura lines fixed; the 3 remaining mentions are correct *historical* references (ADR-0012 framing / "ADR-0029 dropped it") |
| identity | clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **The removal is clean and complete**: `internal/gateway/lura/` gone; `go mod tidy` dropped
  `luraproject/lura/v2` + `krakend/flatmap` (go.mod/go.sum −9 lines); the `gateway.Gateway` port + the
  embedded driver are untouched. Nothing imported Lura but a stale doc comment, so there were no consumer
  edits.
- **Both stale doc comments fixed** (the judge's M1): `gateway.go` (no more "Lura — production") **and**
  `gatewaycontract/contract.go` ("the embedded driver calls it… sole driver since ADR-0029"). The cleanup
  didn't leave a fresh stale comment in the contract suite — the package whose job is to prove the port.
- **The port stays proven with one driver**: the embedded driver passes the shared `gatewaycontract`
  (`TestEmbeddedDriverContract`) and the streaming test still flushes incrementally — the single-driver
  justification (in-process pure-Go driver = real + in-memory) holds in practice (`presets.go` wires it for
  both `InMemory()` and `Production()`).
- **Two regression guards**: the depguard `main` rule denies `github.com/luraproject` (re-import fails lint),
  and `nodep_test.go` asserts `go.mod` is free of `luraproject`/`krakend/flatmap`. Belt-and-suspenders.
- **Blueprint synced** (newest-accepted-wins): 11 stale lines fixed (the repo-layout `lura/` entry + tree
  connector, the "embedded API gateway (Lura)" lines, the `lura.New(...)` example, the driver table/diagram),
  leaving one consistent story — a single embedded `httputil` driver, external-gateway as the V2 second driver.

## Definition of Done
ADR Review-checklist: **5/5** hold (Lura+dep gone · can't-come-back depguard deny · port real with one driver
+ streaming preserved · supersession bookkeeping done at this stamp · no new dep + no leak). Scenarios: 3
runnable named+passing + the depguard deny. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Supersession bookkeeping (done at this Implemented stamp — the judge's M2)
- ADR-0013 gains a `Superseded by ADR-0029` back-link.
- F10's link re-points to ADR-0029 (F10 stays `implemented` — the gateway feature is delivered; the row never
  walked backward, and until this stamp it correctly linked ADR-0013 with Lura still retained).

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0029 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0029 `Reviewing → Implemented`; add the ADR-0013 back-link + re-point F10. The gateway is
now a single streaming-native in-process driver, one framework dependency lighter, with a consistent
blueprint. The external-gateway driver is the named V2 second driver if a strict-≥2 / multi-node need appears.
