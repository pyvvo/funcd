# ADR-0067 implementation review — KV opt-in DR backup (incremental export to object storage)

- **ADR**: [0067](../adr/0067-kv-opt-in-dr-backup.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go tool golangci-lint run` (kvstore/badger · cmd/funcd · config) | **0 issues** |
| `go test ./internal/kvstore/badger/ ./cmd/funcd/ ./internal/config/` | **pass** |
| `go mod verify` | all modules verified |
| new deps | **none** (Badger `Backup`/`Load` + the existing `blob.Bucket` port) |

## Scenarios → tests (named, passing)

- `TestScenarioBackupDefaultOff` — `OpenWithSeams(dir, nil, nil)` builds no backup seam; the base driver
  makes zero object-storage writes.
- `TestScenarioBackupEnabledRequiresTarget` — `NewBackup(db, nil, cfg)` ⇒ `fault.Invalid`; and at the daemon
  level `TestScenarioDaemonBackupEnabledRequiresTarget` — `buildKVStore` with `backup.enabled` + empty target
  ⇒ `fault.Invalid` (refuses to start).
- `TestScenarioIncrementalShipsOnlyDelta` — after a full first export, a 3-key delta ships < 1/10 the bytes
  and advances the cursor.
- `TestScenarioCursorAdvancesOnlyAfterDurableUpload` — an injected upload failure leaves the cursor at 0; the
  next (clean) run re-ships and advances it.
- `TestScenarioRestoreReconstructsStore` — a base + an incremental restore into a fresh instance with full
  key-count and value parity.
- Daemon wiring: `TestScenarioDaemonBackupDefaultOff` (no-op start hook) · `TestScenarioDaemonBackupEnabledBoots`
  (real in-memory target, loop launches, writes succeed).

## ✅ Verified correct (keep)

- **Seam wiring is race-free** — `OpenWithSeams` opens the db, builds the seams over it, then starts the
  gateway with the seams already attached. The `Open`/seam bootstrap order (the seams need the db `Open`
  creates) is resolved without an attach-after-start data race. `badger.go` keeps one tuned-options profile
  (`openDB`) shared by both entry points.
- **Cursor advances only after durable upload** — `Ship` advances the persisted reserved-key watermark
  strictly after the manifest + every part is uploaded; an upload error returns the prior cursor. Idempotent
  re-ship on the same `since` prefix (overwrite).
- **Chunked export** — the `chunkWriter` flushes ≤ `chunkBytes` (default 64 MiB) parts, so neither an
  incremental nor a full re-baseline buffers a whole segment (`blob.Put` is whole-object). `partReader`
  streams parts back one at a time on restore — bounded RSS both ways.
- **KV instance only** — the backup operates on the KV Badger db; the metastore instance is untouched.
- **Default-off costs nothing** — no bucket opened, no loop, no reserved keys unless `backup.enabled`.
- **Hygiene** — `api/fault`, ctx-first, no `any`; the reserved cursor key is NUL-prefixed (never surfaced by
  `List`); no identity/path leak.

## Findings
None (Blocker/Major/Minor).

## DoD
ADR Review-checklist items: 8/8 satisfied (one-file seam impl; default-off asserted; enable-without-target ⇒
`fault.Invalid`; incremental delta + cursor-after-durable-upload; restore parity; low-`NumGo` re-baseline +
chunked parts + pruning; `blob.Bucket` target, no new dep; KV instance only).

## Recommendation
**pass** — `Reviewing → Implemented`. Done.
