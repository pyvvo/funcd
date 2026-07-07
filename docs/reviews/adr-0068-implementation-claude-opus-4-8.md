# ADR-0068 implementation review — KV opt-in CDC (durable, resumable transactional-outbox change-feed)

- **ADR**: [0068](../adr/0068-kv-opt-in-cdc.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go tool golangci-lint run` (kvstore/badger · cmd/funcd · config) | **0 issues** |
| `go test ./internal/kvstore/... ./cmd/funcd/ ./internal/config/` | **pass** |
| `go test ./internal/kvstore/badger/ -race` | **pass** (gateway→OnWrite path race-clean) |
| `go mod verify` | all modules verified |
| new deps | **none** (Badger `GetSequence` + the existing `bus.Bus` port) |

## Scenarios → tests (named, passing)

- `TestScenarioCDCDefaultOff` — no CDC config ⇒ zero `_cdc/` entries written, nothing published.
- `TestScenarioCDCEnabledRequiresSink` — nil sink or empty subject ⇒ `fault.Invalid`; and at the daemon
  level `TestScenarioDaemonCDCEnabledRequiresSink` — `buildKVStore` with `cdc.enabled` + empty sink ⇒
  `fault.Invalid` (refuses to start).
- `TestScenarioCDCLogEntryAtomicWithWrite` — N Puts ⇒ exactly N `_cdc/<seq>` entries alongside the N data
  keys (the outbox entry is written in the data's txn — both or neither).
- `TestScenarioCDCSurvivesConsumerRestart` — a sink that fails after 6 of 15, then recovers: `drain`
  delivers 6, errors, resumes from the durable cursor, and delivers all 15 distinct — **zero loss, zero dup**.
- `TestScenarioCDCRetentionBoundsLog` — after full delivery, `gc` reclaims every `_cdc/` entry ≤ cursor.
- Daemon wiring: `TestScenarioDaemonCDCEnabledBoots` (real in-memory bus, tailer launches, writes succeed).

## ✅ Verified correct (keep)

- **Transactional outbox** — `OnWrite(txn, key, op)` appends `_cdc/<seq>` in the gateway's data txn (the
  hook ADR-0066 placed), so the log can never diverge from the data. `seq` is 1-based over Badger
  `GetSequence` (band-leased; the +1 shift keeps seq reachable from a 0-initialised cursor — a real bug the
  scenario test caught and the fix closed).
- **Durable-cursor tailer** — `drain` publishes in seq order and persists the cursor **after each successful
  publish**, so a publish failure or ctx-cancel stops with the cursor at the last delivered seq; the next
  pass re-reads it and continues. Resume is zero-loss; the at-least-once floor is honest in the ADR.
- **Retention** — `gc` reclaims entries ≤ the consumer cursor, bounding the log (min-cursor GC).
- **`Subscribe` is NOT the feed** — the lossy path is unused; correctness lives in the watermarked outbox.
- **Default-off costs nothing** — no `_cdc/` writes, no sequence, no tailer unless `cdc.enabled`.
- **Race-free seam wiring** — `OpenWithSeams`/`OpenWithSeamsFor` attach CDC before the gateway starts;
  the driver releases the seq lease on Close. `-race` clean on the write path.
- **Hygiene** — `api/fault`, ctx-first, no `any`; all internal keys NUL-prefixed (never surfaced by `List`);
  no identity/path leak.

## Findings
None (Blocker/Major/Minor).

## DoD
ADR Review-checklist items: 7/7 satisfied (same-txn `_cdc/` entry; default-off asserted; enable-without-sink
⇒ `fault.Invalid`; killed-consumer resume zero-loss/zero-dup; retention bounds the log; `bus.Bus` sink with
`Subscribe` unused and no new dep; KV instance only).

## Recommendation
**pass** — `Reviewing → Implemented`. Done.
