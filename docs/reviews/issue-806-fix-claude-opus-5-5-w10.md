# Fix review: issue #806, a write committed during a KV backup can be missing from every later backup

- **Change**: branch `fix/w10-i806`, commit a3105c8e `fix(kvstore): never move the KV backup cursor past a write the export missed`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Bar**: the fix checklist (`.claude/skills/fix-review/SKILL.md`) and the decision recorded for this issue:
  cap the cursor and the segment's `To` at a read timestamp taken before the export starts, in `Ship` and
  `Rebaseline`; leave `Stream.NumGo` to #805.

## Verification run

| Check | Result |
|---|---|
| Revert check: overlay `origin/main` `internal/kvstore/badger/backup.go`, `-run TestIssue806 -count=3` | FAIL 3/3, for the issue's reason: 1786 of 384884, 288 of 354729 and 355 of 350322 written keys in no backup segment (about 5 s each) |
| With the fix, `-race -run TestIssue806 -count=3` | PASS 3/3 (6.4 s each) |
| Mutant A: open the snapshot after `s.Backup` instead of before | killed: `TestIssue806` FAIL, 1328 of 414115 keys missing |
| Mutant B: `max(to, snap.ReadTs())` instead of `min` | killed: `TestIssue806` FAIL (1858 missing) and `TestIssue790_IdleShipUploadsNothing` FAIL |
| `go test -race ./internal/kvstore/...` | ok |
| `go vet ./internal/kvstore/...` | clean |
| `golangci-lint run ./internal/kvstore/...` | 0 issues |
| Worktree after the review | clean (overlays and mutants lived in a scratch directory, now removed) |

The overlay only replaces the non-test file, so the regression test compiles on main (it uses no field or knob
that exists only on the branch) and fails on missing keys, not on compilation.

## Findings

### Blocker

None.

### Major

None.

### Minor

1. **The `export` doc comment now says "every version > since"; Badger exports every version >= since.**
   `internal/kvstore/badger/backup.go:177`. The previous comment said `>= since`, which matches Badger v4.9.2
   (`DB.Backup`/`Stream.Backup` doc: "newer than or equal to the specified version"; `Stream.SinceTs` feeds
   `IteratorOptions.SinceTs`). The behavior is unchanged and harmless (the version at the cursor is re-exported
   and `Load` is idempotent per key-version), but the comment now misstates it. Attribution: `model`.

## Verified correct

- **Cause, not symptom.** The cursor no longer exceeds the read timestamp of a transaction opened before the
  stream: Badger's `oracle.readTs` waits on `txnMark` for every commit at or below that timestamp, and each
  `produceKVs` opens its own `NewTransaction(false)` later, so every producer sees every version at or below
  the cap. Versions above it are re-shipped by the next incremental, as decided. No timeout, retry or
  swallowed error.
- **Both paths covered.** `Ship` and `Rebaseline` both take `To` from the shared `export`, so the cap applies to
  the segment's `To` and the cursor in each; the regression test drives `Ship`, which exercises that same line.
- **The #790 property holds.** An idle tick after a capped ship converges: the re-shipped versions are at or
  below the next read timestamp, and `TestIssue790_IdleShipUploadsNothing` still passes (and kills mutant B).
- **Decision followed.** `Stream.NumGo` is untouched (#805 owns it); the test fails on main at Badger's default
  of 8 producers.
- **Test quality.** A bounded stress test (4 s of ships, about 5 s total) through the real `Ship` and `Restore`,
  failing 3/3 on main. It reuses `openDB`, `openRawDB`, `writeKeys` and `newFakeBucket` from the package; writer
  goroutines stop through `t.Cleanup` on an early failure; each writer appends to its own slice, so `-race` is
  clean.
- **Reuse.** The fix uses a Badger read-only transaction and the built-in `min`; no new helper, type or
  dependency. The only other `NewStream` in the repo (`bench/badger/scenarios.go`) keeps no cursor, so it is not
  a sibling.
- **Scope and conventions.** Two files, both serving the issue; no test weakened or deleted; imports at top
  level; comments explain the why (#806) without narration; no Accepted or Implemented ADR edited, and the
  change conforms to ADR-0066/ADR-0067 (the version-watermarked incremental export).
- **Shape.** `fix(kvstore):` subject, `Fixes #806`, attribution trailer, one issue in one commit.

## Fix checklist

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue806_…` reproduces the issue | yes |
| 2 | Fails on the pre-fix code for the reported reason | yes (3/3) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Mutating the key lines fails a test | yes (2/2 killed) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope; no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (touched packages; the group gate runs the rest) | yes |
| 9 | Conventions hold | no: the comment misstates the export bound (Minor 1) |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |
| 12 | Every case fixed and tested, no sibling left | yes |

11 of 12.

## Recommendation

Pass. Optionally restore `>= since` in the `export` doc comment when the group is integrated.
