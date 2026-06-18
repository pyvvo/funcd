# ADR-0022 Implementation Review — Secrets service (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0022 implementation, model: claude-opus-4-8)

The AES-256-GCM `store.Encryptor` + the PDP-authorized `Resolver` realize the Contracts. Secrets are ciphertext
at rest (proven by a raw-engine read) and the resolver delivers decrypted env vars only to authorized callers.
The judge's Major (injection-owner honesty) was a doc fix, folded pre-accept. All 5 scenarios pass. No findings.

**Reviewed against**: ADR-0022 Contracts/Scenarios/Review-checklist/DoD · blueprint "Secrets management /
Security model" · ADR-0006 (Encryptor seam), ADR-0018 (PDP), ADR-0002 · FEAT-0000/F15.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet` | exit 0 |
| `go test -count=1 ./...` | PASS (full suite, incl. OpenAPI staleness) |
| scenarios | 5/5 PASS (`encryptor-roundtrips`, `encryptor-rejects-bad-key`, `secret-stored-encrypted-at-rest`, `resolver-returns-bound-secret-env`, `resolver-authorizes`) |
| `golangci-lint run ./...` | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (stdlib AEAD, no new dep) |
| crypto | `aesgcm.go`: `cipher.NewGCM` (stdlib AEAD), `io.ReadFull(rand.Reader, nonce)` (random nonce), `len(key) != 32` guard, `gcm.Open` (auth/tamper-detect) — not hand-rolled |
| PEP | `secrets.go:51`: `Authorize(VerbGet, KindSecret, ns)` **before** the `store.Get` |
| distinct resource | no `services.TypeHandler`/`NewDispatcher` — secrets is resource+encryptor+resolver, not on the Service dispatcher (correct) |
| conventions | no `any`; identity clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **Sound at-rest crypto** (`internal/secrets/aesgcm`): stdlib AES-256-GCM, fresh random 12-byte nonce per
  `Encrypt` prepended to the ciphertext (`Seal(nonce, nonce, …)`), 32-byte key guard → `fault.Invalid`,
  `Decrypt` authenticates (`encryptor-roundtrips` proves recover + unique-per-call + tamper-fail;
  `encryptor-rejects-bad-key` proves the guard). **Not hand-rolled** — the stdlib AEAD used correctly. Keep —
  don't swap in tink prematurely (that's the deferred envelope/KMS driver).
- **Encrypted at rest, proven** (`secret-stored-encrypted-at-rest`): a `Secret` in a `WithEncryptor([KindSecret])`
  store reads back as plaintext, but the **raw engine value** (read via `eng.View`) contains neither the
  plaintext nor its JSON base64 — real ciphertext on disk, transparent decrypt on read (ADR-0006 seam filled).
- **Resolver is a PEP**: `Authorize(VerbGet, KindSecret, ns)` **before** any `store.Get`; cross-namespace dev →
  `fault.Forbidden` (`resolver-authorizes`); returns the decrypted `Data` as an env map (`resolver-returns-bound-secret-env`).
- **Correctly modeled as a distinct resource** (no Service `TypeHandler`/dispatcher) — and the ADR honestly
  scopes the consuming injection last-mile to a named P-M-successor follow-up (not claimed delivered).
  `SecretTypeOpaque` added (OpenAPI regenerated); `New(Deps)` guards, ctx-first, `api/fault`, `slog`, no globals,
  no `any`, **no new dep**.

## Definition of Done
ADR Review-checklist: **5/5** hold (AES-GCM encryptor+guard+tamper · encrypted-at-rest · resolver-PEP ·
SecretTypeOpaque+OpenAPI · conventions+no-dep+deferrals+no-leak). Scenarios: 5/5 named, un-skipped, passing.
ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0022 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0022 `Reviewing → Implemented`, feat F15 → `implemented` (the ADR is implemented; the
end-to-end secret *injection* remains a named P-M-successor follow-up the Step-6 reconcile records). Real edges
ADR-0006/0018/0003 (drop the phantom P-N/ADR-0011 edges). Next: P-Q (eventing).
