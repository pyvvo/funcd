## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0209 implementation, re-review round 2, model: claude-opus-5-5)

Scope: `git log 548c1f08..HEAD` on `feat/adr-0209-kv-backup-common-format` (8 commits). The new top commit is
`56cf3480 fix(funcd): address review of ADR-0209 implementation` (5 files, +155/-35). The base carries the unmerged
ADR-0207 and ADR-0208; only this range is reviewed. The round-1 report is `review-0209-loop1.md`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build darwin / linux | `scripts/agent/d go build ./...`, `GOOS=linux …` | exit 0 / exit 0 |
| vet darwin / linux | `go vet ./cmd/funcd/ ./internal/kvstore/...` | exit 0 / exit 0 |
| lint darwin | `go tool golangci-lint run ./cmd/funcd/ ./internal/kvstore/...` | `0 issues.` |
| lint linux | host-built golangci-lint with `GOOS=linux` (same packages) | `0 issues.` |
| gofmt | `gofmt -l cmd internal` | no file |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| tests | `go test -race -count=1 ./cmd/funcd/ ./internal/kvstore/...` | `ok cmd/funcd 36.8s`, `ok internal/kvstore/badger 22.0s`, `ok internal/kvstore/memory 1.6s` |
| new tests | `go test -v -run 'TestRestoreList\|TestSeamRestoreReadsChainsOnce'` | 0.25 s, 0.26 s, 0.07 s; all call `t.Parallel()` (pitfall 5 holds) |
| tree | `docs/feat`, `docs/reviews`, `blueprint.md`, `go.mod`, `go.sum` | untouched |
| identity | added lines grepped for an abs-path prefix, the local username and an email | no hit |

Not run here by instruction (the gate runs them once): `just ci`/`just ci-full`, the e2e suite, repo-wide `go test`, Lima.

### Round-1 findings

- **`restore list` depended on a second target and could not reach a KV-only target** (model) — resolved.
  `cmd/funcd/restore.go` now loads the config once and opens the platform source only when `--from` or
  `backup.target` is set, or when KV rows are not listed (so a config with neither still gets the old
  "no --from and backup.target is empty" refusal). `kvListed` (`restore_kv.go`) lists KV rows only while
  `kvstore.backup.enabled` is on or `kvstore.backup.target` is set. A KV listing failure after platform rows is printed
  as `KV POINTS: cannot list <target>: <err>` and the command still succeeds; with no platform source the error is
  returned. A KV-only config lists from `kvstore.backup.target` (`f.source` keeps `--from` first). `inspect --kv` no
  longer opens the platform source, so it works for a KV-only config too. Tests: `TestRestoreListKVOnlyConfig`
  (list and `inspect --kv` with only `kvstore.backup.target`) and `TestRestoreListKVSourceFailureOwnLine` (a missing
  KV target is its own line after `GENERATION`; KV export off with no own target ⇒ no KV rows).
- **The seam's `Restore` read the chains twice** (model) — resolved. `internal/kvstore/badger/chain.go` factors
  `points(chains)` out of `ListPoints`; `(*exporter).Restore` reuses the one `readChains` result.
  `TestSeamRestoreReadsChainsOnce` wraps the fake bucket and asserts one `List` and two manifest `Get`s (base +
  segment), plus the restored key counts.
- **`restore kv` writes the hold marker** (adr) — unchanged and correct; still carried as the adr-attributed Minor below.

### Mutants (run with `go test -overlay`, the work untouched)

| # | Mutation | Killed by |
|---|---|---|
| M1 | `chain.go` `Restore`: call `ListPoints(ctx, b.bucket)` again instead of `points(chains)` | `TestSeamRestoreReadsChainsOnce` FAIL (expected 1 List, got 2) |
| M2 | `restore.go` list: drop the `kvListed` gate | `TestRestoreListKVSourceFailureOwnLine` FAIL (KV rows printed with the export off) |
| M3 | `restore.go` list: return the KV error instead of `writeKVFailure` | `TestRestoreListKVSourceFailureOwnLine` FAIL |

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **`restore kv` writes the hold marker (`hold.Write` after `hold.Begin`)**. Attribution: adr (ADR-0206 Decision 5,
  per the DR session's decision of 2026-10-10). Correct behaviour, matching `restore blob`; ADR-0206 Decision 5 leaves
  the rule out and a later ADR should state it. Not counted against the model.

### Notes (no finding)

- `restore list` exits 0 when only the KV source fails; the failure is visible on its own line. This is what round 1
  asked for (a row or a warning), so a script that checks only the exit code will not notice it.
- With `kvstore.backup.enabled: false` and no own KV target, `restore list` shows no KV rows even if old chains sit
  on `backup.target`; `restore kv <point>` still reads them. This matches the round-1 fix request.

### ✅ Verified correct (keep it)

- Everything verified in round 1 still holds (Decisions 1-7, all 12 scenarios and the ADR's unit tests pass under
  `-race`); the fix commit touches only `restore list`/`inspect --kv` and the seam's `Restore`.
- The fix reuses what exists: `openConfig` is split out of `open` rather than copied, `writePlatformRows` gathers the
  existing tree and blob printers, and the counting bucket embeds the existing `fakeBucket`.
- The ADR substance is unchanged (only the earlier `Accepted → Reviewing` status line in the range).

### Definition of Done

8 / 9 items hold here (ADR Review checklist 4 + ADR DoD 5), as in round 1. The ninth is `just ci`, left to the PR
gate by instruction (not a miss).

### Recommendation

Pass. Both model findings of round 1 are resolved with tests that kill the matching mutants, and nothing regressed.
Run the PR gate (`just ci-full`, Linux checks, the audit) before the `Implemented` stamp.

```json
{"adr": "0209", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 1, "model_attributed": 0, "dod_passed": 8, "dod_total": 9, "report": "docs/reviews/adr-0209-implementation-claude-opus-5-5-2.md", "notes": "re-review round 2: pass; both round-1 model minors resolved (restore list degrades per source + KV-only config lists; seam Restore reads chains once, call-count test); 1 minor [adr]: restore kv writes the hold marker, outside ADR-0206 D5; 3/3 mutants killed; just ci left to the gate"}
```
